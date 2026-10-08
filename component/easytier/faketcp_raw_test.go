//go:build (linux || android || (windows && amd64)) && !no_fake_tcp

package easytier

import (
	"context"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"github.com/easytier/easytier/easytier-go/platform"
	"github.com/metacubex/mihomo/component/dialer"
	"github.com/metacubex/mihomo/component/resolver"
)

// The fixture exercises real packet capture and injection. Android/Linux need
// CAP_NET_RAW; Windows needs administrator rights and a running WinDivert driver.
func TestFakeTCPRawBidirectional(t *testing.T) {
	if os.Getenv("EASYTIER_FAKETCP_RAW_TEST") != "1" {
		t.Skip("set EASYTIER_FAKETCP_RAW_TEST=1 to exercise the raw packet backend")
	}
	previousIPv6Policy := resolver.DisableIPv6
	resolver.DisableIPv6 = false
	t.Cleanup(func() { resolver.DisableIPv6 = previousIPv6Policy })
	for _, test := range []struct {
		name    string
		address string
		version platform.IPVersion
	}{{"IPv4", "127.0.0.1:0", platform.IPVersionV4}, {"IPv6", "[::1]:0", platform.IPVersionV6}} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			factory := SocketFactory{Dialer: dialer.NewDialer(dialer.WithTFO(true))}
			address, err := net.ResolveTCPAddr("tcp", test.address)
			if err != nil {
				t.Fatal(err)
			}
			bind := platform.TCPBindOptions{Context: platform.SocketContext{IPVersion: test.version}, LocalAddr: address}
			listener, err := factory.ListenFakeTCP(ctx, platform.TCPListenOptions{Bind: bind, Purpose: platform.TCPListenDirect})
			if err != nil {
				t.Fatalf("listen: %v", err)
			}
			defer listener.Close()
			t.Logf("raw listener %s", listener.Addr())
			accepted := make(chan net.Conn, 2)
			acceptError := make(chan error, 1)
			go func() {
				for range 2 {
					connection, err := listener.Accept()
					if err != nil {
						acceptError <- err
						return
					}
					accepted <- connection
				}
			}()
			var clients, servers [2]net.Conn
			for index := range clients {
				client, err := factory.ConnectTCP(ctx, platform.TCPConnectOptions{
					RemoteAddr: listener.Addr().(*net.TCPAddr), Purpose: platform.TCPConnectFake,
					Bind: platform.TCPBindOptions{Context: bind.Context},
				})
				if err != nil {
					t.Fatalf("connect %d: %v", index, err)
				}
				defer client.Close()
				clients[index] = client
				select {
				case server := <-accepted:
					defer server.Close()
					servers[index] = server
				case err := <-acceptError:
					t.Fatalf("accept %d: %v", index, err)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			if clients[0].LocalAddr().(*net.TCPAddr).Port == clients[1].LocalAddr().(*net.TCPAddr).Port {
				t.Fatal("concurrent FakeTCP connections share a source port")
			}
			for index, client := range clients {
				server := servers[index]
				deadline := time.Now().Add(5 * time.Second)
				if err := client.SetDeadline(deadline); err != nil {
					t.Fatal(err)
				}
				if err := server.SetDeadline(deadline); err != nil {
					t.Fatal(err)
				}
				for _, exchange := range []struct {
					name     string
					sender   net.Conn
					receiver net.Conn
					body     string
				}{{"client-to-server", client, server, "easytier-faketcp-client-" + test.name}, {"server-to-client", server, client, "easytier-faketcp-server-" + test.name}} {
					if _, err := exchange.sender.Write([]byte(exchange.body)); err != nil {
						t.Fatalf("connection %d %s write: %v", index, exchange.name, err)
					}
					body := make([]byte, len(exchange.body))
					if _, err := io.ReadFull(exchange.receiver, body); err != nil {
						t.Fatalf("connection %d %s read: %v", index, exchange.name, err)
					}
					if string(body) != exchange.body {
						t.Fatalf("connection %d %s payload: %q", index, exchange.name, body)
					}
					t.Logf("connection %d %s: %d bytes", index, exchange.name, len(body))
				}
			}
		})
	}
}
