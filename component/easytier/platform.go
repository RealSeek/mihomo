package easytier

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"syscall"

	"github.com/metacubex/mihomo/common/sockopt"
	"github.com/metacubex/mihomo/component/dialer"
	"github.com/metacubex/mihomo/component/resolver"
	C "github.com/metacubex/mihomo/constant"

	"github.com/easytier/easytier/easytier-go/platform"
	D "github.com/miekg/dns"
)

// Services wraps a mihomo dialer as EasyTier platform capabilities.
func Services(d C.Dialer) (platform.Services, error) {
	snapshot, err := EnvironmentSnapshot()
	if err != nil {
		return platform.Services{}, err
	}
	return platform.Services{
		Sockets:     SocketFactory{Dialer: d},
		DNS:         DNSResolver{},
		Environment: ConnectorEnvironment{Dialer: d},
		Snapshot:    snapshot,
	}, nil
}

// SocketFactory creates sockets through mihomo's dialer.
//
// Interface and routing-mark come from the mihomo dialer. Direct sockets
// honor local binding and use mihomo's combined address/port reuse policy.
// Proxy/custom dialers cannot provide local binding, reuse, or socket marks.
// Internal TCP reservations bind on the host even when dialer-proxy is set.
// Network namespaces are unavailable through this host adapter.
type SocketFactory struct {
	Dialer C.Dialer
}

func (s SocketFactory) ConnectTCP(ctx context.Context, options platform.TCPConnectOptions) (net.Conn, error) {
	if options.Purpose == platform.TCPConnectFake {
		return s.ConnectFakeTCP(ctx, options)
	}
	if options.RemoteAddr == nil {
		return nil, fmt.Errorf("easytier: TCP connect is missing a remote address")
	}
	if options.Bind.Context.NetNS != nil {
		return nil, fmt.Errorf("easytier: network namespaces are not supported")
	}
	network := tcpNetwork(options.Bind)
	reuse := options.Bind.ReusePort || options.Bind.ReuseAddr != nil && *options.Bind.ReuseAddr
	if direct, ok := s.Dialer.(dialer.Dialer); ok {
		netDialer := &net.Dialer{}
		if options.Bind.LocalAddr != nil {
			netDialer.LocalAddr = options.Bind.LocalAddr
		}
		if reuse {
			netDialer.Control = func(_, _ string, conn syscall.RawConn) error {
				if err := sockopt.RawConnReuseaddr(conn); err != nil {
					return fmt.Errorf("easytier: TCP socket reuse: %w", err)
				}
				return nil
			}
		}
		dialOptions := []dialer.Option{dialer.WithOption(direct.Opt), dialer.WithNetDialer(netDialer)}
		if mark := options.Bind.Context.SocketMark; mark != nil {
			dialOptions = append(dialOptions, dialer.WithRoutingMark(int(*mark)))
		}
		if options.Purpose == platform.TCPConnectSTUNProbe {
			// NAT probes need an established socket before their source port is inspected.
			dialOptions = append(dialOptions, dialer.WithTFO(false))
		}
		conn, err := dialer.DialContext(ctx, network, options.RemoteAddr.String(), dialOptions...)
		if err != nil {
			return nil, err
		}
		if options.Purpose == platform.TCPConnectSTUNProbe {
			if err := conn.(*net.TCPConn).SetLinger(0); err != nil {
				_ = conn.Close()
				return nil, fmt.Errorf("easytier: TCP STUN probe linger: %w", err)
			}
		}
		return conn, nil
	}
	if options.Bind.Context.SocketMark != nil || reuse || options.Bind.LocalAddr != nil && (options.Bind.LocalAddr.Port != 0 || len(options.Bind.LocalAddr.IP) != 0 && !options.Bind.LocalAddr.IP.IsUnspecified()) {
		return nil, fmt.Errorf("easytier: TCP purpose %d requires local binding, reuse, or socket mark unavailable through a proxy or custom dialer", options.Purpose)
	}
	return s.Dialer.DialContext(ctx, network, options.RemoteAddr.String())
}

