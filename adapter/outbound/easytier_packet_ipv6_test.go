//go:build !no_easytier

package outbound

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	et "github.com/metacubex/mihomo/component/easytier"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mipstack"
)

func TestEasyTierSharedIPv6(t *testing.T) {
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
	instances := make([]*EasyTier, 2)
	addresses := []netip.Addr{netip.MustParseAddr("fd00:144::1"), netip.MustParseAddr("fd00:144::2")}
	for i := range instances {
		listeners, peers := []string{peerURL}, []string(nil)
		if i == 1 {
			listeners, peers = nil, []string{peerURL}
		}
		instances[i], err = NewEasyTier(EasyTierOption{
			Name: fmt.Sprintf("shared-ipv6-%d", i), PacketMode: true,
			NetworkName: "shared-ipv6-test", NetworkSecret: "test-secret",
			IPv4: fmt.Sprintf("10.144.0.%d/24", i+1), IPv6: addresses[i].String() + "/64",
			Hostname: fmt.Sprintf("ipv6-%d", i), Listeners: listeners, Peers: peers,
			STUNServers: []string{}, STUNServersV6: []string{}, MTU: 1380,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { instances[i].Close() })
		if _, err := instances[i].OpenPacketSession(ctx); err != nil {
			t.Fatal(err)
		}
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for source, sender := range instances {
		for {
			instance, err := sender.currentInstance()
			if err != nil {
				t.Fatal(err)
			}
			routes, err := instance.ListRoute(ctx)
			if err != nil {
				t.Fatal(err)
			}
			ready := false
			for _, route := range routes {
				ipv6 := route.GetIpv6Addr()
				if ipv6 == nil || ipv6.GetAddress() == nil {
					continue
				}
				ip := ipv6.GetAddress()
				var raw [16]byte
				for i, part := range []uint32{ip.GetPart1(), ip.GetPart2(), ip.GetPart3(), ip.GetPart4()} {
					binary.BigEndian.PutUint32(raw[i*4:], part)
				}
				if netip.AddrFrom16(raw) == addresses[1-source] {
					ready = true
					break
				}
			}
			if ready {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatalf("%s IPv6 peer route: %v", sender.Name(), ctx.Err())
			case <-ticker.C:
			}
		}
	}
	devices := make([]*easyTierMemoryTun, len(instances))
	ordinary := make([]chan []byte, len(instances))
	packetErrors := make(chan error, 8)
	for i, instance := range instances {
		session, err := instance.OpenPacketSession(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(session.Addresses) != 2 || session.Addresses[1] != netip.PrefixFrom(addresses[i], 64) {
			t.Fatalf("runtime shared TUN addresses: %v", session.Addresses)
		}
		devices[i] = &easyTierMemoryTun{make(chan []byte, 8), make(chan []byte, 8), make(chan struct{}), sync.Once{}}
		mux, err := et.NewPacketMux(devices[i], []et.PacketSession{session}, false, nil, func(err error) { packetErrors <- err })
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = mux.Close() })
		ordinary[i] = make(chan []byte, 1)
		go func(index int) {
			buffer := make([]byte, 1380)
			for {
				n, err := mux.Read(buffer)
				if err != nil {
					return
				}
				ordinary[index] <- bytes.Clone(buffer[:n])
			}
		}(i)
	}
	for _, protocol := range []int{mipstack.ProtocolTCP, mipstack.ProtocolUDP, mipstack.ProtocolICMPv6} {
		for source, sender := range instances {
			src, dst := netip.AddrPortFrom(addresses[source], 40001), netip.AddrPortFrom(addresses[1-source], 40002)
			payload := []byte(fmt.Sprintf("shared IPv6 direction %d", source))
			var transport []byte
			switch protocol {
			case mipstack.ProtocolTCP:
				transport, err = (mipstack.TCPSegment{Source: src, Destination: dst, Flags: mipstack.TCPFlagACK, WindowSize: 65535, Payload: payload}).MarshalBinary()
			case mipstack.ProtocolUDP:
				transport, err = (mipstack.UDPDatagram{Source: src, Destination: dst, Payload: payload}).MarshalBinary()
			case mipstack.ProtocolICMPv6:
				transport = append([]byte{128, 0, 0, 0, 0, 1, 0, 1}, payload...)
				checksum, checksumErr := mipstack.IPTransportChecksum(src.Addr(), dst.Addr(), protocol, transport)
				if checksumErr != nil {
					t.Fatal(checksumErr)
				}
				binary.BigEndian.PutUint16(transport[2:4], checksum)
			}
			if err != nil {
				t.Fatal(err)
			}
			packet, err := (mipstack.IPPacket{Source: src.Addr(), Destination: dst.Addr(), Protocol: protocol, HopLimit: 64, Payload: transport}).MarshalRawBinary()
			if err != nil {
				t.Fatal(err)
			}
			devices[source].read <- packet
			select {
			case got := <-devices[1-source].write:
				if !bytes.Equal(got, packet) {
					t.Fatalf("IPv6 protocol %d packet changed: got %x, want %x", protocol, got, packet)
				}
			case err := <-packetErrors:
				t.Fatalf("%s send IPv6 protocol %d: %v", sender.Name(), protocol, err)
			case <-ctx.Done():
				t.Fatalf("%s send IPv6 protocol %d: %v", sender.Name(), protocol, ctx.Err())
			}
		}
	}
	packet, err := (mipstack.IPPacket{Source: addresses[0], Destination: netip.MustParseAddr("2001:db8::9"), Protocol: mipstack.ProtocolNoNextHeader, HopLimit: 64}).MarshalRawBinary()
	if err != nil {
		t.Fatal(err)
	}
	devices[0].read <- packet
	select {
	case got := <-ordinary[0]:
		if !bytes.Equal(got, packet) {
			t.Fatal("ordinary IPv6 packet changed before proxy stack")
		}
	case <-ctx.Done():
		t.Fatalf("ordinary IPv6 packet: %v", ctx.Err())
	}
}
