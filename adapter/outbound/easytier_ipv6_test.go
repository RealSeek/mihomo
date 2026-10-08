//go:build !no_easytier

package outbound

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/metacubex/mihomo/component/easytier"
	C "github.com/metacubex/mihomo/constant"
	D "github.com/miekg/dns"
)

func TestEasyTierIPv6SessionAndDNS(t *testing.T) {
	previousHome := C.Path.HomeDir()
	C.SetHomeDir(t.TempDir())
	t.Cleanup(func() { C.SetHomeDir(previousHome) })
	for _, ipv4 := range []string{"10.144.0.1/24", ""} {
		t.Run(ipv4, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			outbound, err := NewEasyTier(EasyTierOption{
				Name: "dual-stack", NetworkName: "dual-stack", Hostname: "local", PacketMode: true,
				IPv4: ipv4, IPv6: "fd00:144::1/64", Listeners: []string{"tcp://127.0.0.1:0"},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer outbound.Close()
			session, err := outbound.OpenPacketSession(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Contains(session.Addresses, netip.MustParsePrefix("fd00:144::1/64")) || !slices.Contains(session.Routes, netip.MustParsePrefix("fd00:144::/64")) {
				t.Fatalf("missing runtime IPv6 address or subnet route: %+v", session)
			}
			wantCount := 1
			if ipv4 != "" {
				wantCount = 2
			}
			if len(session.Addresses) != wantCount {
				t.Fatalf("runtime addresses: %v", session.Addresses)
			}
			transport := easyTierDNSTransport{outbound}
			if ipv4 == "" {
				query := new(D.Msg)
				query.SetQuestion("local.et.net.", D.TypeA)
				reply, err := transport.ExchangeContext(ctx, query)
				if err != nil || reply.Rcode != D.RcodeSuccess || len(reply.Answer) != 0 {
					t.Fatalf("IPv6-only node A response: %s, %v", reply, err)
				}
				if _, err := outbound.resolveIPv4(ctx, "local.et.net."); err == nil {
					t.Fatal("IPv6-only node accepted by the IPv4 socket dial path")
				}
			}
			for _, name := range []string{"local.", "local.et.net."} {
				query := new(D.Msg)
				query.SetQuestion(name, D.TypeAAAA)
				reply, err := transport.ExchangeContext(ctx, query)
				if err != nil || len(reply.Answer) != 1 || reply.Answer[0].(*D.AAAA).AAAA.String() != "fd00:144::1" {
					t.Fatalf("IPv6 Magic DNS %s: %s, %v", name, reply, err)
				}
			}
			reverse, err := D.ReverseAddr("fd00:144::1")
			if err != nil {
				t.Fatal(err)
			}
			query := new(D.Msg)
			query.SetQuestion(reverse, D.TypePTR)
			reply, err := transport.ExchangeContext(ctx, query)
			if err != nil || len(reply.Answer) != 1 || reply.Answer[0].(*D.PTR).Ptr != "local.et.net." {
				t.Fatalf("IPv6 reverse: %s, %v", reply, err)
			}
			status, err := outbound.EasyTierStatus(ctx)
			if err != nil || status.Node.IPv6 != "fd00:144::1/64" {
				t.Fatalf("IPv6 status: %+v, %v", status.Node, err)
			}
		})
	}
}

func TestEasyTierIPv6AddressValidation(t *testing.T) {
	for _, address := range []string{"::/64", "10.144.0.1/24", "fd00::1/129"} {
		for _, native := range []bool{false, true} {
			option := EasyTierOption{Name: "invalid-ipv6", IPv6: address}
			if native {
				option.IPv6 = ""
				option.ConfigTOML = "ipv6 = '" + address + "'\n"
			}
			if _, err := NewEasyTier(option); err == nil {
				t.Fatalf("invalid IPv6 CIDR accepted: %q (native=%v)", address, native)
			}
		}
	}
}

func TestEasyTierBareIPv6SessionAndDNS(t *testing.T) {
	previousHome := C.Path.HomeDir()
	C.SetHomeDir(t.TempDir())
	t.Cleanup(func() { C.SetHomeDir(previousHome) })
	reservation, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	peerURL := "tcp://" + reservation.Addr().String()
	if err := reservation.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	peer, err := NewEasyTier(EasyTierOption{
		Name: "bare-ipv6-peer", PacketMode: true, NetworkName: "bare-ipv6", NetworkSecret: "test",
		IPv6: "fd00:144::8/64", Listeners: []string{peerURL}, STUNServers: []string{}, STUNServersV6: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	if _, err := peer.OpenPacketSession(ctx); err != nil {
		t.Fatal(err)
	}
	peerInstance, err := peer.currentInstance()
	if err != nil {
		t.Fatal(err)
	}
	for _, native := range []bool{false, true} {
		t.Run(fmt.Sprintf("native-%v", native), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			option := EasyTierOption{
				Name: "bare-ipv6", PacketMode: true, NetworkName: "bare-ipv6", NetworkSecret: "test",
				Hostname: "bare", IPv6: "fd00:144::9", Peers: []string{peerURL},
				STUNServers: []string{}, STUNServersV6: []string{},
			}
			if native {
				option = EasyTierOption{Name: "bare-ipv6", PacketMode: true, ConfigTOML: fmt.Sprintf(`
ipv6 = 'fd00:144::9'
hostname = 'bare'
listeners = []
stun_servers = []
stun_servers_v6 = []
[[peer]]
uri = %q
[network_identity]
network_name = 'bare-ipv6'
network_secret = 'test'
`, peerURL)}
			}
			adapter, err := NewEasyTier(option)
			if err != nil {
				t.Fatal(err)
			}
			defer adapter.Close()
			session, err := adapter.OpenPacketSession(ctx)
			wantPrefix := netip.MustParsePrefix("fd00:144::9/128")
			if err != nil || len(session.Addresses) != 1 || session.Addresses[0] != wantPrefix || !slices.Contains(session.Routes, wantPrefix) {
				t.Fatalf("bare IPv6 session = %+v, err = %v", session, err)
			}
			instance, err := adapter.currentInstance()
			if err != nil {
				t.Fatal(err)
			}
			info, err := instance.ShowNodeInfo(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var runtimeIPv6 netip.Prefix
			ticker := time.NewTicker(25 * time.Millisecond)
			defer ticker.Stop()
			for runtimeIPv6 != wantPrefix {
				routes, err := peerInstance.ListRoute(ctx)
				if err != nil {
					t.Fatal(err)
				}
				for _, route := range routes {
					if route.GetPeerId() != info.GetPeerId() || route.GetIpv6Addr() == nil || route.GetIpv6Addr().GetAddress() == nil {
						continue
					}
					ip := route.GetIpv6Addr().GetAddress()
					runtimeIPv6 = netip.PrefixFrom(easytier.IPv6FromParts(ip.GetPart1(), ip.GetPart2(), ip.GetPart3(), ip.GetPart4()), int(route.GetIpv6Addr().GetNetworkLength()))
				}
				if runtimeIPv6 != wantPrefix {
					select {
					case <-ctx.Done():
						t.Fatalf("official core runtime IPv6 = %s, want %s: %v", runtimeIPv6, wantPrefix, ctx.Err())
					case <-ticker.C:
					}
				}
			}
			transport := easyTierDNSTransport{adapter}
			query := new(D.Msg)
			query.SetQuestion("bare.et.net.", D.TypeAAAA)
			reply, err := transport.ExchangeContext(ctx, query)
			if err != nil || len(reply.Answer) != 1 || reply.Answer[0].(*D.AAAA).AAAA.String() != "fd00:144::9" {
				t.Fatalf("bare IPv6 DNS = %s, err = %v", reply, err)
			}
			status, err := adapter.EasyTierStatus(ctx)
			if err != nil || status.Node.IPv6 != wantPrefix.String() {
				t.Fatalf("bare IPv6 status = %+v, err = %v", status.Node, err)
			}
		})
	}
}
