package config

import (
	"fmt"

	N "github.com/metacubex/mihomo/common/net"
	"github.com/metacubex/mihomo/component/easytier"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/dns"
)

func parseEasyTierDNS(names []string, proxies map[string]C.Proxy) ([]dns.EasyTierNetwork, error) {
	networks := make([]dns.EasyTierNetwork, 0, len(names))
	for _, name := range names {
		proxy, ok := proxies[name]
		if !ok {
			return nil, fmt.Errorf("tun.easytier: proxy %q not found", name)
		}
		provider, ok := N.FindUpstream[easytier.StatusProvider](proxy.Adapter(), nil)
		if !ok {
			return nil, fmt.Errorf("tun.easytier: proxy %q is not an EasyTier instance", name)
		}
		summary := provider.EasyTierSummary()
		if summary.WebManaged {
			networks = append(networks, dns.EasyTierNetwork{Name: name, Dynamic: true})
			continue
		}
		zone := easytier.NormalizeZone(summary.DNSZone)
		networks = append(networks, dns.EasyTierNetwork{Name: name, Zone: zone})
	}
	return networks, nil
}
