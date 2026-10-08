package easytier

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

const defaultListener = "tcp://0.0.0.0:11010"

type requiredFlag struct {
	key   string
	value string
}

// Config is the structured EasyTier outbound configuration rendered to TOML.
type Config struct {
	PacketMode                    bool
	NetworkName                   string
	NetworkSecret                 string
	Hostname                      string
	IPv4                          string
	IPv6                          string
	IPv6PublicAddrProvider        *bool
	IPv6PublicAddrAuto            *bool
	IPv6PublicAddrPrefix          string
	DHCP                          bool
	Peers                         []string
	Listeners                     []string
	NoListener                    *bool
	MappedListeners               []string
	ExitNodes                     []string
	Routes                        *[]string
	ProxyNetworks                 []string
	Socks5Proxy                   string
	PortForwards                  []PortForwardOption
	InstanceName                  string
	AcceptDNS                     *bool
	EnableExitNode                *bool
	EnableEncryption              *bool
	EncryptionAlgorithm           string
	PrivateMode                   *bool
	LatencyFirst                  *bool
	DisableP2P                    *bool
	EnableKCPProxy                *bool
	DisableKCPInput               *bool
	EnableQUICProxy               *bool
	DisableQUICInput              *bool
	MTU                           int
	TLDDNSZone                    string
	SecureMode                    *bool
	LocalPrivateKey               string
	LocalPublicKey                string
	STUNServers                   []string
	TCPSTUNServers                []string
	STUNServersV6                 []string
	TCPWhitelist                  []string
	UDPWhitelist                  []string
	DefaultProtocol               string
	DevName                       string
	EnableIPv6                    *bool
	ProxyForwardBySystem          *bool
	RelayNetworkWhitelist         string
	P2POnly                       *bool
	RelayAllPeerRPC               *bool
	DisableRelayKCP               *bool
	EnableRelayForeignNetworkKCP  *bool
	DisableRelayQUIC              *bool
	EnableRelayForeignNetworkQUIC *bool
	ForeignRelayBPSLimit          *uint64
	InstanceRecvBPSLimit          *uint64
	MultiThread                   *bool
	MultiThreadCount              int
	DataCompressAlgo              string
	DisableUPnP                   *bool
	DisableRelayData              *bool
	PreferPeerRelay               *bool
	EnableUDPBroadcastRelay       *bool
	QUICListenPort                int
	SocketMark                    *uint32
	NeedP2P                       *bool
	LazyP2P                       *bool
	DisableTCPHolePunch           *bool
	DisableUDPHolePunch           *bool
	DisableSymHolePunch           *bool
}

// Peer is one EasyTier [[peer]] table after URI query parameters are extracted.
type Peer struct {
	URI           string
	PeerPublicKey string
}

type PortForwardProtocol string

const (
	PortForwardTCP PortForwardProtocol = "tcp"
	PortForwardUDP PortForwardProtocol = "udp"
)

type PortForwardOption struct {
	Protocol    PortForwardProtocol `proxy:"protocol"`
	Bind        string              `proxy:"bind"`
	Destination string              `proxy:"destination"`
}

func (c Config) listeners() []string {
	if c.NoListener != nil && *c.NoListener {
		return []string{}
	}
	if len(c.Listeners) > 0 {
		return c.Listeners
	}
	if c.NoListener != nil && !*c.NoListener {
		return []string{defaultListener}
	}
	return []string{}
}

