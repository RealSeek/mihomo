package easytier

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/easytier/easytier/easytier-go/platform"
	"github.com/metacubex/mihomo/component/dialer"
)

func TestTCPSTUNProbeReleasesSourcePort(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	reservation, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	source := reservation.Addr().(*net.TCPAddr)
	if err := reservation.Close(); err != nil {
		t.Fatal(err)
	}
	oldDisableTFO := dialer.DisableTFO
	dialer.DisableTFO = false
	t.Cleanup(func() { dialer.DisableTFO = oldDisableTFO })
	factory := SocketFactory{Dialer: dialer.NewDialer(dialer.WithTFO(true))}
	for range 2 {
		conn, err := factory.ConnectTCP(ctx, platform.TCPConnectOptions{
			RemoteAddr: listener.Addr().(*net.TCPAddr),
			Bind:       platform.TCPBindOptions{LocalAddr: source},
			Purpose:    platform.TCPConnectSTUNProbe,
		})
		if err != nil {
			t.Fatal(err)
		}
		if !conn.LocalAddr().(*net.TCPAddr).IP.Equal(source.IP) || conn.LocalAddr().(*net.TCPAddr).Port != source.Port {
			t.Fatalf("STUN probe source = %v, want %v", conn.LocalAddr(), source)
		}
		if err := listener.SetDeadline(time.Now().Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		peer, err := listener.AcceptTCP()
		if err != nil {
			conn.Close()
			t.Fatal(err)
		}
		if err := peer.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
			conn.Close()
			peer.Close()
			t.Fatal(err)
		}
		if err := conn.Close(); err != nil {
			peer.Close()
			t.Fatal(err)
		}
		var buffer [1]byte
		_, err = peer.Read(buffer[:])
		peer.Close()
		if err == nil || errors.Is(err, io.EOF) {
			t.Fatalf("STUN probe close did not reset the connection: %v", err)
		}
		if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
			t.Fatalf("STUN probe close timed out instead of resetting: %v", err)
		}
	}
}
