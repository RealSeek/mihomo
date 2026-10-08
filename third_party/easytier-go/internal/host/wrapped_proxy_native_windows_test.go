package host

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	apiinstance "github.com/easytier/easytier/easytier-go/proto/api/instance"
)

func TestWrappedProxyNativeRawNICTCP(t *testing.T) {
	executable := os.Getenv("EASYTIER_NATIVE_EXECUTABLE")
	if executable == "" {
		t.Skip("EASYTIER_NATIVE_EXECUTABLE must identify an existing native EasyTier core")
	}
	version, err := exec.Command(executable, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("native version: %v: %s", err, version)
	}
	t.Logf("native: %s", bytes.TrimSpace(version))
	for _, transport := range []apiinstance.TcpProxyEntryTransportType{
		apiinstance.TcpProxyEntryTransportType_KCP,
		apiinstance.TcpProxyEntryTransportType_QUIC,
	} {
		t.Run(transport.String(), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			reservation, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := reservation.Addr().(*net.TCPAddr).Port
			if err := reservation.Close(); err != nil {
				t.Fatal(err)
			}
			forwardReservation, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			forward := forwardReservation.Addr().String()
			if err := forwardReservation.Close(); err != nil {
				t.Fatal(err)
			}
			echoListener, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer echoListener.Close()
			destination := netip.AddrPortFrom(netip.MustParseAddr("10.162.0.2"), uint16(echoListener.Addr().(*net.TCPAddr).Port))
			config := fmt.Sprintf(`hostname = "wrapped-destination"
ipv4 = "10.162.0.1/24"
listeners = ["tcp://127.0.0.1:%d"]
rpc_portal = "127.0.0.1:0"
stun_servers = []
stun_servers_v6 = []

[network_identity]
network_name = "wrapped-%s"
network_secret = "test"

[[port_forward]]
proto = "tcp"
bind_addr = %q
dst_addr = %q

[flags]
no_tun = true
use_smoltcp = true
bind_device = false
disable_p2p = true
disable_upnp = true
accept_dns = false
enable_encryption = false
enable_kcp_proxy = %t
disable_kcp_input = %t
enable_quic_proxy = %t
disable_quic_input = %t
`, port, transport, forward, destination.String(),
				transport == apiinstance.TcpProxyEntryTransportType_KCP, transport != apiinstance.TcpProxyEntryTransportType_KCP,
				transport == apiinstance.TcpProxyEntryTransportType_QUIC, transport != apiinstance.TcpProxyEntryTransportType_QUIC)
			directory := t.TempDir()
			configPath := filepath.Join(directory, "native.toml")
			if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			native := exec.CommandContext(ctx, executable, "--config-file", configPath, "--console-log-level", "warn")
			native.Dir = directory
			native.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
			native.Stdout, native.Stderr = &output, &output
			if err := native.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = native.Process.Kill()
				_ = native.Wait()
				if t.Failed() {
					t.Logf("native output:\n%s", &output)
				}
			})
			host, err := New(ctx, Options{})
			if err != nil {
				t.Fatalf("create embedded host: %v", err)
			}
			defer host.Close(context.Background())
			client := startWrappedProxyInstance(t, ctx, host, transport, "source", "10.162.0.2", port, true)
			defer client.Close(context.Background())
			waitWrappedProxyRoute(t, ctx, client, "wrapped-destination")
			testWrappedRawTCPEcho(t, ctx, client, nil, transport)
			testWrappedNativeForward(t, ctx, client, echoListener, forward, destination, transport)
		})
	}
}

func testWrappedNativeForward(t *testing.T, ctx context.Context, instance *Instance, listener net.Listener, forward string, destination netip.AddrPort, transport apiinstance.TcpProxyEntryTransportType) {
	t.Helper()
	echo := make(chan net.Conn, 1)
	echoDone := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err == nil {
			echo <- connection
			_, err = io.Copy(connection, connection)
		}
		echoDone <- err
	}()
	connection, err := net.DialTimeout("tcp4", forward, 5*time.Second)
	if err != nil {
		t.Fatalf("connect native wrapped forward: %v", err)
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(5 * time.Second))
	payload := []byte("native-forward-" + transport.String())
	if _, err := connection.Write(payload); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, len(payload))
	if _, err := io.ReadFull(connection, reply); err != nil || !bytes.Equal(reply, payload) {
		t.Fatalf("native wrapped forward response: %q, %v", reply, err)
	}
	select {
	case connection := <-echo:
		defer connection.Close()
	case err := <-echoDone:
		t.Fatalf("accept native wrapped forward: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	source := connection.LocalAddr().(*net.TCPAddr).AddrPort()
	if transport == apiinstance.TcpProxyEntryTransportType_QUIC {
		// Native QUIC allocates a userspace source port; KCP retains the source hint.
		entries := new(apiinstance.ListTcpProxyEntryResponse)
		if err := instance.callRPC(ctx, listTcpProxyEntryMethod, entries); err != nil {
			t.Fatalf("list native-initiated wrapped destination entry: %v", err)
		}
		for _, entry := range entries.Entries {
			if entry.GetDst().GetIpv4().GetAddr() == binary.BigEndian.Uint32(destination.Addr().AsSlice()) && entry.GetDst().GetPort() == uint32(destination.Port()) {
				source = netip.AddrPortFrom(netip.MustParseAddr("10.162.0.1"), uint16(entry.GetSrc().GetPort()))
				break
			}
		}
	}
	assertWrappedProxyEntry(t, ctx, instance, "destination", transport, source, destination)
}
