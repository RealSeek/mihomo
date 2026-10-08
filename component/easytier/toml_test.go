package easytier

import (
	"strings"
	"testing"
)

func boolPtr(v bool) *bool { return &v }

func peers(uris ...string) []string { return uris }

func TestRenderTOMLDefaultNoListenerRequiresPeers(t *testing.T) {
	_, err := Config{NetworkName: "example"}.RenderTOML()
	if err == nil {
		t.Fatal("expected missing peers to fail")
	}
}

func TestRenderTOMLNoListenerFalseDefaultListener(t *testing.T) {
	toml, err := Config{
		NetworkName: "example",
		NoListener:  boolPtr(false),
	}.RenderTOML()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(toml, `listeners = ["tcp://0.0.0.0:11010"]`) {
		t.Fatalf("missing default listener:\n%s", toml)
	}
	if !strings.Contains(toml, "no_tun = true") || !strings.Contains(toml, "bind_device = false") {
		t.Fatalf("missing required flags:\n%s", toml)
	}
}

func TestRenderTOMLExplicitEmptyListenersWithPeers(t *testing.T) {
	toml, err := Config{
		NetworkName:   "example",
		NetworkSecret: "secret",
		Peers:         peers("tcp://192.0.2.10:11010"),
		Hostname:      "node-a",
		IPv4:          "10.144.0.1/24",
	}.RenderTOML()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(toml, "listeners = []") {
		t.Fatalf("expected empty listeners:\n%s", toml)
	}
	if !strings.Contains(toml, "[[peer]]") || !strings.Contains(toml, `uri = "tcp://192.0.2.10:11010"`) {
		t.Fatalf("missing peer:\n%s", toml)
	}
}

func TestApplyRequiredFlagsInjectsNoTun(t *testing.T) {
	got := ApplyRequiredFlags("[network_identity]\nnetwork_name = \"n\"\n")
	if !strings.Contains(got, "[flags]") || !strings.Contains(got, "no_tun = true") {
		t.Fatalf("did not inject no_tun:\n%s", got)
	}
	if !strings.Contains(got, "bind_device = false") {
		t.Fatalf("missing bind_device:\n%s", got)
	}
}

func TestApplyRequiredFlagsReplacesNoTun(t *testing.T) {
	got := ApplyRequiredFlags("[flags]\nno_tun = false\nmtu = 1200\n")
	if !strings.Contains(got, "no_tun = true") {
		t.Fatalf("did not replace no_tun:\n%s", got)
	}
	if !strings.Contains(got, "bind_device = false") {
		t.Fatalf("missing bind_device:\n%s", got)
	}
	if !strings.Contains(got, "mtu = 1200") {
		t.Fatalf("lost existing flag:\n%s", got)
	}
}

func TestNoListenerConflict(t *testing.T) {
	err := Config{
		NetworkName: "example",
		NoListener:  boolPtr(true),
		Listeners:   []string{"tcp://0.0.0.0:11010"},
	}.ValidateStructured()
	if err == nil {
		t.Fatal("expected conflict")
	}
}

func TestRenderTOMLManualIPv4WithoutPrefix(t *testing.T) {
	toml, err := Config{
		NetworkName:   "example",
		NetworkSecret: "example",
		IPv4:          "10.144.0.10",
		Peers:         peers("tcp://192.0.2.10:11010"),
	}.RenderTOML()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(toml, `ipv4 = "10.144.0.10"`) {
		t.Fatalf("missing manual ipv4:\n%s", toml)
	}
	if !strings.Contains(toml, `uri = "tcp://192.0.2.10:11010"`) {
		t.Fatalf("missing peer:\n%s", toml)
	}
}

