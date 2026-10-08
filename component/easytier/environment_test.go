package easytier

import (
	"context"
	"net"
	"net/netip"
	"reflect"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/metacubex/mihomo/component/dialer"
	"github.com/metacubex/mihomo/component/iface"

	corehost "github.com/easytier/easytier/easytier-go"
	"github.com/easytier/easytier/easytier-go/platform"
)

func TestInterfaceSnapshot(t *testing.T) {
	interfaces := map[string]*iface.Interface{
		"lo": {Index: 1, Flags: net.FlagUp | net.FlagLoopback, Addresses: []netip.Prefix{
			netip.MustParsePrefix("127.0.0.1/8"), netip.MustParsePrefix("::1/128"),
		}},
		"down": {Index: 2, Addresses: []netip.Prefix{netip.MustParsePrefix("192.0.2.1/24")}},
		"ethernet": {Index: 3, Flags: net.FlagUp, HardwareAddr: net.HardwareAddr{2, 0, 0, 0, 0, 1}, Addresses: []netip.Prefix{
			netip.MustParsePrefix("192.168.1.20/24"), netip.MustParsePrefix("2001:db8::20/64"),
			netip.MustParsePrefix("fd00::20/64"), netip.MustParsePrefix("fe80::20/64"),
			netip.MustParsePrefix("169.254.1.20/16"), netip.MustParsePrefix("224.0.0.1/32"),
			netip.MustParsePrefix("0.0.0.0/32"),
		}},
		"mobile": {Index: 4, Flags: net.FlagUp | net.FlagPointToPoint, Addresses: []netip.Prefix{
			netip.MustParsePrefix("100.64.1.20/32"),
		}},
		"tun": {Index: 5, Flags: net.FlagUp | net.FlagPointToPoint, Addresses: []netip.Prefix{
			netip.MustParsePrefix("198.18.0.1/30"),
		}},
	}
	want := platform.EnvironmentSnapshot{
		InterfaceIPv4s: []netip.Addr{netip.MustParseAddr("192.168.1.20")},
		InterfaceIPv6s: []netip.Addr{netip.MustParseAddr("2001:db8::20"), netip.MustParseAddr("fd00::20")},
		LocalIPs: []netip.Addr{
			netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("::1"),
			netip.MustParseAddr("192.168.1.20"), netip.MustParseAddr("2001:db8::20"),
			netip.MustParseAddr("fd00::20"), netip.MustParseAddr("fe80::20"),
			netip.MustParseAddr("169.254.1.20"), netip.MustParseAddr("100.64.1.20"), netip.MustParseAddr("198.18.0.1"),
		},
		PreferredIPv6Sources: []platform.PreferredIPv6Source{{IP: netip.MustParseAddr("2001:db8::20"), IfIndex: 3}},
	}
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		want.InterfaceIPv4s = append(want.InterfaceIPv4s, netip.MustParseAddr("100.64.1.20"), netip.MustParseAddr("198.18.0.1"))
	}
	if got := interfaceSnapshot(interfaces); !reflect.DeepEqual(got, want) {
		t.Fatalf("interface snapshot = %+v, want %+v", got, want)
	}
}

