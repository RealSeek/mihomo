package sing_tun

import (
	"context"
	"encoding/binary"
	"net/netip"
	"syscall"

	et "github.com/metacubex/mihomo/component/easytier"
	"github.com/metacubex/mihomo/component/resolver"
	"github.com/metacubex/mipstack"
	tun "github.com/metacubex/sing-tun"
	"golang.org/x/exp/slices"
)

func newEasyTierDevice(device tun.Tun, sessions []et.PacketSession, mtu uint32, darwinHeader bool, bypass func([]byte) bool, onError func(error)) (tun.Tun, error) {
	// Preserve the native input checksum policy even though the mux exposes
	// plain IP packets and cannot expose native batch or family-header methods.
	native := false
	switch device.(type) {
	case tun.WinTun, tun.LinuxTUN, tun.DarwinTUN:
		native = true
	}
	mux, err := et.NewPacketMux(device, checksumEasyTierSessions(sessions, native), darwinHeader, bypass, onError)
	if err != nil {
		return nil, err
	}
	if !native {
		return mux, nil
	}
	return &easyTierDevice{PacketMux: mux, buffer: make([]byte, mtu), darwinHeader: darwinHeader}, nil
}

func checksumEasyTierSessions(sessions []et.PacketSession, native bool) []et.PacketSession {
	if !native {
		return sessions
	}
	sessions = slices.Clone(sessions)
	for i := range sessions {
		sessions[i].Endpoint = easyTierChecksumEndpoint{sessions[i].Endpoint}
	}
	return sessions
}

func (h *ListenerHandler) keepEasyTierPacketLocal(packet []byte) bool {
	parsed, err := parseEasyTierPacket(packet)
	if err != nil {
		return false
	}
	if resolver.IsFakeIP(parsed.Destination) {
		return true
	}
	for _, prefix := range h.Inet4Address {
		if prefix.Contains(parsed.Destination) {
			return true
		}
	}
	for _, prefix := range h.Inet6Address {
		if prefix.Contains(parsed.Destination) {
			return true
		}
	}
	protocol, payload, err := parsed.UpperLayer()
	if err != nil || len(payload) < 4 || protocol != mipstack.ProtocolTCP && protocol != mipstack.ProtocolUDP {
		return false
	}
	return h.ShouldHijackDns(netip.AddrPortFrom(parsed.Destination, binary.BigEndian.Uint16(payload[2:4])))
}

type easyTierDevice struct {
	*et.PacketMux
	buffer       []byte
	darwinHeader bool
}

var _ tun.WinTun = (*easyTierDevice)(nil)

func (d *easyTierDevice) UpdateSessions(sessions []et.PacketSession) error {
	return d.PacketMux.UpdateSessions(checksumEasyTierSessions(sessions, true))
}

// Mipstack recognizes ReadPacket devices as native checksum-offloaded input.
// The buffer remains owned by this device until its next read.
func (d *easyTierDevice) ReadPacket() ([]byte, func(), error) {
	n, err := d.Read(d.buffer)
	return d.buffer[:n], func() {}, err
}

func (d *easyTierDevice) Write(packet []byte) (int, error) {
	// System packet writers add the platform header independently of ReadPacket.
	// The mux owns native framing, including writes from the plain-IP stacks.
	if d.darwinHeader && len(packet) >= 4 {
		family := binary.BigEndian.Uint32(packet[:4])
		if family == 2 || family == 30 {
			n, err := d.PacketMux.Write(packet[4:])
			return n + 4, err
		}
	}
	return d.PacketMux.Write(packet)
}

type easyTierChecksumEndpoint struct {
	et.PacketEndpoint
}

func (e easyTierChecksumEndpoint) SendPacket(ctx context.Context, packet []byte) error {
	if err := completeEasyTierChecksum(packet); err != nil {
		return err
	}
	return e.PacketEndpoint.SendPacket(ctx, packet)
}

func parseEasyTierPacket(packet []byte) (mipstack.IPPacket, error) {
	if len(packet) == 0 {
		return mipstack.IPPacket{}, syscall.EINVAL
	}
	if packet[0]>>4 == 4 {
		headerSize := int(packet[0]&15) * 4
		if headerSize < 20 || headerSize > len(packet) {
			return mipstack.IPPacket{}, syscall.EINVAL
		}
		packet[10], packet[11] = 0, 0
		binary.BigEndian.PutUint16(packet[10:12], mipstack.InternetChecksum(packet[:headerSize]))
	}
	return mipstack.ParseIPPacket(packet)
}

// Native TUN input can carry deferred checksums. Overlay packets must have
// complete checksums because the remote device receives ordinary raw IP.
func completeEasyTierChecksum(packet []byte) error {
	parsed, err := parseEasyTierPacket(packet)
	if err != nil {
		return err
	}
	if fragment, ok := parsed.Fragment(); ok && !fragment.IsAtomic() {
		// With GSO disabled the host computes the transport checksum before
		// fragmentation. A single fragment cannot recompute that checksum.
		return nil
	}
	protocol, payload, err := parsed.UpperLayer()
	if err != nil {
		return err
	}
	var offset int
	switch protocol {
	case mipstack.ProtocolTCP:
		if len(payload) < 20 {
			return syscall.EINVAL
		}
		offset = 16
	case mipstack.ProtocolUDP:
		if len(payload) < 8 {
			return syscall.EINVAL
		}
		length := int(binary.BigEndian.Uint16(payload[4:6]))
		if length < 8 || length > len(payload) {
			return syscall.EINVAL
		}
		payload = payload[:length]
		if parsed.Source.Is4() && binary.BigEndian.Uint16(payload[6:8]) == 0 {
			return nil // Preserve IPv4 UDP's explicitly omitted checksum.
		}
		offset = 6
	case mipstack.ProtocolICMPv6:
		if len(payload) < 4 {
			return syscall.EINVAL
		}
		offset = 2
	default:
		return nil
	}
	payload[offset], payload[offset+1] = 0, 0
	checksum, err := mipstack.IPTransportChecksum(parsed.Source, parsed.Destination, protocol, payload)
	if err != nil {
		return err
	}
	if protocol == mipstack.ProtocolUDP && checksum == 0 {
		checksum = 0xffff
	}
	binary.BigEndian.PutUint16(payload[offset:offset+2], checksum)
	return nil
}