// ValidateStructured checks structured fields before rendering TOML.
func (c Config) ValidateStructured() error {
	if strings.TrimSpace(c.NetworkName) == "" {
		return fmt.Errorf("easytier: network-name is required")
	}
	if c.NoListener != nil && *c.NoListener && len(c.Listeners) > 0 {
		return fmt.Errorf("easytier: no-listener cannot be combined with listeners")
	}
	if len(c.Peers) == 0 && len(c.listeners()) == 0 {
		return fmt.Errorf("easytier: peers is required when listeners are empty; implicit public.easytier.top is disabled")
	}
	if _, err := c.parsedPeers(); err != nil {
		return err
	}
	if c.LocalPublicKey != "" && c.LocalPrivateKey == "" {
		return fmt.Errorf("easytier: local-public-key requires local-private-key")
	}
	if c.SecureMode != nil && !*c.SecureMode && c.hasSecureModeMaterial() {
		return fmt.Errorf("easytier: local keys and peer-public-key require secure-mode")
	}
	if c.Socks5Proxy != "" {
		portal, err := url.Parse(strings.TrimSpace(c.Socks5Proxy))
		if err != nil || portal.Scheme != "socks5" || portal.Host == "" || portal.User != nil || portal.Path != "" || portal.RawQuery != "" || portal.Fragment != "" {
			return fmt.Errorf("easytier: socks5-proxy must be a socks5 URL with an IP host and port, got %q", c.Socks5Proxy)
		}
		addr, err := netip.ParseAddrPort(portal.Host)
		if err != nil || !addr.Addr().Is4() || addr.Port() == 0 {
			return fmt.Errorf("easytier: socks5-proxy must be a socks5 URL with an IP host and port, got %q", c.Socks5Proxy)
		}
	}
	for i, forward := range c.PortForwards {
		if forward.Protocol != PortForwardTCP && forward.Protocol != PortForwardUDP {
			return fmt.Errorf("easytier: port-forwards[%d].protocol must be tcp or udp", i)
		}
		bind, err := netip.ParseAddrPort(forward.Bind)
		if err != nil || !bind.Addr().Is4() {
			return fmt.Errorf("easytier: port-forwards[%d].bind must be an IPv4 socket address, got %q", i, forward.Bind)
		}
		destination, err := netip.ParseAddrPort(forward.Destination)
		if err != nil || !destination.Addr().Is4() || destination.Port() == 0 {
			return fmt.Errorf("easytier: port-forwards[%d].destination must be an IPv4 socket address with a nonzero port, got %q", i, forward.Destination)
		}
	}
	return nil
}

func (c Config) parsedPeers() ([]Peer, error) {
	out := make([]Peer, 0, len(c.Peers))
	for i, raw := range c.Peers {
		peer, err := parsePeerURI(raw)
		if err != nil {
			return nil, fmt.Errorf("easytier: peers[%d]: %w", i, err)
		}
		out = append(out, peer)
	}
	return out, nil
}

func parsePeerURI(raw string) (Peer, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Peer{}, fmt.Errorf("uri is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return Peer{}, fmt.Errorf("invalid uri: %w", err)
	}
	if u.RawQuery == "" {
		return Peer{URI: raw}, nil
	}
	kept := make([]string, 0)
	key := ""
	stripped := false
	for _, pair := range strings.Split(u.RawQuery, "&") {
		if pair == "" {
			continue
		}
		name, value, _ := strings.Cut(pair, "=")
		decodedName, err := url.PathUnescape(name)
		if err != nil {
			return Peer{}, fmt.Errorf("invalid uri query: %w", err)
		}
		switch decodedName {
		case "peer-public-key", "peer_public_key":
			decodedValue, err := url.PathUnescape(value)
			if err != nil {
				return Peer{}, fmt.Errorf("invalid peer-public-key: %w", err)
			}
			key = decodedValue
			stripped = true
		default:
			kept = append(kept, pair)
		}
	}
	if !stripped {
		return Peer{URI: raw}, nil
	}
	u.RawQuery = strings.Join(kept, "&")
	return Peer{URI: u.String(), PeerPublicKey: key}, nil
}

func (c Config) hasSecureModeMaterial() bool {
	if c.LocalPrivateKey != "" || c.LocalPublicKey != "" {
		return true
	}
	peers, err := c.parsedPeers()
	if err != nil {
		return false
	}
	for _, peer := range peers {
		if peer.PeerPublicKey != "" {
			return true
		}
	}
	return false
}

func (c Config) secureModeEnabled() bool {
	if c.SecureMode != nil {
		return *c.SecureMode
	}
	return c.hasSecureModeMaterial()
}

