package route

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/metacubex/mihomo/adapter"
	"github.com/metacubex/mihomo/adapter/outbound"
	"github.com/metacubex/mihomo/component/easytier"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/tunnel"

	"github.com/metacubex/http"
	"github.com/metacubex/http/httptest"
)

type easyTierStatusAdapter struct {
	*outbound.Base
	status easytier.Status
	err    error
	query  context.Context
}

func (a *easyTierStatusAdapter) EasyTierSummary() easytier.Summary {
	return a.status.Summary
}

func (a *easyTierStatusAdapter) EasyTierStatus(ctx context.Context) (easytier.Status, error) {
	a.query = ctx
	if err := ctx.Err(); err != nil {
		return easytier.Status{}, err
	}
	return a.status, a.err
}

type easyTierStatusWrapper struct{ C.ProxyAdapter }

func (a easyTierStatusWrapper) Upstream() any { return a.ProxyAdapter }

func TestEasyTierAPI(t *testing.T) {
	previousProxies, previousProviders := tunnel.Proxies(), tunnel.Providers()
	t.Cleanup(func() { tunnel.UpdateProxies(previousProxies, previousProviders) })
	instance := &easyTierStatusAdapter{
		Base: outbound.NewBase(outbound.BaseOption{Name: "mesh node", Type: C.EasyTier}),
		status: easytier.Status{
			Summary:    easytier.Summary{Name: "mesh node", State: "running", PacketMode: true, DNSZone: "mesh."},
			Node:       &easytier.NodeStatus{Hostname: "phone", IPv4: "10.144.0.2"},
			Peers:      []easytier.PeerStatus{{PeerID: 42}},
			Routes:     []easytier.RouteStatus{{PeerID: 42, IPv4: "10.144.0.1/24"}},
			Provenance: easytier.Provenance{Commit: "core-commit", SHA256: "core-sha256"},
		},
	}
	tunnel.UpdateProxies(map[string]C.Proxy{
		"mesh node": adapter.NewProxy(easyTierStatusWrapper{instance}),
		"DIRECT":    adapter.NewProxy(outbound.NewBase(outbound.BaseOption{Name: "DIRECT", Type: C.Direct})),
	}, previousProviders)
	handler := router(false, "controller-secret", "", Cors{})
	request := func(path string, authenticate bool, ctx context.Context) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
		if authenticate {
			r.Header.Set("Authorization", "Bearer controller-secret")
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := request("/easytier/", false, context.Background()); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401", w.Code)
	}
	w := request("/easytier/", true, context.Background())
	var list struct {
		Instances map[string]easytier.Summary `json:"instances"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || len(list.Instances) != 1 || list.Instances["mesh node"].State != "running" {
		t.Fatalf("instance list = %s (status %d)", w.Body, w.Code)
	}
	if instance.query != nil {
		t.Fatal("instance listing queried the guest")
	}
	w = request("/easytier/mesh%20node", true, context.Background())
	var status easytier.Status
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || status.Node == nil || status.Node.IPv4 != "10.144.0.2" || len(status.Peers) != 1 || len(status.Routes) != 1 || status.Provenance.Commit != "core-commit" {
		t.Fatalf("instance status = %s (status %d)", w.Body, w.Code)
	}
	if deadline, ok := instance.query.Deadline(); !ok || time.Until(deadline) > 5*time.Second {
		t.Fatal("guest query did not have a bounded deadline")
	}
	for _, path := range []string{"/easytier/missing", "/easytier/DIRECT"} {
		if w := request(path, true, context.Background()); w.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404", path, w.Code)
		}
	}
	instance.err = errors.New("easytier mesh node: list routes failed")
	if w := request("/easytier/mesh%20node", true, context.Background()); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("failed query status = %d, want 503", w.Code)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if w := request("/easytier/mesh%20node", true, ctx); w.Code != http.StatusGatewayTimeout {
		t.Fatalf("cancelled request status = %d, want 504", w.Code)
	}
}
