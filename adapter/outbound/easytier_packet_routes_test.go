//go:build !no_easytier

package outbound

import (
	"context"
	"net"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	C "github.com/metacubex/mihomo/constant"
)

func TestEasyTierIPv4SessionWithDualStackPeer(t *testing.T) {
	previousHome := C.Path.HomeDir()
	C.SetHomeDir(t.TempDir())
	t.Cleanup(func() { C.SetHomeDir(previousHome) })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	reservation, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	peerURL := "tcp://" + reservation.Addr().String()
	if err := reservation.Close(); err != nil {
		t.Fatal(err)
	}
	peer, err := NewEasyTier(EasyTierOption{
		Name: "mixed-peer", PacketMode: true,
		NetworkName: "mixed-routes", NetworkSecret: "test-secret",
		IPv4: "10.144.0.1/24", IPv6: "fd00:144::1/64", Listeners: []string{peerURL},
		ProxyNetworks: []string{"192.0.2.0/24"},
		STUNServers:   []string{}, STUNServersV6: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	if _, err := peer.OpenPacketSession(ctx); err != nil {
		t.Fatal(err)
	}
	local, err := NewEasyTier(EasyTierOption{
		Name: "mixed-local", PacketMode: true,
		NetworkName: "mixed-routes", NetworkSecret: "test-secret",
		IPv4: "10.144.0.2/24", Peers: []string{peerURL}, Listeners: []string{},
		STUNServers: []string{}, STUNServersV6: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	if _, err := local.OpenPacketSession(ctx); err != nil {
		t.Fatal(err)
	}
	instance, err := local.currentInstance()
	if err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		routes, err := instance.ListRoute(ctx)
		if err != nil {
			t.Fatal(err)
		}
		ready := false
		for _, route := range routes {
			ready = ready || route.GetIpv6Addr() != nil
		}
		if ready {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("dual-stack peer's actual IPv6 address/proxy routes not advertised")
		case <-ticker.C:
		}
	}
	session, err := local.OpenPacketSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(session.Addresses) != 1 || !session.Addresses[0].Addr().Is4() {
		t.Fatalf("IPv4-only local addresses = %v", session.Addresses)
	}
	for _, route := range session.Routes {
		if route.Addr().Is6() {
			t.Fatalf("automatic IPv6 route %s has no usable local IPv6 source; session=%+v", route, session)
		}
	}
	if !slices.Contains(session.Routes, netip.MustParsePrefix("192.0.2.0/24")) || !slices.Contains(session.Routes, netip.MustParsePrefix("10.144.0.1/32")) {
		t.Fatalf("usable IPv4 peer/proxy routes lost: %v", session.Routes)
	}
	explicit := local.configMetadata
	explicitRoutes := []string{"fd00:144::/64"}
	explicit.Routes = &explicitRoutes
	if _, err := local.packetSession(ctx, instance, explicit, instance, false); err == nil || !strings.Contains(err.Error(), "invalid explicit IPv4 route") {
		t.Fatalf("unsupported explicit route did not report its configuration error: %v", err)
	}
	explicit = local.configMetadata
	explicit.ExitNodes = []string{"fd00:144::9"}
	exitSession, err := local.packetSession(ctx, instance, explicit, instance, false)
	if err != nil || !slices.Contains(exitSession.Routes, netip.MustParsePrefix("::/0")) {
		t.Fatalf("explicit exit route was silently filtered before route validation: routes=%v err=%v", exitSession.Routes, err)
	}
}
