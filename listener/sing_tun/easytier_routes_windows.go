package sing_tun

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"unsafe"

	et "github.com/metacubex/mihomo/component/easytier"
	tun "github.com/metacubex/sing-tun"
	"golang.org/x/sys/windows"
)

var easyTierIPHelper = windows.NewLazySystemDLL("iphlpapi.dll")

var (
	easyTierInitializeRoute   = easyTierIPHelper.NewProc("InitializeIpForwardEntry")
	easyTierCreateRoute       = easyTierIPHelper.NewProc("CreateIpForwardEntry2")
	easyTierDeleteRoute       = easyTierIPHelper.NewProc("DeleteIpForwardEntry2")
	easyTierGetRoute          = easyTierIPHelper.NewProc("GetIpForwardEntry2")
	easyTierInitializeAddress = easyTierIPHelper.NewProc("InitializeUnicastIpAddressEntry")
	easyTierCreateAddress     = easyTierIPHelper.NewProc("CreateUnicastIpAddressEntry")
	easyTierDeleteAddress     = easyTierIPHelper.NewProc("DeleteUnicastIpAddressEntry")
	easyTierGetAddress        = easyTierIPHelper.NewProc("GetUnicastIpAddressEntry")
)

// These layouts follow SOCKADDR_INET, IP_ADDRESS_PREFIX,
// MIB_IPFORWARD_ROW2 and MIB_UNICASTIPADDRESS_ROW in netioapi.h.
type easyTierWinSockaddr struct {
	Family uint16
	Data   [26]byte
}

type easyTierWinPrefix struct {
	Address easyTierWinSockaddr
	Bits    uint8
	_       [3]byte
}

type easyTierWinRoute struct {
	InterfaceLUID     uint64
	InterfaceIndex    uint32
	Destination       easyTierWinPrefix
	NextHop           easyTierWinSockaddr
	SitePrefixLength  uint8
	_                 [3]byte
	ValidLifetime     uint32
	PreferredLifetime uint32
	Metric            uint32
	Protocol          uint32
	Loopback          uint8
	Autoconfigure     uint8
	Publish           uint8
	Immortal          uint8
	Age               uint32
	Origin            uint32
}

type easyTierWinAddress struct {
	Address            easyTierWinSockaddr
	_                  [4]byte
	InterfaceLUID      uint64
	InterfaceIndex     uint32
	PrefixOrigin       uint32
	SuffixOrigin       uint32
	ValidLifetime      uint32
	PreferredLifetime  uint32
	OnLinkPrefixLength uint8
	SkipAsSource       uint8
	_                  [2]byte
	DadState           uint32
	ScopeID            uint32
	CreationTimeStamp  int64
}

func newEasyTierRoutes(options tun.Options, initial []et.PacketSession) (*easyTierRoutes, error) {
	device, err := net.InterfaceByName(options.Name)
	if err != nil {
		return nil, fmt.Errorf("easytier: find TUN %s: %w", options.Name, err)
	}
	manager := makeEasyTierRoutes(initial)
	addressRow := func(prefix netip.Prefix) easyTierWinAddress {
		var row easyTierWinAddress
		easyTierInitializeAddress.Call(uintptr(unsafe.Pointer(&row)))
		row.InterfaceIndex = uint32(device.Index)
		row.Address = easyTierWindowsSockaddr(prefix.Addr())
		row.OnLinkPrefixLength = uint8(prefix.Bits())
		row.ValidLifetime, row.PreferredLifetime = 0xffffffff, 0xffffffff
		row.DadState = 4 // IpDadStatePreferred
		return row
	}
	manager.addAddr = func(prefix netip.Prefix) (bool, error) {
		row := addressRow(prefix)
		err := easyTierWindowsCall(easyTierCreateAddress, unsafe.Pointer(&row))
		if errors.Is(err, windows.ERROR_OBJECT_ALREADY_EXISTS) {
			if err := easyTierWindowsCall(easyTierGetAddress, unsafe.Pointer(&row)); err != nil {
				return false, err
			}
			if row.OnLinkPrefixLength != uint8(prefix.Bits()) {
				return false, fmt.Errorf("existing TUN address %s has prefix length %d", prefix.Addr(), row.OnLinkPrefixLength)
			}
			return false, nil
		}
		return err == nil, err
	}
	manager.delAddr = func(prefix netip.Prefix) error {
		row := addressRow(prefix)
		return easyTierWindowsDeleteError(easyTierWindowsCall(easyTierDeleteAddress, unsafe.Pointer(&row)))
	}
	owned := make(map[easyTierRoute]easyTierWinRoute)
	manager.addRoute = func(record easyTierRoute) (bool, error) {
		var row easyTierWinRoute
		easyTierInitializeRoute.Call(uintptr(unsafe.Pointer(&row)))
		row.InterfaceIndex = uint32(device.Index)
		row.Destination = easyTierWinPrefix{Address: easyTierWindowsSockaddr(record.Destination.Addr()), Bits: uint8(record.Destination.Bits())}
		nextHop := netip.IPv4Unspecified()
		if record.Destination.Addr().Is6() {
			nextHop = netip.IPv6Unspecified()
		}
		row.NextHop = easyTierWindowsSockaddr(nextHop)
		row.Metric = 0
		err := easyTierWindowsCall(easyTierCreateRoute, unsafe.Pointer(&row))
		if errors.Is(err, windows.ERROR_OBJECT_ALREADY_EXISTS) {
			if err := easyTierWindowsCall(easyTierGetRoute, unsafe.Pointer(&row)); err != nil {
				return false, err
			}
			if row.InterfaceIndex != uint32(device.Index) || row.Destination.Bits != uint8(record.Destination.Bits()) || row.Destination.Address.Family != row.NextHop.Family {
				return false, fmt.Errorf("existing route %s does not use TUN %s", record.Destination, options.Name)
			}
			return false, nil
		}
		if err == nil {
			owned[record] = row
		}
		return err == nil, err
	}
	manager.delRoute = func(record easyTierRoute) error {
		row := owned[record]
		err := easyTierWindowsDeleteError(easyTierWindowsCall(easyTierDeleteRoute, unsafe.Pointer(&row)))
		if err == nil {
			delete(owned, record)
		}
		return err
	}
	// Windows exposes no per-route preferred source. It selects overlay subnet
	// sources from the assigned addresses; arbitrary proxy CIDRs need runtime
	// validation when multiple addresses share this interface.
	if err := manager.Update(initial); err != nil {
		return nil, errors.Join(err, manager.Close())
	}
	return manager, nil
}

func easyTierWindowsSockaddr(address netip.Addr) easyTierWinSockaddr {
	var result easyTierWinSockaddr
	if address.Is6() {
		raw := (*windows.RawSockaddrInet6)(unsafe.Pointer(&result))
		raw.Family = windows.AF_INET6
		raw.Addr = address.As16()
		return result
	}
	raw := (*windows.RawSockaddrInet4)(unsafe.Pointer(&result))
	raw.Family = windows.AF_INET
	raw.Addr = address.As4()
	return result
}

func easyTierWindowsCall(proc *windows.LazyProc, pointer unsafe.Pointer) error {
	result, _, _ := proc.Call(uintptr(pointer))
	if result != 0 {
		return fmt.Errorf("%s: %w", proc.Name, windows.Errno(result))
	}
	return nil
}

func easyTierWindowsDeleteError(err error) error {
	if errors.Is(err, windows.ERROR_NOT_FOUND) {
		return nil
	}
	return err
}
