package sing_tun

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"time"

	N "github.com/metacubex/mihomo/common/net"
	et "github.com/metacubex/mihomo/component/easytier"
	"github.com/metacubex/mihomo/log"
)

func validateEasyTierSessions(sessions []et.PacketSession, mihomoAddresses []netip.Prefix) error {
	for i, session := range sessions {
		for _, address := range session.Addresses {
			for _, existing := range mihomoAddresses {
				if existing.Overlaps(address) {
					return fmt.Errorf("easytier: overlay address %s overlaps mihomo TUN address %s", address, existing)
				}
			}
			for _, existing := range sessions[:i] {
				for _, previous := range existing.Addresses {
					if previous.Overlaps(address) {
						return fmt.Errorf("easytier: shared TUN networks overlap: %s and %s", previous, address)
					}
				}
			}
		}
	}
	_, _, err := easyTierRoutePlan(sessions)
	return err
}

func easyTierSessionAddresses(sessions []et.PacketSession) []netip.Prefix {
	addresses := make([]netip.Prefix, 0)
	for _, session := range sessions {
		addresses = append(addresses, session.Addresses...)
	}
	return addresses
}

func (l *Listener) setEasyTierPacketPolicies() {
	addresses := append(slices.Clone(l.options.Inet4Address), l.options.Inet6Address...)
	for _, adapter := range l.easyTierAdapters {
		provider, _ := N.FindUpstream[et.PacketSessionsProvider](adapter, nil)
		owner := adapter
		provider.SetPacketConfigPolicy(func(_ context.Context, id string, config et.Config) error {
			mtu := 1380
			if config.MTU > 0 {
				mtu = config.MTU
			}
			if l.easyTierMTU > uint32(mtu) {
				return fmt.Errorf("easytier: TUN MTU %d exceeds network %s MTU %d", l.easyTierMTU, id, mtu)
			}
			candidate, err := config.OverlayAddresses()
			if err != nil {
				return fmt.Errorf("easytier: network %s addresses: %w", id, err)
			}
			for _, prefix := range candidate {
				for _, existing := range addresses {
					if prefix.Overlaps(existing) {
						return fmt.Errorf("easytier: network %s address %s overlaps mihomo TUN address %s", id, prefix, existing)
					}
				}
			}
			for name, other := range l.easyTierAdapters {
				if other == owner {
					continue
				}
				otherProvider, _ := N.FindUpstream[et.PacketSessionsProvider](other, nil)
				networks, err := otherProvider.PacketNetworks()
				if err != nil {
					return fmt.Errorf("easytier: configuration snapshot %q: %w", name, err)
				}
				for _, network := range networks {
					previous, err := network.Config.OverlayAddresses()
					if err != nil {
						return fmt.Errorf("easytier: network %s/%s addresses: %w", name, network.ID, err)
					}
					for _, prefix := range candidate {
						for _, address := range previous {
							if prefix.Overlaps(address) {
								return fmt.Errorf("easytier: network %s address %s overlaps %s/%s address %s", id, prefix, name, network.ID, address)
							}
						}
					}
				}
			}
			return nil
		})
	}
}

func (l *Listener) startEasyTierSync(device interface {
	UpdateSessions([]et.PacketSession) error
}) {
	ctx, cancel := context.WithCancel(context.Background())
	l.easyTierCancel = cancel
	l.easyTierDone = make(chan struct{})
	go func() {
		defer close(l.easyTierDone)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			err := l.updateEasyTierRoutes(ctx, device)
			if err != nil && ctx.Err() == nil {
				log.Warnln("[EasyTier] synchronize TUN routes: %v", err)
			}
		}
	}()
}

func (l *Listener) updateEasyTierRoutes(ctx context.Context, device interface {
	UpdateSessions([]et.PacketSession) error
}) error {
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	sessions := make([]et.PacketSession, 0, len(l.options.EasyTier))
	for _, name := range l.options.EasyTier {
		provider, ok := N.FindUpstream[et.PacketSessionsProvider](l.easyTierAdapters[name], nil)
		if !ok {
			return fmt.Errorf("easytier: packet provider %q disappeared during route update", name)
		}
		networks, err := provider.OpenPacketSessions(queryCtx)
		if err != nil {
			return err
		}
		sessions = append(sessions, networks...)
	}
	if err := validateEasyTierSessions(sessions, append(slices.Clone(l.options.Inet4Address), l.options.Inet6Address...)); err != nil {
		return err
	}
	for _, session := range sessions {
		if l.easyTierMTU > session.MTU {
			return fmt.Errorf("easytier: TUN MTU %d exceeds network %s MTU %d", l.easyTierMTU, session.ID, session.MTU)
		}
	}
	// Add OS routes only after publishing their packet destinations; packets
	// captured by a newly installed route must already reach the right core.
	if err := device.UpdateSessions(sessions); err != nil {
		return err
	}
	// Publish address ownership as soon as the packet mux accepts the new
	// sessions. Route reconciliation may fail transiently; the active packet
	// session must still be excluded from the next underlay snapshot.
	l.easyTierOverlay.Update(easyTierSessionAddresses(sessions))
	if err := l.easyTierRoutes.Update(sessions); err != nil {
		return err
	}
	return nil
}
