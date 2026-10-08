//go:build windows && mihomo_integration && !no_easytier

package outbound

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"slices"
	"testing"
	"time"

	corehost "github.com/easytier/easytier/easytier-go"
	"github.com/easytier/easytier/easytier-go/platform"
	"github.com/easytier/easytier/easytier-go/proto/common"
	"github.com/gofrs/uuid/v5"
	et "github.com/metacubex/mihomo/component/easytier"
	C "github.com/metacubex/mihomo/constant"
	mihomoDNS "github.com/metacubex/mihomo/dns"
)

func TestEasyTierWebClientEnvironmentRefreshPreservesNetworks(t *testing.T) {
	previousHome := C.Path.HomeDir()
	C.SetHomeDir(t.TempDir())
	defer C.SetHomeDir(previousHome)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	server := newEasyTierWebTestServer(t, ctx, "_environment-")
	request := server.request
	wait := func(description string, condition func() bool) {
		t.Helper()
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for !condition() {
			select {
			case <-ctx.Done():
				t.Fatalf("wait for %s: %v", description, ctx.Err())
			case <-ticker.C:
			}
		}
	}
	serverReady := func() bool {
		_, status, err := request(http.MethodGet, "/api/internal/sessions", nil)
		return err == nil && status == http.StatusOK
	}
	server.start(t)
	wait("isolated configuration-server API", serverReady)
	networks := []struct {
		id, address, route string
	}{
		{uuid.Must(uuid.NewV4()).String(), "10.145.0.2", "10.210.1.0/24"},
		{uuid.Must(uuid.NewV4()).String(), "10.146.0.2", "10.210.2.0/24"},
	}
	owner, err := NewEasyTier(EasyTierOption{
		Name: "environment-" + networks[0].id, StateDir: "environment-state-" + networks[0].id, PacketMode: true,
		WebClient: &EasyTierWebClientOption{Endpoint: server.endpoint, Hostname: "mihomo-environment"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = owner.Close() }()
	if _, err := owner.OpenPacketSessions(ctx); err != nil {
		t.Fatal(err)
	}
	machineID := owner.EasyTierSummary().InstanceID
	userID := 0
	authenticated := func() bool {
		body, status, err := request(http.MethodGet, "/api/internal/sessions", nil)
		if err != nil || status != http.StatusOK {
			return false
		}
		var sessions []struct {
			MachineID string `json:"machine_id"`
			UserID    int    `json:"user_id"`
		}
		if err := json.Unmarshal(body, &sessions); err != nil {
			t.Fatal(err)
		}
		for _, session := range sessions {
			if session.MachineID == machineID {
				userID = session.UserID
				return owner.EasyTierSummary().WebClientConnected
			}
		}
		return false
	}
	wait("machine authentication", authenticated)
	base := fmt.Sprintf("/api/internal/users/%d/machines/%s/networks", userID, machineID)
	configs := make(map[string]map[string]any, len(networks))
	for _, network := range networks {
		config := map[string]any{
			"instance_id": network.id, "dhcp": false, "virtual_ipv4": network.address, "network_length": 24,
			"hostname": network.id, "network_name": network.id, "network_secret": network.id, "networking_method": "Manual",
			"peer_urls": []string{}, "public_server_url": server.endpoint, "listener_urls": []string{}, "advanced_settings": true,
			"disable_p2p": true, "disable_ipv6": true, "no_tun": true, "bind_device": true, "mtu": 1380,
			"enable_manual_routes": true, "routes": []string{network.route},
		}
		configs[network.id] = config
		body, status, err := request(http.MethodPost, base, map[string]any{"save": true, "config": config})
		if err != nil || status != http.StatusOK {
			t.Fatalf("persist Web network %s: HTTP %d err=%v body=%s", network.id, status, err, body)
		}
	}
	findSessions := func() map[string]et.PacketSession {
		sessions, err := owner.OpenPacketSessions(ctx)
		if err != nil {
			t.Fatal(err)
		}
		found := make(map[string]et.PacketSession, len(sessions))
		for _, session := range sessions {
			found[session.ID] = session
		}
		return found
	}
	var sessions map[string]et.PacketSession
	wait("two Web packet networks", func() bool {
		sessions = findSessions()
		return len(sessions) == len(networks) && slices.ContainsFunc(networks, func(network struct{ id, address, route string }) bool {
			session, ok := sessions[network.id]
			return ok && slices.Contains(session.Addresses, netip.MustParsePrefix(network.address+"/24")) && slices.Contains(session.Routes, netip.MustParsePrefix(network.route))
		})
	})
	instances := func() map[string]*corehost.Instance {
		owner.mu.Lock()
		host := owner.host
		owner.mu.Unlock()
		found := make(map[string]*corehost.Instance)
		for _, snapshot := range host.InstanceConfigurations() {
			found[snapshot.InstanceID] = snapshot.Instance
		}
		return found
	}
	previousInstances := instances()
	previousEndpoints := make(map[string]et.PacketEndpoint, len(sessions))
	for id, session := range sessions {
		previousEndpoints[id] = session.Endpoint
	}
	dnsNetworks := func() []mihomoDNS.EasyTierNetwork {
		networks, err := (easyTierDNSTransport{easytier: owner}).EasyTierDNSNetworks(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return networks
	}
	if got := dnsNetworks(); len(got) != 2 {
		t.Fatalf("initial Web DNS networks = %d", len(got))
	}
	showInterface := func(instance *corehost.Instance, want netip.Addr) bool {
		info, err := instance.ShowNodeInfo(ctx)
		if err != nil || info.GetIpList() == nil {
			return false
		}
		wantValue := binary.BigEndian.Uint32(want.AsSlice())
		return slices.ContainsFunc(info.GetIpList().GetInterfaceIpv4S(), func(address *common.Ipv4Addr) bool { return address.GetAddr() == wantValue })
	}
	refresh := func(address netip.Addr) {
		snapshot := platform.EnvironmentSnapshot{InterfaceIPv4s: []netip.Addr{address}, LocalIPs: []netip.Addr{address}}
		if err := owner.UpdateEnvironment(ctx, snapshot); err != nil {
			t.Fatal(err)
		}
		wait("environment facts after refresh", func() bool {
			for id, instance := range previousInstances {
				if !showInterface(instance, address) {
					return false
				}
				if sessions[id].Endpoint != previousEndpoints[id] {
					return false
				}
			}
			return len(dnsNetworks()) == 2
		})
	}
	refresh(netip.MustParseAddr("192.0.2.10"))
	refresh(netip.MustParseAddr("192.0.2.11"))
	server.stop(t)
	wait("WebClient disconnect", func() bool { return !owner.EasyTierSummary().WebClientConnected })
	server.start(t)
	wait("restarted server API", serverReady)
	wait("same machine reauthentication", authenticated)
	wait("desired networks after server restart", func() bool {
		sessions = findSessions()
		return len(sessions) == 2
	})
	previousInstances = instances()
	previousEndpoints = make(map[string]et.PacketEndpoint, len(sessions))
	for id, session := range sessions {
		previousEndpoints[id] = session.Endpoint
	}
	refresh(netip.MustParseAddr("192.0.2.11"))
	previousInstances, previousEndpoints = instances(), make(map[string]et.PacketEndpoint, len(sessions))
	for id, session := range sessions {
		previousEndpoints[id] = session.Endpoint
	}
	network := networks[0]
	updatedRoute := "10.211.1.0/24"
	configs[network.id]["routes"] = []string{updatedRoute}
	_, status, err := request(http.MethodPut, base, map[string]any{
		"managed_network_configs": []map[string]any{
			{"instance_id": networks[0].id, "network_config": configs[networks[0].id]},
			{"instance_id": networks[1].id, "network_config": configs[networks[1].id]},
		},
		"config_revision": "route-refresh-" + network.id,
	})
	if err != nil || status != http.StatusNoContent {
		t.Fatalf("replace one desired route: HTTP %d err=%v", status, err)
	}
	if err := owner.UpdateEnvironment(ctx, platform.EnvironmentSnapshot{InterfaceIPv4s: []netip.Addr{netip.MustParseAddr("192.0.2.12")}, LocalIPs: []netip.Addr{netip.MustParseAddr("192.0.2.12")}}); err != nil {
		t.Fatal(err)
	}
	wait("same UUID route replacement with refreshed environment", func() bool {
		sessions = findSessions()
		updated, ok := sessions[network.id]
		if !ok || updated.Endpoint == previousEndpoints[network.id] || !slices.Contains(updated.Routes, netip.MustParsePrefix(updatedRoute)) {
			return false
		}
		return showInterface(updated.Endpoint.(*corehost.Instance), netip.MustParseAddr("192.0.2.12"))
	})
	wait("DNS networks after route replacement", func() bool { return len(dnsNetworks()) == 2 })
	_, status, err = request(http.MethodDelete, base+"/"+network.id, nil)
	if err != nil || status != http.StatusOK {
		t.Fatalf("remove first network: HTTP %d err=%v", status, err)
	}
	t.Log("two Web networks retained UUIDs, routes, DNS, and core endpoints across environment refresh; route replacement published new facts")
}
