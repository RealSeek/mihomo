package host_test

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	corehost "github.com/easytier/easytier/easytier-go"
	"github.com/easytier/easytier/easytier-go/platform"
	"github.com/easytier/easytier/easytier-go/platform/netstd"
)

type websocketSocketFactory struct {
	netstd.SocketFactory
	bound    chan net.Addr
	connects atomic.Int32
}

func (factory *websocketSocketFactory) ListenTCP(ctx context.Context, options platform.TCPListenOptions) (net.Listener, error) {
	listener, err := factory.SocketFactory.ListenTCP(ctx, options)
	if err == nil {
		factory.bound <- listener.Addr()
	}
	return listener, err
}

func (factory *websocketSocketFactory) ConnectTCP(ctx context.Context, options platform.TCPConnectOptions) (net.Conn, error) {
	factory.connects.Add(1)
	return factory.SocketFactory.ConnectTCP(ctx, options)
}

type websocketDNS struct {
	netstd.DNSResolver
	address netip.Addr
	queries atomic.Int32
}

func (dns *websocketDNS) LookupIP(ctx context.Context, query platform.DNSQuery) ([]netip.Addr, error) {
	if query.Host == "easytier-ws.test" {
		dns.queries.Add(1)
		return []netip.Addr{dns.address}, nil
	}
	return dns.DNSResolver.LookupIP(ctx, query)
}

func TestWebSocketConfiguredTransportLifecycle(t *testing.T) {
	for _, scheme := range []string{"ws", "wss"} {
		for _, address := range []string{"127.0.0.1", "::1"} {
			t.Run(scheme+"/"+address, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				sockets := &websocketSocketFactory{bound: make(chan net.Addr, 8)}
				dns := &websocketDNS{address: netip.MustParseAddr(address)}
				host, err := corehost.New(ctx, corehost.Options{Platform: platform.Services{Sockets: sockets, DNS: dns}})
				if err != nil {
					t.Fatal(err)
				}
				defer host.Close(context.Background())
				build := func(id int, endpoint string, listen bool) string {
					listeners, peer := "[]", fmt.Sprintf("[[peer]]\nuri = %q\n", endpoint)
					if listen {
						listeners, peer = fmt.Sprintf("[%q]", endpoint), ""
					}
					return fmt.Sprintf(`hostname = "ws-node-%d"
ipv4 = "10.146.0.%d/24"
ipv6 = "fd00:146::%d/64"
listeners = %s
stun_servers = []
stun_servers_v6 = []
%s
[network_identity]
network_name = "ws-config-test"
network_secret = "ws-secret"
[flags]
no_tun = false
disable_p2p = true
enable_encryption = false
`, id, id, id, listeners, peer)
				}
				server, err := host.CreateInstanceTOML(ctx, "ws-server", "", build(1, scheme+"://"+net.JoinHostPort(address, "0")+"/overlay", true))
				if err != nil {
					t.Fatal(err)
				}
				defer server.Close(context.Background())
				if err := server.Start(ctx); err != nil {
					t.Fatal(err)
				}
				var bound net.Addr
				select {
				case bound = <-sockets.bound:
				case <-ctx.Done():
					t.Fatal("configured WebSocket listener did not bind")
				}
				endpoint := scheme + "://" + net.JoinHostPort("easytier-ws.test", strconv.Itoa(bound.(*net.TCPAddr).Port)) + "/overlay"
				client, err := host.CreateInstanceTOML(ctx, "ws-client", "", build(2, endpoint, false))
				if err != nil {
					t.Fatal(err)
				}
				defer client.Close(context.Background())
				if err := client.Start(ctx); err != nil {
					t.Fatal(err)
				}
				waitForEvent(t, ctx, client.Events(), "peer_added")
				waitForDualStackRoute(t, ctx, client, "ws-node-1")
				waitForDualStackRoute(t, ctx, server, "ws-node-2")
				for _, direction := range []struct {
					from, to       *corehost.Instance
					source, target byte
				}{
					{client, server, 2, 1}, {server, client, 1, 2},
				} {
					payload := bytes.Repeat([]byte(scheme), 600)
					for _, packet := range [][]byte{
						ipv4Packet(net.IPv4(10, 146, 0, direction.source), net.IPv4(10, 146, 0, direction.target), payload),
						ipv6Packet(netip.MustParseAddr(fmt.Sprintf("fd00:146::%d", direction.source)), netip.MustParseAddr(fmt.Sprintf("fd00:146::%d", direction.target)), payload),
					} {
						if err := direction.from.SendPacket(ctx, packet); err != nil {
							t.Fatal(err)
						}
						received, err := direction.to.ReceivePacket(ctx)
						if err != nil {
							t.Fatal(err)
						}
						if !bytes.Equal(received, packet) {
							t.Fatalf("WebSocket IPv%d packet boundary mismatch", packet[0]>>4)
						}
						t.Logf("overlay IPv%d %d -> %d: %d bytes", packet[0]>>4, direction.source, direction.target, len(packet))
					}
				}
				if dns.queries.Load() == 0 || sockets.connects.Load() == 0 {
					t.Fatal("WebSocket bypassed platform DNS/socket services")
				}
				if err := client.Close(ctx); err != nil {
					t.Fatal(err)
				}
				if err := server.Close(ctx); err != nil {
					t.Fatal(err)
				}
				listener, err := net.Listen("tcp", bound.String())
				if err != nil {
					t.Fatalf("configured listener was not released: %v", err)
				}
				_ = listener.Close()
			})
		}
	}
}
