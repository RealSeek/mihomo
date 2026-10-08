//go:build !no_easytier

package outbound

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	et "github.com/metacubex/mihomo/component/easytier"
	"github.com/metacubex/mihomo/component/resolver"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mipstack"
)

func TestEasyTierNativeInterop(t *testing.T) {
	protocols := []string{"tcp", "wg", "quic", "ws", "wss"}
	if os.Getenv("EASYTIER_FAKETCP_RAW_TEST") == "1" {
		protocols = append(protocols, "faketcp")
	}
	for _, protocol := range protocols {
		t.Run(protocol, func(t *testing.T) { testEasyTierNativeInterop(t, protocol, false) })
	}
}

func TestEasyTierNativeInteropIPv6(t *testing.T) {
	previousIPv6 := resolver.DisableIPv6
	resolver.DisableIPv6 = false
	t.Cleanup(func() { resolver.DisableIPv6 = previousIPv6 })
	for _, protocol := range []string{"wg", "quic", "ws", "wss"} {
		t.Run(protocol, func(t *testing.T) { testEasyTierNativeInterop(t, protocol, true) })
	}
}

func testEasyTierNativeInterop(t *testing.T, protocol string, ipv6 bool) {
	executable := os.Getenv("EASYTIER_NATIVE_EXECUTABLE")
	if executable == "" {
		t.Skip("EASYTIER_NATIVE_EXECUTABLE must identify an existing native EasyTier core")
	}
	version, err := exec.Command(executable, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("native version: %v: %s", err, version)
	}
	t.Logf("native: %s", bytes.TrimSpace(version))
	previousHome := C.Path.HomeDir()
	C.SetHomeDir(t.TempDir())
	t.Cleanup(func() { C.SetHomeDir(previousHome) })
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	identity := uuid.Must(uuid.NewV4()).String()
	reserveTCP := func() string {
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		address := listener.Addr().String()
		if err := listener.Close(); err != nil {
			t.Fatal(err)
		}
		return address
	}
	underlay, tcpForward := reserveTCP(), reserveTCP()
	underlayNetwork, underlayAddress := "tcp4", "127.0.0.1:0"
	if ipv6 {
		underlayNetwork, underlayAddress = "tcp6", "[::1]:0"
	}
	if protocol == "wg" || protocol == "quic" {
		if ipv6 {
			underlayNetwork = "udp6"
		} else {
			underlayNetwork = "udp4"
		}
		reservation, err := net.ListenPacket(underlayNetwork, underlayAddress)
		if err != nil {
			t.Fatal(err)
		}
		underlay = reservation.LocalAddr().String()
		if err := reservation.Close(); err != nil {
			t.Fatal(err)
		}
	} else if ipv6 {
		reservation, err := net.Listen(underlayNetwork, underlayAddress)
		if err != nil {
			t.Fatal(err)
		}
		underlay = reservation.Addr().String()
		if err := reservation.Close(); err != nil {
			t.Fatal(err)
		}
	}
	peerURL := protocol + "://" + underlay
	if protocol == "ws" || protocol == "wss" {
		peerURL += "/"
	}
	t.Logf("native underlay: %s", peerURL)
	udpReservation, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	udpForward := udpReservation.LocalAddr().String()
	if err := udpReservation.Close(); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf(`instance_id = %q
instance_name = "native-interop"
hostname = "native"
ipv4 = "10.144.0.1/24"
listeners = [%q]
rpc_portal = "127.0.0.1:0"
stun_servers = []
stun_servers_v6 = []
[network_identity]
network_name = %q
network_secret = "native-interop-secret"
[[proxy_network]]
cidr = "127.0.0.1/32"
[[port_forward]]
proto = "tcp"
bind_addr = %q
dst_addr = "10.144.0.2:42000"
[[port_forward]]
proto = "udp"
bind_addr = %q
dst_addr = "10.144.0.2:42001"
[flags]
no_tun = true
use_smoltcp = true
bind_device = false
disable_p2p = true
disable_upnp = true
accept_dns = false
`, identity, peerURL, identity, tcpForward, udpForward)
	configDirectory := t.TempDir()
	configPath := filepath.Join(configDirectory, "native.toml")
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	native := exec.CommandContext(ctx, executable, "--config-file", configPath, "--console-log-level", "warn")
	native.Dir = configDirectory
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
	hostListener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer hostListener.Close()
	embeddedForward := reserveTCP()
	client, err := NewEasyTier(EasyTierOption{
		PacketMode: true, Name: "native-interop", NetworkName: identity,
		NetworkSecret: "native-interop-secret", Hostname: "embedded",
		IPv4: "10.144.0.2/24", Peers: []string{peerURL},
		STUNServers: []string{}, STUNServersV6: []string{},
		PortForwards: []et.PortForwardOption{{
			Protocol: et.PortForwardTCP, Bind: embeddedForward,
			Destination: net.JoinHostPort("10.144.0.1", fmt.Sprint(hostListener.Addr().(*net.TCPAddr).Port)),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	session, err := client.OpenPacketSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		nodes, err := client.overlayNodes(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, found := et.LookupOverlayHost("native.et.net", "et.net", nodes); found {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("discover native peer: %v", ctx.Err())
		case <-ticker.C:
		}
	}
	instance, err := client.currentInstance()
	if err != nil {
		t.Fatal(err)
	}
	listener, err := instance.Listen("tcp4", ":42000")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serverResult := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err == nil {
			defer connection.Close()
			_, err = io.Copy(connection, connection)
		}
		serverResult <- err
	}()
	connection, err := net.DialTimeout("tcp4", tcpForward, 5*time.Second)
	if err != nil {
		t.Fatalf("connect native TCP forward: %v", err)
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(10 * time.Second))
	payload := []byte("native TCP forward to embedded core")
	if _, err := connection.Write(payload); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, len(payload))
	if _, err := io.ReadFull(connection, reply); err != nil || !bytes.Equal(reply, payload) {
		t.Fatalf("native TCP forward response: %q, %v", reply, err)
	}
	_ = connection.Close()
	if err := <-serverResult; err != nil {
		t.Fatal(err)
	}
	go func() {
		connection, err := hostListener.Accept()
		if err == nil {
			defer connection.Close()
			_, err = io.Copy(connection, connection)
		}
		serverResult <- err
	}()
	connection, err = net.DialTimeout("tcp4", embeddedForward, 5*time.Second)
	if err != nil {
		t.Fatalf("connect mihomo structured TCP forward: %v", err)
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(10 * time.Second))
	payload = []byte("mihomo structured TCP forward to native host socket")
	if _, err := connection.Write(payload); err != nil {
		t.Fatal(err)
	}
	reply = make([]byte, len(payload))
	if _, err := io.ReadFull(connection, reply); err != nil || !bytes.Equal(reply, payload) {
		t.Fatalf("mihomo TCP forward response: %q, %v", reply, err)
	}
	_ = connection.Close()
	if err := <-serverResult; err != nil {
		t.Fatal(err)
	}

	session.Routes = append(session.Routes, netip.MustParsePrefix("127.0.0.1/32"))
	device := &easyTierMemoryTun{make(chan []byte, 8), make(chan []byte, 8), make(chan struct{}), sync.Once{}}
	packetErrors := make(chan error, 8)
	mux, err := et.NewPacketMux(device, []et.PacketSession{session}, false, nil, func(err error) { packetErrors <- err })
	if err != nil {
		t.Fatal(err)
	}
	defer mux.Close()
	go func() {
		_, err := mux.Read(make([]byte, 65535))
		if err == nil {
			packetErrors <- fmt.Errorf("native packet unexpectedly bypassed overlay")
		}
	}()
	receive := func() mipstack.UDPDatagram {
		t.Helper()
		select {
		case packet := <-device.write:
			parsed, err := mipstack.ParseIPPacket(packet)
			if err != nil {
				t.Fatal(err)
			}
			datagram, err := parsed.UDPDatagram()
			if err != nil {
				t.Fatal(err)
			}
			return datagram
		case err := <-packetErrors:
			t.Fatal(err)
		case <-ctx.Done():
			t.Fatalf("native raw UDP receive: %v", ctx.Err())
		}
		return mipstack.UDPDatagram{}
	}
	marshal := func(source, destination netip.AddrPort, payload []byte) []byte {
		t.Helper()
		transport, err := (mipstack.UDPDatagram{Source: source, Destination: destination, Payload: payload}).MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		packet, err := (mipstack.IPPacket{Source: source.Addr(), Destination: destination.Addr(), Protocol: mipstack.ProtocolUDP, HopLimit: 64, Payload: transport}).MarshalRawBinary()
		if err != nil {
			t.Fatal(err)
		}
		return packet
	}
	echo, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	_ = echo.SetDeadline(time.Now().Add(10 * time.Second))
	go func() {
		buffer := make([]byte, 1500)
		n, source, err := echo.ReadFromUDP(buffer)
		if err == nil {
			_, err = echo.WriteToUDP(buffer[:n], source)
		}
		serverResult <- err
	}()
	source := netip.MustParseAddrPort("10.144.0.2:42002")
	// Native no-tun maps its virtual address to local loopback services.
	destination := netip.AddrPortFrom(netip.MustParseAddr("10.144.0.1"), uint16(echo.LocalAddr().(*net.UDPAddr).Port))
	payload = []byte("shared TUN to native ordinary UDP socket")
	device.read <- marshal(source, destination, payload)
	datagram := receive()
	if datagram.Source != destination || datagram.Destination != source || !bytes.Equal(datagram.Payload, payload) {
		t.Fatalf("native subnet UDP response: %+v", datagram)
	}
	if err := <-serverResult; err != nil {
		t.Fatal(err)
	}
	udp, err := net.Dial("udp4", udpForward)
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	_ = udp.SetDeadline(time.Now().Add(10 * time.Second))
	payload = []byte("native ordinary UDP socket to shared TUN")
	if _, err := udp.Write(payload); err != nil {
		t.Fatal(err)
	}
	datagram = receive()
	if datagram.Source.Addr() != netip.MustParseAddr("10.144.0.1") || datagram.Destination != netip.MustParseAddrPort("10.144.0.2:42001") || !bytes.Equal(datagram.Payload, payload) {
		t.Fatalf("native first inbound UDP packet: %+v", datagram)
	}
	device.read <- marshal(datagram.Destination, datagram.Source, datagram.Payload)
	reply = make([]byte, 1500)
	n, err := udp.Read(reply)
	if err != nil || !bytes.Equal(reply[:n], payload) {
		t.Fatalf("native UDP forward response: %q, %v", reply[:n], err)
	}
}