func TestRenderTOMLMultiplePeers(t *testing.T) {
	toml, err := Config{
		NetworkName:   "example",
		NetworkSecret: "secret",
		Peers: peers(
			"tcp://192.0.2.10:11010",
			"udp://192.0.2.11:11010",
		),
	}.RenderTOML()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(toml, "[[peer]]") != 2 {
		t.Fatalf("want 2 peer tables:\n%s", toml)
	}
	if !strings.Contains(toml, `uri = "tcp://192.0.2.10:11010"`) || !strings.Contains(toml, `uri = "udp://192.0.2.11:11010"`) {
		t.Fatalf("missing peer uris:\n%s", toml)
	}
}

func TestApplyRequiredFlagsSectionComment(t *testing.T) {
	got := ApplyRequiredFlags("[flags] # tun flags\nno_tun = false\nmtu = 1200\n")
	if strings.Count(got, "[flags]") != 1 {
		t.Fatalf("duplicate flags table:\n%s", got)
	}
	if !strings.Contains(got, "no_tun = true") || !strings.Contains(got, "mtu = 1200") {
		t.Fatalf("did not rewrite flags:\n%s", got)
	}
}

func TestApplyRequiredFlagsQuotedKey(t *testing.T) {
	got := ApplyRequiredFlags("[flags]\n\"no_tun\" = false\n")
	if strings.Count(got, "no_tun") != 1 {
		t.Fatalf("duplicate no_tun:\n%s", got)
	}
	if !strings.Contains(got, "no_tun = true") {
		t.Fatalf("did not replace quoted no_tun:\n%s", got)
	}
}

func TestRenderTOMLDefaultsDHCPWhenIPv4Omitted(t *testing.T) {
	toml, err := Config{
		NetworkName:   "example",
		NetworkSecret: "secret",
		Peers:         peers("tcp://192.0.2.10:11010"),
	}.RenderTOML()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(toml, "dhcp = true") {
		t.Fatalf("expected default dhcp:\n%s", toml)
	}
	if strings.Contains(toml, "ipv4") {
		t.Fatalf("did not expect static ipv4:\n%s", toml)
	}
}

func TestRenderTOMLStaticIPv4OmitsDHCP(t *testing.T) {
	toml, err := Config{
		NetworkName:   "example",
		NetworkSecret: "secret",
		IPv4:          "10.144.0.10",
		Peers:         peers("tcp://192.0.2.10:11010"),
	}.RenderTOML()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(toml, "dhcp = true") {
		t.Fatalf("did not expect dhcp with static ipv4:\n%s", toml)
	}
}

func TestRenderTOMLWritesManualRoutes(t *testing.T) {
	routes := []string{"192.0.2.0/24", "198.51.100.7/32"}
	toml, err := Config{
		NetworkName: "example",
		Peers:       peers("tcp://192.0.2.10:11010"),
		Routes:      &routes,
	}.RenderTOML()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(toml, `routes = ["192.0.2.0/24", "198.51.100.7/32"]`) {
		t.Fatalf("missing manual routes:\n%s", toml)
	}
}

func TestRenderTOMLWritesTLDDNSZone(t *testing.T) {
	toml, err := Config{
		NetworkName:   "example",
		NetworkSecret: "secret",
		Peers:         peers("tcp://192.0.2.10:11010"),
		TLDDNSZone:    "overlay.example.",
	}.RenderTOML()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(toml, `tld_dns_zone = "overlay.example."`) {
		t.Fatalf("missing tld_dns_zone:\n%s", toml)
	}
}

func TestRenderTOMLWritesSocks5Proxy(t *testing.T) {
	toml, err := Config{
		NetworkName: "example",
		Peers:       peers("tcp://192.0.2.10:11010"),
		Socks5Proxy: "socks5://127.0.0.1:1080",
	}.RenderTOML()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(toml, `socks5_proxy = "socks5://127.0.0.1:1080"`) {
		t.Fatalf("missing socks5 proxy:\n%s", toml)
	}
}

