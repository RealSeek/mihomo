//go:build !no_easytier

package outbound

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"sync"
	"testing"
	"time"

	et "github.com/metacubex/mihomo/component/easytier"
	C "github.com/metacubex/mihomo/constant"
)

// The three nodes form client -> relay -> gateway. All overlay traffic uses
// loopback TCP; subnet and exit targets are ordinary local UDP sockets.
func TestEasyTierACLRelaySubnetAndExit(t *testing.T) {
	previousHome := C.Path.HomeDir()
	C.SetHomeDir(t.TempDir())
	t.Cleanup(func() { C.SetHomeDir(previousHome) })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	reservation, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	peerURL := "tcp://" + reservation.Addr().String()
	if err := reservation.Close(); err != nil {
		t.Fatal(err)
	}
	proxySocket, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer proxySocket.Close()
	exitSocket, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 1, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer exitSocket.Close()
	proxyPort := uint16(proxySocket.LocalAddr().(*net.UDPAddr).Port)
	exitPort := uint16(exitSocket.LocalAddr().(*net.UDPAddr).Port)

	disableP2P := true
	relay, err := NewEasyTier(EasyTierOption{
		Name: "acl-relay", PacketMode: true, NetworkName: "acl-route-test", NetworkSecret: "acl-secret",
		IPv4: "10.144.0.1/24", Listeners: []string{peerURL}, DisableP2P: &disableP2P,
		STUNServers: []string{}, STUNServersV6: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	if _, err := relay.OpenPacketSession(ctx); err != nil {
		t.Fatal(err)
	}

	// ACL fields are the official protobuf/serde schema. The enum fields are
	// integer values: Inbound=1, Forward=3, UDP=2, Allow=1, Drop=2.
	gatewayConfig := fmt.Sprintf(`ipv4 = "10.144.0.3/24"
hostname = "acl-gateway"
listeners = []
stun_servers = []
stun_servers_v6 = []

[network_identity]
network_name = "acl-route-test"
network_secret = "acl-secret"

[[peer]]
uri = %q

[[proxy_network]]
cidr = "127.0.0.0/24"
mapped_cidr = "198.18.77.0/24"

[acl.acl_v1]
[[acl.acl_v1.chains]]
name = "inbound"
chain_type = 1
enabled = true
default_action = 2
[[acl.acl_v1.chains.rules]]
name = "allow-control"
priority = 100
enabled = true
protocol = 2
ports = ["40002"]
source_ips = ["10.144.0.2/32"]
action = 1

[[acl.acl_v1.chains]]
name = "forward"
chain_type = 3
enabled = true
default_action = 2
[[acl.acl_v1.chains.rules]]
name = "allow-services"
priority = 100
enabled = true
protocol = 2
ports = ["%d", "%d"]
source_ips = ["10.144.0.2/32"]
action = 1

[flags]
mtu = 1380
use_smoltcp = true
enable_exit_node = true
disable_p2p = true
`, peerURL, proxyPort, exitPort)
	gateway, err := NewEasyTier(EasyTierOption{Name: "acl-gateway", PacketMode: true, ConfigTOML: gatewayConfig})
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	if _, err := gateway.OpenPacketSession(ctx); err != nil {
		t.Fatal(err)
	}
	client, err := NewEasyTier(EasyTierOption{
		Name: "acl-client", PacketMode: true, NetworkName: "acl-route-test", NetworkSecret: "acl-secret",
		IPv4: "10.144.0.2/24", Peers: []string{peerURL}, ExitNodes: []string{"10.144.0.3"}, DisableP2P: &disableP2P,
		STUNServers: []string{}, STUNServersV6: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.OpenPacketSession(ctx); err != nil {
		t.Fatal(err)
	}
	relayInstance, err := relay.currentInstance()
	if err != nil {
		t.Fatal(err)
	}
	relayInfo, err := relayInstance.ShowNodeInfo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, direction := range [][2]*EasyTier{{client, gateway}, {gateway, client}} {
		instance, err := direction[0].currentInstance()
		if err != nil {
			t.Fatal(err)
		}
		remoteInstance, err := direction[1].currentInstance()
		if err != nil {
			t.Fatal(err)
		}
		remoteInfo, err := remoteInstance.ShowNodeInfo(ctx)
		if err != nil {
			t.Fatal(err)
		}
		ticker := time.NewTicker(25 * time.Millisecond)
		for {
			routes, err := instance.ListRoute(ctx)
			if err != nil {
				t.Fatal(err)
			}
			ready := false
			for _, route := range routes {
				if route.GetPeerId() == remoteInfo.GetPeerId() && route.GetNextHopPeerId() == relayInfo.GetPeerId() && route.GetCost() == 2 {
					ready = true
					break
				}
			}
			if ready {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatalf("%s two-hop route to %s: %v; routes: %v", direction[0].Name(), direction[1].Name(), ctx.Err(), routes)
			case <-ticker.C:
			}
		}
		ticker.Stop()
	}
	clientSession, err := client.OpenPacketSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(clientSession.Routes, netip.MustParsePrefix("198.18.77.0/24")) || !slices.Contains(clientSession.Routes, netip.MustParsePrefix("0.0.0.0/0")) {
		t.Fatalf("missing advertised subnet or exit route: %v", clientSession.Routes)
	}
	device := &easyTierMemoryTun{make(chan []byte, 8), make(chan []byte, 8), make(chan struct{}), sync.Once{}}
	packetErrors := make(chan error, 4)
	mux, err := et.NewPacketMux(device, []et.PacketSession{clientSession}, false, nil, func(err error) { packetErrors <- err })
	if err != nil {
		t.Fatal(err)
	}
	defer mux.Close()
	go func() {
		buffer := make([]byte, 65535)
		if _, err := mux.Read(buffer); err != nil && !errors.Is(err, net.ErrClosed) {
			select {
			case packetErrors <- err:
			default:
			}
		}
	}()

	// A denied packet precedes the allowed control on the same TCP path. The
	// first delivered packet must be the allowed one, proving a live route and
	// the Inbound rule's port match as well as its default Drop action.
	blocked := easyTierRoutingUDP(netip.MustParseAddr("10.144.0.2"), netip.MustParseAddr("10.144.0.3"), 41001, 40003, []byte("deny-packet"))
	allowed := easyTierRoutingUDP(netip.MustParseAddr("10.144.0.2"), netip.MustParseAddr("10.144.0.3"), 41002, 40002, []byte("allow-packet"))
	device.read <- blocked
	device.read <- allowed
	got, err := gateway.ReceivePacket(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, allowed) {
		t.Fatalf("ACL delivered %x; want allowed control %x", got, allowed)
	}
	droppedCtx, droppedCancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer droppedCancel()
	if got, err := gateway.ReceivePacket(droppedCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("denied packet remained in the raw receive queue: %x, %v", got, err)
	}
	for _, target := range []struct {
		name        string
		address     netip.Addr
		port        uint16
		service     *net.UDPConn
		virtualPort uint16
	}{
		{"mapped-subnet", netip.MustParseAddr("198.18.77.1"), proxyPort, proxySocket, 42001},
		{"exit-node", netip.MustParseAddr("127.0.1.1"), exitPort, exitSocket, 42002},
	} {
		payload := []byte(target.name)
		device.read <- easyTierRoutingUDP(netip.MustParseAddr("10.144.0.2"), target.address, target.virtualPort, target.port, payload)
		if err := target.service.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
			t.Fatal(err)
		}
		buffer := make([]byte, 1024)
		n, source, err := target.service.ReadFromUDP(buffer)
		if err != nil || !bytes.Equal(buffer[:n], payload) {
			t.Fatalf("%s ordinary OS service: %q, %v", target.name, buffer[:n], err)
		}
		if _, err := target.service.WriteToUDP(buffer[:n], source); err != nil {
			t.Fatal(err)
		}
		select {
		case reply := <-device.write:
			if len(reply) < 28 || !bytes.Equal(reply[28:], payload) || binary.BigEndian.Uint16(reply[22:24]) != target.virtualPort || !bytes.Equal(reply[16:20], []byte{10, 144, 0, 2}) {
				t.Fatalf("%s return packet: %x", target.name, reply)
			}
		case err := <-packetErrors:
			t.Fatalf("%s raw packet error: %v", target.name, err)
		case <-ctx.Done():
			t.Fatalf("%s return packet: %v", target.name, ctx.Err())
		}
	}
}

func easyTierRoutingUDP(source, destination netip.Addr, sourcePort, destinationPort uint16, payload []byte) []byte {
	packet := make([]byte, 28+len(payload))
	packet[0], packet[8], packet[9] = 0x45, 64, 17
	binary.BigEndian.PutUint16(packet[2:4], uint16(len(packet)))
	copy(packet[12:16], source.AsSlice())
	copy(packet[16:20], destination.AsSlice())
	binary.BigEndian.PutUint16(packet[10:12], easyTierTestChecksum(packet[:20]))
	binary.BigEndian.PutUint16(packet[20:22], sourcePort)
	binary.BigEndian.PutUint16(packet[22:24], destinationPort)
	binary.BigEndian.PutUint16(packet[24:26], uint16(8+len(payload)))
	copy(packet[28:], payload)
	// A zero UDP checksum is valid for IPv4 and accepted by the real TUN path.
	return packet
}

func TestEasyTierConfiguredRoutesOverridePeerProxyCIDRs(t *testing.T) {
	previousHome := C.Path.HomeDir()
	C.SetHomeDir(t.TempDir())
	t.Cleanup(func() { C.SetHomeDir(previousHome) })
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	reservation, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	peerURL := "tcp://" + reservation.Addr().String()
	if err := reservation.Close(); err != nil {
		t.Fatal(err)
	}
	server, err := NewEasyTier(EasyTierOption{
		Name: "routes-server", PacketMode: true, NetworkName: "routes-test", NetworkSecret: "routes-secret",
		IPv4: "10.144.0.3/24", Listeners: []string{peerURL}, STUNServers: []string{}, STUNServersV6: []string{},
		ProxyNetworks: []string{"127.0.0.0/24"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if _, err := server.OpenPacketSession(ctx); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name       string
		routes     string
		wantManual netip.Prefix
	}{
		{name: "empty", routes: "[]", wantManual: netip.Prefix{}},
		{name: "explicit", routes: `["198.18.99.0/24"]`, wantManual: netip.MustParsePrefix("198.18.99.0/24")},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := fmt.Sprintf(`ipv4 = "10.144.0.2/24"
hostname = "routes-client"
listeners = []
stun_servers = []
stun_servers_v6 = []
routes = %s

[network_identity]
network_name = "routes-test"
network_secret = "routes-secret"

[[peer]]
uri = %q
`, test.routes, peerURL)
			client, err := NewEasyTier(EasyTierOption{Name: "routes-client-" + test.name, PacketMode: true, ConfigTOML: config})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			session, err := client.OpenPacketSession(ctx)
			if err != nil {
				t.Fatal(err)
			}
			clientInstance, err := client.currentInstance()
			if err != nil {
				t.Fatal(err)
			}
			serverInstance, err := server.currentInstance()
			if err != nil {
				t.Fatal(err)
			}
			serverInfo, err := serverInstance.ShowNodeInfo(ctx)
			if err != nil {
				t.Fatal(err)
			}
			// Wait until the peer has actually advertised its proxy CIDR. A
			// local lease alone is insufficient evidence for route ownership.
			ticker := time.NewTicker(25 * time.Millisecond)
			for {
				routes, err := clientInstance.ListRoute(ctx)
				if err != nil {
					t.Fatal(err)
				}
				advertised := false
				for _, route := range routes {
					if route.GetPeerId() == serverInfo.GetPeerId() && slices.Contains(route.GetProxyCidrs(), "127.0.0.0/24") {
						advertised = true
						break
					}
				}
				if advertised {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatalf("waiting for server proxy CIDR advertisement: %v", ctx.Err())
				case <-ticker.C:
				}
			}
			ticker.Stop()
			session, err = client.OpenPacketSession(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Contains(session.Addresses, netip.MustParsePrefix("10.144.0.2/24")) {
				t.Fatalf("local overlay address missing: %v", session.Addresses)
			}
			if !slices.Contains(session.Routes, netip.MustParsePrefix("10.144.0.0/24")) {
				t.Fatalf("local overlay route missing: %v", session.Routes)
			}
			if !slices.Contains(session.Routes, netip.MustParsePrefix("10.144.0.3/32")) {
				t.Fatalf("peer overlay host route missing: %v", session.Routes)
			}
			if slices.Contains(session.Routes, netip.MustParsePrefix("127.0.0.0/24")) {
				t.Fatalf("peer proxy CIDR leaked into explicitly configured routes: %v", session.Routes)
			}
			if test.wantManual.IsValid() && !slices.Contains(session.Routes, test.wantManual) {
				t.Fatalf("explicit route missing: %v", session.Routes)
			}
		})
	}
}
