package easytier

import (
	"net/netip"
	"testing"

	D "github.com/miekg/dns"
)

func TestLookupOverlayHost(t *testing.T) {
	ip := netip.MustParseAddr("10.144.0.2")
	nodes := []Node{{Hostname: "peer", IPv4: ip}}
	got, ok := LookupOverlayHost("peer.et.net.", "et.net.", nodes)
	if !ok || got.IPv4 != ip {
		t.Fatalf("got %v %v", got, ok)
	}
	if _, ok := LookupOverlayHost("missing", "et.net.", nodes); ok {
		t.Fatal("expected miss")
	}
}

func TestOverlayIPv6NamesAndReverse(t *testing.T) {
	ip := netip.MustParseAddr("fd00:144::2")
	nodes := []Node{{Hostname: "peer", IPv6: ip}}
	node, ok := LookupOverlayHost("peer.et.net.", "et.net.", nodes)
	if !ok || node.IPv6 != ip || node.IPv4.IsValid() {
		t.Fatalf("IPv6-only node: %+v, %v", node, ok)
	}
	reverse, err := D.ReverseAddr(ip.String())
	if err != nil {
		t.Fatal(err)
	}
	parsed, ok := ParsePTR(reverse)
	if !ok || parsed != ip {
		t.Fatalf("IPv6 reverse: %s, %v", parsed, ok)
	}
	if hostname, ok := LookupOverlayPTR(parsed, "et.net.", nodes); !ok || hostname != "peer.et.net." {
		t.Fatalf("IPv6 reverse hostname: %s, %v", hostname, ok)
	}
	if _, ok := ParsePTR("g." + reverse[2:]); ok {
		t.Fatal("invalid IPv6 reverse nibble accepted")
	}
}

func TestParsePTRAndNodeIPv4(t *testing.T) {
	ip, ok := ParsePTR("2.0.144.10.in-addr.arpa.")
	if !ok || ip.String() != "10.144.0.2" {
		t.Fatalf("ptr: %v %v", ip, ok)
	}
	got, err := ParseNodeIPv4("10.144.0.1/24")
	if err != nil || got.String() != "10.144.0.1" {
		t.Fatalf("cidr: %v %v", got, err)
	}
}

func TestParseIPv6Prefix(t *testing.T) {
	for _, test := range []struct{ input, want string }{
		{"fd00:144::1", "fd00:144::1/128"},
		{"fd00:144::1/64", "fd00:144::1/64"},
	} {
		got, err := ParseIPv6Prefix(test.input)
		if err != nil || got.String() != test.want {
			t.Fatalf("%q = %s, %v; want %s", test.input, got, err, test.want)
		}
	}
	for _, input := range []string{"10.144.0.1", "10.144.0.1/24", "::ffff:10.144.0.1", "::", "fd00:144::1/129"} {
		if _, err := ParseIPv6Prefix(input); err == nil {
			t.Fatalf("invalid IPv6 address accepted: %q", input)
		}
	}
}

func TestIsMagicDNS(t *testing.T) {
	if IsMagicDNS("peer", "et.net.") {
		t.Fatal("single-label name should not be magic dns")
	}
	if !IsMagicDNS("peer.et.net", "") {
		t.Fatal("expected magic dns")
	}
	if IsMagicDNS("example.com", "et.net.") {
		t.Fatal("public name should not be magic dns")
	}
}
