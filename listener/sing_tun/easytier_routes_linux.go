//go:build linux || android

package sing_tun

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"runtime"

	et "github.com/metacubex/mihomo/component/easytier"
	tun "github.com/metacubex/sing-tun"
	"github.com/sagernet/netlink"
	"golang.org/x/sys/unix"
)

func newEasyTierRoutes(options tun.Options, initial []et.PacketSession) (*easyTierRoutes, error) {
	link, err := netlink.LinkByName(options.Name)
	if err != nil {
		return nil, fmt.Errorf("easytier: find TUN %s: %w", options.Name, err)
	}
	manager := makeEasyTierRoutes(initial)
	manager.addAddr = func(prefix netip.Prefix) (bool, error) {
		address := &netlink.Addr{IPNet: easyTierIPNet(prefix)}
		err := netlink.AddrAdd(link, address)
		if errors.Is(err, unix.EEXIST) {
			return false, nil
		}
		return err == nil, err
	}
	manager.delAddr = func(prefix netip.Prefix) error {
		return easyTierLinuxDeleteError(netlink.AddrDel(link, &netlink.Addr{IPNet: easyTierIPNet(prefix)}))
	}
	autoTable := options.IPRoute2TableIndex
	if autoTable == 0 {
		autoTable = tun.DefaultIPRoute2TableIndex
	}
	tables := []int{unix.RT_TABLE_MAIN}
	if options.AutoRoute && autoTable != unix.RT_TABLE_MAIN {
		tables = append(tables, autoTable)
	}
	autoRoutes := make(map[netip.Prefix]netlink.Route)
	if options.AutoRoute && options.FileDescriptor == 0 {
		prefixes, err := options.BuildAutoRouteRanges(false)
		if err != nil {
			return nil, fmt.Errorf("easytier: identify TUN auto routes: %w", err)
		}
		gateway4, gateway6 := options.Inet4GatewayAddr(), options.Inet6GatewayAddr()
		for _, prefix := range prefixes {
			var gateway net.IP
			if prefix.Addr().Is4() {
				if !gateway4.IsUnspecified() {
					gateway = gateway4.AsSlice()
				}
			} else if !gateway6.IsUnspecified() {
				gateway = gateway6.AsSlice()
			}
			family := unix.AF_INET
			if prefix.Addr().Is6() {
				family = unix.AF_INET6
			}
			autoRoutes[prefix.Masked()] = netlink.Route{
				LinkIndex: link.Attrs().Index, Family: family,
				Dst: easyTierIPNet(prefix.Masked()), Gw: gateway, Table: autoTable,
				Scope: netlink.SCOPE_UNIVERSE, Protocol: unix.RTPROT_BOOT, Type: unix.RTN_UNICAST,
			}
			if family == unix.AF_INET6 {
				expected := autoRoutes[prefix.Masked()]
				expected.Priority = 1024 // Linux IP6_RT_PRIO_USER for metric zero.
				autoRoutes[prefix.Masked()] = expected
			}
		}
	}
	type routeChange struct {
		route    netlink.Route
		original *netlink.Route
	}
	owned := make(map[easyTierRoute][]routeChange)
	ownedRules := make(map[easyTierRoute]*netlink.Rule)
	manager.delRoute = func(record easyTierRoute) error {
		var errs []error
		if rule := ownedRules[record]; rule != nil {
			if err := easyTierLinuxDeleteError(netlink.RuleDel(rule)); err != nil {
				errs = append(errs, fmt.Errorf("remove Android IPv6 reply rule %s: %w", rule, err))
			} else {
				delete(ownedRules, record)
			}
		}
		var remaining []routeChange
		for _, change := range owned[record] {
			var err error
			if change.original != nil {
				err = netlink.RouteReplace(change.original)
			} else {
				err = easyTierLinuxDeleteError(netlink.RouteDel(&change.route))
			}
			if err != nil {
				errs = append(errs, err)
				remaining = append(remaining, change)
			}
		}
		if len(remaining) == 0 {
			delete(owned, record)
		} else {
			owned[record] = remaining
		}
		return errors.Join(errs...)
	}
	manager.addRoute = func(record easyTierRoute) (isOwned bool, resultErr error) {
		defer func() {
			if resultErr != nil {
				resultErr = errors.Join(resultErr, manager.delRoute(record))
			}
		}()
		family := unix.AF_INET
		scope := netlink.SCOPE_LINK
		priority := 0
		if record.Destination.Addr().Is6() {
			family = unix.AF_INET6
			scope = netlink.SCOPE_UNIVERSE
			priority = 1024
		}
		for _, table := range tables {
			route := netlink.Route{
				LinkIndex: link.Attrs().Index, Family: family,
				Dst: easyTierIPNet(record.Destination), Src: record.Source.AsSlice(),
				Table: table, Scope: scope, Protocol: unix.RTPROT_STATIC, Priority: priority,
			}
			err := netlink.RouteAdd(&route)
			if errors.Is(err, unix.EEXIST) {
				existing, listErr := netlink.RouteListFiltered(route.Family, &route, netlink.RT_FILTER_TABLE|netlink.RT_FILTER_DST)
				if listErr != nil {
					return false, listErr
				}
				matches := false
				for _, current := range existing {
					if current.LinkIndex == route.LinkIndex && current.Src.Equal(route.Src) && len(current.Gw) == 0 {
						matches = true
						break
					}
					// AddrAdd creates an IPv6 connected route with no preferred
					// source. Borrow only that exact kernel route for the local
					// overlay network; arbitrary nil-source routes are rejected.
					localAddress := netip.PrefixFrom(record.Source, record.Destination.Bits())
					_, localConnected := manager.addresses[localAddress]
					localConnected = localConnected && localAddress.Masked() == record.Destination
					if localConnected && route.Family == unix.AF_INET6 && current.Table == unix.RT_TABLE_MAIN && current.LinkIndex == route.LinkIndex &&
						len(current.Src) == 0 && len(current.Gw) == 0 &&
						current.Scope == netlink.SCOPE_UNIVERSE && current.Protocol == unix.RTPROT_KERNEL && current.Type == unix.RTN_UNICAST {
						matches = true
						break
					}
					// Replace only the exact route sing-tun installed for this
					// listener, retaining it for lease changes and shutdown.
					if expected, ok := autoRoutes[record.Destination]; ok && current.Equal(expected) {
						if err := netlink.RouteReplace(&route); err != nil {
							return false, err
						}
						owned[record] = append(owned[record], routeChange{route: route, original: &current})
						matches = true
						break
					}
				}
				if !matches {
					return false, fmt.Errorf("existing route in table %d does not select %s on %s", table, record.Source, options.Name)
				}
				continue
			}
			if err != nil {
				return false, err
			}
			owned[record] = append(owned[record], routeChange{route: route})
		}
		if runtime.GOOS == "android" {
			if rule := easyTierAndroidReplyRule(options, record); rule != nil {
				if err := netlink.RuleAdd(rule); err != nil {
					return false, fmt.Errorf("add Android IPv6 reply rule %s: %w", rule, err)
				}
				ownedRules[record] = rule
			}
		}
		return len(owned[record]) > 0 || ownedRules[record] != nil, nil
	}
	if err := manager.Update(initial); err != nil {
		return nil, errors.Join(err, manager.Close())
	}
	return manager, nil
}

