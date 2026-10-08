package sing_tun

import (
	"net/netip"
	"reflect"
	"testing"

	et "github.com/metacubex/mihomo/component/easytier"
)

func TestEasyTierRoutesUpdateLeaseAndOwnership(t *testing.T) {
	first := et.PacketSession{Addresses: []netip.Prefix{netip.MustParsePrefix("10.144.0.1/24")}, Routes: []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}}
	next := et.PacketSession{Addresses: []netip.Prefix{netip.MustParsePrefix("10.144.0.3/24")}, Routes: []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("0.0.0.0/0")}}
	manager := makeEasyTierRoutes([]et.PacketSession{first})
	var operations []string
	manager.addAddr = func(prefix netip.Prefix) (bool, error) {
		operations = append(operations, "add address "+prefix.String())
		return true, nil
	}
	manager.delAddr = func(prefix netip.Prefix) error {
		operations = append(operations, "remove address "+prefix.String())
		return nil
	}
	manager.addRoute = func(route easyTierRoute) (bool, error) {
		// The OS already owns this connected route; it must never be deleted
		// through the route manager when the lease or listener changes.
		return route.Destination != netip.PrefixFrom(route.Source, 24).Masked(), nil
	}
	var deleted []easyTierRoute
	manager.delRoute = func(route easyTierRoute) error {
		deleted = append(deleted, route)
		return nil
	}
	if err := manager.Update([]et.PacketSession{first}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Update([]et.PacketSession{next}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(operations, []string{"remove address 10.144.0.1/24", "add address 10.144.0.3/24"}) {
		t.Fatalf("lease operations: %v", operations)
	}
	for route := range manager.routes {
		if route.Source != next.Addresses[0].Addr() || route.Destination.Bits() == 0 {
			t.Fatalf("route has stale source or replaces default route: %+v", route)
		}
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	for _, route := range deleted {
		if route.Destination == netip.MustParsePrefix("10.144.0.0/24") {
			t.Fatal("borrowed connected route was deleted")
		}
	}
	if len(deleted) != 4 || len(manager.routes) != 0 || len(manager.addresses) != 0 {
		t.Fatalf("owned route cleanup: deleted=%v routes=%v addresses=%v", deleted, manager.routes, manager.addresses)
	}
}

func TestEasyTierRoutesRejectConflictingNetworkOwners(t *testing.T) {
	_, _, err := easyTierRoutePlan([]et.PacketSession{
		{Addresses: []netip.Prefix{netip.MustParsePrefix("10.144.0.1/24")}, Routes: []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}},
		{Addresses: []netip.Prefix{netip.MustParsePrefix("10.145.0.1/24")}, Routes: []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}},
	})
	if err == nil {
		t.Fatal("the same destination was assigned to two overlay sources")
	}
}

func TestEasyTierRoutesDualStackSources(t *testing.T) {
	addresses := []netip.Prefix{
		netip.MustParsePrefix("10.144.0.1/24"),
		netip.MustParsePrefix("fd00::1/8"),
		netip.MustParsePrefix("fd00:1::1/32"),
	}
	_, routes, err := easyTierRoutePlan([]et.PacketSession{{
		Addresses: addresses,
		Routes: []netip.Prefix{
			netip.MustParsePrefix("192.0.2.0/24"),
			netip.MustParsePrefix("fd00:1:0:2::/64"),
			netip.MustParsePrefix("2001:db8::/64"),
			netip.MustParsePrefix("0.0.0.0/0"),
			netip.MustParsePrefix("::/0"),
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[easyTierRoute]bool{
		{Destination: netip.MustParsePrefix("192.0.2.0/24"), Source: netip.MustParseAddr("10.144.0.1")}:   true,
		{Destination: netip.MustParsePrefix("fd00:1:0:2::/64"), Source: netip.MustParseAddr("fd00:1::1")}: true,
		{Destination: netip.MustParsePrefix("2001:db8::/64"), Source: netip.MustParseAddr("fd00::1")}:     true,
		{Destination: netip.MustParsePrefix("0.0.0.0/1"), Source: netip.MustParseAddr("10.144.0.1")}:      true,
		{Destination: netip.MustParsePrefix("128.0.0.0/1"), Source: netip.MustParseAddr("10.144.0.1")}:    true,
		{Destination: netip.MustParsePrefix("::/1"), Source: netip.MustParseAddr("fd00::1")}:              true,
		{Destination: netip.MustParsePrefix("8000::/1"), Source: netip.MustParseAddr("fd00::1")}:          true,
	}
	if len(routes) != len(want) {
		t.Fatalf("route count: got %d, want %d (%v)", len(routes), len(want), routes)
	}
	for route := range routes {
		if !want[route] {
			t.Fatalf("unexpected route: %+v", route)
		}
	}
}

func TestEasyTierRoutesRejectMissingRouteFamily(t *testing.T) {
	_, _, err := easyTierRoutePlan([]et.PacketSession{{
		Addresses: []netip.Prefix{netip.MustParsePrefix("10.144.0.1/24")},
		Routes:    []netip.Prefix{netip.MustParsePrefix("fd00::/64")},
	}})
	if err == nil {
		t.Fatal("IPv6 route accepted without an IPv6 session address")
	}
}

func TestEasyTierRoutesIPv6AddressLifecycle(t *testing.T) {
	first := et.PacketSession{
		Addresses: []netip.Prefix{netip.MustParsePrefix("fd00:144::1/64")},
		Routes:    []netip.Prefix{netip.MustParsePrefix("fd00:144::/64"), netip.MustParsePrefix("2001:db8::/64")},
	}
	next := et.PacketSession{
		Addresses: []netip.Prefix{netip.MustParsePrefix("fd00:144::3/64")},
		Routes:    first.Routes,
	}
	manager := makeEasyTierRoutes([]et.PacketSession{first})
	var operations []string
	manager.addAddr = func(prefix netip.Prefix) (bool, error) {
		operations = append(operations, "add address "+prefix.String())
		return true, nil
	}
	manager.delAddr = func(prefix netip.Prefix) error {
		operations = append(operations, "remove address "+prefix.String())
		return nil
	}
	manager.addRoute = func(route easyTierRoute) (bool, error) {
		return route.Destination != netip.MustParsePrefix("fd00:144::/64"), nil
	}
	manager.delRoute = func(route easyTierRoute) error {
		operations = append(operations, "remove route "+route.Source.String())
		return nil
	}
	if err := manager.Update([]et.PacketSession{first}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Update([]et.PacketSession{next}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"remove route fd00:144::1", "remove address fd00:144::1/64",
		"add address fd00:144::3/64", "remove route fd00:144::3", "remove address fd00:144::3/64",
	}
	if !reflect.DeepEqual(operations, want) {
		t.Fatalf("IPv6 lifecycle operations: got %v, want %v", operations, want)
	}
}
