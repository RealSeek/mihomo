package easytier

import (
	"encoding/binary"
	"io"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"
)

func TestFakeTCPWirePackets(t *testing.T) {
	for _, addresses := range [][2]string{
		{"192.0.2.1:12345", "198.51.100.2:23456"},
		{"[2001:db8::1]:12345", "[2001:db8::2]:23456"},
	} {
		local, remote := netip.MustParseAddrPort(addresses[0]), netip.MustParseAddrPort(addresses[1])
		payload := []byte("hello fake tcp")
		packet, err := buildFakeTCPPacket(local, remote, 10, 20, false, payload)
		if err != nil {
			t.Fatal(err)
		}
		segment, err := parseFakeTCPSegment(fakeTCPFrame{packet: packet})
		if err != nil {
			t.Fatal(err)
		}
		if segment.source != local || segment.destination != remote || segment.tcp.Seq != 10 || segment.tcp.Ack != 20 || !segment.tcp.ACK || segment.tcp.PSH || string(segment.tcp.Payload) != string(payload) {
			t.Fatalf("incompatible FakeTCP segment: %+v", segment)
		}
		headerLength := 40
		if local.Addr().Is4() {
			headerLength = 20
			if packet[8] != 64 || packet[6] != 0x40 || fakeTCPTestChecksum(packet[:headerLength]) != 0 {
				t.Fatalf("incorrect native IPv4 header: %x", packet[:headerLength])
			}
		} else if packet[7] != 64 {
			t.Fatalf("incorrect native IPv6 hop limit: %x", packet[:headerLength])
		}
		tcp := packet[headerLength:]
		pseudoHeader := append(local.Addr().AsSlice(), remote.Addr().AsSlice()...)
		if local.Addr().Is4() {
			pseudoHeader = append(pseudoHeader, 0, 6, byte(len(tcp)>>8), byte(len(tcp)))
		} else {
			pseudoHeader = binary.BigEndian.AppendUint32(pseudoHeader, uint32(len(tcp)))
			pseudoHeader = append(pseudoHeader, 0, 0, 0, 6)
		}
		if fakeTCPTestChecksum(append(pseudoHeader, tcp...)) != 0 {
			t.Fatalf("invalid TCP checksum for %s: %x", local, tcp)
		}
		if tcp[12] != 5<<4 || tcp[13] != 0x10 || binary.BigEndian.Uint16(tcp[14:16]) != 65535 {
			t.Fatalf("incorrect native TCP header: %x", tcp)
		}
	}
}

func TestFakeTCPHandshakeAndStream(t *testing.T) {
	local := netip.MustParseAddrPort("192.0.2.1:12345")
	remote := netip.MustParseAddrPort("198.51.100.2:23456")
	decoy, peer := net.Pipe()
	defer peer.Close()
	packets := &fakeTCPTestPacketIO{incoming: make(chan fakeTCPFrame, 4)}
	connection := newFakeTCPConn(fakeTCPTestDecoy{Conn: decoy, local: local, remote: remote}, packets)
	defer connection.Close()
	packet, err := buildFakeTCPPacket(remote, local, 200, 100, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	packet[33] |= 0x02
	packets.incoming <- fakeTCPFrame{packet: packet}
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	if err := connection.establish(timer.C); err != nil {
		t.Fatal(err)
	}
	packet, err = buildFakeTCPPacket(remote, local, 201, 100, false, []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	packets.incoming <- fakeTCPFrame{packet: packet}
	if err := connection.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 3)
	if n, err := io.ReadFull(connection, buffer); err != nil || n != 3 || string(buffer) != "hel" {
		t.Fatalf("first stream read: %q %d %v", buffer, n, err)
	}
	buffer = make([]byte, 2)
	if n, err := io.ReadFull(connection, buffer); err != nil || n != 2 || string(buffer) != "lo" {
		t.Fatalf("remaining stream read: %q %d %v", buffer, n, err)
	}
	if _, err := connection.Write([]byte("world")); err != nil {
		t.Fatal(err)
	}
	packets.mu.Lock()
	segment, err := parseFakeTCPSegment(fakeTCPFrame{packet: packets.outgoing[0]})
	packets.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if segment.tcp.Seq != 100 || segment.tcp.Ack != 206 || string(segment.tcp.Payload) != "world" || connection.seq.Load() != 105 {
		t.Fatalf("FakeTCP state after kernel handshake: %+v seq=%d", segment, connection.seq.Load())
	}
}

func fakeTCPTestChecksum(data []byte) uint16 {
	var checksum uint32
	for len(data) >= 2 {
		checksum += uint32(binary.BigEndian.Uint16(data))
		data = data[2:]
	}
	if len(data) != 0 {
		checksum += uint32(data[0]) << 8
	}
	for checksum > 65535 {
		checksum = checksum>>16 + checksum&65535
	}
	return ^uint16(checksum)
}

type fakeTCPTestDecoy struct {
	net.Conn
	local, remote netip.AddrPort
}

func (d fakeTCPTestDecoy) LocalAddr() net.Addr  { return net.TCPAddrFromAddrPort(d.local) }
func (d fakeTCPTestDecoy) RemoteAddr() net.Addr { return net.TCPAddrFromAddrPort(d.remote) }

type fakeTCPTestPacketIO struct {
	mu       sync.Mutex
	incoming chan fakeTCPFrame
	outgoing [][]byte
}

func (p *fakeTCPTestPacketIO) ReadPacket() (fakeTCPFrame, error) {
	frame, ok := <-p.incoming
	if !ok {
		return fakeTCPFrame{}, net.ErrClosed
	}
	return frame, nil
}
func (p *fakeTCPTestPacketIO) WritePacket(packet []byte, _ [8]byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.outgoing = append(p.outgoing, append([]byte(nil), packet...))
	return nil
}
func (p *fakeTCPTestPacketIO) SetWriteDeadline(time.Time) error { return nil }
func (p *fakeTCPTestPacketIO) Close() error {
	close(p.incoming)
	return nil
}
