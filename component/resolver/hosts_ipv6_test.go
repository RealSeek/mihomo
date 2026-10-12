package resolver

import (
	"context"
	"net/netip"
	"testing"

	"github.com/metacubex/mihomo/component/trie"
)

func TestLookupIPWithResolverFiltersIPv6Hosts(t *testing.T) {
	oldHosts, oldDisabled := DefaultHosts, DisableIPv6
	t.Cleanup(func() { DefaultHosts, DisableIPv6 = oldHosts, oldDisabled })
	DefaultHosts = NewHosts(trie.New[HostValue]())
	value, err := NewHostValue([]string{"192.0.2.10", "2001:db8::10"})
	if err != nil {
		t.Fatal(err)
	}
	DefaultHosts.DomainTrie.Insert("example.test", value)
	DisableIPv6 = true
	ips, err := LookupIPWithResolver(context.Background(), "example.test", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := netip.MustParseAddr("192.0.2.10")
	if len(ips) != 1 || ips[0] != want {
		t.Fatalf("hosts result = %v, want [%s]", ips, want)
	}
}
