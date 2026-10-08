//go:build !no_easytier

package netchange

import (
	"context"

	"github.com/easytier/easytier/easytier-go/platform"
	"github.com/metacubex/mihomo/component/easytier"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/log"
	"github.com/metacubex/mihomo/tunnel"
)

type environmentUpdater interface {
	UpdateEnvironment(context.Context, platform.EnvironmentSnapshot) error
}

func refreshEasyTierEnvironment(ctx context.Context) {
	snapshot, err := easytier.EnvironmentSnapshot()
	if err != nil {
		log.Warnln("[NetChange] %s", err)
		return
	}
	seen := make(map[environmentUpdater]struct{})
	update := func(proxy C.Proxy) {
		owner, ok := proxy.Adapter().(environmentUpdater)
		if !ok || ctx.Err() != nil {
			return
		}
		if _, exists := seen[owner]; exists {
			return
		}
		seen[owner] = struct{}{}
		if err := owner.UpdateEnvironment(ctx, snapshot); err != nil {
			log.Warnln("[NetChange] EasyTier %q environment update: %s", proxy.Name(), err)
		}
	}
	for _, proxy := range tunnel.Proxies() {
		update(proxy)
	}
	for _, provider := range tunnel.Providers() {
		for _, proxy := range provider.Proxies() {
			update(proxy)
		}
	}
}