func TestInterfaceSnapshotExcludesRegisteredOverlayAddresses(t *testing.T) {
	lease := NewOverlayAddressLease()
	lease.Update([]netip.Prefix{
		netip.MustParsePrefix("10.144.0.2/24"),
		netip.MustParsePrefix("fd00:144::2/64"),
	})
	defer lease.Close()

	got := interfaceSnapshot(map[string]*iface.Interface{
		"underlay": {Index: 1, Flags: net.FlagUp, HardwareAddr: net.HardwareAddr{2, 0, 0, 0, 0, 1}, Addresses: []netip.Prefix{
			netip.MustParsePrefix("192.168.1.20/24"),
			netip.MustParsePrefix("2001:db8::20/64"),
		}},
		"easytier": {Index: 2, Flags: net.FlagUp | net.FlagPointToPoint, Addresses: []netip.Prefix{
			netip.MustParsePrefix("10.144.0.2/24"),
			netip.MustParsePrefix("fd00:144::2/64"),
		}},
	})
	if slices.Contains(got.InterfaceIPv4s, netip.MustParseAddr("10.144.0.2")) ||
		slices.Contains(got.InterfaceIPv6s, netip.MustParseAddr("fd00:144::2")) ||
		slices.Contains(got.LocalIPs, netip.MustParseAddr("10.144.0.2")) ||
		slices.Contains(got.LocalIPs, netip.MustParseAddr("fd00:144::2")) {
		t.Fatalf("registered overlay addresses leaked into environment snapshot: %+v", got)
	}
	if !slices.Contains(got.InterfaceIPv4s, netip.MustParseAddr("192.168.1.20")) ||
		!slices.Contains(got.InterfaceIPv6s, netip.MustParseAddr("2001:db8::20")) {
		t.Fatalf("underlay addresses were removed with overlay addresses: %+v", got)
	}
}

func TestAndroidInterfaceSnapshotFiltersTUNButKeepsMobile(t *testing.T) {
	got := interfaceSnapshotForOS(map[string]*iface.Interface{
		"rmnet0": {Name: "rmnet0", Index: 1, Flags: net.FlagUp | net.FlagPointToPoint, Addresses: []netip.Prefix{
			netip.MustParsePrefix("100.64.1.20/32"),
		}},
		"tun0": {Name: "tun0", Index: 2, Flags: net.FlagUp | net.FlagPointToPoint, Addresses: []netip.Prefix{
			netip.MustParsePrefix("198.18.0.1/30"),
		}},
	}, "android", func(name string) bool { return name == "tun0" })
	if !slices.Contains(got.InterfaceIPv4s, netip.MustParseAddr("100.64.1.20")) {
		t.Fatalf("Android mobile address was not retained: %+v", got)
	}
	if slices.Contains(got.InterfaceIPv4s, netip.MustParseAddr("198.18.0.1")) {
		t.Fatalf("Android TUN address was advertised as underlay: %+v", got)
	}
	if !slices.Contains(got.LocalIPs, netip.MustParseAddr("198.18.0.1")) {
		t.Fatalf("TUN local address should remain visible for local bookkeeping: %+v", got)
	}
}

func TestHostAdvertisesMihomoInterfaces(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	services, err := Services(dialer.NewDialer())
	if err != nil {
		t.Fatal(err)
	}
	host, err := corehost.New(ctx, corehost.Options{Platform: services})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close(context.Background())
	instance, err := host.CreateInstanceTOML(ctx, "interface-discovery", "", `
hostname = "interface-discovery"
ipv4 = "10.144.0.1/24"
listeners = []
stun_servers = []
stun_servers_v6 = []
[network_identity]
network_name = "interface-discovery"
network_secret = "test"
[flags]
no_tun = true
disable_p2p = true
`)
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.Start(ctx); err != nil {
		t.Fatal(err)
	}
	for {
		info, err := instance.ShowNodeInfo(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var ipv4s, ipv6s []netip.Addr
		for _, address := range info.GetIpList().GetInterfaceIpv4S() {
			ipv4s = append(ipv4s, IPv4FromUint32(address.GetAddr()))
		}
		for _, address := range info.GetIpList().GetInterfaceIpv6S() {
			ipv6s = append(ipv6s, IPv6FromParts(address.GetPart1(), address.GetPart2(), address.GetPart3(), address.GetPart4()))
		}
		if slices.Equal(ipv4s, services.Snapshot.InterfaceIPv4s) && slices.Equal(ipv6s, services.Snapshot.InterfaceIPv6s) {
			t.Logf("core advertises %d IPv4 and %d IPv6 interface addresses", len(ipv4s), len(ipv6s))
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("core advertised IPv4=%v IPv6=%v, want IPv4=%v IPv6=%v: %v", ipv4s, ipv6s,
				services.Snapshot.InterfaceIPv4s, services.Snapshot.InterfaceIPv6s, ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
}
