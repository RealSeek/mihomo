//go:build mihomo_integration && !no_easytier

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
	"os"
	"slices"
	"strconv"
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

// Run from the root module with an official server supporting managed PUT/PATCH.
// The server protocol, REST API, and internal token are supplied explicitly.
func TestEasyTierWebClientServerPacketMux(t *testing.T) {
	endpoint, apiURL, auth := os.Getenv("EASYTIER_WEB_E2E_ENDPOINT"), os.Getenv("EASYTIER_WEB_E2E_API"), os.Getenv("EASYTIER_WEB_E2E_AUTH")
	if endpoint == "" || apiURL == "" || auth == "" {
		t.Fatal("set EASYTIER_WEB_E2E_ENDPOINT, EASYTIER_WEB_E2E_API, and EASYTIER_WEB_E2E_AUTH for the official configuration server")
	}
	userID := 0
	if value := os.Getenv("EASYTIER_WEB_E2E_USER_ID"); value != "" {
		var err error
		userID, err = strconv.Atoi(value)
		if err != nil || userID <= 0 {
			t.Fatal("EASYTIER_WEB_E2E_USER_ID must be a positive integer when provided")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	previousHome := C.Path.HomeDir()
	C.SetHomeDir(t.TempDir())
	defer C.SetHomeDir(previousHome)
	machineID, networkID := uuid.Must(uuid.NewV4()).String(), uuid.Must(uuid.NewV4()).String()
	networkName, hostname := "mihomo-e2e-"+networkID, "managed-"+networkID[:8]
	client := &http.Client{Timeout: 10 * time.Second}
	request := func(requestCtx context.Context, method, path string, payload any) ([]byte, int, error) {
		var body []byte
		var err error
		if payload != nil {
			body, err = json.Marshal(payload)
			if err != nil {
				return nil, 0, err
			}
		}
		req, err := http.NewRequestWithContext(requestCtx, method, strings.TrimRight(apiURL, "/")+path, bytes.NewReader(body))
		if err != nil {
			return nil, 0, err
		}
		req.Header.Set("X-Internal-Auth", auth)
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		response, err := client.Do(req)
		if err != nil {
			return nil, 0, err
		}
		defer response.Body.Close()
		body, err = io.ReadAll(response.Body)
		return body, response.StatusCode, err
	}
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
	owner, err := NewEasyTier(EasyTierOption{
		Name: "web-" + machineID, PacketMode: true,
		WebClient: &EasyTierWebClientOption{Endpoint: endpoint, MachineID: machineID, Hostname: "mihomo-e2e"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if sessions, err := owner.OpenPacketSessions(ctx); err != nil || len(sessions) != 0 {
		t.Fatalf("start empty managed owner: sessions=%d err=%v", len(sessions), err)
	}
	wait("this machine's authenticated server session", func() bool {
		body, status, err := request(ctx, http.MethodGet, "/api/internal/sessions", nil)
		if err != nil {
			return false
		}
		if status != http.StatusOK {
			t.Fatalf("list authenticated sessions: HTTP %d", status)
		}
		// Session responses also contain private tokens; decode only identity.
		var sessions []struct {
			MachineID string `json:"machine_id"`
			UserID    int    `json:"user_id"`
		}
		if err := json.Unmarshal(body, &sessions); err != nil {
			t.Fatalf("decode authenticated session identities: %v", err)
		}
		for _, session := range sessions {
			if session.MachineID == machineID {
				if userID != 0 && userID != session.UserID {
					t.Fatalf("configured user ID %d differs from this machine's user %d", userID, session.UserID)
				}
				userID = session.UserID
				return true
			}
		}
		return false
	})
	base := fmt.Sprintf("/api/internal/users/%d/machines/%s/networks", userID, machineID)
	defer func() {
		cleanupCtx, done := context.WithTimeout(context.Background(), 15*time.Second)
		defer done()
		for _, operation := range []struct {
			method, path string
			payload      any
		}{
			{http.MethodPut, base, map[string]any{"managed_network_configs": []any{}, "config_revision": "cleanup-" + networkID}},
			{http.MethodDelete, base + "/" + networkID, nil},
		} {
			_, status, err := request(cleanupCtx, operation.method, operation.path, operation.payload)
			if err != nil || status != http.StatusOK && status != http.StatusNoContent && status != http.StatusNotFound {
				t.Errorf("clean up this test's network: %s HTTP %d err=%v", operation.method, status, err)
			}
		}
	}()
	reservation, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	peerURL := "tcp://" + reservation.Addr().String()
	if err := reservation.Close(); err != nil {
		t.Fatal(err)
	}
	peer, err := NewEasyTier(EasyTierOption{
		Name: "peer-" + networkID, PacketMode: true, Hostname: "peer",
		NetworkName: networkName, NetworkSecret: networkID, IPv4: "10.144.0.1/24",
		Listeners: []string{peerURL}, STUNServers: []string{}, STUNServersV6: []string{},
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
	config := map[string]any{
		"instance_id": networkID, "dhcp": false, "virtual_ipv4": "10.144.0.2", "network_length": 24,
		"hostname": hostname, "network_name": networkName, "network_secret": networkID,
		"networking_method": "Manual", "peer_urls": []string{peerURL}, "public_server_url": peerURL,
		"listener_urls": []string{}, "advanced_settings": true, "disable_p2p": true, "disable_ipv6": true,
		"mtu": 1380, "no_tun": true, "bind_device": true, "enable_exit_node": false,
		"enable_manual_routes": true, "routes": []string{"10.211.0.0/24"},
		"proxy_cidrs": []string{"10.210.0.0/24"},
	}
	expect := func(method string, payload any, want int) {
		t.Helper()
		body, status, err := request(ctx, method, base, payload)
		if err != nil || status != want {
			t.Fatalf("%s managed network: HTTP %d want %d err=%v body=%s", method, status, want, err, body)
		}
	}
	expect(http.MethodPost, map[string]any{"config": config, "save": true}, http.StatusOK)
	var session et.PacketSession
	openSession := func(address string) bool {
		sessions, err := owner.OpenPacketSessions(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(sessions) == 1 && sessions[0].ID == networkID && slices.Contains(sessions[0].Addresses, netip.MustParsePrefix(address)) {
			session = sessions[0]
			return true
		}
		return false
	}
	wait("HTTP-created managed packet session", func() bool { return openSession("10.144.0.2/24") })
	first := session.Endpoint.(*corehost.Instance)
	checkMetadata := func() {
		t.Helper()
		networks, err := owner.PacketNetworks()
		if err != nil || len(networks) != 1 {
			t.Fatalf("managed metadata: networks=%d err=%v", len(networks), err)
		}
		metadata := networks[0].Config
		if networks[0].ID != networkID || metadata.Routes == nil || !slices.Equal(*metadata.Routes, []string{"10.211.0.0/24"}) || metadata.EnableExitNode != nil && *metadata.EnableExitNode {
			t.Fatalf("managed effective routing config mismatch: %+v", metadata)
		}
		if !slices.Contains(session.Routes, netip.MustParsePrefix("10.211.0.0/24")) || !slices.Contains(session.Routes, netip.MustParsePrefix("10.144.0.0/24")) {
			t.Fatalf("manual routes not reflected in shared session: %v", session.Routes)
		}
		owner.mu.Lock()
		host := owner.host
		owner.mu.Unlock()
		var flags struct {
			Flags struct {
				NoTun      *bool `toml:"no_tun"`
				BindDevice *bool `toml:"bind_device"`
			} `toml:"flags"`
		}
		if err := toml.Unmarshal([]byte(host.InstanceConfigurations()[0].ConfigTOML), &flags); err != nil || flags.Flags.NoTun != nil && *flags.Flags.NoTun || flags.Flags.BindDevice == nil || *flags.Flags.BindDevice {
			t.Fatalf("effective config escaped shared packet ownership: %+v err=%v", flags.Flags, err)
		}
	}
	checkMetadata()
	waitRoutes := func() {
		managed := session.Endpoint.(*corehost.Instance)
		for _, pair := range [][2]*corehost.Instance{{managed, peerCore}, {peerCore, managed}} {
			info, err := pair[1].ShowNodeInfo(ctx)
			if err != nil {
				t.Fatal(err)
			}
			wait("current core peer route", func() bool {
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
	}
	waitRoutes()
	if !openSession("10.144.0.2/24") {
		t.Fatal("created packet session disappeared after peer discovery")
	}
	device := &easyTierMemoryTun{read: make(chan []byte, 4), write: make(chan []byte, 4), closed: make(chan struct{})}
	mux, err := et.NewPacketMux(device, nil, false, nil, func(err error) { t.Logf("packet receiver: %v", err) })
	if err != nil {
		t.Fatal(err)
	}
	defer mux.Close()
	stopMux := context.AfterFunc(ctx, func() { _ = mux.Close() })
	defer stopMux()
	if err := mux.UpdateSessions([]et.PacketSession{session}); err != nil {
		t.Fatal(err)
	}
	ordinary := easyTierTestIPPacket(2, 99, 1)
	copy(ordinary[16:20], []byte{192, 0, 2, 1})
	ordinary[10], ordinary[11] = 0, 0
	checksum := easyTierTestChecksum(ordinary[:20])
	ordinary[10], ordinary[11] = byte(checksum>>8), byte(checksum)
	exchange := func(managedIP byte) {
		t.Helper()
		packet := easyTierTestIPPacket(managedIP, 1, 17)
		device.read <- packet
		device.read <- ordinary
		buffer := make([]byte, 65535)
		if n, err := mux.Read(buffer); err != nil || !bytes.Equal(buffer[:n], ordinary) {
			t.Fatalf("ordinary dispatch: %v", err)
		}
		packetCtx, done := context.WithTimeout(ctx, 5*time.Second)
		defer done()
		if got, err := peerCore.ReceivePacket(packetCtx); err != nil || !bytes.Equal(got, packet) {
			t.Fatalf("shared TUN -> actual peer: %x err=%v", got, err)
		}
		packet = easyTierTestIPPacket(1, managedIP, 17)
		if err := peerCore.SendPacket(packetCtx, packet); err != nil {
			t.Fatal(err)
		}
		select {
		case got := <-device.write:
			if !bytes.Equal(got, packet) {
				t.Fatalf("actual peer -> shared TUN: %x", got)
			}
		case <-packetCtx.Done():
			t.Fatal(packetCtx.Err())
		}
	}
	dnsTransport := easyTierDNSTransport{easytier: owner}
	checkDNS := func(name, address string) {
		t.Helper()
		query := new(D.Msg)
		query.SetQuestion(name+".et.net.", D.TypeA)
		reply, err := dnsTransport.ExchangeContext(ctx, query)
		if err != nil || reply.Rcode != D.RcodeSuccess || len(reply.Answer) != 1 {
			t.Fatalf("managed Magic DNS: %v err=%v", reply, err)
		}
		answer, ok := reply.Answer[0].(*D.A)
		if !ok || answer.A.String() != address {
			t.Fatalf("managed Magic DNS address: %v", reply.Answer)
		}
	}
	exchange(2)
	checkDNS(hostname, "10.144.0.2")
	update := func(revision string) {
		expect(http.MethodPut, map[string]any{
			"managed_network_configs": []map[string]any{{"instance_id": networkID, "network_config": config}},
			"config_revision":         revision,
		}, http.StatusNoContent)
	}
	config["disable_relay_data"] = true
	config["proxy_cidrs"] = []string{"10.212.0.0/24"}
	update("patch-" + networkID)
	wait("effective hot-patched TOML", func() bool {
		owner.mu.Lock()
		host := owner.host
		owner.mu.Unlock()
		for _, snapshot := range host.InstanceConfigurations() {
			if snapshot.InstanceID == networkID {
				var effective struct {
					Flags struct {
						DisableRelayData bool `toml:"disable_relay_data"`
					} `toml:"flags"`
				}
				if err := toml.Unmarshal([]byte(snapshot.ConfigTOML), &effective); err != nil {
					t.Fatal(err)
				}
				return effective.Flags.DisableRelayData
			}
		}
		return false
	})
	if !openSession("10.144.0.2/24") || session.Endpoint != first {
		t.Fatal("HTTP hot patch replaced the packet endpoint")
	}
	checkMetadata()
	if err := mux.UpdateSessions([]et.PacketSession{session}); err != nil {
		t.Fatal(err)
	}
	exchange(2)
	oldHostname := hostname
	hostname = "replaced-" + networkID[:8]
	config["hostname"], config["virtual_ipv4"] = hostname, "10.144.0.3"
	update("replace-" + networkID)
	wait("same UUID core replacement", func() bool { return openSession("10.144.0.3/24") && session.Endpoint != first })
	checkMetadata()
	waitRoutes()
	if !openSession("10.144.0.3/24") {
		t.Fatal("replacement packet session disappeared after peer discovery")
	}
	if err := mux.UpdateSessions([]et.PacketSession{session}); err != nil {
		t.Fatal(err)
	}
	exchange(3)
	checkDNS(hostname, "10.144.0.3")
	query := new(D.Msg)
	query.SetQuestion(oldHostname+".et.net.", D.TypeA)
	if reply, err := dnsTransport.ExchangeContext(ctx, query); err != nil || reply.Rcode != D.RcodeNameError {
		t.Fatalf("stale managed DNS name after replacement: %v err=%v", reply, err)
	}
	body, status, err := request(ctx, http.MethodDelete, base+"/"+networkID, nil)
	if err != nil || status != http.StatusOK {
		t.Fatalf("delete managed network: HTTP %d err=%v body=%s", status, err, body)
	}
	wait("deleted network sessions", func() bool {
		sessions, err := owner.OpenPacketSessions(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return len(sessions) == 0
	})
	if err := mux.UpdateSessions(nil); err != nil {
		t.Fatal(err)
	}
	packet := easyTierTestIPPacket(3, 1, 17)
	device.read <- packet
	buffer := make([]byte, 65535)
	if n, err := mux.Read(buffer); err != nil || !bytes.Equal(buffer[:n], packet) {
		t.Fatalf("deleted overlay route still intercepted: %v", err)
	}
	if networks, err := dnsTransport.EasyTierDNSNetworks(ctx); err != nil || len(networks) != 0 {
		t.Fatalf("deleted DNS network metadata: %v err=%v", networks, err)
	}
	query.SetQuestion(hostname+".et.net.", D.TypeA)
	if reply, err := dnsTransport.ExchangeContext(ctx, query); err != nil || reply.Rcode != D.RcodeNameError || len(reply.Answer) != 0 {
		t.Fatalf("deleted managed DNS name: %v err=%v", reply, err)
	}
}
