//go:build android && !no_fake_tcp

package easytier

import (
	"net"
	"net/netip"
	"os"
	"testing"

	"github.com/metacubex/mihomo/component/iface"
)

// This test is packaged into output/easytier/faketcp-android.test. It is
// opt-in because opening AF_PACKET requires a rooted Android shell with
// CAP_NET_RAW. The test only opens and closes the production backend; it does
// not install a driver, alter routes, or create a TUN.
func TestFakeTCPAndroidRawSocketFixture(t *testing.T) {
	if os.Getenv("EASYTIER_FAKETCP_RAW_TEST") != "1" {
		t.Skip("set EASYTIER_FAKETCP_RAW_TEST=1 on a rooted Android shell")
	}
	interfaces, err := iface.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	var addresses []netip.Addr
	for _, networkInterface := range interfaces {
		if networkInterface.Flags&net.FlagUp == 0 || networkInterface.Flags&net.FlagLoopback != 0 {
			continue
		}
		for _, prefix := range networkInterface.Addresses {
			if prefix.Addr().IsGlobalUnicast() && !prefix.Addr().IsLoopback() {
				addresses = append(addresses, prefix.Addr())
			}
		}
	}
	if len(addresses) == 0 {
		t.Fatal("no non-loopback address available for AF_PACKET fixture")
	}
	for _, address := range addresses {
		local := netip.AddrPortFrom(address, 0)
		remote := netip.AddrPortFrom(address, 9)
		packets, err := newFakeTCPPacketSocket(local, remote)
		if err != nil {
			t.Fatalf("%s raw socket: %v", address, err)
		}
		if err := packets.Close(); err != nil {
			t.Fatalf("%s raw socket close: %v", address, err)
		}
	}
}
