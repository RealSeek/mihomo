package easytier

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/metacubex/mihomo/component/dialer"
	"github.com/metacubex/mihomo/component/resolver"

	"github.com/easytier/easytier/easytier-go/platform"
	D "github.com/miekg/dns"
)

type testDNSResolver struct {
	resolver.Resolver
	exchange func(context.Context, *D.Msg) (*D.Msg, error)
}

func (*testDNSResolver) Invalid() bool { return true }

func (r *testDNSResolver) ExchangeContext(ctx context.Context, m *D.Msg) (*D.Msg, error) {
	return r.exchange(ctx, m)
}

func TestDNSRecordsUseMihomoResolver(t *testing.T) {
	oldProxy, oldDefault := resolver.ProxyServerHostResolver, net.DefaultResolver
	t.Cleanup(func() {
		resolver.ProxyServerHostResolver = oldProxy
		net.DefaultResolver = oldDefault
	})
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(context.Context, string, string) (net.Conn, error) {
			t.Error("net.DefaultResolver was used")
			return nil, errors.New("forbidden resolver")
		},
	}
	resolver.ProxyServerHostResolver = &testDNSResolver{exchange: func(_ context.Context, m *D.Msg) (*D.Msg, error) {
		reply := new(D.Msg).SetReply(m)
		switch m.Question[0].Qtype {
		case D.TypeTXT:
			reply.Answer = []D.RR{&D.TXT{Txt: []string{"ok"}}}
		case D.TypeSRV:
			reply.Answer = []D.RR{&D.SRV{Target: "peer.example.", Port: 11010}}
		}
		return reply, nil
	}}

	txt, err := (DNSResolver{}).LookupTXT(context.Background(), platform.DNSQuery{Host: "example"})
	if err != nil || txt != "ok" {
		t.Fatalf("TXT: %q %v", txt, err)
	}
	srv, err := (DNSResolver{}).LookupSRV(context.Background(), platform.DNSQuery{Host: "example"})
	if err != nil || len(srv) != 1 || srv[0].Target != "peer.example." || srv[0].Port != 11010 {
		t.Fatalf("SRV: %v %v", srv, err)
	}
}

func TestUDPBindPreservesWildcardPort(t *testing.T) {
	_, address := udpBind(platform.UDPBindOptions{LocalAddr: &net.UDPAddr{Port: 45678}})
	_, port, err := net.SplitHostPort(address)
	if err != nil || port != "45678" {
		t.Fatalf("%s: %v", address, err)
	}
}

type forbiddenDialer struct{}

func (forbiddenDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	panic("unexpected proxy session")
}

func (forbiddenDialer) ListenPacket(context.Context, string, string, netip.AddrPort) (net.PacketConn, error) {
	panic("unexpected proxy socket")
}

func TestListenTCPInternalAllowedThroughProxyDialer(t *testing.T) {
	factory := SocketFactory{Dialer: forbiddenDialer{}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	listener, err := factory.ListenTCP(ctx, platform.TCPListenOptions{
		Bind:    platform.TCPBindOptions{LocalAddr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)}},
		Purpose: platform.TCPListenProxyNAT,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if _, err := factory.ListenTCP(ctx, platform.TCPListenOptions{
		Bind:    platform.TCPBindOptions{LocalAddr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)}},
		Purpose: platform.TCPListenDirect,
	}); err == nil {
		t.Fatal("external listener through proxy must fail")
	}
}

