//go:build (linux || android) && !no_fake_tcp

package easytier

import (
	"net/netip"
	"testing"

	"golang.org/x/net/bpf"
)

func TestFakeTCPReceiveFilter(t *testing.T) {
	for _, addresses := range [][2]string{
		{"192.0.2.1:12345", "198.51.100.2:23456"},
		{"[2001:db8::1]:12345", "[2001:db8::2]:23456"},
	} {
		local, remote := netip.MustParseAddrPort(addresses[0]), netip.MustParseAddrPort(addresses[1])
		filter, err := fakeTCPReceiveFilter(local, remote)
		if err != nil {
			t.Fatal(err)
		}
		raw := make([]bpf.RawInstruction, len(filter))
		for index, instruction := range filter {
			raw[index] = bpf.RawInstruction{Op: instruction.Code, Jt: instruction.Jt, Jf: instruction.Jf, K: instruction.K}
		}
		instructions, ok := bpf.Disassemble(raw)
		if !ok {
			t.Fatal("invalid FakeTCP socket filter")
		}
		vm, err := bpf.NewVM(instructions)
		if err != nil {
			t.Fatal(err)
		}
		for _, ports := range []struct {
			remotePort, localPort uint16
			accepted              bool
		}{{remote.Port(), local.Port(), true}, {remote.Port() + 1, local.Port(), false}, {remote.Port(), local.Port() + 1, false}} {
			packet, err := buildFakeTCPPacket(netip.AddrPortFrom(remote.Addr(), ports.remotePort), netip.AddrPortFrom(local.Addr(), ports.localPort), 1, 2, false, []byte("frame"))
			if err != nil {
				t.Fatal(err)
			}
			length, err := vm.Run(packet)
			if err != nil || (length != 0) != ports.accepted {
				t.Fatalf("socket filter ports %+v: %d %v", ports, length, err)
			}
		}
	}
}
