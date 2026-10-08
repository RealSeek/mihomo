//go:build (linux || android || (windows && amd64)) && !no_fake_tcp

package easytier

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/netip"
	"os"
	"testing"
	"time"

	corehost "github.com/easytier/easytier/easytier-go"
	"github.com/easytier/easytier/easytier-go/platform"
	"github.com/metacubex/gopacket"
	"github.com/metacubex/gopacket/layers"
	"github.com/metacubex/mihomo/component/dialer"
	"github.com/metacubex/mihomo/component/resolver"
)

type fakeTCPCoreSockets struct {
	SocketFactory
	bound chan net.Addr
}

func (s fakeTCPCoreSockets) ListenFakeTCP(ctx context.Context, options platform.TCPListenOptions) (net.Listener, error) {
	listener, err := s.SocketFactory.ListenFakeTCP(ctx, options)
	if err == nil {
		s.bound <- listener.Addr()
	}
	return listener, err
}

func TestFakeTCPCoreUnderlayAndOverlay(t *testing.T) {
	if os.Getenv("EASYTIER_FAKETCP_RAW_TEST") != "1" {
		t.Skip("set EASYTIER_FAKETCP_RAW_TEST=1 to exercise core FakeTCP over real raw sockets")
	}
	previousIPv6Policy := resolver.DisableIPv6
	resolver.DisableIPv6 = false
	t.Cleanup(func() { resolver.DisableIPv6 = previousIPv6Policy })
	t.Logf("embedded core SHA256 %s", corehost.CoreInfo().SHA256)
	for _, address := range []string{"127.0.0.1", "::1"} {
		t.Run(address, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			services, err := Services(dialer.NewDialer())
			if err != nil {
				t.Fatal(err)
			}
			sockets := fakeTCPCoreSockets{SocketFactory: services.Sockets.(SocketFactory), bound: make(chan net.Addr, 4)}
			services.Sockets = sockets
			host, err := corehost.New(ctx, corehost.Options{Platform: services})
			if err != nil {
				t.Fatal(err)
			}
			defer host.Close(context.Background())
			configuration := func(id int, endpoint string, listen bool) string {
				transports := fmt.Sprintf("[[peer]]\nuri = %q\n", endpoint)
				if listen {
					transports = ""
				}
				listeners := "[]"
				if listen {
					listeners = fmt.Sprintf("[%q]", endpoint)
				}
				return fmt.Sprintf(`hostname = "faketcp-core-%d"
ipv4 = "10.145.0.%d/24"
ipv6 = "fd00:145::%d/64"
listeners = %s
stun_servers = []
stun_servers_v6 = []
%s
[network_identity]
network_name = "faketcp-core-fixture"
network_secret = "faketcp-core-fixture-secret"
[flags]
no_tun = false
disable_p2p = true
enable_encryption = false
`, id, id, id, listeners, transports)
			}
			server, err := host.CreateInstanceTOML(ctx, "faketcp-core-server", "", configuration(1, "faketcp://"+net.JoinHostPort(address, "0"), true))
			if err != nil {
				t.Fatalf("create server: %v", err)
			}
			if err := server.Start(ctx); err != nil {
				t.Fatalf("start server: %v", err)
			}
			var bound net.Addr
			select {
			case bound = <-sockets.bound:
			case <-ctx.Done():
				t.Fatal("core did not call the configured FakeTCP listener")
			}
			t.Logf("configuration bound faketcp://%s", bound)
			client, err := host.CreateInstanceTOML(ctx, "faketcp-core-client", "", configuration(2, "faketcp://"+bound.String(), false))
			if err != nil {
				t.Fatalf("create client: %v", err)
			}
			if err := client.Start(ctx); err != nil {
				t.Fatalf("start client: %v", err)
			}
			for _, instance := range []*corehost.Instance{client, server} {
				for {
					routes, err := instance.ListRoute(ctx)
					if err != nil {
						t.Fatal(err)
					}
					ready := false
					for _, route := range routes {
						if route.GetIpv4Addr() != nil && route.GetIpv6Addr() != nil {
							ready = true
							break
						}
					}
					if ready {
						break
					}
					select {
					case event := <-instance.Events():
						t.Logf("core event: %s", event.Message)
					case <-ctx.Done():
						t.Fatalf("route convergence: %v", ctx.Err())
					case <-time.After(50 * time.Millisecond):
					}
				}
			}
			for _, direction := range []struct {
				name         string
				from, to     *corehost.Instance
				source, dest int
			}{{"client-to-server", client, server, 2, 1}, {"server-to-client", server, client, 1, 2}} {
				for _, family := range []int{4, 6} {
					packet := fakeTCPCorePacket(t, family, direction.source, direction.dest)
					if err := direction.from.SendPacket(ctx, packet); err != nil {
						t.Fatalf("%s IPv%d send: %v", direction.name, family, err)
					}
					receiveContext, stopReceive := context.WithTimeout(ctx, 5*time.Second)
					received, err := direction.to.ReceivePacket(receiveContext)
					stopReceive()
					if err != nil {
						t.Fatalf("%s IPv%d receive: %v", direction.name, family, err)
					}
					if !bytes.Equal(received, packet) {
						t.Fatalf("%s IPv%d packet mismatch: got %d, want %d bytes", direction.name, family, len(received), len(packet))
					}
					t.Logf("%s overlay IPv%d: %d bytes", direction.name, family, len(packet))
				}
			}
			peers, err := client.ListPeer(ctx)
			if err != nil || len(peers) == 0 {
				t.Fatalf("connected FakeTCP peers: %v (count %d)", err, len(peers))
			}
			for _, peer := range peers {
				for _, connection := range peer.GetConns() {
					metadata := connection.GetTunnel()
					t.Logf("tunnel %s local=%s remote=%s", metadata.GetTunnelType(), metadata.GetLocalAddr().GetUrl(), metadata.GetRemoteAddr().GetUrl())
				}
			}
		})
	}
}

func fakeTCPCorePacket(t *testing.T, family, source, destination int) []byte {
	t.Helper()
	udp := &layers.UDP{SrcPort: 24001, DstPort: 24002}
	var network gopacket.SerializableLayer
	if family == 4 {
		ipv4 := &layers.IPv4{Version: 4, TTL: 64, Protocol: layers.IPProtocolUDP,
			SrcIP: net.IPv4(10, 145, 0, byte(source)), DstIP: net.IPv4(10, 145, 0, byte(destination))}
		_ = udp.SetNetworkLayerForChecksum(ipv4)
		network = ipv4
	} else {
		ipv6 := &layers.IPv6{Version: 6, HopLimit: 64, NextHeader: layers.IPProtocolUDP,
			SrcIP: netip.MustParseAddr(fmt.Sprintf("fd00:145::%d", source)).AsSlice(),
			DstIP: netip.MustParseAddr(fmt.Sprintf("fd00:145::%d", destination)).AsSlice()}
		_ = udp.SetNetworkLayerForChecksum(ipv6)
		network = ipv6
	}
	buffer := gopacket.NewSerializeBuffer()
	if err := gopacket.SerializeLayers(buffer, gopacket.SerializeOptions{FixLengths: true, ComputeChecksums: true}, network, udp,
		gopacket.Payload(bytes.Repeat([]byte("easytier-core-faketcp"), 48))); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
