//go:build (linux || android) && !no_fake_tcp

package easytier

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"os"
	"syscall"
	"time"

	"github.com/metacubex/mihomo/component/iface"
	"golang.org/x/net/bpf"
	"golang.org/x/sys/unix"
)

type fakeTCPPacketSocket struct {
	file     *os.File
	control  syscall.RawConn
	ifindex  int
	protocol uint16
	halen    uint8
}

func newFakeTCPPacketSocket(local, remote netip.AddrPort) (fakeTCPPacketIO, error) {
	local = netip.AddrPortFrom(local.Addr().Unmap().WithZone(""), local.Port())
	remote = netip.AddrPortFrom(remote.Addr().Unmap().WithZone(""), remote.Port())
	networkInterface, err := iface.ResolveInterfaceByAddr(local.Addr())
	if err != nil {
		return nil, fmt.Errorf("easytier: FakeTCP interface for %s: %w", local.Addr(), err)
	}
	protocol := uint16(unix.ETH_P_IP)
	if local.Addr().Is6() {
		protocol = unix.ETH_P_IPV6
	}
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_DGRAM|unix.SOCK_NONBLOCK|unix.SOCK_CLOEXEC, int(fakeTCPNetworkOrder(protocol)))
	if err != nil {
		return nil, fmt.Errorf("easytier: FakeTCP AF_PACKET socket requires CAP_NET_RAW: %w", err)
	}
	file := os.NewFile(uintptr(fd), "easytier-faketcp-packet")
	control, err := file.SyscallConn()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if err = unix.Bind(fd, &unix.SockaddrLinklayer{Protocol: fakeTCPNetworkOrder(protocol), Ifindex: networkInterface.Index}); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("easytier: FakeTCP bind interface %s: %w", networkInterface.Name, err)
	}
	filter, err := fakeTCPReceiveFilter(local, remote)
	if err == nil {
		err = unix.SetsockoptSockFprog(fd, unix.SOL_SOCKET, unix.SO_ATTACH_FILTER, &unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]})
	}
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("easytier: FakeTCP packet filter: %w", err)
	}
	return &fakeTCPPacketSocket{file: file, control: control, ifindex: networkInterface.Index, protocol: protocol, halen: uint8(len(networkInterface.HardwareAddr))}, nil
}

func fakeTCPReceiveFilter(local, remote netip.AddrPort) ([]unix.SockFilter, error) {
	var instructions []bpf.Instruction
	check := func(load bpf.Instruction, value uint32) {
		instructions = append(instructions, load, bpf.JumpIf{Cond: bpf.JumpEqual, Val: value, SkipTrue: 1}, bpf.RetConstant{Val: 0})
	}
	addressOffset, destinationOffset, protocolOffset := uint32(12), uint32(16), uint32(9)
	if local.Addr().Is6() {
		addressOffset, destinationOffset, protocolOffset = 8, 24, 6
	}
	check(bpf.LoadAbsolute{Off: protocolOffset, Size: 1}, unix.IPPROTO_TCP)
	if local.Addr().Is4() {
		instructions = append(instructions, bpf.LoadAbsolute{Off: 6, Size: 2}, bpf.JumpIf{Cond: bpf.JumpBitsNotSet, Val: 0x1fff, SkipTrue: 1}, bpf.RetConstant{Val: 0})
	}
	for offset := 0; offset < len(local.Addr().AsSlice()); offset += 4 {
		check(bpf.LoadAbsolute{Off: addressOffset + uint32(offset), Size: 4}, binary.BigEndian.Uint32(remote.Addr().AsSlice()[offset:offset+4]))
		check(bpf.LoadAbsolute{Off: destinationOffset + uint32(offset), Size: 4}, binary.BigEndian.Uint32(local.Addr().AsSlice()[offset:offset+4]))
	}
	if local.Addr().Is4() {
		instructions = append(instructions, bpf.LoadMemShift{Off: 0})
		check(bpf.LoadIndirect{Off: 0, Size: 2}, uint32(remote.Port()))
		if local.Port() != 0 {
			check(bpf.LoadIndirect{Off: 2, Size: 2}, uint32(local.Port()))
		}
	} else {
		check(bpf.LoadAbsolute{Off: 40, Size: 2}, uint32(remote.Port()))
		if local.Port() != 0 {
			check(bpf.LoadAbsolute{Off: 42, Size: 2}, uint32(local.Port()))
		}
	}
	instructions = append(instructions, bpf.RetConstant{Val: 65535})
	raw, err := bpf.Assemble(instructions)
	if err != nil {
		return nil, err
	}
	filter := make([]unix.SockFilter, len(raw))
	for index, instruction := range raw {
		filter[index] = unix.SockFilter{Code: instruction.Op, Jt: instruction.Jt, Jf: instruction.Jf, K: instruction.K}
	}
	return filter, nil
}

func (s *fakeTCPPacketSocket) ReadPacket() (fakeTCPFrame, error) {
	buffer := make([]byte, 65535)
	for {
		var size int
		var address unix.Sockaddr
		var receiveError error
		err := s.control.Read(func(fd uintptr) bool {
			size, address, receiveError = unix.Recvfrom(int(fd), buffer, 0)
			return receiveError != unix.EAGAIN && receiveError != unix.EWOULDBLOCK
		})
		if err != nil {
			return fakeTCPFrame{}, err
		}
		if receiveError != nil {
			return fakeTCPFrame{}, receiveError
		}
		link := address.(*unix.SockaddrLinklayer)
		if link.Pkttype == unix.PACKET_OUTGOING {
			continue
		}
		return fakeTCPFrame{packet: buffer[:size], mac: link.Addr}, nil
	}
}

func (s *fakeTCPPacketSocket) WritePacket(packet []byte, mac [8]byte) error {
	var sendError error
	err := s.control.Write(func(fd uintptr) bool {
		sendError = unix.Sendto(int(fd), packet, 0, &unix.SockaddrLinklayer{Protocol: fakeTCPNetworkOrder(s.protocol), Ifindex: s.ifindex, Halen: s.halen, Addr: mac})
		return sendError != unix.EAGAIN && sendError != unix.EWOULDBLOCK
	})
	if err != nil {
		return err
	}
	return sendError
}

func (s *fakeTCPPacketSocket) Close() error { return s.file.Close() }
func (s *fakeTCPPacketSocket) SetWriteDeadline(value time.Time) error {
	return s.file.SetWriteDeadline(value)
}

func fakeTCPNetworkOrder(value uint16) uint16 {
	var bytes [2]byte
	binary.BigEndian.PutUint16(bytes[:], value)
	return binary.NativeEndian.Uint16(bytes[:])
}
