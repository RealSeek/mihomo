package sing_tun

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"syscall"
	"unsafe"

	et "github.com/metacubex/mihomo/component/easytier"
	tun "github.com/metacubex/sing-tun"
	"golang.org/x/net/route"
	"golang.org/x/sys/unix"
)

type easyTierDarwinAddress struct {
	Name        [unix.IFNAMSIZ]byte
	Address     unix.RawSockaddrInet4
	Destination unix.RawSockaddrInet4
	Mask        unix.RawSockaddrInet4
}

const (
	easyTierSIOCAIFADDRIn6 = 2155899162
	easyTierSIOCDIFADDRIn6 = 0x81206919
	easyTierIN6IFFNODAD    = 0x0020
	easyTierIN6IFFSECURED  = 0x0400
	easyTierND6Infinite    = 0xffffffff
)

type easyTierDarwinAddress6 struct {
	Name     [unix.IFNAMSIZ]byte
	Address  unix.RawSockaddrInet6
	Dstaddr  unix.RawSockaddrInet6
	Mask     unix.RawSockaddrInet6
	Flags    uint32
	Lifetime easyTierDarwinLifetime6
}

type easyTierDarwinLifetime6 struct {
	Expire    float64
	Preferred float64
	Vltime    uint32
	Pltime    uint32
}

// in6_ifreq's union is 272 bytes (the ICMPv6 interface counters).
type easyTierDarwinDeleteAddress6 struct {
	Name    [unix.IFNAMSIZ]byte
	Address unix.RawSockaddrInet6
	_       [244]byte
}

func newEasyTierRoutes(options tun.Options, initial []et.PacketSession) (*easyTierRoutes, error) {
	device, err := net.InterfaceByName(options.Name)
	if err != nil {
		return nil, fmt.Errorf("easytier: find TUN %s: %w", options.Name, err)
	}
	fd, err := unix.Socket(unix.AF_ROUTE, unix.SOCK_RAW, unix.AF_UNSPEC)
	if err != nil {
		return nil, err
	}
	if err := unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &unix.Timeval{Sec: 2}); err != nil {
		unix.Close(fd)
		return nil, err
	}
	manager := makeEasyTierRoutes(initial)
	manager.finish = func() error { return unix.Close(fd) }
	manager.addAddr = func(prefix netip.Prefix) (bool, error) {
		err := easyTierDarwinAddressChange(options.Name, prefix, unix.SIOCAIFADDR)
		if errors.Is(err, unix.EEXIST) {
			return false, nil
		}
		return err == nil, err
	}
	manager.delAddr = func(prefix netip.Prefix) error {
		err := easyTierDarwinAddressChange(options.Name, prefix, unix.SIOCDIFADDR)
		if errors.Is(err, unix.EADDRNOTAVAIL) || errors.Is(err, unix.ENXIO) {
			return nil
		}
		return err
	}
	sequence := 0
	changeRoute := func(record easyTierRoute, kind int) (*route.RouteMessage, error) {
		sequence++
		var destination, mask, source route.Addr
		if record.Destination.Addr().Is6() {
			destination = &route.Inet6Addr{IP: record.Destination.Addr().As16()}
			mask = &route.Inet6Addr{IP: [16]byte(net.CIDRMask(record.Destination.Bits(), 128))}
			source = &route.Inet6Addr{IP: record.Source.As16()}
		} else {
			destination = &route.Inet4Addr{IP: record.Destination.Addr().As4()}
			mask = &route.Inet4Addr{IP: [4]byte(net.CIDRMask(record.Destination.Bits(), 32))}
			source = &route.Inet4Addr{IP: record.Source.As4()}
		}
		message := &route.RouteMessage{
			Version: unix.RTM_VERSION, Type: kind, Index: device.Index,
			ID: uintptr(os.Getpid()), Seq: sequence,
			Flags: unix.RTF_UP | unix.RTF_STATIC,
			Addrs: []route.Addr{
				unix.RTAX_DST:     destination,
				unix.RTAX_GATEWAY: &route.LinkAddr{Index: device.Index, Name: options.Name},
				unix.RTAX_NETMASK: mask,
				unix.RTAX_IFA:     source,
			},
		}
		if record.Destination.Bits() == record.Destination.Addr().BitLen() {
			message.Flags |= unix.RTF_HOST
		}
		packet, err := message.Marshal()
		if err != nil {
			return nil, err
		}
		if _, err := unix.Write(fd, packet); err != nil {
			return nil, err
		}
		buffer := make([]byte, 8192)
		for {
			n, err := unix.Read(fd, buffer)
			if err != nil {
				return nil, err
			}
			messages, err := route.ParseRIB(route.RIBTypeRoute, buffer[:n])
			if err != nil {
				return nil, err
			}
			for _, response := range messages {
				if response, ok := response.(*route.RouteMessage); ok && response.ID == message.ID && response.Seq == message.Seq {
					return response, response.Err
				}
			}
		}
	}
	manager.addRoute = func(record easyTierRoute) (bool, error) {
		_, err := changeRoute(record, unix.RTM_ADD)
		if errors.Is(err, unix.EEXIST) {
			current, getErr := changeRoute(record, unix.RTM_GET)
			if getErr != nil {
				return false, getErr
			}
			if current.Index != device.Index {
				return false, fmt.Errorf("existing route uses interface %d instead of %s", current.Index, options.Name)
			}
			if len(current.Addrs) <= unix.RTAX_IFA {
				return false, fmt.Errorf("existing route has no source address")
			}
			var currentSource netip.Addr
			switch ifa := current.Addrs[unix.RTAX_IFA].(type) {
			case *route.Inet4Addr:
				currentSource = netip.AddrFrom4(ifa.IP)
			case *route.Inet6Addr:
				currentSource = netip.AddrFrom16(ifa.IP)
			}
			if currentSource != record.Source {
				return false, fmt.Errorf("existing route does not select source %s", record.Source)
			}
			return false, nil
		}
		return err == nil, err
	}
	manager.delRoute = func(record easyTierRoute) error {
		_, err := changeRoute(record, unix.RTM_DELETE)
		if errors.Is(err, unix.ESRCH) || errors.Is(err, unix.ENXIO) {
			return nil
		}
		return err
	}
	if err := manager.Update(initial); err != nil {
		return nil, errors.Join(err, manager.Close())
	}
	return manager, nil
}