// RenderTOML converts structured fields to EasyTier TOML.
func (c Config) RenderTOML() (string, error) {
	if err := c.ValidateStructured(); err != nil {
		return "", err
	}
	var encoded strings.Builder
	if c.InstanceName != "" {
		writeTOMLStringField(&encoded, "instance_name", c.InstanceName)
	}
	if c.Hostname != "" {
		writeTOMLStringField(&encoded, "hostname", c.Hostname)
	}
	if strings.TrimSpace(c.IPv4) != "" {
		writeTOMLStringField(&encoded, "ipv4", c.IPv4)
	}
	if strings.TrimSpace(c.IPv6) != "" {
		writeTOMLStringField(&encoded, "ipv6", c.IPv6)
	}
	writeOptionalBoolField(&encoded, "ipv6_public_addr_provider", c.IPv6PublicAddrProvider)
	writeOptionalBoolField(&encoded, "ipv6_public_addr_auto", c.IPv6PublicAddrAuto)
	if c.IPv6PublicAddrPrefix != "" {
		writeTOMLStringField(&encoded, "ipv6_public_addr_prefix", c.IPv6PublicAddrPrefix)
	}
	if c.DHCP || (strings.TrimSpace(c.IPv4) == "" && strings.TrimSpace(c.IPv6) == "") {
		writeTOMLBoolField(&encoded, "dhcp", true)
	}
	writeTOMLStringArrayField(&encoded, "listeners", c.listeners())
	if len(c.MappedListeners) > 0 {
		writeTOMLStringArrayField(&encoded, "mapped_listeners", c.MappedListeners)
	}
	if len(c.ExitNodes) > 0 {
		writeTOMLStringArrayField(&encoded, "exit_nodes", c.ExitNodes)
	}
	if c.Routes != nil {
		writeTOMLStringArrayField(&encoded, "routes", *c.Routes)
	}
	if c.Socks5Proxy != "" {
		writeTOMLStringField(&encoded, "socks5_proxy", strings.TrimSpace(c.Socks5Proxy))
	}
	if c.STUNServers != nil {
		writeTOMLStringArrayField(&encoded, "stun_servers", c.STUNServers)
	}
	if c.TCPSTUNServers != nil {
		writeTOMLStringArrayField(&encoded, "tcp_stun_servers", c.TCPSTUNServers)
	}
	if c.STUNServersV6 != nil {
		writeTOMLStringArrayField(&encoded, "stun_servers_v6", c.STUNServersV6)
	}
	if c.TCPWhitelist != nil {
		writeTOMLStringArrayField(&encoded, "tcp_whitelist", c.TCPWhitelist)
	}
	if c.UDPWhitelist != nil {
		writeTOMLStringArrayField(&encoded, "udp_whitelist", c.UDPWhitelist)
	}
	encoded.WriteByte('\n')
	encoded.WriteString("[network_identity]\n")
	writeTOMLStringField(&encoded, "network_name", c.NetworkName)
	writeTOMLStringField(&encoded, "network_secret", c.NetworkSecret)

	peers, err := c.parsedPeers()
	if err != nil {
		return "", err
	}

	if c.secureModeEnabled() {
		encoded.WriteString("\n[secure_mode]\n")
		writeTOMLBoolField(&encoded, "enabled", true)
		if c.LocalPrivateKey != "" {
			writeTOMLStringField(&encoded, "local_private_key", c.LocalPrivateKey)
		}
		if c.LocalPublicKey != "" {
			writeTOMLStringField(&encoded, "local_public_key", c.LocalPublicKey)
		}
	}

	for _, peer := range peers {
		encoded.WriteString("\n[[peer]]\n")
		writeTOMLStringField(&encoded, "uri", peer.URI)
		if peer.PeerPublicKey != "" {
			writeTOMLStringField(&encoded, "peer_public_key", peer.PeerPublicKey)
		}
	}
	for _, network := range c.ProxyNetworks {
		encoded.WriteString("\n[[proxy_network]]\n")
		writeTOMLStringField(&encoded, "cidr", network)
	}
	for _, forward := range c.PortForwards {
		encoded.WriteString("\n[[port_forward]]\n")
		writeTOMLStringField(&encoded, "proto", string(forward.Protocol))
		writeTOMLStringField(&encoded, "bind_addr", forward.Bind)
		writeTOMLStringField(&encoded, "dst_addr", forward.Destination)
	}

	encoded.WriteString("\n[flags]\n")
	writeTOMLBoolField(&encoded, "no_tun", !c.PacketMode)
	writeTOMLBoolField(&encoded, "bind_device", false)
	if c.DefaultProtocol != "" {
		writeTOMLStringField(&encoded, "default_protocol", c.DefaultProtocol)
	}
	if c.DevName != "" {
		writeTOMLStringField(&encoded, "dev_name", c.DevName)
	}
	writeOptionalBoolField(&encoded, "enable_ipv6", c.EnableIPv6)
	writeOptionalBoolField(&encoded, "accept_dns", c.AcceptDNS)
	writeOptionalBoolField(&encoded, "enable_exit_node", c.EnableExitNode)
	writeOptionalBoolField(&encoded, "enable_encryption", c.EnableEncryption)
	if c.EncryptionAlgorithm != "" {
		writeTOMLStringField(&encoded, "encryption_algorithm", c.EncryptionAlgorithm)
	}
	writeOptionalBoolField(&encoded, "private_mode", c.PrivateMode)
	writeOptionalBoolField(&encoded, "latency_first", c.LatencyFirst)
	writeOptionalBoolField(&encoded, "disable_p2p", c.DisableP2P)
	writeOptionalBoolField(&encoded, "need_p2p", c.NeedP2P)
	writeOptionalBoolField(&encoded, "lazy_p2p", c.LazyP2P)
	writeOptionalBoolField(&encoded, "disable_tcp_hole_punching", c.DisableTCPHolePunch)
	writeOptionalBoolField(&encoded, "disable_udp_hole_punching", c.DisableUDPHolePunch)
	writeOptionalBoolField(&encoded, "disable_sym_hole_punching", c.DisableSymHolePunch)
	writeOptionalBoolField(&encoded, "enable_kcp_proxy", c.EnableKCPProxy)
	writeOptionalBoolField(&encoded, "disable_kcp_input", c.DisableKCPInput)
	writeOptionalBoolField(&encoded, "enable_quic_proxy", c.EnableQUICProxy)
	writeOptionalBoolField(&encoded, "disable_quic_input", c.DisableQUICInput)
	writeOptionalBoolField(&encoded, "proxy_forward_by_system", c.ProxyForwardBySystem)
	if c.RelayNetworkWhitelist != "" {
		writeTOMLStringField(&encoded, "relay_network_whitelist", c.RelayNetworkWhitelist)
	}
	writeOptionalBoolField(&encoded, "p2p_only", c.P2POnly)
	writeOptionalBoolField(&encoded, "relay_all_peer_rpc", c.RelayAllPeerRPC)
	writeOptionalBoolField(&encoded, "disable_relay_kcp", c.DisableRelayKCP)
	writeOptionalBoolField(&encoded, "enable_relay_foreign_network_kcp", c.EnableRelayForeignNetworkKCP)
	writeOptionalBoolField(&encoded, "disable_relay_quic", c.DisableRelayQUIC)
	writeOptionalBoolField(&encoded, "enable_relay_foreign_network_quic", c.EnableRelayForeignNetworkQUIC)
	writeOptionalBoolField(&encoded, "multi_thread", c.MultiThread)
	if c.MultiThreadCount > 0 {
		fmt.Fprintf(&encoded, "multi_thread_count = %d\n", c.MultiThreadCount)
	}
	if c.DataCompressAlgo != "" {
		writeTOMLStringField(&encoded, "data_compress_algo", c.DataCompressAlgo)
	}
	writeOptionalBoolField(&encoded, "disable_upnp", c.DisableUPnP)
	writeOptionalBoolField(&encoded, "disable_relay_data", c.DisableRelayData)
	writeOptionalBoolField(&encoded, "prefer_peer_relay", c.PreferPeerRelay)
	writeOptionalBoolField(&encoded, "enable_udp_broadcast_relay", c.EnableUDPBroadcastRelay)
	if c.ForeignRelayBPSLimit != nil {
		writeTOMLStringField(&encoded, "foreign_relay_bps_limit", strconv.FormatUint(*c.ForeignRelayBPSLimit, 10))
	}
	if c.InstanceRecvBPSLimit != nil {
		writeTOMLStringField(&encoded, "instance_recv_bps_limit", strconv.FormatUint(*c.InstanceRecvBPSLimit, 10))
	}
	if c.QUICListenPort > 0 {
		fmt.Fprintf(&encoded, "quic_listen_port = %d\n", c.QUICListenPort)
	}
	if c.SocketMark != nil {
		fmt.Fprintf(&encoded, "socket_mark = %d\n", *c.SocketMark)
	}
	if c.MTU > 0 {
		fmt.Fprintf(&encoded, "mtu = %d\n", c.MTU)
	}
	if c.TLDDNSZone != "" {
		writeTOMLStringField(&encoded, "tld_dns_zone", c.TLDDNSZone)
	}
	return encoded.String(), nil
}