func TestValidateStructuredSocks5Proxy(t *testing.T) {
	base := Config{
		NetworkName: "example",
		Peers:       peers("tcp://192.0.2.10:11010"),
	}
	for _, value := range []string{
		"http://127.0.0.1:1080",
		"socks5://127.0.0.1",
		"socks5://proxy.example.test:1080",
		"socks5://127.0.0.1:1080/path",
		"socks5://127.0.0.1:0",
		"socks5://[::1]:1080",
	} {
		base.Socks5Proxy = value
		if err := base.ValidateStructured(); err == nil {
			t.Errorf("expected invalid socks5-proxy %q to fail", value)
		}
	}

	base.Socks5Proxy = "socks5://127.0.0.1:1080"
	if err := base.ValidateStructured(); err != nil {
		t.Fatalf("valid IPv4 socks5-proxy rejected: %v", err)
	}
}

func TestRenderTOMLWritesOfficialTransportAndRelayFlags(t *testing.T) {
	foreignLimit := uint64(1234)
	recvLimit := uint64(5678)
	mark := uint32(42)
	toml, err := Config{
		NetworkName:                   "example",
		NetworkSecret:                 "secret",
		Peers:                         peers("tcp://192.0.2.10:11010"),
		IPv6PublicAddrProvider:        boolPtr(true),
		IPv6PublicAddrAuto:            boolPtr(false),
		IPv6PublicAddrPrefix:          "2001:db8:1::/48",
		TCPSTUNServers:                []string{"stun.example.test:3478"},
		TCPWhitelist:                  []string{"80", "443"},
		UDPWhitelist:                  []string{"53"},
		DefaultProtocol:               "udp",
		DevName:                       "mihomo-et",
		EnableIPv6:                    boolPtr(false),
		ProxyForwardBySystem:          boolPtr(true),
		RelayNetworkWhitelist:         "corp,prod",
		P2POnly:                       boolPtr(true),
		RelayAllPeerRPC:               boolPtr(true),
		DisableRelayKCP:               boolPtr(true),
		EnableRelayForeignNetworkKCP:  boolPtr(true),
		DisableRelayQUIC:              boolPtr(true),
		EnableRelayForeignNetworkQUIC: boolPtr(true),
		ForeignRelayBPSLimit:          &foreignLimit,
		InstanceRecvBPSLimit:          &recvLimit,
		MultiThread:                   boolPtr(false),
		MultiThreadCount:              4,
		DataCompressAlgo:              "Zstd",
		DisableUPnP:                   boolPtr(true),
		DisableRelayData:              boolPtr(true),
		PreferPeerRelay:               boolPtr(true),
		EnableUDPBroadcastRelay:       boolPtr(true),
		QUICListenPort:                23456,
		SocketMark:                    &mark,
	}.RenderTOML()
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`ipv6_public_addr_provider = true`,
		`ipv6_public_addr_auto = false`,
		`ipv6_public_addr_prefix = "2001:db8:1::/48"`,
		`tcp_stun_servers = ["stun.example.test:3478"]`,
		`tcp_whitelist = ["80", "443"]`,
		`udp_whitelist = ["53"]`,
		`default_protocol = "udp"`,
		`enable_ipv6 = false`,
		`relay_network_whitelist = "corp,prod"`,
		`p2p_only = true`,
		`disable_relay_kcp = true`,
		`enable_relay_foreign_network_quic = true`,
		`foreign_relay_bps_limit = "1234"`,
		`instance_recv_bps_limit = "5678"`,
		`data_compress_algo = "Zstd"`,
		`disable_relay_data = true`,
		`quic_listen_port = 23456`,
		`socket_mark = 42`,
	} {
		if !strings.Contains(toml, expected) {
			t.Fatalf("missing %q:\n%s", expected, toml)
		}
	}
}