func easyTierDarwinAddressChange(name string, prefix netip.Prefix, operation uintptr) error {
	if prefix.Addr().Is6() {
		return easyTierDarwinAddress6Change(name, prefix, operation)
	}
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	request := easyTierDarwinAddress{
		Address:     unix.RawSockaddrInet4{Len: unix.SizeofSockaddrInet4, Family: unix.AF_INET, Addr: prefix.Addr().As4()},
		Destination: unix.RawSockaddrInet4{Len: unix.SizeofSockaddrInet4, Family: unix.AF_INET, Addr: prefix.Addr().As4()},
		Mask:        unix.RawSockaddrInet4{Len: unix.SizeofSockaddrInet4, Family: unix.AF_INET, Addr: [4]byte(net.CIDRMask(prefix.Bits(), 32))},
	}
	copy(request.Name[:], name)
	_, _, errno := syscall.Syscall(unix.SYS_IOCTL, uintptr(fd), operation, uintptr(unsafe.Pointer(&request)))
	if errno != 0 {
		return errno
	}
	return nil
}

func easyTierDarwinAddress6Change(name string, prefix netip.Prefix, operation uintptr) error {
	request := easyTierDarwinAddress6{
		Address:  unix.RawSockaddrInet6{Len: unix.SizeofSockaddrInet6, Family: unix.AF_INET6, Addr: prefix.Addr().As16()},
		Mask:     unix.RawSockaddrInet6{Len: unix.SizeofSockaddrInet6, Family: unix.AF_INET6, Addr: [16]byte(net.CIDRMask(prefix.Bits(), 128))},
		Flags:    easyTierIN6IFFNODAD | easyTierIN6IFFSECURED,
		Lifetime: easyTierDarwinLifetime6{Vltime: easyTierND6Infinite, Pltime: easyTierND6Infinite},
	}
	if prefix.Bits() == 128 {
		request.Dstaddr = unix.RawSockaddrInet6{Len: unix.SizeofSockaddrInet6, Family: unix.AF_INET6, Addr: prefix.Addr().Next().As16()}
	}
	copy(request.Name[:], name)
	pointer := unsafe.Pointer(&request)
	var deleteRequest easyTierDarwinDeleteAddress6
	if operation == unix.SIOCAIFADDR {
		operation = easyTierSIOCAIFADDRIn6
	} else {
		operation = easyTierSIOCDIFADDRIn6
		copy(deleteRequest.Name[:], name)
		deleteRequest.Address = request.Address
		pointer = unsafe.Pointer(&deleteRequest)
	}
	fd, err := unix.Socket(unix.AF_INET6, unix.SOCK_DGRAM, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	_, _, errno := syscall.Syscall(unix.SYS_IOCTL, uintptr(fd), operation, uintptr(pointer))
	if errno != 0 {
		return errno
	}
	return nil
}