func easyTierAndroidReplyRule(options tun.Options, record easyTierRoute) *netlink.Rule {
	if !options.AutoRoute || !record.Source.Is6() {
		return nil
	}
	priority := options.IPRoute2RuleIndex
	if priority == 0 {
		priority = tun.DefaultIPRoute2RuleIndex
	}
	table := options.IPRoute2TableIndex
	if table == 0 {
		table = tun.DefaultIPRoute2TableIndex
	}
	// sing-tun skips locally sourced IPv6 before its overlay source rules.
	// Keep replies from this overlay address on its exact destination routes.
	rule := netlink.NewRule()
	rule.Priority = priority - 1
	rule.Family = unix.AF_INET6
	rule.Table = table
	rule.IifName = "lo"
	rule.Src = netip.PrefixFrom(record.Source, 128)
	rule.Dst = record.Destination
	return rule
}

func easyTierIPNet(prefix netip.Prefix) *net.IPNet {
	return &net.IPNet{IP: prefix.Addr().AsSlice(), Mask: net.CIDRMask(prefix.Bits(), prefix.Addr().BitLen())}
}

func easyTierLinuxDeleteError(err error) error {
	if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ESRCH) || errors.Is(err, unix.ENODEV) || errors.Is(err, unix.EADDRNOTAVAIL) {
		return nil
	}
	return err
}
