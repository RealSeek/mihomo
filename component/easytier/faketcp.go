package easytier

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/metacubex/gopacket"
	"github.com/metacubex/gopacket/layers"
	"github.com/metacubex/mihomo/common/net/deadline"
)

type fakeTCPFrame struct {
	packet []byte
	mac    [8]byte
}

type fakeTCPPacketIO interface {
	ReadPacket() (fakeTCPFrame, error)
	WritePacket([]byte, [8]byte) error
	SetWriteDeadline(time.Time) error
	Close() error
}

type fakeTCPSegment struct {
	source, destination netip.AddrPort
	tcp                 layers.TCP
	mac                 [8]byte
}

// FakeTCP deliberately preserves the upstream's lossy TCP payload semantics;
// the kernel connection supplies its handshake and remains open as a decoy.
type fakeTCPConn struct {
	decoy         net.Conn
	packets       fakeTCPPacketIO
	local, remote netip.AddrPort
	seq, ack      atomic.Uint32
	readMu        sync.Mutex
	writeMu       sync.Mutex
	macMu         sync.Mutex
	remoteMAC     [8]byte
	incoming      chan fakeTCPSegment
	readError     error
	buffered      []byte
	closed        chan struct{}
	closeOnce     sync.Once
	readDeadline  deadline.PipeDeadline
}

func newFakeTCPConn(decoy net.Conn, packets fakeTCPPacketIO) *fakeTCPConn {
	connection := &fakeTCPConn{
		decoy: decoy, packets: packets,
		local: decoy.LocalAddr().(*net.TCPAddr).AddrPort(), remote: decoy.RemoteAddr().(*net.TCPAddr).AddrPort(),
		incoming: make(chan fakeTCPSegment, 512), closed: make(chan struct{}),
		readDeadline: deadline.MakePipeDeadline(),
	}
	connection.local = netip.AddrPortFrom(connection.local.Addr().Unmap().WithZone(""), connection.local.Port())
	connection.remote = netip.AddrPortFrom(connection.remote.Addr().Unmap().WithZone(""), connection.remote.Port())
	go connection.capture()
	go func() { _, _ = io.Copy(io.Discard, decoy) }()
	return connection
}

func (c *fakeTCPConn) capture() {
	defer close(c.incoming)
	for {
		frame, err := c.packets.ReadPacket()
		if err != nil {
			c.readError = err
			return
		}
		segment, err := parseFakeTCPSegment(frame)
		if err != nil || segment.source != c.remote || segment.destination != c.local {
			continue
		}
		select {
		case c.incoming <- segment:
		case <-c.closed:
			c.readError = net.ErrClosed
			return
		default:
			// The native stack drops frames when its bounded receive queue fills.
		}
	}
}

func (c *fakeTCPConn) establish(timeout <-chan time.Time) error {
	for {
		select {
		case <-timeout:
			return fmt.Errorf("easytier: FakeTCP SYN+ACK capture: %w", os.ErrDeadlineExceeded)
		case segment, ok := <-c.incoming:
			if !ok {
				return fmt.Errorf("easytier: FakeTCP handshake capture: %w", c.readError)
			}
			if segment.tcp.RST {
				return fmt.Errorf("easytier: FakeTCP handshake reset by peer")
			}
			if segment.tcp.SYN && segment.tcp.ACK {
				c.seq.Store(segment.tcp.Ack)
				c.ack.Store(segment.tcp.Seq + 1)
				c.remoteMAC = segment.mac
				return nil
			}
		}
	}
}

func (c *fakeTCPConn) Read(buffer []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	if len(buffer) == 0 {
		return 0, nil
	}
	for len(c.buffered) == 0 {
		select {
		case <-c.closed:
			return 0, net.ErrClosed
		case <-c.readDeadline.Wait():
			return 0, os.ErrDeadlineExceeded
		case segment, ok := <-c.incoming:
			if !ok {
				return 0, c.readError
			}
			if segment.tcp.RST {
				return 0, io.EOF
			}
			c.macMu.Lock()
			c.remoteMAC = segment.mac
			c.macMu.Unlock()
			if segment.tcp.ACK && len(segment.tcp.Payload) == 0 {
				c.seq.Store(segment.tcp.Ack)
			}
			c.ack.Store(segment.tcp.Seq + uint32(len(segment.tcp.Payload)))
			if err := c.replySACK(segment.tcp); err != nil {
				return 0, err
			}
			c.buffered = segment.tcp.Payload
		}
	}
	n := copy(buffer, c.buffered)
	c.buffered = c.buffered[n:]
	return n, nil
}

func (c *fakeTCPConn) replySACK(tcp layers.TCP) error {
	for _, option := range tcp.Options {
		if option.OptionType != layers.TCPOptionKindSACK {
			continue
		}
		for offset := 0; offset+8 <= len(option.OptionData); offset += 8 {
			left := tcp.Ack
			right := binary.BigEndian.Uint32(option.OptionData[offset : offset+4])
			end := binary.BigEndian.Uint32(option.OptionData[offset+4 : offset+8])
			length := right - left
			if length == 0 || end <= left {
				continue
			}
			return c.send(left, c.ack.Load(), false, make([]byte, min(length, 1400)))
		}
	}
	return nil
}

