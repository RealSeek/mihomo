//go:build windows && mihomo_integration && !no_easytier

package outbound

import (
	"bytes"
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
	"github.com/gofrs/uuid/v5"
	et "github.com/metacubex/mihomo/component/easytier"
	C "github.com/metacubex/mihomo/constant"
	D "github.com/miekg/dns"
)

func TestEasyTierWebClientServerMultipleNetworks(t *testing.T) {
	previousHome := C.Path.HomeDir()
	C.SetHomeDir(t.TempDir())
	defer C.SetHomeDir(previousHome)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	server := newEasyTierWebTestServer(t, ctx, "_multinetwork-")
	request := server.request
	peer1Address, _ := easyTierWebTestAddress(t)
	peer2Address, _ := easyTierWebTestAddress(t)
	machineID := uuid.Must(uuid.NewV4()).String()
	network1ID, network2ID := uuid.Must(uuid.NewV4()).String(), uuid.Must(uuid.NewV4()).String()
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
	server.start(t)
	wait("isolated configuration-server API", func() bool {
		_, status, err := request(http.MethodGet, "/api/internal/sessions", nil)
		return err == nil && status == http.StatusOK
	})
	option := EasyTierOption{
		Name: "multi-" + machineID, StateDir: "multinetwork-state-" + machineID, PacketMode: true,
		WebClient: &EasyTierWebClientOption{Endpoint: server.endpoint, MachineID: machineID, Hostname: "multi-host"},
	}
	owner, err := NewEasyTier(option)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = owner.Close() }()
	if _, err := owner.OpenPacketSessions(ctx); err != nil {
		t.Fatal(err)
	}
	userID := 0
	wait("authenticated multi-network machine", func() bool {
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
				userID = session.UserID
				return true
			}
		}
		return false
	})
	base := fmt.Sprintf("/api/internal/users/%d/machines/%s/networks", userID, machineID)
	peer1, err := NewEasyTier(EasyTierOption{
		Name: "multi-peer-1-" + network1ID, PacketMode: true, Hostname: "peer-alpha",
		NetworkName: network1ID, NetworkSecret: network1ID, IPv4: "10.144.0.1/24",
		Listeners: []string{"tcp://" + peer1Address}, STUNServers: []string{}, STUNServersV6: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer peer1.Close()
	if _, err := peer1.OpenPacketSession(ctx); err != nil {
		t.Fatal(err)
	}
	peer1Core, err := peer1.currentInstance()
	if err != nil {
		t.Fatal(err)
	}
	peer2, err := NewEasyTier(EasyTierOption{
		Name: "multi-peer-2-" + network2ID, PacketMode: true, Hostname: "peer-bravo",
		NetworkName: network2ID, NetworkSecret: network2ID, IPv4: "10.145.0.1/24",
		Listeners: []string{"tcp://" + peer2Address}, STUNServers: []string{}, STUNServersV6: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer peer2.Close()
	if _, err := peer2.OpenPacketSession(ctx); err != nil {
		t.Fatal(err)
	}
	peer2Core, err := peer2.currentInstance()
	if err != nil {
		t.Fatal(err)
	}
	config := func(id, name, hostname, ipv4, peerURL string) map[string]any {
		return map[string]any{
			"instance_id": id, "dhcp": false, "virtual_ipv4": ipv4, "network_length": 24,
			"hostname": hostname, "network_name": name, "network_secret": name, "networking_method": "Manual",
			"peer_urls": []string{peerURL}, "public_server_url": peerURL, "listener_urls": []string{},
			"advanced_settings": true, "disable_p2p": true, "disable_ipv6": true,
			"no_tun": true, "bind_device": true, "mtu": 1380,
		}
	}
	config1 := config(network1ID, network1ID, "alpha", "10.144.0.2", "tcp://"+peer1Address)
	config2 := config(network2ID, network2ID, "bravo", "10.145.0.2", "tcp://"+peer2Address)
	expect := func(method, path string, payload any, want int) {
		t.Helper()
		body, status, err := request(method, path, payload)
		if err != nil || status != want {
			t.Fatalf("%s managed request: HTTP %d want %d err=%v body=%s", method, status, want, err, body)
		}
	}
	expect(http.MethodPost, base, map[string]any{"config": config1, "save": true}, http.StatusOK)
	expect(http.MethodPost, base, map[string]any{"config": config2, "save": true}, http.StatusOK)
	var sessions map[string]et.PacketSession
	openNetworks := func() bool {
		opened, err := owner.OpenPacketSessions(ctx)
		if err != nil || len(opened) != 2 {
			return false
		}
		next := make(map[string]et.PacketSession, len(opened))
		for _, session := range opened {
			next[session.ID] = session
		}
		if !slices.Contains(next[network1ID].Addresses, netip.MustParsePrefix("10.144.0.2/24")) || !slices.Contains(next[network2ID].Addresses, netip.MustParsePrefix("10.145.0.2/24")) {
			return false
		}
		sessions = next
		return true
	}
	wait("two independent managed packet sessions", openNetworks)
	waitRoutes := func(networkID string, peer *corehost.Instance) {
		t.Helper()
		managed := sessions[networkID].Endpoint.(*corehost.Instance)
		for _, pair := range [][2]*corehost.Instance{{managed, peer}, {peer, managed}} {
			info, err := pair[1].ShowNodeInfo(ctx)
			if err != nil {
				t.Fatal(err)
			}
			wait("independent network peer route", func() bool {
				routes, err := pair[0].ListRoute(ctx)
				if err != nil {
					return false
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
	waitRoutes(network1ID, peer1Core)
	waitRoutes(network2ID, peer2Core)
	if !openNetworks() {
		t.Fatal("refresh independent packet routes")
	}
	device := &easyTierMemoryTun{read: make(chan []byte, 8), write: make(chan []byte, 8), closed: make(chan struct{})}
	mux, err := et.NewPacketMux(device, []et.PacketSession{sessions[network1ID], sessions[network2ID]}, false, nil, func(err error) { t.Logf("multi-network packet receiver: %v", err) })
	if err != nil {
		t.Fatal(err)
	}
	defer mux.Close()
	stopMux := context.AfterFunc(ctx, func() { _ = mux.Close() })
	defer stopMux()
	sendPacket := func(networkID string, peer *corehost.Instance, sourceIP, peerIP [4]byte) {
		t.Helper()
		packet := easyTierWebNetworkPacket(sourceIP, peerIP)
		ordinary := easyTierTestIPPacket(9, 99, 1)
		copy(ordinary[16:20], []byte{192, 0, 2, 1})
		ordinary[10], ordinary[11] = 0, 0
		ordinaryChecksum := easyTierTestChecksum(ordinary[:20])
		ordinary[10], ordinary[11] = byte(ordinaryChecksum>>8), byte(ordinaryChecksum)
		device.read <- packet
		device.read <- ordinary
		buffer := make([]byte, 65535)
		if n, err := mux.Read(buffer); err != nil || !bytes.Equal(buffer[:n], ordinary) {
			t.Fatalf("ordinary traffic with network %s: %v", networkID, err)
		}
		packetCtx, done := context.WithTimeout(ctx, 5*time.Second)
		defer done()
		if got, err := peer.ReceivePacket(packetCtx); err != nil || !bytes.Equal(got, packet) {
			t.Fatalf("network %s shared TUN -> peer: %v", networkID, err)
		}
		packet = easyTierWebNetworkPacket(peerIP, sourceIP)
		if err := peer.SendPacket(packetCtx, packet); err != nil {
			t.Fatal(err)
		}
		select {
		case got := <-device.write:
			if !bytes.Equal(got, packet) {
				t.Fatalf("network %s peer -> shared TUN changed packet", networkID)
			}
		case <-packetCtx.Done():
			t.Fatal(packetCtx.Err())
		}
	}
	sendPacket(network1ID, peer1Core, [4]byte{10, 144, 0, 2}, [4]byte{10, 144, 0, 1})
	sendPacket(network2ID, peer2Core, [4]byte{10, 145, 0, 2}, [4]byte{10, 145, 0, 1})
	dnsTransport := easyTierDNSTransport{easytier: owner}
	dnsAddresses := func(name string, addresses ...string) {
		t.Helper()
		query := new(D.Msg)
		query.SetQuestion(name+".et.net.", D.TypeA)
		slices.Sort(addresses)
		wait("Magic DNS "+name+" addresses", func() bool {
			reply, err := dnsTransport.ExchangeContext(ctx, query)
			if err != nil || reply.Rcode != D.RcodeSuccess {
				return false
			}
			var actual []string
			for _, record := range reply.Answer {
				answer, ok := record.(*D.A)
				if !ok {
					t.Fatalf("Magic DNS %s record: %v", name, record)
				}
				actual = append(actual, answer.A.String())
			}
			slices.Sort(actual)
			return slices.Equal(actual, addresses)
		})
	}
	dnsAddresses("alpha", "10.144.0.2")
	dnsAddresses("bravo", "10.145.0.2")
	previousSecond := sessions[network2ID].Endpoint
	config2["hostname"] = "alpha"
	expect(http.MethodPut, base, map[string]any{
		"managed_network_configs": []map[string]any{{"instance_id": network1ID, "network_config": config1}, {"instance_id": network2ID, "network_config": config2}},
		"config_revision":         "rename-" + network2ID,
	}, http.StatusNoContent)
	dnsAddresses("alpha", "10.144.0.2", "10.145.0.2")
	if !openNetworks() || sessions[network2ID].Endpoint != previousSecond {
		t.Fatal("hostname hot patch replaced the second network endpoint")
	}
	config2["hostname"] = "bravo"
	expect(http.MethodPut, base, map[string]any{
		"managed_network_configs": []map[string]any{{"instance_id": network1ID, "network_config": config1}, {"instance_id": network2ID, "network_config": config2}},
		"config_revision":         "restore-" + network2ID,
	}, http.StatusNoContent)
	dnsAddresses("bravo", "10.145.0.2")
	dnsAddresses("alpha", "10.144.0.2")
	if !openNetworks() || sessions[network2ID].Endpoint != previousSecond {
		t.Fatal("hostname restore replaced the second network endpoint")
	}
	if err := mux.UpdateSessions([]et.PacketSession{sessions[network1ID], sessions[network2ID]}); err != nil {
		t.Fatal(err)
	}
	expect(http.MethodDelete, base+"/"+network1ID, nil, http.StatusOK)
	wait("first network deletion", func() bool {
		opened, err := owner.OpenPacketSessions(ctx)
		if err != nil || len(opened) != 1 || opened[0].ID != network2ID {
			return false
		}
		sessions = map[string]et.PacketSession{network2ID: opened[0]}
		return true
	})
	if err := mux.UpdateSessions([]et.PacketSession{sessions[network2ID]}); err != nil {
		t.Fatal(err)
	}
	dnsAddresses("bravo", "10.145.0.2")
	query := new(D.Msg)
	query.SetQuestion("alpha.et.net.", D.TypeA)
	if reply, err := dnsTransport.ExchangeContext(ctx, query); err != nil || reply.Rcode != D.RcodeNameError || len(reply.Answer) != 0 {
		t.Fatalf("deleted network hostname: %v err=%v", reply, err)
	}
	sendPacket(network2ID, peer2Core, [4]byte{10, 145, 0, 2}, [4]byte{10, 145, 0, 1})
	previousEndpoint := sessions[network2ID].Endpoint
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
	dnsTransport.easytier = owner
	wait("recreated owner's persisted second network", func() bool {
		opened, err := owner.OpenPacketSessions(ctx)
		if err != nil || len(opened) != 1 || opened[0].ID != network2ID {
			return false
		}
		sessions = map[string]et.PacketSession{network2ID: opened[0]}
		return true
	})
	if sessions[network2ID].Endpoint == previousEndpoint {
		t.Fatal("owner recreation retained the old core endpoint")
	}
	waitRoutes(network2ID, peer2Core)
	opened, err := owner.OpenPacketSessions(ctx)
	if err != nil || len(opened) != 1 || opened[0].ID != network2ID {
		t.Fatalf("refresh recreated owner's packet routes: %v", err)
	}
	sessions[network2ID] = opened[0]
	if err := mux.UpdateSessions([]et.PacketSession{sessions[network2ID]}); err != nil {
		t.Fatal(err)
	}
	sendPacket(network2ID, peer2Core, [4]byte{10, 145, 0, 2}, [4]byte{10, 145, 0, 1})
	dnsAddresses("bravo", "10.145.0.2")
	expect(http.MethodDelete, base+"/"+network2ID, nil, http.StatusOK)
	t.Logf("multi-network lifecycle verified: machine=%s networks=%s,%s", machineID, network1ID, network2ID)
}

func easyTierWebNetworkPacket(source, destination [4]byte) []byte {
	packet := easyTierTestIPPacket(2, 1, 17)
	copy(packet[12:16], source[:])
	copy(packet[16:20], destination[:])
	packet[10], packet[11], packet[26], packet[27] = 0, 0, 0, 0
	pseudo := make([]byte, 12+len(packet)-20)
	copy(pseudo[:8], packet[12:20])
	pseudo[9] = 17
	binary.BigEndian.PutUint16(pseudo[10:12], uint16(len(packet)-20))
	copy(pseudo[12:], packet[20:])
	binary.BigEndian.PutUint16(packet[26:28], easyTierTestChecksum(pseudo))
	binary.BigEndian.PutUint16(packet[10:12], easyTierTestChecksum(packet[:20]))
	return packet
}
