package easytier

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"

	"github.com/metacubex/mihomo/component/iface"

	"github.com/easytier/easytier/easytier-go/platform"
)

func EnvironmentSnapshot() (platform.EnvironmentSnapshot, error) {
	interfaces, err := iface.Interfaces()
	if err != nil {
		return platform.EnvironmentSnapshot{}, fmt.Errorf("easytier: collect network interfaces: %w", err)
	}
	return interfaceSnapshot(interfaces), nil
}

func interfaceSnapshot(interfaces map[string]*iface.Interface) platform.EnvironmentSnapshot {
	return interfaceSnapshotForOS(interfaces, runtime.GOOS, func(name string) bool {
		_, err := os.Stat(filepath.Join("/sys/class/net", name, "tun_flags"))
		return err == nil
	})
}

func interfaceSnapshotForOS(interfaces map[string]*iface.Interface, goos string, isTUN func(string) bool) platform.EnvironmentSnapshot {
	ordered := make([]*iface.Interface, 0, len(interfaces))
	for _, networkInterface := range interfaces {
		if networkInterface.Flags&net.FlagUp != 0 {
			ordered = append(ordered, networkInterface)
		}
	}
	slices.SortFunc(ordered, func(a, b *iface.Interface) int { return a.Index - b.Index })
	var snapshot platform.EnvironmentSnapshot
	for _, networkInterface := range ordered {
		advertiseIPv4 := true
		switch goos {
		case "linux":
			advertiseIPv4 = networkInterface.Flags&net.FlagPointToPoint == 0 && !isTUN(networkInterface.Name)
		case "android":
			// Android's mobile data interfaces are commonly point-to-point;
			// retain them as underlay candidates while excluding actual TUNs.
			advertiseIPv4 = !isTUN(networkInterface.Name)
		case "darwin":
			advertiseIPv4 = networkInterface.Flags&net.FlagPointToPoint == 0
		case "windows":
			advertiseIPv4 = networkInterface.Flags&net.FlagPointToPoint == 0 && slices.ContainsFunc(networkInterface.HardwareAddr, func(octet byte) bool { return octet != 0 })
		}
		for _, prefix := range networkInterface.Addresses {
			address := prefix.Addr().Unmap()
			if !address.IsValid() || address.IsUnspecified() || address.IsMulticast() {
				continue
			}
			if isOverlayAddress(address) {
				continue
			}
			snapshot.LocalIPs = append(snapshot.LocalIPs, address)
			if networkInterface.Flags&net.FlagLoopback != 0 || !address.IsGlobalUnicast() {
				continue
			}
			if address.Is4() {
				if advertiseIPv4 {
					snapshot.InterfaceIPv4s = append(snapshot.InterfaceIPv4s, address)
				}
				continue
			}
			snapshot.InterfaceIPv6s = append(snapshot.InterfaceIPv6s, address)
			if !address.IsPrivate() {
				snapshot.PreferredIPv6Sources = append(snapshot.PreferredIPv6Sources, platform.PreferredIPv6Source{
					IP: address, IfIndex: uint32(networkInterface.Index),
				})
			}
		}
	}
	return snapshot
}
