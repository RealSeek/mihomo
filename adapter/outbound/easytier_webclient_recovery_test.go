//go:build windows && mihomo_integration && !no_easytier

package outbound

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	corehost "github.com/easytier/easytier/easytier-go"
	"github.com/gofrs/uuid/v5"
	et "github.com/metacubex/mihomo/component/easytier"
	C "github.com/metacubex/mihomo/constant"
	D "github.com/miekg/dns"
	"github.com/pelletier/go-toml/v2"
)

func TestEasyTierWebClientServerRecovery(t *testing.T) {
	previousHome := C.Path.HomeDir()
	C.SetHomeDir(t.TempDir())
	defer C.SetHomeDir(previousHome)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	server := newEasyTierWebTestServer(t, ctx, "_recovery-")
	request := server.request
	peerAddress, _ := easyTierWebTestAddress(t)
	networkID, peerURL := uuid.Must(uuid.NewV4()).String(), "tcp://"+peerAddress
	wait := func(description string, condition func() bool) {
		t.Helper()
		ticker := time.NewTicker(100 * time.Millisecond)
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
	option := EasyTierOption{
		Name: "recovery-" + networkID, StateDir: "recovery-state-" + networkID, PacketMode: true,
		WebClient: &EasyTierWebClientOption{Endpoint: server.endpoint, Hostname: "mihomo-recovery"},
	}
	owner, err := NewEasyTier(option)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = owner.Close() }()
	if sessions, err := owner.OpenPacketSessions(ctx); err != nil || len(sessions) != 0 {
		t.Fatalf("start fresh recovery owner: sessions=%d err=%v", len(sessions), err)
	}
	machineID := owner.EasyTierSummary().InstanceID
	if _, err := uuid.FromString(machineID); err != nil {
		t.Fatalf("generated machine identity: %v", err)
	}
	identity, err := os.ReadFile(filepath.Join(owner.stateDir, easyTierInstanceIDFile))
	if err != nil || strings.TrimSpace(string(identity)) != machineID {
		t.Fatalf("persisted generated machine identity: %v", err)
	}
	userID := 0
	authenticated := func() bool {
		body, status, err := request(http.MethodGet, "/api/internal/sessions", nil)
		if err != nil {
			return false
		}
		if status != http.StatusOK {
			t.Fatalf("authenticated sessions: HTTP %d", status)
		}
		var sessions []struct {
			MachineID string `json:"machine_id"`
			UserID    int    `json:"user_id"`
		}
		if err := json.Unmarshal(body, &sessions); err != nil {
			t.Fatal("decode authenticated session identities")
		}
		for _, session := range sessions {
			if session.MachineID == machineID {
				if userID != 0 && session.UserID != userID {
					t.Fatal("restart changed the persisted server user identity")
				}
				userID = session.UserID
				return owner.EasyTierSummary().WebClientConnected
			}
		}
		return false
	}
	wait("initial machine authentication", authenticated)
	peer, err := NewEasyTier(EasyTierOption{
		Name: "recovery-peer-" + networkID, PacketMode: true, Hostname: "peer",
		NetworkName: networkID, NetworkSecret: networkID, IPv4: "10.144.0.1/24", Listeners: []string{peerURL},
		STUNServers: []string{}, STUNServersV6: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	if _, err := peer.OpenPacketSession(ctx); err != nil {
		t.Fatal(err)
	}
	peerCore, err := peer.currentInstance()
	if err != nil {
		t.Fatal(err)
	}
	base := fmt.Sprintf("/api/internal/users/%d/machines/%s/networks", userID, machineID)
	route := "10.211.0.0/24"
	preferPeerRelay := false
	config := map[string]any{
		"instance_id": networkID, "dhcp": false, "virtual_ipv4": "10.144.0.2", "network_length": 24,
		"hostname": "managed", "network_name": networkID, "network_secret": networkID, "networking_method": "Manual",
		"peer_urls": []string{peerURL}, "public_server_url": peerURL, "listener_urls": []string{}, "advanced_settings": true,
		"disable_p2p": true, "disable_ipv6": true, "no_tun": true, "bind_device": true, "mtu": 1380,
		"enable_manual_routes": true, "routes": []string{route},
		"prefer_peer_relay": preferPeerRelay,
	}
	_, status, err := request(http.MethodPost, base, map[string]any{"save": true, "config": config})
	if err != nil || status != http.StatusOK {
		t.Fatalf("persist managed network: HTTP %d err=%v", status, err)
	}
	device := &easyTierMemoryTun{read: make(chan []byte, 4), write: make(chan []byte, 4), closed: make(chan struct{})}
	mux, err := et.NewPacketMux(device, nil, false, nil, func(err error) { t.Logf("managed packet receiver: %v", err) })
	if err != nil {
		t.Fatal(err)
	}
	defer mux.Close()
	stopMux := context.AfterFunc(ctx, func() { _ = mux.Close() })
	defer stopMux()
	var session et.PacketSession
	verifyPacketNetwork := func() {
		t.Helper()
		wait("persisted managed packet network", func() bool {
			sessions, err := owner.OpenPacketSessions(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(sessions) == 1 && sessions[0].ID == networkID && slices.Contains(sessions[0].Addresses, netip.MustParsePrefix("10.144.0.2/24")) {
				session = sessions[0]
				return true
			}
			return false
		})
		managed := session.Endpoint.(*corehost.Instance)
		owner.mu.Lock()
		host := owner.host
		owner.mu.Unlock()
		configurations := host.InstanceConfigurations()
		if len(configurations) != 1 || configurations[0].InstanceID != networkID {
			t.Fatal("restored configuration snapshot has the wrong network")
		}
		var effective struct {
			Flags struct {
				PreferPeerRelay bool `toml:"prefer_peer_relay"`
			} `toml:"flags"`
		}
		if err := toml.Unmarshal([]byte(configurations[0].ConfigTOML), &effective); err != nil {
			t.Fatal(err)
		}
		if effective.Flags.PreferPeerRelay != preferPeerRelay {
			t.Fatalf("restored peer relay preference = %v, want %v", effective.Flags.PreferPeerRelay, preferPeerRelay)
		}
		for _, pair := range [][2]*corehost.Instance{{managed, peerCore}, {peerCore, managed}} {
			info, err := pair[1].ShowNodeInfo(ctx)
			if err != nil {
				t.Fatal(err)
			}
			wait("current restored core peer route", func() bool {
				routes, err := pair[0].ListRoute(ctx)
				if err != nil {
					t.Fatal(err)
				}
				for _, route := range routes {
					if route.GetPeerId() == info.GetPeerId() && route.GetNextHopPeerId() == info.GetPeerId() {
						return true
					}
				}
				return false
			})
		}
		sessions, err := owner.OpenPacketSessions(ctx)
		if err != nil || len(sessions) != 1 || sessions[0].ID != networkID {
			t.Fatalf("refresh restored packet routes: sessions=%d err=%v", len(sessions), err)
		}
		session = sessions[0]
		if !slices.Contains(session.Routes, netip.MustParsePrefix(route)) {
			t.Fatalf("restored manual route %s missing from packet session: %v", route, session.Routes)
		}
		if route != "10.211.0.0/24" && slices.Contains(session.Routes, netip.MustParsePrefix("10.211.0.0/24")) {
			t.Fatalf("old desired manual route survived replacement: %v", session.Routes)
		}
		if err := mux.UpdateSessions([]et.PacketSession{session}); err != nil {
			t.Fatal(err)
		}
		ordinary := easyTierTestIPPacket(2, 99, 1)
		copy(ordinary[16:20], []byte{192, 0, 2, 1})
		ordinary[10], ordinary[11] = 0, 0
		checksum := easyTierTestChecksum(ordinary[:20])
		ordinary[10], ordinary[11] = byte(checksum>>8), byte(checksum)
		packet := easyTierTestIPPacket(2, 1, 17)
		device.read <- packet
		device.read <- ordinary
		buffer := make([]byte, 65535)
		if n, err := mux.Read(buffer); err != nil || !bytes.Equal(buffer[:n], ordinary) {
			t.Fatalf("ordinary flow after recovery: %v", err)
		}
		packetCtx, done := context.WithTimeout(ctx, 5*time.Second)
		defer done()
		if got, err := peerCore.ReceivePacket(packetCtx); err != nil || !bytes.Equal(got, packet) {
			t.Fatalf("restored shared TUN -> peer: err=%v", err)
		}
		packet = easyTierTestIPPacket(1, 2, 17)
		if err := peerCore.SendPacket(packetCtx, packet); err != nil {
			t.Fatal(err)
		}
		select {
		case got := <-device.write:
			if !bytes.Equal(got, packet) {
				t.Fatal("restored peer -> shared TUN packet changed")
			}
		case <-packetCtx.Done():
			t.Fatal(packetCtx.Err())
		}
		query := new(D.Msg)
		query.SetQuestion("managed.et.net.", D.TypeA)
		reply, err := (easyTierDNSTransport{easytier: owner}).ExchangeContext(ctx, query)
		if err != nil || reply.Rcode != D.RcodeSuccess || len(reply.Answer) != 1 {
			t.Fatalf("restored Magic DNS: %v", err)
		}
		answer, ok := reply.Answer[0].(*D.A)
		if !ok || answer.A.String() != "10.144.0.2" {
			t.Fatalf("restored Magic DNS address: %v", reply.Answer)
		}
	}
	verifyPacketNetwork()
	t.Logf("initial managed network verified: machine=%s network=%s", machineID, networkID)
	previous := session.Endpoint
	preferPeerRelay = true
	config["prefer_peer_relay"] = preferPeerRelay
	_, status, err = request(http.MethodPut, base, map[string]any{
		"managed_network_configs": []map[string]any{{"instance_id": networkID, "network_config": config}},
		"config_revision":         "relay-" + networkID,
	})
	if err != nil || status != http.StatusNoContent {
		t.Fatalf("update desired peer relay preference: HTTP %d err=%v", status, err)
	}
	wait("peer relay preference hot patch", func() bool {
		owner.mu.Lock()
		host := owner.host
		owner.mu.Unlock()
		configurations := host.InstanceConfigurations()
		if len(configurations) != 1 {
			return false
		}
		var effective struct {
			Flags struct {
				PreferPeerRelay bool `toml:"prefer_peer_relay"`
			} `toml:"flags"`
		}
		return toml.Unmarshal([]byte(configurations[0].ConfigTOML), &effective) == nil && effective.Flags.PreferPeerRelay
	})
	verifyPacketNetwork()
	if session.Endpoint != previous {
		t.Fatal("peer relay preference hot patch replaced the managed core")
	}
	t.Logf("desired peer relay preference hot patch verified: network=%s", networkID)
	route = "10.212.0.0/24"
	config["routes"] = []string{route}
	_, status, err = request(http.MethodPut, base, map[string]any{
		"managed_network_configs": []map[string]any{{"instance_id": networkID, "network_config": config}},
		"config_revision":         "routes-" + networkID,
	})
	if err != nil || status != http.StatusNoContent {
		t.Fatalf("update desired manual route: HTTP %d err=%v", status, err)
	}
	wait("manual-route change replaces the managed core", func() bool {
		sessions, err := owner.OpenPacketSessions(ctx)
		return err == nil && len(sessions) == 1 && sessions[0].Endpoint != previous &&
			slices.Contains(sessions[0].Routes, netip.MustParsePrefix(route))
	})
	verifyPacketNetwork()
	t.Logf("desired route replacement verified: network=%s route=%s", networkID, route)
	server.stop(t)
	wait("WebClient disconnected from stopped server", func() bool { return !owner.EasyTierSummary().WebClientConnected })
	server.start(t)
	wait("restarted server API", serverReady)
	wait("same machine authenticated after server restart", authenticated)
	verifyPacketNetwork()
	t.Logf("server reconnect verified: machine=%s network=%s", machineID, networkID)
	previous = session.Endpoint
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	if err := mux.UpdateSessions(nil); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewEasyTier(option)
	if err != nil {
		t.Fatal(err)
	}
	owner = reopened
	if _, err := owner.OpenPacketSessions(ctx); err != nil {
		t.Fatal(err)
	}
	if owner.EasyTierSummary().InstanceID != machineID {
		t.Fatal("owner recreation changed its persisted generated machine identity")
	}
	wait("recreated owner authenticated with persisted machine identity", authenticated)
	verifyPacketNetwork()
	if session.Endpoint == previous {
		t.Fatal("owner recreation retained the old core endpoint")
	}
	t.Logf("owner identity and desired network restored: machine=%s network=%s", machineID, networkID)
	_, status, err = request(http.MethodDelete, base+"/"+networkID, nil)
	if err != nil || status != http.StatusOK {
		t.Fatalf("remove persisted recovery network: HTTP %d err=%v", status, err)
	}
}
