//go:build !no_easytier

package outbound

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"os"
	"sync"
	"testing"
	"time"

	et "github.com/metacubex/mihomo/component/easytier"
	C "github.com/metacubex/mihomo/constant"
	D "github.com/miekg/dns"
)

type easyTierMemoryTun struct {
	read   chan []byte
	write  chan []byte
	closed chan struct{}
	once   sync.Once
}

func (d *easyTierMemoryTun) Read(buffer []byte) (int, error) {
	select {
	case packet := <-d.read:
		return copy(buffer, packet), nil
	case <-d.closed:
		return 0, os.ErrClosed
	}
}

func (d *easyTierMemoryTun) Write(packet []byte) (int, error) {
	select {
	case d.write <- bytes.Clone(packet):
		return len(packet), nil
	case <-d.closed:
		return 0, os.ErrClosed
	}
}

func (d *easyTierMemoryTun) Close() error {
	d.once.Do(func() { close(d.closed) })
	return nil
}

func TestEasyTierDHCPPacketSession(t *testing.T) {
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
	server, err := NewEasyTier(EasyTierOption{
		Name: "dhcp-server", PacketMode: true, NetworkName: "dhcp-packet-test",
		NetworkSecret: "test-secret", IPv4: "10.144.0.1/24", Listeners: []string{peerURL},
		STUNServers: []string{}, STUNServersV6: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := server.OpenPacketSession(ctx); err != nil {
		t.Fatal(err)
	}
	outbound, err := NewEasyTier(EasyTierOption{
		Name: "dhcp-packet", PacketMode: true, NetworkName: "dhcp-packet-test",
		NetworkSecret: "test-secret", Peers: []string{peerURL},
		STUNServers: []string{}, STUNServersV6: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer outbound.Close()
	session, err := outbound.OpenPacketSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(session.Addresses) != 1 || !session.Addresses[0].Addr().Is4() || len(session.Routes) == 0 || session.Routes[0] != session.Addresses[0].Masked() {
		t.Fatalf("runtime DHCP packet session: %+v", session)
	}
	t.Logf("official core DHCP lease: %s", session.Addresses[0])
}

func TestEasyTierSharedPacketPlane(t *testing.T) {
	previousHome := C.Path.HomeDir()
	C.SetHomeDir(t.TempDir())
	t.Cleanup(func() { C.SetHomeDir(previousHome) })
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	reservation, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	peerURL := fmt.Sprintf("tcp://%s", reservation.Addr())
	if err := reservation.Close(); err != nil {
		t.Fatal(err)
	}
	server, err := NewEasyTier(EasyTierOption{
		PacketMode: true,
		Name:       "packet-server",
		ConfigTOML: fmt.Sprintf(`ipv4 = "10.144.0.1/24"
hostname = "server"
listeners = [%q]
[network_identity]
network_name = "packet-test"
network_secret = "test-secret"
[flags]
mtu = 1380
`, peerURL),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	serverSession, err := server.OpenPacketSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewEasyTier(EasyTierOption{
		PacketMode: true,
		Name:       "packet-client", NetworkName: "packet-test", NetworkSecret: "test-secret",
		Hostname: "client", IPv4: "10.144.0.2/24", Peers: []string{peerURL},
		STUNServers: []string{}, STUNServersV6: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	clientSession, err := client.OpenPacketSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Both directions must discover the current remote peer before the first
	// packet. A hostname can still refer to an old peer after a core restart.
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	waitForRoutes := func() {
		for source, sender := range []*EasyTier{server, client} {
			receiver := []*EasyTier{client, server}[source]
			remote, err := receiver.currentInstance()
			if err != nil {
				t.Fatal(err)
			}
			info, err := remote.ShowNodeInfo(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for {
				local, err := sender.currentInstance()
				if err != nil {
					t.Fatal(err)
				}
				routes, err := local.ListRoute(ctx)
				if err != nil {
					t.Fatal(err)
				}
				ready := false
				for _, route := range routes {
					if route.GetPeerId() == info.GetPeerId() && route.GetNextHopPeerId() == info.GetPeerId() {
						ready = true
						break
					}
				}
				if ready {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatalf("%s route to %s peer %d: %v; routes: %v", sender.Name(), receiver.Name(), info.GetPeerId(), ctx.Err(), routes)
				case <-ticker.C:
				}
			}
		}
	}
	waitForRoutes()

	devices := []*easyTierMemoryTun{}
	muxes := []*et.PacketMux{}
	ordinary := []chan []byte{}
	packetErrors := make(chan error, 8)
	for _, session := range []et.PacketSession{serverSession, clientSession} {
		device := &easyTierMemoryTun{make(chan []byte, 8), make(chan []byte, 8), make(chan struct{}), sync.Once{}}
		mux, err := et.NewPacketMux(device, []et.PacketSession{session}, false, nil, func(err error) { packetErrors <- err })
		if err != nil {
			t.Fatal(err)
		}
		defer mux.Close()
		devices, muxes = append(devices, device), append(muxes, mux)
		unmatched := make(chan []byte, 1)
		ordinary = append(ordinary, unmatched)
		go func() {
			buffer := make([]byte, 65535)
			for {
				n, err := mux.Read(buffer)
				if err != nil {
					return
				}
				unmatched <- bytes.Clone(buffer[:n])
			}
		}()
	}

	for _, protocol := range []byte{1, 6, 17} {
		for source := range 2 {
			packet := easyTierTestIPPacket(byte(source+1), byte(2-source), protocol)
			devices[source].read <- packet
			select {
			case got := <-devices[1-source].write:
				if !bytes.Equal(got, packet) {
					t.Fatalf("protocol %d packet changed: got %x, want %x", protocol, got, packet)
				}
			case err := <-packetErrors:
				t.Fatalf("protocol %d: %v", protocol, err)
			case <-ctx.Done():
				t.Fatalf("protocol %d, %s -> %s: %v", protocol, []string{"server", "client"}[source], []string{"client", "server"}[source], ctx.Err())
			}
		}
	}
	previousInstance, err := client.currentInstance()
	if err != nil {
		t.Fatal(err)
	}
	if err := previousInstance.Close(ctx); err != nil {
		t.Fatal(err)
	}
	for {
		if err := client.ensureStarted(ctx); err != nil {
			t.Fatal(err)
		}
		current, err := client.currentInstance()
		if err != nil {
			t.Fatal(err)
		}
		if current != previousInstance {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
	waitForRoutes()
	// The existing mux must receive packets after core replacement, without
	// closing or replacing the shared OS device.
	packet := easyTierTestIPPacket(1, 2, 17)
	devices[0].read <- packet
	select {
	case got := <-devices[1].write:
		if !bytes.Equal(got, packet) {
			t.Fatal("packet changed after reconnect")
		}
	case err := <-packetErrors:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	other := easyTierTestIPPacket(2, 9, 17)
	copy(other[16:20], netip.MustParseAddr("203.0.113.9").AsSlice())
	devices[1].read <- other
	select {
	case got := <-ordinary[1]:
		if !bytes.Equal(got, other) {
			t.Fatal("ordinary mihomo packet changed")
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	for _, name := range []string{"server.et.net.", "client.et.net."} {
		query := new(D.Msg)
		query.SetQuestion(name, D.TypeA)
		reply, err := (easyTierDNSTransport{client}).ExchangeContext(ctx, query)
		if err != nil || len(reply.Answer) != 1 {
			t.Fatalf("Magic DNS %s: %v, %v", name, reply, err)
		}
	}
	status, err := server.EasyTierStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Node == nil || status.Node.Hostname != "server" || status.Provenance.Commit == "" || len(status.Routes) == 0 {
		t.Fatalf("incomplete live status: %+v", status)
	}
	query := new(D.Msg)
	query.SetQuestion("1.0.144.10.in-addr.arpa.", D.TypePTR)
	reply, err := (easyTierDNSTransport{client}).ExchangeContext(ctx, query)
	if err != nil || len(reply.Answer) != 1 || reply.Answer[0].(*D.PTR).Ptr != "server.et.net." {
		t.Fatalf("Magic DNS PTR: %v, %v", reply, err)
	}
	for _, mux := range muxes {
		if err := mux.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func easyTierTestIPPacket(source, destination, protocol byte) []byte {
	packet := make([]byte, 40)
	packet[0], packet[8], packet[9] = 0x45, 64, protocol
	binary.BigEndian.PutUint16(packet[2:4], uint16(len(packet)))
	copy(packet[12:16], []byte{10, 144, 0, source})
	copy(packet[16:20], []byte{10, 144, 0, destination})
	switch protocol {
	case 1:
		packet[20] = 8
		binary.BigEndian.PutUint16(packet[22:24], easyTierTestChecksum(packet[20:]))
	case 6, 17:
		binary.BigEndian.PutUint16(packet[20:22], 40001)
		binary.BigEndian.PutUint16(packet[22:24], 40002)
		checksumOffset := 36
		if protocol == 6 {
			packet[32], packet[33] = 0x50, 2 // TCP SYN, 20-byte header
			binary.BigEndian.PutUint16(packet[34:36], 65535)
		} else {
			binary.BigEndian.PutUint16(packet[24:26], 20)
			checksumOffset = 26
		}
		pseudo := make([]byte, 12+len(packet)-20)
		copy(pseudo[:8], packet[12:20])
		pseudo[9] = protocol
		binary.BigEndian.PutUint16(pseudo[10:12], uint16(len(packet)-20))
		copy(pseudo[12:], packet[20:])
		binary.BigEndian.PutUint16(packet[checksumOffset:checksumOffset+2], easyTierTestChecksum(pseudo))
	}
	binary.BigEndian.PutUint16(packet[10:12], easyTierTestChecksum(packet[:20]))
	return packet
}

func easyTierTestChecksum(packet []byte) uint16 {
	var sum uint32
	for i := 0; i < len(packet); i += 2 {
		sum += uint32(binary.BigEndian.Uint16(packet[i : i+2]))
	}
	for sum > 65535 {
		sum = sum&65535 + sum>>16
	}
	return ^uint16(sum)
}
