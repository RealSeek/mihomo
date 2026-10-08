package sing_tun

import (
	"errors"
	"fmt"
	"net/netip"
	"sync"

	et "github.com/metacubex/mihomo/component/easytier"
)

type easyTierRoute struct {
	Destination netip.Prefix
	Source      netip.Addr
}

type easyTierRoutes struct {
	mu        sync.Mutex
	addresses map[netip.Prefix]bool
	routes    map[easyTierRoute]bool
	addAddr   func(netip.Prefix) (bool, error)
	delAddr   func(netip.Prefix) error
	addRoute  func(easyTierRoute) (bool, error)
	delRoute  func(easyTierRoute) error
	finish    func() error
}

func makeEasyTierRoutes(initial []et.PacketSession) *easyTierRoutes {
	m := &easyTierRoutes{addresses: make(map[netip.Prefix]bool), routes: make(map[easyTierRoute]bool)}
	// The listener added these addresses when creating its own TUN. Take
	// ownership so a changed DHCP lease removes its previous address.
	for _, session := range initial {
		for _, address := range session.Addresses {
			if address.IsValid() {
				m.addresses[address] = true
			}
		}
	}
	return m
}

func easyTierRoutePlan(sessions []et.PacketSession) (map[netip.Prefix]struct{}, map[easyTierRoute]struct{}, error) {
	addresses := make(map[netip.Prefix]struct{}, len(sessions))
	routes := make(map[easyTierRoute]struct{})
	sources := make(map[netip.Prefix]netip.Addr)
	for _, session := range sessions {
		if len(session.Addresses) == 0 {
			return nil, nil, fmt.Errorf("easytier: route session requires an address")
		}
		for _, address := range session.Addresses {
			if !address.IsValid() {
				return nil, nil, fmt.Errorf("easytier: route session requires valid addresses")
			}
			addresses[address] = struct{}{}
		}
		for _, prefix := range session.Routes {
			if !prefix.IsValid() {
				return nil, nil, fmt.Errorf("easytier: invalid route %s", prefix)
			}
			source, ok := easyTierRouteSource(session.Addresses, prefix)
			if !ok {
				return nil, nil, fmt.Errorf("easytier: no address matching route family %s", prefix)
			}
			prefix = prefix.Masked()
			prefixes := []netip.Prefix{prefix}
			// More specific routes coexist with mihomo's default route without
			// replacing or taking ownership of that existing entry.
			if prefix.Bits() == 0 {
				if prefix.Addr().Is6() {
					prefixes = []netip.Prefix{netip.MustParsePrefix("::/1"), netip.MustParsePrefix("8000::/1")}
				} else {
					prefixes = []netip.Prefix{netip.MustParsePrefix("0.0.0.0/1"), netip.MustParsePrefix("128.0.0.0/1")}
				}
			}
			for _, destination := range prefixes {
				if previous, ok := sources[destination]; ok && previous != source {
					return nil, nil, fmt.Errorf("easytier: route %s belongs to both %s and %s", destination, previous, source)
				}
				sources[destination] = source
				routes[easyTierRoute{Destination: destination, Source: source}] = struct{}{}
			}
		}
	}
	return addresses, routes, nil
}

func easyTierRouteSource(addresses []netip.Prefix, destination netip.Prefix) (netip.Addr, bool) {
	var first netip.Addr
	longest := -1
	for _, address := range addresses {
		if !address.IsValid() || address.Addr().BitLen() != destination.Addr().BitLen() {
			continue
		}
		network := address.Masked()
		if !first.IsValid() {
			first = address.Addr()
		}
		if address.Bits() <= destination.Bits() && network.Contains(destination.Addr()) && address.Bits() > longest {
			longest = address.Bits()
			first = address.Addr()
		}
	}
	return first, first.IsValid()
}

func (m *easyTierRoutes) Update(sessions []et.PacketSession) error {
	addresses, routes, err := easyTierRoutePlan(sessions)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	// Retire old routes before adding replacements for a changed lease: the
	// kernel indexes a route by destination, independently of preferred source.
	for route, owned := range m.routes {
		if _, keep := routes[route]; keep {
			continue
		}
		if owned {
			if err := m.delRoute(route); err != nil {
				return fmt.Errorf("easytier: remove TUN route %s: %w", route.Destination, err)
			}
		}
		delete(m.routes, route)
	}
	// Remove an old lease before adding its replacement. Linux can otherwise
	// remove secondary addresses together with the old primary subnet address.
	for address, owned := range m.addresses {
		if _, keep := addresses[address]; keep {
			continue
		}
		if owned {
			if err := m.delAddr(address); err != nil {
				return fmt.Errorf("easytier: remove TUN address %s: %w", address, err)
			}
		}
		delete(m.addresses, address)
	}
	for address := range addresses {
		if _, exists := m.addresses[address]; exists {
			continue
		}
		owned, err := m.addAddr(address)
		if err != nil {
			return fmt.Errorf("easytier: add TUN address %s: %w", address, err)
		}
		m.addresses[address] = owned
	}
	for route := range routes {
		if _, exists := m.routes[route]; exists {
			continue
		}
		owned, err := m.addRoute(route)
		if err != nil {
			return fmt.Errorf("easytier: add TUN route %s source %s: %w", route.Destination, route.Source, err)
		}
		m.routes[route] = owned
	}
	return nil
}

func (m *easyTierRoutes) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var errs []error
	for route, owned := range m.routes {
		if owned {
			if err := m.delRoute(route); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		delete(m.routes, route)
	}
	for address, owned := range m.addresses {
		if owned {
			if err := m.delAddr(address); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		delete(m.addresses, address)
	}
	if m.finish != nil {
		errs = append(errs, m.finish())
		m.finish = nil
	}
	return errors.Join(errs...)
}