func TestNATSocketBinding(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	factory := SocketFactory{Dialer: dialer.NewDialer(dialer.WithPreferIPv4())}
	t.Run("TCP-local-port-reuse", func(t *testing.T) {
		reservation, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		bindAddr := reservation.Addr().(*net.TCPAddr)
		if err := reservation.Close(); err != nil {
			t.Fatal(err)
		}
		reuse := true
		for range 2 {
			listener, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			conn, err := factory.ConnectTCP(ctx, platform.TCPConnectOptions{
				RemoteAddr: listener.Addr().(*net.TCPAddr),
				Bind:       platform.TCPBindOptions{LocalAddr: bindAddr, ReuseAddr: &reuse, ReusePort: true},
				Purpose:    platform.TCPConnectHolePunch,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if local := conn.LocalAddr().(*net.TCPAddr); local.Port != bindAddr.Port || !local.IP.Equal(bindAddr.IP) {
				t.Fatalf("local TCP address = %v, want %v", local, bindAddr)
			}
		}
	})
	t.Run("UDP-local-port-reuse", func(t *testing.T) {
		options := platform.UDPBindOptions{
			LocalAddr: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)},
			ReuseAddr: true, ReusePort: true, Purpose: platform.UDPBindHolePunchCandidate,
		}
		first, err := factory.BindUDP(ctx, options)
		if err != nil {
			t.Fatal(err)
		}
		defer first.Close()
		options.LocalAddr = first.LocalAddr().(*net.UDPAddr)
		second, err := factory.BindUDP(ctx, options)
		if err != nil {
			t.Fatal(err)
		}
		defer second.Close()
		if local := second.LocalAddr().(*net.UDPAddr); local.Port != options.LocalAddr.Port || !local.IP.Equal(options.LocalAddr.IP) {
			t.Fatalf("local UDP address = %v, want %v", local, options.LocalAddr)
		}
	})
	t.Run("proxy-requires-socket-capability", func(t *testing.T) {
		proxy := SocketFactory{Dialer: forbiddenDialer{}}
		mark := uint32(123)
		if _, err := proxy.ConnectTCP(ctx, platform.TCPConnectOptions{
			RemoteAddr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 11010},
			Bind:       platform.TCPBindOptions{Context: platform.SocketContext{SocketMark: &mark}},
			Purpose:    platform.TCPConnectHolePunch,
		}); err == nil || !strings.Contains(err.Error(), "socket mark") {
			t.Fatalf("proxy TCP bind error = %v", err)
		}
		if _, err := proxy.BindUDP(ctx, platform.UDPBindOptions{
			ReusePort: true, Purpose: platform.UDPBindHolePunchCandidate,
		}); err == nil || !strings.Contains(err.Error(), "UDP purpose 1") {
			t.Fatalf("proxy UDP reuse error = %v", err)
		}
		if _, err := proxy.BindUDP(ctx, platform.UDPBindOptions{
			Context: platform.SocketContext{SocketMark: &mark}, Purpose: platform.UDPBindDirect,
		}); err == nil || !strings.Contains(err.Error(), "socket mark") {
			t.Fatalf("proxy UDP socket mark error = %v", err)
		}
	})
}

func TestConnectorEnvironmentSocketContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	listener, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	// A zero mark exercises the optional field without requiring CAP_NET_ADMIN
	// on Linux runners (nonzero SO_MARK is privileged there).
	mark := uint32(0)
	environment := ConnectorEnvironment{Dialer: dialer.NewDialer(dialer.WithPreferIPv4())}
	local, err := environment.LocalAddrForRemote(ctx, listener.LocalAddr().(*net.UDPAddr), platform.SocketContext{SocketMark: &mark})
	if err != nil {
		t.Fatalf("direct connector with socket mark: %v", err)
	}
	if local.(*net.UDPAddr).IP.IsUnspecified() {
		t.Fatalf("connector returned unspecified local address: %v", local)
	}
	proxy := ConnectorEnvironment{Dialer: forbiddenDialer{}}
	if _, err := proxy.LocalAddrForRemote(ctx, listener.LocalAddr().(*net.UDPAddr), platform.SocketContext{SocketMark: &mark}); err == nil || !strings.Contains(err.Error(), "socket mark") {
		t.Fatalf("proxy connector socket mark error = %v", err)
	}
	netns := "underlay"
	if _, err := environment.LocalAddrForRemote(ctx, listener.LocalAddr().(*net.UDPAddr), platform.SocketContext{NetNS: &netns}); err == nil || !strings.Contains(err.Error(), "network namespaces") {
		t.Fatalf("connector netns error = %v", err)
	}
}
