package easytier

import (
	"net/netip"
	"sync"
)

// OverlayAddressLease marks addresses currently owned by an EasyTier shared
// TUN. Interface discovery must not advertise those addresses as underlay
// addresses after the TUN has been installed.
type OverlayAddressLease struct {
	mu        sync.Mutex
	addresses map[netip.Addr]struct{}
	closed    bool
}

var overlayAddressRegistry struct {
	sync.RWMutex
	addresses map[netip.Addr]int
}

// NewOverlayAddressLease creates an ownership handle for one shared TUN.
func NewOverlayAddressLease() *OverlayAddressLease {
	return &OverlayAddressLease{addresses: make(map[netip.Addr]struct{})}
}

// Update replaces the addresses owned by this TUN. Prefixes are represented
// by their assigned address; a physical underlay address in the same subnet
// is therefore not accidentally filtered.
func (l *OverlayAddressLease) Update(prefixes []netip.Prefix) {
	if l == nil {
		return
	}
	next := make(map[netip.Addr]struct{}, len(prefixes))
	for _, prefix := range prefixes {
		if prefix.IsValid() {
			next[prefix.Addr().Unmap()] = struct{}{}
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	overlayAddressRegistry.Lock()
	if overlayAddressRegistry.addresses == nil {
		overlayAddressRegistry.addresses = make(map[netip.Addr]int)
	}
	for address := range l.addresses {
		decrementOverlayAddress(address)
	}
	for address := range next {
		overlayAddressRegistry.addresses[address]++
	}
	overlayAddressRegistry.Unlock()
	l.addresses = next
}

// Close releases all addresses owned by this TUN.
func (l *OverlayAddressLease) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	overlayAddressRegistry.Lock()
	for address := range l.addresses {
		decrementOverlayAddress(address)
	}
	overlayAddressRegistry.Unlock()
	l.addresses = nil
	l.closed = true
	return nil
}

func decrementOverlayAddress(address netip.Addr) {
	if count := overlayAddressRegistry.addresses[address]; count <= 1 {
		delete(overlayAddressRegistry.addresses, address)
	} else {
		overlayAddressRegistry.addresses[address] = count - 1
	}
}

func isOverlayAddress(address netip.Addr) bool {
	overlayAddressRegistry.RLock()
	_, ok := overlayAddressRegistry.addresses[address.Unmap()]
	overlayAddressRegistry.RUnlock()
	return ok
}
