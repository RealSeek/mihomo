package sing_tun

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"

	et "github.com/metacubex/mihomo/component/easytier"
	C "github.com/metacubex/mihomo/constant"
	LC "github.com/metacubex/mihomo/listener/config"
)

type easyTierPolicyAdapter struct {
	C.ProxyAdapter
	networks []et.PacketNetwork
	policy   et.PacketConfigPolicy
}

func (a *easyTierPolicyAdapter) OpenPacketSessions(context.Context) ([]et.PacketSession, error) {
	return nil, errors.New("candidate admission must not query the live packet plane")
}

func (a *easyTierPolicyAdapter) PacketNetworks() ([]et.PacketNetwork, error) {
	return a.networks, nil
}

func (a *easyTierPolicyAdapter) SetPacketConfigPolicy(policy et.PacketConfigPolicy) {
	a.policy = policy
}

func TestEasyTierCandidateChecksOtherOwners(t *testing.T) {
	owner := &easyTierPolicyAdapter{}
	other := &easyTierPolicyAdapter{networks: []et.PacketNetwork{{
		ID: "existing", Config: et.Config{IPv4: "10.144.0.1", IPv6: "fd00:144::1/64", TLDDNSZone: "Mesh.Net."},
	}}}
	listener := &Listener{
		options:          LC.Tun{Inet4Address: []netip.Prefix{netip.MustParsePrefix("198.18.0.1/16")}},
		easyTierMTU:      1380,
		easyTierAdapters: map[string]C.ProxyAdapter{"owner": owner, "other": other},
	}
	listener.setEasyTierPacketPolicies()
	for _, test := range []struct {
		name      string
		candidate et.Config
		want      string
	}{
		{"shared-zone", et.Config{IPv4: "10.145.0.1/24", TLDDNSZone: "mesh.net"}, ""},
		{"ipv4", et.Config{IPv4: "10.144.0.2", TLDDNSZone: "other.net"}, "overlaps other/existing"},
		{"ipv6", et.Config{IPv6: "fd00:144::2", TLDDNSZone: "other.net"}, "overlaps other/existing"},
		{"tun-address", et.Config{IPv4: "198.18.0.4/24", TLDDNSZone: "other.net"}, "mihomo TUN address"},
		{"mtu", et.Config{IPv4: "10.145.0.1/24", TLDDNSZone: "other.net", MTU: 1280}, "TUN MTU"},
		{"distinct", et.Config{IPv4: "10.145.0.1/24", IPv6: "fd00:145::1", TLDDNSZone: "other.net"}, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := owner.policy(context.Background(), "candidate", test.candidate)
			if test.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("candidate error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestListenerCloseWithoutEasyTier(t *testing.T) {
	listener := &Listener{easyTierAdapters: map[string]C.ProxyAdapter{"missing": nil}}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
}
