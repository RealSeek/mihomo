package host_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"testing"
	"time"

	corehost "github.com/easytier/easytier/easytier-go"
	"github.com/easytier/easytier/easytier-go/platform"
	"github.com/easytier/easytier/easytier-go/platform/netstd"
)

type transportSocketFactory struct {
	netstd.SocketFactory
	bound chan net.Addr
}

func (s transportSocketFactory) BindUDP(ctx context.Context, options platform.UDPBindOptions) (net.PacketConn, error) {
	conn, err := s.SocketFactory.BindUDP(ctx, options)
	if err == nil && options.LocalAddr != nil && options.LocalAddr.IP.IsLoopback() {
		s.bound <- conn.LocalAddr()
	}
	return conn, err
}

func TestDatagramUnderlayIPv4AndIPv6(t *testing.T) {
	for _, protocol := range []string{"wg", "quic"} {
		for _, address := range []string{"127.0.0.1", "::1"} {
			t.Run(protocol+"/"+address, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
				defer cancel()
				sockets := transportSocketFactory{bound: make(chan net.Addr, 8)}
				host, err := corehost.New(ctx, corehost.Options{Platform: platform.Services{Sockets: sockets}})
				if err != nil {
					t.Fatal(err)
				}
				defer host.Close(ctx)
				build := func(id int, endpoint string, listen bool) string {
					listeners, peer := "[]", fmt.Sprintf("[[peer]]\nuri = %q\n", endpoint)
					if listen {
						listeners, peer = fmt.Sprintf("[%q]", endpoint), ""
					}
					return fmt.Sprintf(`hostname = "datagram-%d"
ipv4 = "10.145.0.%d/24"
ipv6 = "fd00:145::%d/64"
listeners = %s
stun_servers = []
stun_servers_v6 = []
%s
[network_identity]
network_name = "datagram-test"
network_secret = "datagram-secret"
[flags]
no_tun = false
disable_p2p = true
enable_encryption = false
`, id, id, id, listeners, peer)
				}
				server, err := host.CreateInstanceTOML(ctx, "datagram-server", "", build(1, protocol+"://"+net.JoinHostPort(address, "0"), true))
				if err != nil {
					t.Fatal(err)
				}
				defer server.Close(ctx)
				if err := server.Start(ctx); err != nil {
					t.Fatal(err)
				}
				var bound net.Addr
				select {
				case bound = <-sockets.bound:
				case <-ctx.Done():
					t.Fatal("datagram listener did not bind")
				}
				client, err := host.CreateInstanceTOML(ctx, "datagram-client", "", build(2, protocol+"://"+bound.String(), false))
				if err != nil {
					t.Fatal(err)
				}
				defer client.Close(ctx)
				if err := client.Start(ctx); err != nil {
					t.Fatal(err)
				}
				waitForEvent(t, ctx, client.Events(), "peer_added")
				waitForDualStackRoute(t, ctx, client, "datagram-1")
				waitForDualStackRoute(t, ctx, server, "datagram-2")
				for _, direction := range []struct {
					from, to       *corehost.Instance
					source, target byte
				}{
					{client, server, 2, 1}, {server, client, 1, 2},
				} {
					payload := bytes.Repeat([]byte("wg"), 600)
					for _, packet := range [][]byte{
						ipv4Packet(net.IPv4(10, 145, 0, direction.source), net.IPv4(10, 145, 0, direction.target), payload),
						ipv6Packet(netip.MustParseAddr(fmt.Sprintf("fd00:145::%d", direction.source)), netip.MustParseAddr(fmt.Sprintf("fd00:145::%d", direction.target)), payload),
					} {
						if err := direction.from.SendPacket(ctx, packet); err != nil {
							t.Fatal(err)
						}
						received, err := direction.to.ReceivePacket(ctx)
						if err != nil {
							t.Fatal(err)
						}
						if !bytes.Equal(received, packet) {
							t.Fatalf("%s IPv%d packet mismatch: got %d bytes, want %d", protocol, packet[0]>>4, len(received), len(packet))
						}
						t.Logf("overlay IPv%d %d -> %d: %d bytes", packet[0]>>4, direction.source, direction.target, len(packet))
					}
				}
				peers, err := client.ListPeer(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if len(peers) == 0 {
					t.Fatal("datagram peer missing")
				}
			})
		}
	}
}

func waitForDualStackRoute(t *testing.T, ctx context.Context, instance *corehost.Instance, hostname string) {
	t.Helper()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		routes, err := instance.ListRoute(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, route := range routes {
			if route.Hostname == hostname && route.GetIpv4Addr() != nil && route.GetIpv6Addr() != nil {
				return
			}
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatalf("wait for dual-stack route to %s: %v", hostname, ctx.Err())
		}
	}
}

func ipv6Packet(source, destination netip.Addr, payload []byte) []byte {
	packet := make([]byte, 48+len(payload))
	packet[0], packet[6], packet[7] = 0x60, 58, 64
	binary.BigEndian.PutUint16(packet[4:6], uint16(len(packet)-40))
	copy(packet[8:24], source.AsSlice())
	copy(packet[24:40], destination.AsSlice())
	packet[40], packet[45], packet[47] = 128, 1, 1
	copy(packet[48:], payload)
	pseudoHeader := make([]byte, (len(packet)+1)&^1)
	copy(pseudoHeader[:32], packet[8:40])
	binary.BigEndian.PutUint32(pseudoHeader[32:36], uint32(len(packet)-40))
	pseudoHeader[39] = 58
	copy(pseudoHeader[40:], packet[40:])
	binary.BigEndian.PutUint16(packet[42:44], ipv4Checksum(pseudoHeader))
	return packet
}