func (c *fakeTCPConn) Write(payload []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	select {
	case <-c.closed:
		return 0, net.ErrClosed
	default:
	}
	sequence := c.seq.Add(uint32(len(payload))) - uint32(len(payload))
	if err := c.send(sequence, c.ack.Load(), false, payload); err != nil {
		return 0, err
	}
	return len(payload), nil
}

func (c *fakeTCPConn) send(sequence, acknowledge uint32, reset bool, payload []byte) error {
	packet, err := buildFakeTCPPacket(c.local, c.remote, sequence, acknowledge, reset, payload)
	if err != nil {
		return err
	}
	c.macMu.Lock()
	mac := c.remoteMAC
	c.macMu.Unlock()
	if err := c.packets.WritePacket(packet, mac); err != nil {
		return fmt.Errorf("easytier: FakeTCP raw packet send: %w", err)
	}
	return nil
}

func (c *fakeTCPConn) Close() error {
	var closeError error
	c.closeOnce.Do(func() {
		close(c.closed)
		closeError = errors.Join(c.decoy.Close(), c.packets.Close())
	})
	return closeError
}

func (c *fakeTCPConn) LocalAddr() net.Addr  { return c.decoy.LocalAddr() }
func (c *fakeTCPConn) RemoteAddr() net.Addr { return c.decoy.RemoteAddr() }
func (c *fakeTCPConn) SetDeadline(value time.Time) error {
	c.readDeadline.Set(value)
	return c.packets.SetWriteDeadline(value)
}
func (c *fakeTCPConn) SetReadDeadline(value time.Time) error {
	c.readDeadline.Set(value)
	return nil
}
func (c *fakeTCPConn) SetWriteDeadline(value time.Time) error {
	return c.packets.SetWriteDeadline(value)
}

func buildFakeTCPPacket(local, remote netip.AddrPort, sequence, acknowledge uint32, reset bool, payload []byte) ([]byte, error) {
	layers.Init()
	tcp := &layers.TCP{
		SrcPort: layers.TCPPort(local.Port()), DstPort: layers.TCPPort(remote.Port()),
		Seq: sequence, Ack: acknowledge, ACK: !reset, RST: reset, Window: 65535,
	}
	var network gopacket.SerializableLayer
	if local.Addr().Is4() {
		ipv4 := &layers.IPv4{Version: 4, TTL: 64, Protocol: layers.IPProtocolTCP, Flags: layers.IPv4DontFragment, SrcIP: local.Addr().AsSlice(), DstIP: remote.Addr().AsSlice()}
		_ = tcp.SetNetworkLayerForChecksum(ipv4)
		network = ipv4
	} else {
		ipv6 := &layers.IPv6{Version: 6, HopLimit: 64, NextHeader: layers.IPProtocolTCP, SrcIP: local.Addr().AsSlice(), DstIP: remote.Addr().AsSlice()}
		_ = tcp.SetNetworkLayerForChecksum(ipv6)
		network = ipv6
	}
	buffer := gopacket.NewSerializeBuffer()
	if err := gopacket.SerializeLayers(buffer, gopacket.SerializeOptions{FixLengths: true, ComputeChecksums: true}, network, tcp, gopacket.Payload(payload)); err != nil {
		return nil, fmt.Errorf("easytier: encode FakeTCP packet: %w", err)
	}
	return buffer.Bytes(), nil
}

func parseFakeTCPSegment(frame fakeTCPFrame) (fakeTCPSegment, error) {
	var source, destination netip.Addr
	var tcpData []byte
	if len(frame.packet) == 0 {
		return fakeTCPSegment{}, io.ErrUnexpectedEOF
	}
	switch frame.packet[0] >> 4 {
	case 4:
		var ipv4 layers.IPv4
		if err := ipv4.DecodeFromBytes(frame.packet, gopacket.NilDecodeFeedback); err != nil {
			return fakeTCPSegment{}, err
		}
		if ipv4.Protocol != layers.IPProtocolTCP {
			return fakeTCPSegment{}, fmt.Errorf("non-TCP IPv4 packet")
		}
		source, _ = netip.AddrFromSlice(ipv4.SrcIP)
		destination, _ = netip.AddrFromSlice(ipv4.DstIP)
		tcpData = ipv4.Payload
	case 6:
		var ipv6 layers.IPv6
		if err := ipv6.DecodeFromBytes(frame.packet, gopacket.NilDecodeFeedback); err != nil {
			return fakeTCPSegment{}, err
		}
		if ipv6.NextHeader != layers.IPProtocolTCP {
			return fakeTCPSegment{}, fmt.Errorf("non-TCP IPv6 packet")
		}
		source, _ = netip.AddrFromSlice(ipv6.SrcIP)
		destination, _ = netip.AddrFromSlice(ipv6.DstIP)
		tcpData = ipv6.Payload
	default:
		return fakeTCPSegment{}, fmt.Errorf("unknown IP version")
	}
	var tcp layers.TCP
	if err := tcp.DecodeFromBytes(tcpData, gopacket.NilDecodeFeedback); err != nil {
		return fakeTCPSegment{}, err
	}
	return fakeTCPSegment{
		source:      netip.AddrPortFrom(source.Unmap(), uint16(tcp.SrcPort)),
		destination: netip.AddrPortFrom(destination.Unmap(), uint16(tcp.DstPort)), tcp: tcp, mac: frame.mac,
	}, nil
}
