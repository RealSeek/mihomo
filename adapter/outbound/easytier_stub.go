//go:build no_easytier

package outbound

import (
	"fmt"

	"github.com/metacubex/mihomo/component/easytier"
)

type EasyTier struct {
	*Base
}

type EasyTierOption struct {
	BasicOption
	PacketMode                    bool                         `proxy:"packet-mode,omitempty"`
	ConfigTOML                    string                       `proxy:"config-toml,omitempty"`
	WebClient                     *EasyTierWebClientOption     `proxy:"web-client,omitempty"`
	Name                          string                       `proxy:"name"`
	NetworkName                   string                       `proxy:"network-name,omitempty"`
	NetworkSecret                 string                       `proxy:"network-secret,omitempty"`
	Hostname                      string                       `proxy:"hostname,omitempty"`
	IPv4                          string                       `proxy:"ipv4,omitempty"`
	IPv6                          string                       `proxy:"ipv6,omitempty"`
	IPv6PublicAddrProvider        *bool                        `proxy:"ipv6-public-addr-provider,omitempty"`
	IPv6PublicAddrAuto            *bool                        `proxy:"ipv6-public-addr-auto,omitempty"`
	IPv6PublicAddrPrefix          string                       `proxy:"ipv6-public-addr-prefix,omitempty"`
	DHCP                          bool                         `proxy:"dhcp,omitempty"`
	Peers                         []string                     `proxy:"peers,omitempty"`
	Listeners                     []string                     `proxy:"listeners,omitempty"`
	NoListener                    *bool                        `proxy:"no-listener,omitempty"`
	MappedListeners               []string                     `proxy:"mapped-listeners,omitempty"`
	ExitNodes                     []string                     `proxy:"exit-nodes,omitempty"`
	Routes                        []string                     `proxy:"routes,omitempty"`
	ProxyNetworks                 []string                     `proxy:"proxy-networks,omitempty"`
	Socks5Proxy                   string                       `proxy:"socks5-proxy,omitempty"`
	PortForwards                  []easytier.PortForwardOption `proxy:"port-forwards,omitempty"`
	InstanceName                  string                       `proxy:"instance-name,omitempty"`
	StateDir                      string                       `proxy:"state-dir,omitempty"`
	UDP                           bool                         `proxy:"udp,omitempty"`
	AcceptDNS                     *bool                        `proxy:"accept-dns,omitempty"`
	EnableExitNode                *bool                        `proxy:"enable-exit-node,omitempty"`
	EnableEncryption              *bool                        `proxy:"enable-encryption,omitempty"`
	EncryptionAlgorithm           string                       `proxy:"encryption-algorithm,omitempty"`
	PrivateMode                   *bool                        `proxy:"private-mode,omitempty"`
	LatencyFirst                  *bool                        `proxy:"latency-first,omitempty"`
	DisableP2P                    *bool                        `proxy:"disable-p2p,omitempty"`
	EnableKCPProxy                *bool                        `proxy:"enable-kcp-proxy,omitempty"`
	DisableKCPInput               *bool                        `proxy:"disable-kcp-input,omitempty"`
	EnableQUICProxy               *bool                        `proxy:"enable-quic-proxy,omitempty"`
	DisableQUICInput              *bool                        `proxy:"disable-quic-input,omitempty"`
	MTU                           int                          `proxy:"mtu,omitempty"`
	TLDDNSZone                    string                       `proxy:"tld-dns-zone,omitempty"`
	SecureMode                    *bool                        `proxy:"secure-mode,omitempty"`
	LocalPrivateKey               string                       `proxy:"local-private-key,omitempty"`
	LocalPublicKey                string                       `proxy:"local-public-key,omitempty"`
	STUNServers                   []string                     `proxy:"stun-servers,omitempty"`
	TCPSTUNServers                []string                     `proxy:"tcp-stun-servers,omitempty"`
	STUNServersV6                 []string                     `proxy:"stun-servers-v6,omitempty"`
	TCPWhitelist                  []string                     `proxy:"tcp-whitelist,omitempty"`
	UDPWhitelist                  []string                     `proxy:"udp-whitelist,omitempty"`
	DefaultProtocol               string                       `proxy:"default-protocol,omitempty"`
	DevName                       string                       `proxy:"dev-name,omitempty"`
	EnableIPv6                    *bool                        `proxy:"enable-ipv6,omitempty"`
	ProxyForwardBySystem          *bool                        `proxy:"proxy-forward-by-system,omitempty"`
	RelayNetworkWhitelist         string                       `proxy:"relay-network-whitelist,omitempty"`
	P2POnly                       *bool                        `proxy:"p2p-only,omitempty"`
	RelayAllPeerRPC               *bool                        `proxy:"relay-all-peer-rpc,omitempty"`
	DisableRelayKCP               *bool                        `proxy:"disable-relay-kcp,omitempty"`
	EnableRelayForeignNetworkKCP  *bool                        `proxy:"enable-relay-foreign-network-kcp,omitempty"`
	DisableRelayQUIC              *bool                        `proxy:"disable-relay-quic,omitempty"`
	EnableRelayForeignNetworkQUIC *bool                        `proxy:"enable-relay-foreign-network-quic,omitempty"`
	ForeignRelayBPSLimit          *uint64                      `proxy:"foreign-relay-bps-limit,omitempty"`
	InstanceRecvBPSLimit          *uint64                      `proxy:"instance-recv-bps-limit,omitempty"`
	MultiThread                   *bool                        `proxy:"multi-thread,omitempty"`
	MultiThreadCount              int                          `proxy:"multi-thread-count,omitempty"`
	DataCompressAlgo              string                       `proxy:"data-compress-algo,omitempty"`
	DisableUPnP                   *bool                        `proxy:"disable-upnp,omitempty"`
	DisableRelayData              *bool                        `proxy:"disable-relay-data,omitempty"`
	PreferPeerRelay               *bool                        `proxy:"prefer-peer-relay,omitempty"`
	EnableUDPBroadcastRelay       *bool                        `proxy:"enable-udp-broadcast-relay,omitempty"`
	QUICListenPort                int                          `proxy:"quic-listen-port,omitempty"`
	SocketMark                    *uint32                      `proxy:"socket-mark,omitempty"`
	NeedP2P                       *bool                        `proxy:"need-p2p,omitempty"`
	LazyP2P                       *bool                        `proxy:"lazy-p2p,omitempty"`
	DisableTCPHolePunch           *bool                        `proxy:"disable-tcp-hole-punch,omitempty"`
	DisableUDPHolePunch           *bool                        `proxy:"disable-udp-hole-punch,omitempty"`
	DisableSymHolePunch           *bool                        `proxy:"disable-sym-hole-punch,omitempty"`
}

func NewEasyTier(EasyTierOption) (*EasyTier, error) {
	return nil, fmt.Errorf("EasyTier support is disabled by \"no_easytier\" build tag")
}
