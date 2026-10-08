//go:build !no_easytier

package executor

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/metacubex/mihomo/adapter"
	"github.com/metacubex/mihomo/adapter/outbound"
	C "github.com/metacubex/mihomo/constant"
	P "github.com/metacubex/mihomo/constant/provider"
	"github.com/metacubex/mihomo/tunnel"
)

type easyTierTestProvider struct {
	P.ProxyProvider
	proxies []C.Proxy
}

func (p *easyTierTestProvider) Proxies() []C.Proxy { return p.proxies }

func TestUpdateProxiesClosesReplacedEasyTier(t *testing.T) {
	useTempHomeDir(t)
	previousProxies, previousProviders := tunnel.Proxies(), tunnel.Providers()
	tunnel.UpdateProxies(nil, nil)
	t.Cleanup(func() {
		updateProxies(nil, nil)
		tunnel.UpdateProxies(previousProxies, previousProviders)
	})

	reservation, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := reservation.Addr().String()
	if err := reservation.Close(); err != nil {
		t.Fatal(err)
	}

	newProxy := func(name string, listen bool) (*outbound.EasyTier, C.Proxy) {
		t.Helper()
		option := outbound.EasyTierOption{
			Name: name, PacketMode: true, NetworkName: "executor-lifecycle-test",
			IPv4: "10.144.0.1/24", STUNServers: []string{}, STUNServersV6: []string{},
		}
		if listen {
			option.Listeners = []string{"tcp://" + address}
		} else {
			noListener := true
			option.NoListener = &noListener
			option.Peers = []string{"tcp://" + address}
		}
		easyTier, err := outbound.NewEasyTier(option)
		if err != nil {
			t.Fatal(err)
		}
		proxy := adapter.NewProxy(outbound.NewAutoCloseProxyAdapter(easyTier))
		t.Cleanup(func() { _ = proxy.Close() })
		return easyTier, proxy
	}

	old, oldProxy := newProxy("mesh", true)
	removed, removedProxy := newProxy("provider-only", false)
	retained, retainedProxy := newProxy("retained", false)
	tunnel.UpdateProxies(
		map[string]C.Proxy{"mesh": oldProxy, "retained": retainedProxy},
		map[string]P.ProxyProvider{"old": &easyTierTestProvider{proxies: []C.Proxy{oldProxy, removedProxy}}},
	)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := old.OpenPacketSession(ctx); err != nil {
		t.Fatal(err)
	}
	if listener, err := net.Listen("tcp4", address); err == nil {
		_ = listener.Close()
		t.Fatal("old EasyTier did not own its configured TCP listener")
	}

	replacement, replacementProxy := newProxy("mesh", true)
	newProxies := map[string]C.Proxy{"mesh": replacementProxy}
	newProviders := map[string]P.ProxyProvider{"new": &easyTierTestProvider{proxies: []C.Proxy{retainedProxy}}}
	updateProxies(newProxies, newProviders)
	for _, old := range []*outbound.EasyTier{old, removed} {
		if state := old.EasyTierSummary().State; state != "closed" {
			t.Fatalf("removed EasyTier %s state = %s, want closed", old.Name(), state)
		}
	}
	if state := retained.EasyTierSummary().State; state != "idle" {
		t.Fatalf("retained EasyTier state = %s, want idle", state)
	}
	listener, err := net.Listen("tcp4", address)
	if err != nil {
		t.Fatalf("replaced EasyTier retained its TCP listener: %v", err)
	}
	_ = listener.Close()
	if _, err := replacement.OpenPacketSession(ctx); err != nil {
		t.Fatalf("replacement EasyTier could not reuse the listener: %v", err)
	}
	updateProxies(newProxies, newProviders)
	if state := replacement.EasyTierSummary().State; state != "running" {
		t.Fatalf("retained replacement state = %s, want running", state)
	}
	updateProxies(nil, nil)
	listener, err = net.Listen("tcp4", address)
	if err != nil {
		t.Fatalf("removed EasyTier retained its TCP listener: %v", err)
	}
	_ = listener.Close()
}
