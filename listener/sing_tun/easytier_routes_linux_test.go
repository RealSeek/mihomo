//go:build linux || android

package sing_tun

import (
	"net/netip"
	"testing"

	tun "github.com/metacubex/sing-tun"
	"golang.org/x/sys/unix"
)

func TestEasyTierAndroidIPv6ReplyRule(t *testing.T) {
	record := easyTierRoute{
		Destination: netip.MustParsePrefix("fd00:144::/64"),
		Source:      netip.MustParseAddr("fd00:144::2"),
	}
	for _, test := range []struct {
		name     string
		options  tun.Options
		priority int
		table    int
	}{
		{"defaults", tun.Options{AutoRoute: true}, tun.DefaultIPRoute2RuleIndex - 1, tun.DefaultIPRoute2TableIndex},
		{"custom", tun.Options{AutoRoute: true, IPRoute2RuleIndex: 8000, IPRoute2TableIndex: 42022}, 7999, 42022},
	} {
		t.Run(test.name, func(t *testing.T) {
			rule := easyTierAndroidReplyRule(test.options, record)
			if rule == nil || rule.Priority != test.priority || rule.Table != test.table || rule.Family != unix.AF_INET6 ||
				rule.IifName != "lo" || rule.Src != netip.MustParsePrefix("fd00:144::2/128") || rule.Dst != record.Destination {
				t.Fatalf("unexpected reply rule: %+v", rule)
			}
			if rule.Goto != -1 || rule.SuppressPrefixlen != -1 || rule.Invert || rule.MarkSet {
				t.Fatalf("reply rule has unrelated selectors: %+v", rule)
			}
		})
	}
	if rule := easyTierAndroidReplyRule(tun.Options{}, record); rule != nil {
		t.Fatalf("reply rule installed without auto-route: %+v", rule)
	}
	record.Source = netip.MustParseAddr("10.144.0.2")
	record.Destination = netip.MustParsePrefix("10.144.0.0/24")
	if rule := easyTierAndroidReplyRule(tun.Options{AutoRoute: true}, record); rule != nil {
		t.Fatalf("IPv6 reply rule installed for IPv4: %+v", rule)
	}
}
