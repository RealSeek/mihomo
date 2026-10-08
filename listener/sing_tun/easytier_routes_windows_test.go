package sing_tun

import (
	"net/netip"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestEasyTierWindowsIPHelperLayouts(t *testing.T) {
	var route easyTierWinRoute
	var address easyTierWinAddress
	if unsafe.Sizeof(route) != 104 || unsafe.Offsetof(route.Destination) != 12 || unsafe.Offsetof(route.NextHop) != 44 || unsafe.Offsetof(route.Metric) != 84 {
		t.Fatalf("MIB_IPFORWARD_ROW2 layout: size=%d destination=%d nextHop=%d metric=%d", unsafe.Sizeof(route), unsafe.Offsetof(route.Destination), unsafe.Offsetof(route.NextHop), unsafe.Offsetof(route.Metric))
	}
	if unsafe.Sizeof(address) != 80 || unsafe.Offsetof(address.InterfaceLUID) != 32 || unsafe.Offsetof(address.OnLinkPrefixLength) != 60 || unsafe.Offsetof(address.CreationTimeStamp) != 72 {
		t.Fatalf("MIB_UNICASTIPADDRESS_ROW layout: size=%d luid=%d prefix=%d timestamp=%d", unsafe.Sizeof(address), unsafe.Offsetof(address.InterfaceLUID), unsafe.Offsetof(address.OnLinkPrefixLength), unsafe.Offsetof(address.CreationTimeStamp))
	}
	raw := easyTierWindowsSockaddr(netip.MustParseAddr("10.144.0.1"))
	inet := (*windows.RawSockaddrInet4)(unsafe.Pointer(&raw))
	if inet.Family != windows.AF_INET || inet.Addr != [4]byte{10, 144, 0, 1} {
		t.Fatalf("SOCKADDR_INET encoding: %+v", inet)
	}
	ipv6 := netip.MustParseAddr("fd00:144::1")
	raw = easyTierWindowsSockaddr(ipv6)
	inet6 := (*windows.RawSockaddrInet6)(unsafe.Pointer(&raw))
	if inet6.Family != windows.AF_INET6 || inet6.Addr != ipv6.As16() || inet6.Port != 0 || inet6.Flowinfo != 0 || inet6.Scope_id != 0 {
		t.Fatalf("IPv6 SOCKADDR_INET encoding: %+v", inet6)
	}
	raw = easyTierWindowsSockaddr(netip.IPv6Unspecified())
	inet6 = (*windows.RawSockaddrInet6)(unsafe.Pointer(&raw))
	if inet6.Family != windows.AF_INET6 || inet6.Addr != [16]byte{} {
		t.Fatalf("IPv6 on-link next hop: %+v", inet6)
	}
}
