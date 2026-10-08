//go:build !no_easytier

package config

import (
	"testing"

	N "github.com/metacubex/mihomo/common/net"
	"github.com/metacubex/mihomo/component/easytier"
	C "github.com/metacubex/mihomo/constant"
)

func TestSharedEasyTierDNSConfiguration(t *testing.T) {
	previousHome := C.Path.HomeDir()
	C.SetHomeDir(t.TempDir())
	t.Cleanup(func() { C.SetHomeDir(previousHome) })
	raw := DefaultRawConfig()
	raw.Tun.EasyTier = []string{"mesh"}
	raw.Proxy = []map[string]any{
		{"name": "mesh", "type": "easytier", "network-name": "mesh", "ipv4": "10.144.0.1/24", "tld-dns-zone": "Mesh.NET.", "listeners": []string{"tcp://127.0.0.1:0"}},
		{"name": "other", "type": "easytier", "network-name": "other", "ipv4": "10.145.0.1/24", "tld-dns-zone": "mesh.net", "listeners": []string{"tcp://127.0.0.1:0"}},
	}
	proxies, _, err := parseProxies(raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, proxy := range proxies {
			proxy.Close()
		}
	})
	networks, err := parseEasyTierDNS(raw.Tun.EasyTier, proxies)
	if err != nil {
		t.Fatal(err)
	}
	if len(networks) != 1 || networks[0].Name != "mesh" || networks[0].Zone != "mesh.net" {
		t.Fatalf("automatic Magic DNS configuration = %+v", networks)
	}
	for _, name := range []string{"mesh", "other"} {
		provider, _ := N.FindUpstream[easytier.StatusProvider](proxies[name].Adapter(), nil)
		if provider.EasyTierSummary().State != "idle" {
			t.Fatalf("configuration parsing started instance %q", name)
		}
	}
	networks, err = parseEasyTierDNS([]string{"mesh", "other"}, proxies)
	if err != nil || len(networks) != 2 || networks[0].Zone != networks[1].Zone {
		t.Fatalf("shared Magic DNS zone configuration = %+v, %v", networks, err)
	}
}
