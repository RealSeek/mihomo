//go:build (linux || android || (windows && amd64)) && !no_fake_tcp

package easytier

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/easytier/easytier/easytier-go/platform"
	"github.com/metacubex/mihomo/component/dialer"
)

func (s SocketFactory) ConnectFakeTCP(ctx context.Context, options platform.TCPConnectOptions) (net.Conn, error) {
	direct, ok := s.Dialer.(dialer.Dialer)
	if !ok {
		return nil, fmt.Errorf("easytier: FakeTCP requires a direct host dialer")
	}
	if options.RemoteAddr == nil {
		return nil, fmt.Errorf("easytier: FakeTCP connect is missing a remote address")
	}
	// Disable lazy TFO for both route probing and the decoy handshake.
	s.Dialer = dialer.NewDialer(dialer.WithOption(direct.Opt), dialer.WithTFO(false))
	probe, err := (ConnectorEnvironment{Dialer: s.Dialer}).LocalAddrForRemote(ctx,
		&net.UDPAddr{IP: options.RemoteAddr.IP, Port: options.RemoteAddr.Port, Zone: options.RemoteAddr.Zone}, options.Bind.Context)
	if err != nil {
		return nil, fmt.Errorf("easytier: FakeTCP local route: %w", err)
	}
	local := &net.TCPAddr{IP: probe.(*net.UDPAddr).IP, Zone: probe.(*net.UDPAddr).Zone}
	if options.Bind.LocalAddr != nil {
		local.Port = options.Bind.LocalAddr.Port
		if !options.Bind.LocalAddr.IP.IsUnspecified() && len(options.Bind.LocalAddr.IP) != 0 {
			local.IP, local.Zone = options.Bind.LocalAddr.IP, options.Bind.LocalAddr.Zone
		}
	}
	var reservation net.Listener
	if local.Port == 0 {
		// WinDivert captures need a source port so same-peer filters do not overlap.
		bind := options.Bind
		bind.LocalAddr = local
		reservation, err = s.ListenTCP(ctx, platform.TCPListenOptions{Bind: bind, Purpose: platform.TCPListenPortLease})
		if err != nil {
			return nil, fmt.Errorf("easytier: FakeTCP source port reservation: %w", err)
		}
		local.Port = reservation.Addr().(*net.TCPAddr).Port
	}
	packets, err := newFakeTCPPacketSocket(local.AddrPort(), options.RemoteAddr.AddrPort())
	if err != nil {
		if reservation != nil {
			_ = reservation.Close()
		}
		return nil, err
	}
	if reservation != nil {
		if err := reservation.Close(); err != nil {
			_ = packets.Close()
			return nil, fmt.Errorf("easytier: FakeTCP release source port reservation: %w", err)
		}
	}
	options.Purpose = platform.TCPConnectManual
	options.Bind.LocalAddr = local
	decoy, err := s.ConnectTCP(ctx, options)
	if err != nil {
		_ = packets.Close()
		return nil, fmt.Errorf("easytier: FakeTCP kernel handshake: %w", err)
	}
	connection := newFakeTCPConn(decoy, packets)
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	if err := connection.establish(timer.C); err != nil {
		_ = connection.Close()
		return nil, err
	}
	return connection, nil
}

func (s SocketFactory) ListenFakeTCP(ctx context.Context, options platform.TCPListenOptions) (net.Listener, error) {
	listener, err := s.ListenTCP(ctx, options)
	if err != nil {
		return nil, err
	}
	return &fakeTCPListener{Listener: listener}, nil
}

type fakeTCPListener struct{ net.Listener }

func (l *fakeTCPListener) Accept() (net.Conn, error) {
	decoy, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	packets, err := newFakeTCPPacketSocket(decoy.LocalAddr().(*net.TCPAddr).AddrPort(), decoy.RemoteAddr().(*net.TCPAddr).AddrPort())
	if err != nil {
		_ = decoy.Close()
		return nil, err
	}
	return newFakeTCPConn(decoy, packets), nil
}
