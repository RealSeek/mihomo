package sing_tun

import (
	"bytes"
	"context"
	"encoding/binary"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	et "github.com/metacubex/mihomo/component/easytier"
	"github.com/metacubex/mipstack"
	tun "github.com/metacubex/sing-tun"
	"github.com/metacubex/sing/common/buf"
	"github.com/metacubex/sing/common/logger"
	M "github.com/metacubex/sing/common/metadata"
	N "github.com/metacubex/sing/common/network"
)

type easyTierTestDevice struct {
	in, out chan []byte
	done    chan struct{}
	once    sync.Once
}

func (d *easyTierTestDevice) Read(p []byte) (int, error) {
	select {
	case packet := <-d.in:
		return copy(p, packet), nil
	case <-d.done:
		return 0, net.ErrClosed
	}
}

func (d *easyTierTestDevice) ReadPacket() ([]byte, func(), error) {
	select {
	case packet := <-d.in:
		return packet, nil, nil
	case <-d.done:
		return nil, nil, net.ErrClosed
	}
}

func (d *easyTierTestDevice) Write(p []byte) (int, error) {
	select {
	case d.out <- bytes.Clone(p):
		return len(p), nil
	case <-d.done:
		return 0, net.ErrClosed
	}
}

func (d *easyTierTestDevice) Close() error {
	d.once.Do(func() { close(d.done) })
	return nil
}

type easyTierTestEndpoint struct {
	sent, received chan []byte
}

