//go:build !no_easytier

package outbound

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"slices"
	"testing"
	"time"

	corehost "github.com/easytier/easytier/easytier-go"
	C "github.com/metacubex/mihomo/constant"
	"golang.org/x/net/proxy"
)

func TestEasyTierStructuredSocks5GatewayTCP(t *testing.T) {
	previousHome := C.Path.HomeDir()
	C.SetHomeDir(t.TempDir())
	t.Cleanup(func() { C.SetHomeDir(previousHome) })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	addresses := make([]string, 2)
	for index := range addresses {
		reservation, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addresses[index] = reservation.Addr().String()
		if err := reservation.Close(); err != nil {
			t.Fatal(err)
		}
	}
	peerURL, portalAddress := "tcp://"+addresses[0], addresses[1]
	peer, err := NewEasyTier(EasyTierOption{
		Name: "socks5-peer", PacketMode: true, NetworkName: "socks5-gateway-test", NetworkSecret: "socks5-secret",
		IPv4: "10.144.0.1/24", Listeners: []string{peerURL}, STUNServers: []string{}, STUNServersV6: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	if _, err := peer.OpenPacketSession(ctx); err != nil {
		t.Fatal(err)
	}
	gateway, err := NewEasyTier(EasyTierOption{
		Name: "socks5-gateway", PacketMode: true, NetworkName: "socks5-gateway-test", NetworkSecret: "socks5-secret",
		IPv4: "10.144.0.2/24", Peers: []string{peerURL}, Socks5Proxy: "socks5://" + portalAddress,
		STUNServers: []string{}, STUNServersV6: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	if _, err := gateway.OpenPacketSession(ctx); err != nil {
		t.Fatal(err)
	}
	peerCore, err := peer.currentInstance()
	if err != nil {
		t.Fatal(err)
	}
	gatewayCore, err := gateway.currentInstance()
	if err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for _, pair := range [][2]*corehost.Instance{{peerCore, gatewayCore}, {gatewayCore, peerCore}} {
		for {
			routes, err := pair[0].ListRoute(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if slices.ContainsFunc(routes, func(route *corehost.Route) bool { return route.GetInstId() == pair[1].ID() }) {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatalf("wait for SOCKS5 overlay peer route: %v", ctx.Err())
			case <-ticker.C:
			}
		}
	}

	listener, err := peerCore.Listen("tcp4", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serverResult := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err == nil {
			defer connection.Close()
			_ = connection.SetDeadline(time.Now().Add(5 * time.Second))
			_, err = io.Copy(connection, connection)
		}
		serverResult <- err
	}()
	dialer, err := proxy.SOCKS5("tcp4", portalAddress, nil, &net.Dialer{})
	if err != nil {
		t.Fatal(err)
	}
	target := net.JoinHostPort("10.144.0.1", fmt.Sprint(listener.Addr().(*net.TCPAddr).Port))
	connection, err := dialer.(proxy.ContextDialer).DialContext(ctx, "tcp", target)
	if err != nil {
		t.Fatalf("SOCKS5 handshake and overlay TCP connect: %v", err)
	}
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	payload := []byte("mihomo structured SOCKS5 to embedded EasyTier TCP")
	if _, err := connection.Write(payload); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, len(payload))
	if _, err := io.ReadFull(connection, reply); err != nil || !bytes.Equal(reply, payload) {
		t.Fatalf("SOCKS5 overlay echo = %q, error = %v", reply, err)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-serverResult:
		if err != nil {
			t.Fatalf("SOCKS5 echo server: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("SOCKS5 session close: %v", ctx.Err())
	}
	if err := gateway.Close(); err != nil {
		t.Fatal(err)
	}
	rebound, err := net.Listen("tcp4", portalAddress)
	if err != nil {
		t.Fatalf("SOCKS5 portal bind still held after gateway close: %v", err)
	}
	if err := rebound.Close(); err != nil {
		t.Fatal(err)
	}
}
