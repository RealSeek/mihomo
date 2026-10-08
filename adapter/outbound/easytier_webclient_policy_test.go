//go:build windows && mihomo_integration && !no_easytier

package outbound

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"testing"
	"time"

	corehost "github.com/easytier/easytier/easytier-go"
	"github.com/gofrs/uuid/v5"
	et "github.com/metacubex/mihomo/component/easytier"
	C "github.com/metacubex/mihomo/constant"
	"github.com/pelletier/go-toml/v2"
)

func TestEasyTierWebClientServerACLAndPortForwardRecovery(t *testing.T) {
	previousHome := C.Path.HomeDir()
	C.SetHomeDir(t.TempDir())
	defer C.SetHomeDir(previousHome)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	server := newEasyTierWebTestServer(t, ctx, "_policy-")
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
	networkID := uuid.Must(uuid.NewV4()).String()
	option := EasyTierOption{
		Name: "policy-" + networkID, StateDir: "policy-state-" + networkID, PacketMode: true,
		WebClient: &EasyTierWebClientOption{Endpoint: server.endpoint, Hostname: "mihomo-policy"},
	}
	owner, err := NewEasyTier(option)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = owner.Close() }()
	if sessions, err := owner.OpenPacketSessions(ctx); err != nil || len(sessions) != 0 {
		t.Fatalf("start fresh managed owner: sessions=%d err=%v", len(sessions), err)
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
			t.Fatal("decode authenticated session identities")
		}
		for _, session := range sessions {
			if session.MachineID == machineID {
				if userID != 0 && session.UserID != userID {
					t.Fatal("reconnect changed the server user identity")
				}
				userID = session.UserID
				return owner.EasyTierSummary().WebClientConnected
			}
		}
		return false
	}
	wait("machine authentication", authenticated)
	peerAddress, _ := easyTierWebTestAddress(t)
	peerURL := "tcp://" + peerAddress
	peer, err := NewEasyTier(EasyTierOption{
		Name: "policy-peer-" + networkID, PacketMode: true, Hostname: "peer",
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
	echo, err := peerCore.Listen("tcp4", ":43000")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		for {
			connection, err := echo.Accept()
			if err != nil {
				return
			}
			go func() {
				defer connection.Close()
				_, _ = io.Copy(connection, connection)
			}()
		}
	}()
	forwardA, portA := easyTierWebTestAddress(t)
	forwardB, portB := easyTierWebTestAddress(t)
	allowedPort, blockedPort := uint16(40002), uint16(40003)
	acl := func(port uint16) map[string]any {
		return map[string]any{"acl_v1": map[string]any{"chains": []map[string]any{{
			"name": "inbound", "chain_type": 1, "enabled": true, "default_action": 2,
			"rules": []map[string]any{
				{"name": "allow-control", "priority": 100, "enabled": true, "protocol": 2,
					"ports": []string{fmt.Sprint(port)}, "source_ips": []string{"10.144.0.1/32"}, "action": 1},
				{"name": "allow-forward-replies", "priority": 90, "enabled": true, "protocol": 1, "action": 1},
			},
		}}}}
	}
	forward := func(port int) []map[string]any {
		return []map[string]any{{"proto": "tcp", "bind_ip": "127.0.0.1", "bind_port": port, "dst_ip": "10.144.0.1", "dst_port": 43000}}
	}
	config := map[string]any{
		"instance_id": networkID, "dhcp": false, "virtual_ipv4": "10.144.0.2", "network_length": 24,
		"hostname": "managed", "network_name": networkID, "network_secret": networkID, "networking_method": "Manual",
		"peer_urls": []string{peerURL}, "public_server_url": peerURL, "listener_urls": []string{}, "advanced_settings": true,
		"disable_p2p": true, "disable_ipv6": true, "no_tun": true, "bind_device": true, "mtu": 1380,
		"acl": acl(allowedPort), "port_forwards": forward(portA),
	}
	base := fmt.Sprintf("/api/internal/users/%d/machines/%s/networks", userID, machineID)
	_, status, err := request(http.MethodPost, base, map[string]any{"save": true, "config": config})
	if err != nil || status != http.StatusOK {
		t.Fatalf("persist managed ACL and port forward: HTTP %d err=%v", status, err)
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
	attach := func() {
		t.Helper()
		wait("managed packet network and peer route", func() bool {
			sessions, err := owner.OpenPacketSessions(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(sessions) != 1 || sessions[0].ID != networkID || !slices.Contains(sessions[0].Addresses, netip.MustParsePrefix("10.144.0.2/24")) {
				return false
			}
			session = sessions[0]
			for _, pair := range [][2]*corehost.Instance{{session.Endpoint.(*corehost.Instance), peerCore}, {peerCore, session.Endpoint.(*corehost.Instance)}} {
				routes, err := pair[0].ListRoute(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if !slices.ContainsFunc(routes, func(route *corehost.Route) bool { return route.GetInstId() == pair[1].ID() }) {
					return false
				}
			}
			return true
		})
		if err := mux.UpdateSessions([]et.PacketSession{session}); err != nil {
			t.Fatal(err)
		}
	}
	verifyACL := func(allow, deny uint16) {
		t.Helper()
		blocked := easyTierRoutingUDP(netip.MustParseAddr("10.144.0.1"), netip.MustParseAddr("10.144.0.2"), 41001, deny, []byte("denied"))
		allowed := easyTierRoutingUDP(netip.MustParseAddr("10.144.0.1"), netip.MustParseAddr("10.144.0.2"), 41002, allow, []byte("allowed"))
		packetCtx, done := context.WithTimeout(ctx, 5*time.Second)
		defer done()
		for _, packet := range [][]byte{blocked, allowed} {
			if err := peerCore.SendPacket(packetCtx, packet); err != nil {
				t.Fatal(err)
			}
		}
		select {
		case got := <-device.write:
			if !bytes.Equal(got, allowed) {
				t.Fatal("ACL delivered a denied packet before the allowed control")
			}
		case <-packetCtx.Done():
			t.Fatal("ACL allowed control did not reach the shared TUN")
		}
		select {
		case <-device.write:
			t.Fatal("ACL denied packet reached the shared TUN after the control")
		case <-time.After(300 * time.Millisecond):
		}
	}
	verifyForward := func(address, phase string) {
		t.Helper()
		payload := []byte("managed TCP forward " + phase)
		wait("ordinary TCP socket through managed port forward", func() bool {
			connection, err := net.DialTimeout("tcp4", address, 200*time.Millisecond)
			if err != nil {
				return false
			}
			defer connection.Close()
			_ = connection.SetDeadline(time.Now().Add(time.Second))
			if _, err := connection.Write(payload); err != nil {
				return false
			}
			reply := make([]byte, len(payload))
			_, err = io.ReadFull(connection, reply)
			return err == nil && bytes.Equal(reply, payload)
		})
	}
	verifyReleased := func(address string) {
		t.Helper()
		listener, err := net.Listen("tcp4", address)
		if err != nil {
			t.Fatalf("removed port-forward bind remains owned: %v", err)
		}
		if err := listener.Close(); err != nil {
			t.Fatal(err)
		}
	}
	update := func(revision string) {
		t.Helper()
		_, status, err := request(http.MethodPut, base, map[string]any{
			"managed_network_configs": []map[string]any{{"instance_id": networkID, "network_config": config}}, "config_revision": revision,
		})
		if err != nil || status != http.StatusNoContent {
			t.Fatalf("update desired policies: HTTP %d err=%v", status, err)
		}
	}
	waitPolicies := func(port int, allow uint16) {
		t.Helper()
		wait("effective ACL and port-forward TOML", func() bool {
			owner.mu.Lock()
			host := owner.host
			owner.mu.Unlock()
			configurations := host.InstanceConfigurations()
			if len(configurations) != 1 {
				return false
			}
			var effective struct {
				ACL struct {
					V1 struct {
						Chains []struct {
							DefaultAction string `toml:"default_action"`
							Rules         []struct {
								Ports []string `toml:"ports"`
							} `toml:"rules"`
						} `toml:"chains"`
					} `toml:"acl_v1"`
				} `toml:"acl"`
				Forwards []struct {
					Bind string `toml:"bind_addr"`
				} `toml:"port_forward"`
			}
			if err := toml.Unmarshal([]byte(configurations[0].ConfigTOML), &effective); err != nil {
				t.Fatal(err)
			}
			if port == 0 {
				return len(effective.Forwards) == 0 && len(effective.ACL.V1.Chains) == 0
			}
			return len(effective.Forwards) == 1 && effective.Forwards[0].Bind == net.JoinHostPort("127.0.0.1", fmt.Sprint(port)) &&
				len(effective.ACL.V1.Chains) == 1 && effective.ACL.V1.Chains[0].DefaultAction == "Drop" &&
				len(effective.ACL.V1.Chains[0].Rules) == 2 && slices.Equal(effective.ACL.V1.Chains[0].Rules[0].Ports, []string{fmt.Sprint(allow)})
		})
	}
	attach()
	waitPolicies(portA, allowedPort)
	verifyACL(allowedPort, blockedPort)
	verifyForward(forwardA, "created")
	initial := session.Endpoint
	allowedPort, blockedPort = blockedPort, allowedPort
	config["acl"], config["port_forwards"] = acl(allowedPort), forward(portB)
	update("hot-policy-" + networkID)
	waitPolicies(portB, allowedPort)
	attach()
	if session.Endpoint != initial {
		t.Fatal("ACL and port-forward hot update replaced the managed core")
	}
	verifyACL(allowedPort, blockedPort)
	verifyForward(forwardB, "hot-updated")
	verifyReleased(forwardA)
	server.stop(t)
	wait("WebClient disconnect", func() bool { return !owner.EasyTierSummary().WebClientConnected })
	server.start(t)
	wait("restarted server API", serverReady)
	wait("same machine reauthentication", authenticated)
	attach()
	waitPolicies(portB, allowedPort)
	verifyACL(allowedPort, blockedPort)
	verifyForward(forwardB, "server-restarted")
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	if err := mux.UpdateSessions(nil); err != nil {
		t.Fatal(err)
	}
	owner, err = NewEasyTier(option)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.OpenPacketSessions(ctx); err != nil {
		t.Fatal(err)
	}
	if owner.EasyTierSummary().InstanceID != machineID {
		t.Fatal("owner recreation changed the persisted machine identity")
	}
	wait("recreated owner authentication", authenticated)
	attach()
	if session.Endpoint == initial {
		t.Fatal("owner recreation retained the previous core endpoint")
	}
	waitPolicies(portB, allowedPort)
	verifyACL(allowedPort, blockedPort)
	verifyForward(forwardB, "owner-recreated")
	verifyReleased(forwardA)
	restored := session.Endpoint
	config["acl"] = map[string]any{"acl_v1": map[string]any{"chains": []any{}}}
	config["port_forwards"] = []any{}
	update("clear-policy-" + networkID)
	waitPolicies(0, 0)
	attach()
	if session.Endpoint != restored {
		t.Fatal("ACL and port-forward CLEAR replaced the restored core")
	}
	verifyReleased(forwardB)
	packet := easyTierRoutingUDP(netip.MustParseAddr("10.144.0.1"), netip.MustParseAddr("10.144.0.2"), 41003, blockedPort, []byte("ACL cleared"))
	if err := peerCore.SendPacket(ctx, packet); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-device.write:
		if !bytes.Equal(got, packet) {
			t.Fatal("clearing ACL did not restore the previously denied packet")
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	_, status, err = request(http.MethodDelete, base+"/"+networkID, nil)
	if err != nil || status != http.StatusOK {
		t.Fatalf("remove persisted policy network: HTTP %d err=%v", status, err)
	}
	t.Log("saved ACL and ordinary TCP forwarding passed create, hot update, server restart, owner recreation, and CLEAR")
}