func (e easyTierTestEndpoint) SendPacket(ctx context.Context, p []byte) error {
	select {
	case e.sent <- bytes.Clone(p):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e easyTierTestEndpoint) ReceivePacket(ctx context.Context) ([]byte, error) {
	select {
	case p := <-e.received:
		return p, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type easyTierTestHandler struct {
	packets chan []byte
}

func (h easyTierTestHandler) NewConnection(_ context.Context, conn net.Conn, _ M.Metadata) error {
	return conn.Close()
}

func (h easyTierTestHandler) NewPacket(_ context.Context, _ netip.AddrPort, b *buf.Buffer, _ M.Metadata, _ func(N.PacketConn) N.PacketWriter) {
	h.packets <- bytes.Clone(b.Bytes())
	b.Release()
}

func (easyTierTestHandler) PrepareConnection(string, M.Socksaddr, M.Socksaddr, tun.DirectRouteContext, time.Duration) (tun.DirectRouteDestination, error) {
	return nil, nil
}

func (easyTierTestHandler) NewError(context.Context, error) {}

func TestEasyTierExitRoutePreservesMihomoDNSAndFakeIP(t *testing.T) {
	native := &easyTierTestDevice{in: make(chan []byte, 4), out: make(chan []byte, 1), done: make(chan struct{})}
	endpoint := easyTierTestEndpoint{sent: make(chan []byte, 1), received: make(chan []byte)}
	handler := &ListenerHandler{
		Inet4Address: []netip.Prefix{netip.MustParsePrefix("198.18.0.1/16")},
		DnsAddrPorts: []netip.AddrPort{netip.MustParseAddrPort("0.0.0.0:53")},
	}
	device, err := newEasyTierDevice(native, []et.PacketSession{{
		ID: "exit", Endpoint: endpoint, Addresses: []netip.Prefix{netip.MustParsePrefix("10.144.0.1/24")},
		Routes: []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")},
	}}, 1380, false, handler.keepEasyTierPacketLocal, func(err error) { t.Error(err) })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = device.Close() })
	deadline := time.AfterFunc(5*time.Second, func() { _ = device.Close() })
	defer deadline.Stop()

	overlay := easyTierTestPacket(t, "10.144.0.1", "10.144.0.2", mipstack.ProtocolUDP, []byte("overlay"), false)
	native.in <- overlay
	for _, entry := range []struct {
		destination string
		protocol    int
		port        uint16
	}{
		{"198.18.1.2", mipstack.ProtocolTCP, 443},
		{"1.1.1.1", mipstack.ProtocolUDP, 53},
		{"1.1.1.1", mipstack.ProtocolTCP, 53},
	} {
		packet := easyTierTestPacket(t, "198.18.0.1", entry.destination, entry.protocol, []byte("local"), false)
		binary.BigEndian.PutUint16(packet[22:24], entry.port)
		if err := completeEasyTierChecksum(packet); err != nil {
			t.Fatal(err)
		}
		input := bytes.Clone(packet)
		input[10], input[11] = 0, 0 // Native input can defer its IP checksum.
		native.in <- input
		buffer := make([]byte, 1380)
		n, err := device.Read(buffer)
		if err != nil || !bytes.Equal(buffer[:n], packet) {
			t.Fatalf("mihomo packet %s:%d: %x, %v", entry.destination, entry.port, buffer[:n], err)
		}
	}
	select {
	case packet := <-endpoint.sent:
		if !bytes.Equal(packet, overlay) {
			t.Fatal("overlay packet changed")
		}
	default:
		t.Fatal("overlay packet did not reach EasyTier")
	}
}

func TestEasyTierDevicePreservesNativePacketPaths(t *testing.T) {
	native := &easyTierTestDevice{in: make(chan []byte, 3), out: make(chan []byte, 1), done: make(chan struct{})}
	endpoint := easyTierTestEndpoint{sent: make(chan []byte, 1), received: make(chan []byte, 1)}
	packets := make(chan []byte, 1)
	errors := make(chan error, 1)
	device, err := newEasyTierDevice(native, []et.PacketSession{{
		ID:        "native",
		Endpoint:  endpoint,
		Addresses: []netip.Prefix{netip.MustParsePrefix("10.144.0.1/24")},
		Routes:    []netip.Prefix{netip.MustParsePrefix("10.144.0.0/24")},
	}}, 1380, false, nil, func(err error) { errors <- err })
	if err != nil {
		t.Fatal(err)
	}
	address := netip.MustParsePrefix("198.18.0.1/30")
	stack, err := tun.NewStack("mips", tun.StackOptions{
		Context: context.Background(), Tun: device, Logger: logger.NOP(),
		UDPTimeout: time.Minute, ICMPTimeout: time.Minute,
		Handler:    easyTierTestHandler{packets: packets},
		TunOptions: tun.Options{MTU: 1380, Inet4Address: []netip.Prefix{address}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := stack.Start(); err != nil {
		_ = device.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stack.Close(); _ = device.Close() })

	overlay := easyTierTestPacket(t, "10.144.0.1", "10.144.0.2", mipstack.ProtocolUDP, []byte("overlay"), false)
	overlay[10], overlay[11], overlay[26], overlay[27] = 0, 0, 0, 1
	native.in <- overlay
	ordinary := easyTierTestPacket(t, "198.18.0.1", "192.0.2.1", mipstack.ProtocolUDP, []byte("ordinary"), false)
	ordinary[10], ordinary[11], ordinary[26], ordinary[27] = 0, 0, 0, 1
	native.in <- ordinary
	select {
	case sent := <-endpoint.sent:
		parsed, err := mipstack.ParseIPPacket(sent)
		if err != nil {
			t.Fatal(err)
		}
		udp, err := parsed.UDPDatagram()
		if err != nil || parsed.Source.String() != "10.144.0.1" || !bytes.Equal(udp.Payload, []byte("overlay")) {
			t.Fatalf("overlay packet: %+v, %v", parsed, err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("overlay packet was not sent")
	}
	select {
	case payload := <-packets:
		if !bytes.Equal(payload, []byte("ordinary")) {
			t.Fatalf("ordinary payload: %q", payload)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("native deferred checksums prevented ordinary UDP delivery")
	}

	inbound := easyTierTestPacket(t, "10.144.0.2", "10.144.0.1", mipstack.ProtocolTCP, []byte("incoming"), false)
	endpoint.received <- inbound
	select {
	case received := <-native.out:
		if !bytes.Equal(received, inbound) {
			t.Fatal("incoming overlay packet was changed before OS delivery")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("incoming overlay packet did not reach the OS device")
	}
	select {
	case err := <-errors:
		t.Fatal(err)
	default:
	}
}

func TestEasyTierDeviceIPv6(t *testing.T) {
	native := &easyTierTestDevice{in: make(chan []byte, 4), out: make(chan []byte, 1), done: make(chan struct{})}
	endpoint := easyTierTestEndpoint{sent: make(chan []byte, 2), received: make(chan []byte, 1)}
	handler := &ListenerHandler{
		Inet6Address: []netip.Prefix{netip.MustParsePrefix("fdfe:dcba:9876::1/126")},
		DnsAddrPorts: []netip.AddrPort{netip.MustParseAddrPort("[::]:53")},
	}
	device, err := newEasyTierDevice(native, []et.PacketSession{{
		ID: "ipv6", Endpoint: endpoint, Addresses: []netip.Prefix{netip.MustParsePrefix("fd00:144::1/64")},
		Routes: []netip.Prefix{netip.MustParsePrefix("::/0")},
	}}, 1380, false, handler.keepEasyTierPacketLocal, func(err error) { t.Error(err) })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = device.Close() })
	deadline := time.AfterFunc(5*time.Second, func() { _ = device.Close() })
	defer deadline.Stop()

	for _, protocol := range []int{mipstack.ProtocolTCP, mipstack.ProtocolUDP} {
		packet := easyTierTestPacket(t, "fd00:144::1", "fd00:144::2", protocol, []byte("ipv6"), false)
		parsed, err := mipstack.ParseIPPacket(packet)
		if err != nil {
			t.Fatal(err)
		}
		// An extension header moves the transport checksum beyond the base header.
		if err := parsed.SetIPv6ExtensionHeaders([]mipstack.IPv6ExtensionHeader{{Type: mipstack.IPv6ExtensionHeaderDestination, Data: []byte{0, 0, 0, 0, 0, 0, 0}}}, parsed.Protocol, parsed.Payload); err != nil {
			t.Fatal(err)
		}
		packet, err = parsed.MarshalRawBinary()
		if err != nil {
			t.Fatal(err)
		}
		checksumOffset := 48 + 16
		if protocol == mipstack.ProtocolUDP {
			checksumOffset = 48 + 6
		}
		packet[checksumOffset], packet[checksumOffset+1] = 0, 0
		native.in <- packet
	}
	ordinary := easyTierTestPacket(t, "fdfe:dcba:9876::1", "2606:4700:4700::1111", mipstack.ProtocolUDP, []byte("dns"), false)
	binary.BigEndian.PutUint16(ordinary[42:44], 53)
	if err := completeEasyTierChecksum(ordinary); err != nil {
		t.Fatal(err)
	}
	native.in <- ordinary
	buffer := make([]byte, 1380)
	n, err := device.Read(buffer)
	if err != nil || !bytes.Equal(buffer[:n], ordinary) {
		t.Fatalf("IPv6 DNS bypass: %x, %v", buffer[:n], err)
	}
	for _, protocol := range []int{mipstack.ProtocolTCP, mipstack.ProtocolUDP} {
		packet := <-endpoint.sent
		parsed, err := mipstack.ParseIPPacket(packet)
		if err != nil {
			t.Fatal(err)
		}
		if protocol == mipstack.ProtocolTCP {
			segment, err := parsed.TCPSegment()
			if err != nil || !bytes.Equal(segment.Payload, []byte("ipv6")) {
				t.Fatalf("IPv6 TCP checksum: %+v, %v", segment, err)
			}
		} else {
			datagram, err := parsed.UDPDatagram()
			if err != nil || !bytes.Equal(datagram.Payload, []byte("ipv6")) {
				t.Fatalf("IPv6 UDP checksum: %+v, %v", datagram, err)
			}
		}
	}
	for _, protocol := range []int{mipstack.ProtocolTCP, mipstack.ProtocolUDP} {
		inbound := easyTierTestPacket(t, "fd00:144::2", "fd00:144::1", protocol, []byte("incoming IPv6"), false)
		endpoint.received <- inbound
		select {
		case received := <-native.out:
			if !bytes.Equal(received, inbound) {
				t.Fatal("incoming IPv6 packet changed before OS delivery")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("incoming IPv6 packet did not reach the shared device")
		}
	}
}

func TestEasyTierDeviceDarwinFraming(t *testing.T) {
	native := &easyTierTestDevice{in: make(chan []byte, 1), out: make(chan []byte, 1), done: make(chan struct{})}
	device, err := newEasyTierDevice(native, nil, 1380, true, nil, func(err error) { t.Error(err) })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = device.Close() })
	packet := easyTierTestPacket(t, "198.18.0.1", "192.0.2.1", mipstack.ProtocolUDP, []byte("ordinary"), false)
	framed := append([]byte{0, 0, 0, 2}, packet...)
	native.in <- framed
	read, release, err := device.(tun.WinTun).ReadPacket()
	if err != nil || !bytes.Equal(read, packet) {
		t.Fatalf("native frame was not removed: %x, %v", read, err)
	}
	release()
	for _, output := range [][]byte{packet, framed} {
		n, err := device.Write(output)
		if err != nil || n != len(output) {
			t.Fatalf("write size: %d, %v", n, err)
		}
		if received := <-native.out; !bytes.Equal(received, framed) {
			t.Fatalf("native frame was duplicated: %x", received)
		}
	}
}

func TestEasyTierDeviceDynamicNetworks(t *testing.T) {
	native := &easyTierTestDevice{in: make(chan []byte, 4), out: make(chan []byte, 4), done: make(chan struct{})}
	device, err := newEasyTierDevice(native, nil, 1380, false, nil, func(err error) { t.Error(err) })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = device.Close() })
	deadline := time.AfterFunc(5*time.Second, func() { _ = device.Close() })
	defer deadline.Stop()
	updater := device.(interface {
		UpdateSessions([]et.PacketSession) error
	})
	first := easyTierTestEndpoint{sent: make(chan []byte, 1), received: make(chan []byte, 1)}
	replacement := easyTierTestEndpoint{sent: make(chan []byte, 1), received: make(chan []byte, 1)}
	second := easyTierTestEndpoint{sent: make(chan []byte, 1), received: make(chan []byte, 1)}
	sessions := []et.PacketSession{
		{ID: "first", Endpoint: first, Routes: []netip.Prefix{netip.MustParsePrefix("10.144.0.0/24")}},
		{ID: "second", Endpoint: second, Routes: []netip.Prefix{netip.MustParsePrefix("fd00:145::/64")}},
	}
	ordinary := easyTierTestPacket(t, "198.18.0.1", "192.0.2.1", mipstack.ProtocolUDP, []byte("ordinary"), false)
	read := func() {
		t.Helper()
		native.in <- ordinary
		buffer := make([]byte, 1380)
		n, err := device.Read(buffer)
		if err != nil || !bytes.Equal(buffer[:n], ordinary) {
			t.Fatalf("ordinary packet: %x, %v", buffer[:n], err)
		}
	}
	if err := updater.UpdateSessions(sessions); err != nil {
		t.Fatal(err)
	}
	for i, endpoint := range []easyTierTestEndpoint{first, second} {
		src, dst := "10.144.0.1", "10.144.0.2"
		if i == 1 {
			src, dst = "fd00:145::1", "fd00:145::2"
		}
		packet := easyTierTestPacket(t, src, dst, mipstack.ProtocolUDP, []byte("added"), false)
		native.in <- packet
		read()
		if got := <-endpoint.sent; !bytes.Equal(got, packet) {
			t.Fatal("added network did not receive its packet")
		}
	}
	sessions[0].Endpoint = replacement
	if err := updater.UpdateSessions(sessions); err != nil {
		t.Fatal(err)
	}
	packet := easyTierTestPacket(t, "10.144.0.2", "10.144.0.1", mipstack.ProtocolUDP, []byte("replacement"), false)
	first.received <- ordinary
	replacement.received <- packet
	select {
	case got := <-native.out:
		if !bytes.Equal(got, packet) {
			t.Fatal("replaced receiver injected a stale packet")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("replacement receiver did not inject its packet")
	}
	native.in <- packet
	read()
	if got := <-replacement.sent; !bytes.Equal(got, packet) {
		t.Fatal("replacement packet did not reach new endpoint")
	}
	if err := updater.UpdateSessions(sessions[1:]); err != nil {
		t.Fatal(err)
	}
	native.in <- packet
	buffer := make([]byte, 1380)
	n, err := device.Read(buffer)
	if err != nil || !bytes.Equal(buffer[:n], packet) {
		t.Fatal("deleted network still captured outgoing packets")
	}
	if err := updater.UpdateSessions(nil); err != nil {
		t.Fatal(err)
	}
	read()
}

func TestEasyTierDeviceCompletesTCPChecksum(t *testing.T) {
	packet := easyTierTestPacket(t, "10.144.0.1", "10.144.0.2", mipstack.ProtocolTCP, []byte("tcp"), false)
	packet[10], packet[11], packet[36], packet[37] = 0, 0, 0, 0
	if err := completeEasyTierChecksum(packet); err != nil {
		t.Fatal(err)
	}
	parsed, err := mipstack.ParseIPPacket(packet)
	if err != nil {
		t.Fatal(err)
	}
	tcp, err := parsed.TCPSegment()
	if err != nil || !bytes.Equal(tcp.Payload, []byte("tcp")) {
		t.Fatalf("TCP packet: %+v, %v", tcp, err)
	}
}

func TestEasyTierDevicePreservesFragmentsAndUDPZeroChecksum(t *testing.T) {
	packet := easyTierTestPacket(t, "10.144.0.1", "10.144.0.2", mipstack.ProtocolUDP, bytes.Repeat([]byte("x"), 80), false)
	parsed, err := mipstack.ParseIPPacket(packet)
	if err != nil {
		t.Fatal(err)
	}
	fragments, err := parsed.MarshalFragments(60, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range fragments {
		payload := bytes.Clone(fragment[20:])
		fragment[10], fragment[11] = 0, 0
		if err := completeEasyTierChecksum(fragment); err != nil {
			t.Fatal(err)
		}
		if _, err := mipstack.ParseIPPacket(fragment); err != nil || !bytes.Equal(fragment[20:], payload) {
			t.Fatalf("fragment changed: %x, %v", fragment, err)
		}
	}
	zero := easyTierTestPacket(t, "10.144.0.1", "10.144.0.2", mipstack.ProtocolUDP, []byte("zero"), true)
	if err := completeEasyTierChecksum(zero); err != nil {
		t.Fatal(err)
	}
	if binary.BigEndian.Uint16(zero[26:28]) != 0 {
		t.Fatal("IPv4 UDP zero checksum was replaced")
	}
}

func easyTierTestPacket(t *testing.T, source, destination string, protocol int, payload []byte, udpZeroChecksum bool) []byte {
	t.Helper()
	src, dst := netip.MustParseAddrPort(net.JoinHostPort(source, "1234")), netip.MustParseAddrPort(net.JoinHostPort(destination, "4321"))
	var transport []byte
	var err error
	if protocol == mipstack.ProtocolTCP {
		transport, err = (mipstack.TCPSegment{Source: src, Destination: dst, Flags: mipstack.TCPFlagACK, WindowSize: 65535, Payload: payload}).MarshalBinary()
	} else {
		transport, err = (mipstack.UDPDatagram{Source: src, Destination: dst, ChecksumDisabled: udpZeroChecksum, Payload: payload}).MarshalBinary()
	}
	if err != nil {
		t.Fatal(err)
	}
	packet, err := (mipstack.IPPacket{Source: src.Addr(), Destination: dst.Addr(), Protocol: protocol, HopLimit: 64, Payload: transport}).MarshalRawBinary()
	if err != nil {
		t.Fatal(err)
	}
	return packet
}
