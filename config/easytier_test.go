//go:build !no_easytier

package config

import (
	"testing"

	N "github.com/metacubex/mihomo/common/net"
	"github.com/metacubex/mihomo/component/easytier"
)

func TestSharedEasyTierConfigSelectsPacketPlane(t *testing.T) {
	raw := &RawConfig{
		Tun: RawTun{EasyTier: []string{"overlay"}},
		Proxy: []map[string]any{{
			"name": "overlay", "type": "easytier", "network-name": "test",
			"ipv4": "10.144.0.1/24", "listeners": []string{"tcp://127.0.0.1:0"},
		}},
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
	if _, ok := N.FindUpstream[easytier.PacketProvider](proxies["overlay"].Adapter(), nil); !ok {
		t.Fatal("shared TUN proxy does not expose the packet plane")
	}
	if _, ok := raw.Proxy[0]["packet-mode"]; ok {
		t.Fatal("parsing mutated the user's proxy mapping")
	}
}