func (s SocketFactory) BindUDP(ctx context.Context, options platform.UDPBindOptions) (net.PacketConn, error) {
	if options.Context.NetNS != nil {
		return nil, fmt.Errorf("easytier: network namespaces are not supported")
	}
	network, address := udpBind(options)
	reuse := options.ReuseAddr || options.ReusePort
	if direct, ok := s.Dialer.(dialer.Dialer); ok {
		listenOptions := []dialer.Option{dialer.WithOption(direct.Opt), dialer.WithAddrReuse(reuse)}
		if mark := options.Context.SocketMark; mark != nil {
			listenOptions = append(listenOptions, dialer.WithRoutingMark(int(*mark)))
		}
		return dialer.ListenPacket(ctx, network, address, netip.AddrPort{}, listenOptions...)
	}
	if options.Context.SocketMark != nil || reuse || options.LocalAddr != nil && (options.LocalAddr.Port != 0 || len(options.LocalAddr.IP) != 0 && !options.LocalAddr.IP.IsUnspecified()) {
		return nil, fmt.Errorf("easytier: UDP purpose %d requires local binding, reuse, or socket mark unavailable through a proxy or custom dialer", options.Purpose)
	}
	return s.Dialer.ListenPacket(ctx, network, address, netip.AddrPort{})
}

func (s SocketFactory) ListenTCP(ctx context.Context, options platform.TCPListenOptions) (net.Listener, error) {
	if options.Bind.Context.NetNS != nil {
		return nil, fmt.Errorf("easytier: network namespaces are not supported")
	}
	address := ":0"
	if options.Bind.LocalAddr != nil {
		address = options.Bind.LocalAddr.String()
	}
	network := tcpNetwork(options.Bind)
	reuse := options.Bind.ReusePort || options.Bind.ReuseAddr != nil && *options.Bind.ReuseAddr
	listenOpts := []dialer.Option{dialer.WithAddrReuse(reuse)}
	if direct, ok := s.Dialer.(dialer.Dialer); ok {
		listenOpts = append([]dialer.Option{dialer.WithOption(direct.Opt)}, listenOpts...)
		if mark := options.Bind.Context.SocketMark; mark != nil {
			listenOpts = append(listenOpts, dialer.WithRoutingMark(int(*mark)))
		}
		return dialer.Listen(ctx, network, address, listenOpts...)
	}
	if externalTCPListen(options.Purpose) {
		return nil, fmt.Errorf("easytier: TCP listeners are unavailable through a proxy or custom dialer")
	}
	// ProxyNAT, port leases, and hole-punch reservations must bind on the host
	// even when peer traffic uses dialer-proxy.
	if options.Bind.Context.SocketMark != nil {
		listenOpts = append(listenOpts, dialer.WithRoutingMark(int(*options.Bind.Context.SocketMark)))
	}
	return dialer.Listen(ctx, network, address, listenOpts...)
}

func externalTCPListen(purpose platform.TCPListenPurpose) bool {
	switch purpose {
	case platform.TCPListenDirect, platform.TCPListenManual:
		return true
	default:
		return false
	}
}

func tcpNetwork(options platform.TCPBindOptions) string {
	if options.LocalAddr != nil && options.LocalAddr.IP.To4() != nil {
		return "tcp4"
	}
	if options.OnlyV6 || options.Context.IPVersion == platform.IPVersionV6 {
		return "tcp6"
	}
	if options.Context.IPVersion == platform.IPVersionV4 {
		return "tcp4"
	}
	return "tcp"
}

func udpBind(options platform.UDPBindOptions) (network, address string) {
	if options.LocalAddr != nil {
		address = options.LocalAddr.String()
		if options.LocalAddr.IP.To4() != nil {
			return "udp4", address
		}
		if len(options.LocalAddr.IP) != 0 {
			if options.OnlyV6 || options.LocalAddr.IP.To16() != nil && options.LocalAddr.IP.To4() == nil {
				return "udp6", address
			}
			return "udp", address
		}
		if options.OnlyV6 || options.Context.IPVersion == platform.IPVersionV6 {
			return "udp6", address
		}
		if options.Context.IPVersion == platform.IPVersionV4 {
			return "udp4", address
		}
		return "udp", address
	}
	switch {
	case options.OnlyV6 || options.Context.IPVersion == platform.IPVersionV6:
		return "udp6", "[::]:0"
	case options.Context.IPVersion == platform.IPVersionV4:
		return "udp4", "0.0.0.0:0"
	default:
		return "udp", ":0"
	}
}