func TestRenderTOMLSecureMode(t *testing.T) {
	toml, err := Config{
		NetworkName:     "example",
		NetworkSecret:   "secret",
		Peers:           peers("tcp://192.0.2.10:11010"),
		SecureMode:      boolPtr(true),
		LocalPrivateKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
		LocalPublicKey:  "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB=",
	}.RenderTOML()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(toml, "[secure_mode]") || !strings.Contains(toml, "enabled = true") {
		t.Fatalf("missing secure_mode:\n%s", toml)
	}
	if !strings.Contains(toml, `local_private_key = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="`) {
		t.Fatalf("missing local_private_key:\n%s", toml)
	}
	if !strings.Contains(toml, `local_public_key = "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB="`) {
		t.Fatalf("missing local_public_key:\n%s", toml)
	}
}

func TestRenderTOMLPeerPublicKeyEnablesSecureMode(t *testing.T) {
	toml, err := Config{
		NetworkName:   "example",
		NetworkSecret: "secret",
		Peers:         peers("tcp://relay.example.com:11010?peer-public-key=CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC="),
	}.RenderTOML()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(toml, "[secure_mode]") || !strings.Contains(toml, "enabled = true") {
		t.Fatalf("pinning should enable secure_mode:\n%s", toml)
	}
	if !strings.Contains(toml, `uri = "tcp://relay.example.com:11010"`) {
		t.Fatalf("missing peer uri:\n%s", toml)
	}
	if strings.Contains(toml, "peer-public-key=") {
		t.Fatalf("peer-public-key should be stripped from uri:\n%s", toml)
	}
	if !strings.Contains(toml, `peer_public_key = "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC="`) {
		t.Fatalf("missing peer_public_key:\n%s", toml)
	}
}

func TestRenderTOMLSecureModeFalseRejectsPinnedPeer(t *testing.T) {
	err := Config{
		NetworkName:   "example",
		NetworkSecret: "secret",
		SecureMode:    boolPtr(false),
		Peers:         peers("tcp://relay.example.com:11010?peer-public-key=CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC="),
	}.ValidateStructured()
	if err == nil {
		t.Fatal("expected pinned peer without secure-mode to fail")
	}
}

func TestRenderTOMLLocalPublicKeyRequiresPrivateKey(t *testing.T) {
	err := Config{
		NetworkName:    "example",
		NetworkSecret:  "secret",
		Peers:          peers("tcp://192.0.2.10:11010"),
		LocalPublicKey: "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB=",
	}.ValidateStructured()
	if err == nil {
		t.Fatal("expected local-public-key without private key to fail")
	}
}

func TestRenderTOMLEmptyPeerURI(t *testing.T) {
	err := Config{
		NetworkName:   "example",
		NetworkSecret: "secret",
		Peers:         []string{""},
	}.ValidateStructured()
	if err == nil {
		t.Fatal("expected empty peer uri to fail")
	}
}

func TestParsePeerURIQuery(t *testing.T) {
	peer, err := parsePeerURI(" tcp://relay.example.com:11010?foo=1&peer-public-key=CC%2BCC/CC=&bar=2 ")
	if err != nil {
		t.Fatal(err)
	}
	if peer.URI != "tcp://relay.example.com:11010?foo=1&bar=2" {
		t.Fatalf("uri: %q", peer.URI)
	}
	if peer.PeerPublicKey != "CC+CC/CC=" {
		t.Fatalf("key: %q", peer.PeerPublicKey)
	}

	peer, err = parsePeerURI("tcp://relay.example.com:11010?peer_public_key=CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC=")
	if err != nil {
		t.Fatal(err)
	}
	if peer.URI != "tcp://relay.example.com:11010" || peer.PeerPublicKey == "" {
		t.Fatalf("snake_case: %+v", peer)
	}

	peer, err = parsePeerURI("tcp://192.0.2.10:11010?foo=1")
	if err != nil {
		t.Fatal(err)
	}
	if peer.URI != "tcp://192.0.2.10:11010?foo=1" || peer.PeerPublicKey != "" {
		t.Fatalf("other query: %+v", peer)
	}
}
