package route

import (
	"context"
	"errors"
	"time"

	N "github.com/metacubex/mihomo/common/net"
	"github.com/metacubex/mihomo/component/easytier"
	"github.com/metacubex/mihomo/tunnel"

	"github.com/metacubex/chi"
	"github.com/metacubex/chi/render"
	"github.com/metacubex/http"
)

func easyTierRouter() http.Handler {
	r := chi.NewRouter()
	r.Get("/", getEasyTiers)
	r.Route("/{name}", func(r chi.Router) {
		r.Get("/", getEasyTier)
	})
	return r
}

func getEasyTiers(w http.ResponseWriter, r *http.Request) {
	instances := map[string]easytier.Summary{}
	for name, proxy := range tunnel.Proxies() {
		if provider, ok := N.FindUpstream[easytier.StatusProvider](proxy.Adapter(), nil); ok {
			instances[name] = provider.EasyTierSummary()
		}
	}
	render.JSON(w, r, render.M{"instances": instances})
}

func getEasyTier(w http.ResponseWriter, r *http.Request) {
	proxy, exists := tunnel.Proxies()[getEscapeParam(r, "name")]
	if !exists {
		render.Status(r, http.StatusNotFound)
		render.JSON(w, r, ErrNotFound)
		return
	}
	provider, ok := N.FindUpstream[easytier.StatusProvider](proxy.Adapter(), nil)
	if !ok {
		render.Status(r, http.StatusNotFound)
		render.JSON(w, r, newError("Proxy is not an EasyTier instance"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	status, err := provider.EasyTierStatus(ctx)
	if err != nil {
		code := http.StatusServiceUnavailable
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			code = http.StatusGatewayTimeout
		}
		render.Status(r, code)
		render.JSON(w, r, newError(err.Error()))
		return
	}
	render.JSON(w, r, status)
}
