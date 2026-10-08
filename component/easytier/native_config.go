package easytier

import (
	"fmt"
	"net/netip"

	"github.com/pelletier/go-toml/v2"
)

// OverlayAddresses decodes configured addresses using the native core's defaults.
func (c Config) OverlayAddresses() ([]netip.Prefix, error) {
	var addresses []netip.Prefix
	if c.IPv4 != "" {
		prefix, err := netip.ParsePrefix(c.IPv4)
		if err != nil {
			address, parseErr := netip.ParseAddr(c.IPv4)
			if parseErr != nil {
				return nil, fmt.Errorf("easytier: invalid IPv4 address %q: %w", c.IPv4, err)
			}
			prefix = netip.PrefixFrom(address, 32)
		}
		if !prefix.IsValid() || !prefix.Addr().Is4() || prefix.Addr().IsUnspecified() {
			return nil, fmt.Errorf("easytier: invalid IPv4 address %q", c.IPv4)
		}
		if prefix.Bits() == 32 {
			prefix = netip.PrefixFrom(prefix.Addr(), 24)
		}
		addresses = append(addresses, prefix)
	}
	if c.IPv6 != "" {
		prefix, err := ParseIPv6Prefix(c.IPv6)
		if err != nil {
			return nil, err
		}
		addresses = append(addresses, prefix)
	}
	return addresses, nil
}

// PrepareNativeConfig retains upstream fields while mihomo owns the packet
// delivery mode and underlay binding. Rust remains the config schema owner.
func PrepareNativeConfig(configTOML string, packetMode bool) (string, Config, error) {
	document := make(map[string]any)
	if err := toml.Unmarshal([]byte(configTOML), &document); err != nil {
		return "", Config{}, fmt.Errorf("easytier: parse config-toml: %w", err)
	}
	// Decode only metadata consumed by mihomo. Everything else is retained in
	// document and validated by the embedded official core.
	var metadata struct {
		IPv4            string    `toml:"ipv4"`
		IPv6            string    `toml:"ipv6"`
		DHCP            bool      `toml:"dhcp"`
		ExitNodes       []string  `toml:"exit_nodes"`
		Routes          *[]string `toml:"routes"`
		Hostname        string    `toml:"hostname"`
		NetworkIdentity struct {
			NetworkName string `toml:"network_name"`
		} `toml:"network_identity"`
		Flags struct {
			TLDDNSZone     string `toml:"tld_dns_zone"`
			MTU            int    `toml:"mtu"`
			EnableExitNode *bool  `toml:"enable_exit_node"`
		} `toml:"flags"`
	}
	if err := toml.Unmarshal([]byte(configTOML), &metadata); err != nil {
		return "", Config{}, fmt.Errorf("easytier: config-toml metadata: %w", err)
	}
	flags, ok := document["flags"].(map[string]any)
	if !ok {
		if _, exists := document["flags"]; exists {
			return "", Config{}, fmt.Errorf("easytier: config-toml flags must be a table")
		}
		flags = make(map[string]any)
		document["flags"] = flags
	}
	flags["no_tun"] = !packetMode
	flags["bind_device"] = false
	encoded, err := toml.Marshal(document)
	if err != nil {
		return "", Config{}, fmt.Errorf("easytier: encode config-toml: %w", err)
	}
	return string(encoded), Config{
		PacketMode: packetMode, NetworkName: metadata.NetworkIdentity.NetworkName,
		IPv4: metadata.IPv4, IPv6: metadata.IPv6, DHCP: metadata.DHCP, Hostname: metadata.Hostname, ExitNodes: metadata.ExitNodes,
		Routes:     metadata.Routes,
		TLDDNSZone: metadata.Flags.TLDDNSZone, MTU: metadata.Flags.MTU, EnableExitNode: metadata.Flags.EnableExitNode,
	}, nil
}
