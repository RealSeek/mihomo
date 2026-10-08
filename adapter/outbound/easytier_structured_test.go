//go:build !no_easytier

package outbound

import (
	"strings"
	"testing"
)

func easyTierBoolPtr(value bool) *bool { return &value }

func TestEasyTierStructuredOfficialFlagsReachCoreConfig(t *testing.T) {
	foreignLimit := uint64(1234)
	mark := uint32(7)
	adapter, err := NewEasyTier(EasyTierOption{
		Name:                 "structured-official-flags",
		PacketMode:           true,
		NetworkName:          "structured-official-flags",
		Peers:                []string{"tcp://127.0.0.1:11010"},
		TCPSTUNServers:       []string{"stun.example.test:3478"},
		TCPWhitelist:         []string{"80", "443"},
		UDPWhitelist:         []string{"53"},
		DefaultProtocol:      "udp",
		EnableIPv6:           easyTierBoolPtr(false),
		P2POnly:              easyTierBoolPtr(true),
		DisableRelayData:     easyTierBoolPtr(true),
		ForeignRelayBPSLimit: &foreignLimit,
		SocketMark:           &mark,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Close() })
	for _, expected := range []string{
		`tcp_stun_servers = ["stun.example.test:3478"]`,
		`tcp_whitelist = ["80", "443"]`,
		`udp_whitelist = ["53"]`,
		`default_protocol = "udp"`,
		`enable_ipv6 = false`,
		`p2p_only = true`,
		`disable_relay_data = true`,
		`foreign_relay_bps_limit = "1234"`,
		`socket_mark = 7`,
	} {
		if !strings.Contains(adapter.configTOML, expected) {
			t.Fatalf("structured option missing %q:\n%s", expected, adapter.configTOML)
		}
	}
}

func TestEasyTierStructuredRoutesReachCoreConfig(t *testing.T) {
	adapter, err := NewEasyTier(EasyTierOption{
		Name:        "structured-routes",
		PacketMode:  true,
		NetworkName: "structured-routes",
		Peers:       []string{"tcp://127.0.0.1:11010"},
		Routes:      []string{"192.0.2.0/24", "198.51.100.7/32"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Close() })
	if !strings.Contains(adapter.configTOML, `routes = ["192.0.2.0/24", "198.51.100.7/32"]`) {
		t.Fatalf("structured routes missing from core TOML:\n%s", adapter.configTOML)
	}
}

func TestEasyTierWebClientRejectsLocalRoutes(t *testing.T) {
	_, err := NewEasyTier(EasyTierOption{
		Name:       "managed-routes",
		PacketMode: true,
		Routes:     []string{"192.0.2.0/24"},
		WebClient:  &EasyTierWebClientOption{Endpoint: "tcp://127.0.0.1:11010"},
	})
	if err == nil || !strings.Contains(err.Error(), "routes") {
		t.Fatalf("expected web-client routes rejection, got %v", err)
	}
}