func requiredFlags() []requiredFlag {
	return []requiredFlag{
		{key: "no_tun", value: "true"},
		{key: "bind_device", value: "false"},
	}
}

// ApplyRequiredFlags forces no_tun and bind_device=false.
func ApplyRequiredFlags(configTOML string) string {
	flags := requiredFlags()
	required := make(map[string]string, len(flags))
	for _, flag := range flags {
		required[flag.key] = flag.value
	}
	lines := strings.Split(configTOML, "\n")
	var out []string
	section := ""
	seen := map[string]bool{}
	wroteFlags := false
	flushFlags := func() {
		if section != "flags" {
			return
		}
		for _, flag := range flags {
			if !seen[flag.key] {
				out = append(out, flag.key+" = "+flag.value)
			}
		}
		wroteFlags = true
		seen = map[string]bool{}
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if isTOMLSection(trimmed) {
			flushFlags()
			section = sectionName(trimmed)
			out = append(out, line)
			continue
		}
		if section == "flags" {
			key := flagKey(trimmed)
			if value, ok := required[key]; ok {
				out = append(out, key+" = "+value)
				seen[key] = true
				continue
			}
		}
		out = append(out, line)
	}
	flushFlags()
	if !wroteFlags {
		if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
			out = append(out, "")
		}
		out = append(out, "[flags]")
		for _, flag := range flags {
			out = append(out, flag.key+" = "+flag.value)
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n")) + "\n"
}

func isTOMLSection(trimmed string) bool {
	if !strings.HasPrefix(trimmed, "[") {
		return false
	}
	end := strings.IndexByte(trimmed, ']')
	if end < 0 {
		return false
	}
	rest := strings.TrimSpace(trimmed[end+1:])
	return rest == "" || strings.HasPrefix(rest, "#")
}

func sectionName(trimmed string) string {
	end := strings.IndexByte(trimmed, ']')
	if end < 0 {
		return ""
	}
	name := strings.TrimSpace(trimmed[:end+1])
	name = strings.TrimPrefix(name, "[[")
	name = strings.TrimPrefix(name, "[")
	name = strings.TrimSuffix(name, "]]")
	name = strings.TrimSuffix(name, "]")
	return strings.Trim(name, `"'`)
}

func flagKey(trimmed string) string {
	if idx := strings.IndexByte(trimmed, '='); idx >= 0 {
		return strings.Trim(strings.TrimSpace(trimmed[:idx]), `"'`)
	}
	return ""
}

func writeOptionalBoolField(encoded *strings.Builder, name string, value *bool) {
	if value != nil {
		writeTOMLBoolField(encoded, name, *value)
	}
}

func writeTOMLStringField(encoded *strings.Builder, name string, value string) {
	fmt.Fprintf(encoded, "%s = %s\n", name, quoteTOMLString(value))
}

func writeTOMLBoolField(encoded *strings.Builder, name string, value bool) {
	fmt.Fprintf(encoded, "%s = %t\n", name, value)
}

func writeTOMLStringArrayField(encoded *strings.Builder, name string, values []string) {
	fmt.Fprintf(encoded, "%s = [", name)
	for index, value := range values {
		if index != 0 {
			encoded.WriteString(", ")
		}
		encoded.WriteString(quoteTOMLString(value))
	}
	encoded.WriteString("]\n")
}

func quoteTOMLString(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic("encoding a validated Go string as JSON cannot fail")
	}
	return string(encoded)
}
