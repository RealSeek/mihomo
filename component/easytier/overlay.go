package easytier

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/netip"
	"strings"

	D "github.com/miekg/dns"
)

const DefaultTLDDNSZone = "et.net."

// Node describes one overlay hostname and its assigned addresses.
type Node struct {
	Hostname   string
	IPv4       netip.Addr
	IPv6       netip.Addr
	PublicIPv6 netip.Addr
}

// NormalizeDNSName lowercases a name and strips a trailing dot.
func NormalizeDNSName(name string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
}

// NormalizeZone returns a MagicDNS zone without a trailing dot.
func NormalizeZone(zone string) string {
	zone = NormalizeDNSName(zone)
	if zone == "" {
		return NormalizeDNSName(DefaultTLDDNSZone)
	}
	return zone
}

// OverlayNames returns hostname forms that MagicDNS may use.
func OverlayNames(hostname, zone string) []string {
	hostname = NormalizeDNSName(hostname)
	if hostname == "" {
		return nil
	}
	zone = NormalizeZone(zone)
	names := []string{hostname}
	if hostname != zone && !strings.HasSuffix(hostname, "."+zone) {
		names = append(names, hostname+"."+zone)
	}
	return names
}

// IsMagicDNS reports whether host should stay on the overlay resolver.
func IsMagicDNS(host, zone string) bool {
	host = NormalizeDNSName(host)
	if host == "" {
		return false
	}
	zone = NormalizeZone(zone)
	return host == zone || strings.HasSuffix(host, "."+zone)
}

// LookupOverlayHost finds an overlay node for host, regardless of address family.
func LookupOverlayHost(host, zone string, nodes []Node) (Node, bool) {
	host = NormalizeDNSName(host)
	if host == "" {
		return Node{}, false
	}
	for _, node := range nodes {
		for _, name := range OverlayNames(node.Hostname, zone) {
			if host == name {
				return node, true
			}
		}
	}
	return Node{}, false
}

// LookupOverlayPTR finds a MagicDNS name for an overlay address.
func LookupOverlayPTR(ip netip.Addr, zone string, nodes []Node) (string, bool) {
	if !ip.IsValid() {
		return "", false
	}
	zone = NormalizeZone(zone)
	for _, node := range nodes {
		if node.IPv4 != ip && node.IPv6 != ip && node.PublicIPv6 != ip {
			continue
		}
		names := OverlayNames(node.Hostname, zone)
		if len(names) == 0 {
			continue
		}
		return names[len(names)-1] + ".", true
	}
	return "", false
}

// ParseNodeIPv4 parses a node IPv4 address or CIDR such as "10.144.0.1/24".
func ParseNodeIPv4(value string) (netip.Addr, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return netip.Addr{}, fmt.Errorf("easytier: empty overlay IPv4 address")
	}
	if prefix, err := netip.ParsePrefix(value); err == nil {
		return prefix.Addr().Unmap(), nil
	}
	ip, err := netip.ParseAddr(value)
	if err != nil {
		return netip.Addr{}, err
	}
	return ip.Unmap(), nil
}

// ParseIPv6Prefix preserves explicit prefixes; native bare addresses are /128.
func ParseIPv6Prefix(value string) (netip.Prefix, error) {
	value = strings.TrimSpace(value)
	prefix, err := netip.ParsePrefix(value)
	if err != nil {
		address, parseErr := netip.ParseAddr(value)
		if parseErr != nil {
			return netip.Prefix{}, fmt.Errorf("easytier: invalid IPv6 address %q: %w", value, err)
		}
		prefix = netip.PrefixFrom(address, 128)
	}
	if !prefix.IsValid() || !prefix.Addr().Is6() || prefix.Addr().Is4In6() || prefix.Addr().IsUnspecified() {
		return netip.Prefix{}, fmt.Errorf("easytier: invalid IPv6 address %q", value)
	}
	return prefix, nil
}

// IPv4FromUint32 converts a big-endian IPv4 integer to an address.
func IPv4FromUint32(addr uint32) netip.Addr {
	var bytes [4]byte
	binary.BigEndian.PutUint32(bytes[:], addr)
	return netip.AddrFrom4(bytes)
}

// ParsePTR parses standard IPv4 octet and IPv6 nibble reverse DNS names.
func ParsePTR(name string) (netip.Addr, bool) {
	name = NormalizeDNSName(name)
	var ip netip.Addr
	switch {
	case strings.HasSuffix(name, ".in-addr.arpa"):
		labels := D.SplitDomainName(strings.TrimSuffix(name, ".in-addr.arpa"))
		if len(labels) != 4 {
			return netip.Addr{}, false
		}
		ip, _ = netip.ParseAddr(strings.Join([]string{labels[3], labels[2], labels[1], labels[0]}, "."))
	case strings.HasSuffix(name, ".ip6.arpa"):
		labels := D.SplitDomainName(strings.TrimSuffix(name, ".ip6.arpa"))
		if len(labels) != 32 {
			return netip.Addr{}, false
		}
		var nibbles [32]byte
		for i, label := range labels {
			if len(label) != 1 {
				return netip.Addr{}, false
			}
			nibbles[31-i] = label[0]
		}
		var address [16]byte
		if _, err := hex.Decode(address[:], nibbles[:]); err != nil {
			return netip.Addr{}, false
		}
		ip = netip.AddrFrom16(address)
	default:
		return netip.Addr{}, false
	}
	if !ip.IsValid() {
		return netip.Addr{}, false
	}
	canonical, err := D.ReverseAddr(ip.String())
	return ip, err == nil && NormalizeDNSName(canonical) == name
}

func IPv6FromParts(part1, part2, part3, part4 uint32) netip.Addr {
	var address [16]byte
	binary.BigEndian.PutUint32(address[0:4], part1)
	binary.BigEndian.PutUint32(address[4:8], part2)
	binary.BigEndian.PutUint32(address[8:12], part3)
	binary.BigEndian.PutUint32(address[12:16], part4)
	return netip.AddrFrom16(address)
}