// DNSResolver resolves EasyTier control-plane names through the proxy-server resolver.
type DNSResolver struct{}

func (DNSResolver) LookupIP(ctx context.Context, query platform.DNSQuery) ([]netip.Addr, error) {
	if address, err := netip.ParseAddr(query.Host); err == nil {
		return []netip.Addr{address.Unmap()}, nil
	}
	switch query.IPVersion {
	case 4:
		return resolver.LookupIPv4WithResolver(ctx, query.Host, resolver.ProxyServerHostResolver)
	case 6:
		return resolver.LookupIPv6WithResolver(ctx, query.Host, resolver.ProxyServerHostResolver)
	default:
		return resolver.LookupIPWithResolver(ctx, query.Host, resolver.ProxyServerHostResolver)
	}
}

func exchangeDNS(ctx context.Context, host string, qtype uint16) (*D.Msg, error) {
	r := resolver.ProxyServerHostResolver
	if r == nil || !r.Invalid() {
		r = resolver.SystemResolver
	}
	if r == nil {
		return nil, fmt.Errorf("easytier: DNS resolver is unavailable")
	}
	request := new(D.Msg)
	request.SetQuestion(D.Fqdn(host), qtype)
	reply, err := r.ExchangeContext(ctx, request)
	if err != nil {
		return nil, err
	}
	if reply == nil {
		return nil, fmt.Errorf("easytier: empty DNS response for %q", host)
	}
	if reply.Rcode != D.RcodeSuccess {
		return nil, fmt.Errorf("easytier: DNS query for %q returned %s", host, D.RcodeToString[reply.Rcode])
	}
	return reply, nil
}

func (DNSResolver) LookupTXT(ctx context.Context, query platform.DNSQuery) (string, error) {
	reply, err := exchangeDNS(ctx, query.Host, D.TypeTXT)
	if err != nil {
		return "", err
	}
	for _, answer := range reply.Answer {
		if txt, ok := answer.(*D.TXT); ok {
			return strings.Join(txt.Txt, ""), nil
		}
	}
	return "", fmt.Errorf("easytier: DNS TXT query for %q returned no records", query.Host)
}

func (DNSResolver) LookupSRV(ctx context.Context, query platform.DNSQuery) ([]*net.SRV, error) {
	reply, err := exchangeDNS(ctx, query.Host, D.TypeSRV)
	if err != nil {
		return nil, err
	}
	var records []*net.SRV
	for _, answer := range reply.Answer {
		if srv, ok := answer.(*D.SRV); ok {
			records = append(records, &net.SRV{Target: srv.Target, Port: srv.Port, Priority: srv.Priority, Weight: srv.Weight})
		}
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("easytier: DNS SRV query for %q returned no records", query.Host)
	}
	return records, nil
}

// ConnectorEnvironment reports the local address used toward a remote UDP peer.
type ConnectorEnvironment struct {
	Dialer C.Dialer
}

func (e ConnectorEnvironment) LocalAddrForRemote(ctx context.Context, remote *net.UDPAddr, socketContext platform.SocketContext) (net.Addr, error) {
	if remote == nil {
		return nil, fmt.Errorf("easytier: missing remote address")
	}
	if socketContext.NetNS != nil {
		return nil, fmt.Errorf("easytier: network namespaces are not supported")
	}
	network := "udp"
	if remote.IP.To4() != nil {
		network = "udp4"
	} else if remote.IP.To16() != nil {
		network = "udp6"
	}
	var conn net.Conn
	var err error
	if direct, ok := e.Dialer.(dialer.Dialer); ok {
		options := []dialer.Option{dialer.WithOption(direct.Opt)}
		if mark := socketContext.SocketMark; mark != nil {
			options = append(options, dialer.WithRoutingMark(int(*mark)))
		}
		conn, err = dialer.DialContext(ctx, network, remote.String(), options...)
	} else {
		if socketContext.SocketMark != nil {
			return nil, fmt.Errorf("easytier: connector socket mark is unavailable through a proxy or custom dialer")
		}
		conn, err = e.Dialer.DialContext(ctx, network, remote.String())
	}
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return conn.LocalAddr(), nil
}
