//go:build !no_easytier

package outbound

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/metacubex/mihomo/component/easytier"
	"github.com/metacubex/mihomo/component/resolver"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/dns"
	"github.com/metacubex/mihomo/log"

	corehost "github.com/easytier/easytier/easytier-go"
	D "github.com/miekg/dns"
)

const (
	easyTierDefaultStateDir = "easytier"
	easyTierInstanceIDFile  = "instance_id"
	easyTierDNSTTL          = 60
	easyTierMinBackoff      = time.Second
	easyTierMaxBackoff      = 30 * time.Second
)

var (
	errEasyTierClosed           = errors.New("easytier outbound closed")
	errEasyTierAddressesPending = errors.New("easytier overlay addresses are not ready")
)

type EasyTier struct {
	*Base
	option             EasyTierOption
	configTOML         string
	configMetadata     easytier.Config
	ipv6Address        netip.Prefix
	stateDir           string
	instanceID         string
	zone               string
	ctx                context.Context
	cancel             context.CancelFunc
	loopOnce           sync.Once
	loopDone           chan struct{}
	startMu            sync.Mutex
	closed             bool
	readyCh            chan struct{}
	mu                 sync.Mutex
	host               *corehost.Host
	instance           *corehost.Instance
	webClient          *corehost.WebClient
	packetConfigPolicy easytier.PacketConfigPolicy
	unregister         func()
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

func (o EasyTierOption) structuredConfig() easytier.Config {
	instanceName := o.InstanceName
	if instanceName == "" {
		instanceName = o.Name
	}
	var routes *[]string
	if o.Routes != nil {
		values := slices.Clone(o.Routes)
		routes = &values
	}
	return easytier.Config{
		PacketMode:             o.PacketMode,
		NetworkName:            o.NetworkName,
		NetworkSecret:          o.NetworkSecret,
		Hostname:               o.Hostname,
		IPv4:                   o.IPv4,
		IPv6:                   o.IPv6,
		IPv6PublicAddrProvider: o.IPv6PublicAddrProvider,
		IPv6PublicAddrAuto:     o.IPv6PublicAddrAuto,
		IPv6PublicAddrPrefix:   o.IPv6PublicAddrPrefix,
		DHCP:                   o.DHCP,
		Peers:                  o.Peers,
		Listeners:              o.Listeners,
		NoListener:             o.NoListener,
		MappedListeners:        o.MappedListeners,
		ExitNodes:              o.ExitNodes,
		Routes:                 routes,
		ProxyNetworks:          o.ProxyNetworks,
		Socks5Proxy:            o.Socks5Proxy,
		PortForwards:           o.PortForwards,
		InstanceName:           instanceName,
		AcceptDNS:              o.AcceptDNS,
		EnableExitNode:         o.EnableExitNode,
		EnableEncryption:       o.EnableEncryption,
		EncryptionAlgorithm:    o.EncryptionAlgorithm,
		PrivateMode:            o.PrivateMode,
		LatencyFirst:           o.LatencyFirst,
		DisableP2P:             o.DisableP2P,
		EnableKCPProxy:         o.EnableKCPProxy,
		DisableKCPInput:        o.DisableKCPInput,
		EnableQUICProxy:        o.EnableQUICProxy,
		DisableQUICInput:       o.DisableQUICInput,
		MTU:                    o.MTU,
		TLDDNSZone:             o.TLDDNSZone,
		SecureMode:             o.SecureMode,
		LocalPrivateKey:        o.LocalPrivateKey,
		LocalPublicKey:         o.LocalPublicKey,
		STUNServers:            o.STUNServers, TCPSTUNServers: o.TCPSTUNServers, STUNServersV6: o.STUNServersV6,
		TCPWhitelist: o.TCPWhitelist, UDPWhitelist: o.UDPWhitelist,
		DefaultProtocol: o.DefaultProtocol, DevName: o.DevName, EnableIPv6: o.EnableIPv6,
		ProxyForwardBySystem: o.ProxyForwardBySystem, RelayNetworkWhitelist: o.RelayNetworkWhitelist,
		P2POnly: o.P2POnly, RelayAllPeerRPC: o.RelayAllPeerRPC, DisableRelayKCP: o.DisableRelayKCP,
		EnableRelayForeignNetworkKCP: o.EnableRelayForeignNetworkKCP, DisableRelayQUIC: o.DisableRelayQUIC,
		EnableRelayForeignNetworkQUIC: o.EnableRelayForeignNetworkQUIC, ForeignRelayBPSLimit: o.ForeignRelayBPSLimit,
		InstanceRecvBPSLimit: o.InstanceRecvBPSLimit, MultiThread: o.MultiThread, MultiThreadCount: o.MultiThreadCount,
		DataCompressAlgo: o.DataCompressAlgo, DisableUPnP: o.DisableUPnP, DisableRelayData: o.DisableRelayData,
		PreferPeerRelay: o.PreferPeerRelay, EnableUDPBroadcastRelay: o.EnableUDPBroadcastRelay,
		QUICListenPort: o.QUICListenPort, SocketMark: o.SocketMark,
		NeedP2P: o.NeedP2P, LazyP2P: o.LazyP2P,
		DisableTCPHolePunch: o.DisableTCPHolePunch, DisableUDPHolePunch: o.DisableUDPHolePunch,
		DisableSymHolePunch: o.DisableSymHolePunch,
	}
}

func NewEasyTier(option EasyTierOption) (*EasyTier, error) {
	var configTOML string
	var configMetadata easytier.Config
	var err error
	if option.WebClient != nil {
		if err := validateEasyTierWebClient(option); err != nil {
			return nil, err
		}
	} else if option.ConfigTOML != "" {
		configTOML, configMetadata, err = easytier.PrepareNativeConfig(option.ConfigTOML, option.PacketMode)
		option.IPv4, option.IPv6, option.DHCP = configMetadata.IPv4, configMetadata.IPv6, configMetadata.DHCP
		option.Hostname, option.NetworkName = configMetadata.Hostname, configMetadata.NetworkName
		option.TLDDNSZone, option.MTU = configMetadata.TLDDNSZone, configMetadata.MTU
		option.ExitNodes = configMetadata.ExitNodes
	} else {
		configMetadata = option.structuredConfig()
		configTOML, err = configMetadata.RenderTOML()
	}
	if err != nil {
		return nil, err
	}
	var ipv6Address netip.Prefix
	if option.IPv6 != "" {
		ipv6Address, err = easytier.ParseIPv6Prefix(option.IPv6)
		if err != nil {
			return nil, fmt.Errorf("easytier %q: overlay IPv6: %w", option.Name, err)
		}
	}

	stateDir := option.StateDir
	if stateDir == "" {
		stateDir = filepath.Join(easyTierDefaultStateDir, option.Name)
	}
	stateDir = C.Path.Resolve(stateDir)
	if !C.Path.IsSafePath(stateDir) {
		return nil, C.Path.ErrNotSafePath(stateDir)
	}

	addr := option.NetworkName
	if addr == "" {
		addr = "easytier"
	}
	ctx, cancel := context.WithCancel(context.Background())
	outbound := &EasyTier{
		Base: NewBase(BaseOption{
			Name:         option.Name,
			Addr:         addr,
			Type:         C.EasyTier,
			ProviderName: option.ProviderName,
			UDP:          option.UDP,
			Interface:    option.Interface,
			RoutingMark:  option.RoutingMark,
			Prefer:       option.IPVersion,
		}),
		option:         option,
		configTOML:     configTOML,
		configMetadata: configMetadata,
		ipv6Address:    ipv6Address,
		stateDir:       stateDir,
		zone:           easytier.NormalizeZone(option.TLDDNSZone),
		ctx:            ctx,
		cancel:         cancel,
		loopDone:       make(chan struct{}),
	}
	outbound.dialer = option.NewDialer(outbound.DialOptions())
	outbound.unregister = dns.RegisterEasyTierDnsClient(option.Name, easyTierDNSTransport{easytier: outbound})
	return outbound, nil
}

func (e *EasyTier) ensureStarted(ctx context.Context) error {
	e.loopOnce.Do(func() {
		go e.loop()
	})
	for {
		if err := e.ctx.Err(); err != nil {
			return errEasyTierClosed
		}
		e.startMu.Lock()
		closed := e.closed
		if e.readyCh == nil && !closed {
			e.readyCh = make(chan struct{})
		}
		readyCh := e.readyCh
		e.startMu.Unlock()
		if closed {
			return errEasyTierClosed
		}
		e.mu.Lock()
		instance, host, webClient := e.instance, e.host, e.webClient
		e.mu.Unlock()
		if e.option.WebClient != nil && host != nil && webClient != nil {
			return nil
		}
		if instance != nil && instance.State() == corehost.StateRunning {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-e.ctx.Done():
			return errEasyTierClosed
		case <-readyCh:
		}
	}
}

func (e *EasyTier) signalReady() {
	e.startMu.Lock()
	if e.readyCh != nil {
		close(e.readyCh)
		e.readyCh = nil
	}
	e.startMu.Unlock()
}

func (e *EasyTier) loop() {
	defer close(e.loopDone)
	defer e.shutdown()
	backoff := easyTierMinBackoff
	for {
		if e.ctx.Err() != nil {
			return
		}
		e.startMu.Lock()
		closed := e.closed
		e.startMu.Unlock()
		if closed {
			return
		}
		err := e.init()
		if err != nil {
			log.Warnln("[EasyTier](%s) start failed: %v; retry in %s", e.Name(), err, backoff)
			_ = e.shutdown()
			timer := time.NewTimer(backoff)
			select {
			case <-e.ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			if backoff < easyTierMaxBackoff {
				backoff *= 2
				if backoff > easyTierMaxBackoff {
					backoff = easyTierMaxBackoff
				}
			}
			continue
		}
		backoff = easyTierMinBackoff
		e.signalReady()
		reason := e.serve()
		_ = e.shutdown()
		if e.ctx.Err() != nil {
			return
		}
		e.startMu.Lock()
		closed = e.closed
		e.startMu.Unlock()
		if closed {
			return
		}
		if reason == "" {
			reason = "instance stopped"
		}
		log.Warnln("[EasyTier](%s) %s; restarting in %s", e.Name(), reason, backoff)
		timer := time.NewTimer(backoff)
		select {
		case <-e.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if backoff < easyTierMaxBackoff {
			backoff *= 2
			if backoff > easyTierMaxBackoff {
				backoff = easyTierMaxBackoff
			}
		}
	}
}

func (e *EasyTier) init() error {
	if err := os.MkdirAll(e.stateDir, 0o755); err != nil {
		return fmt.Errorf("easytier: create state-dir: %w", err)
	}
	if e.option.WebClient != nil {
		return e.initWebClient()
	}
	instanceID := loadInstanceID(e.stateDir)
	instanceName := e.option.InstanceName
	if instanceName == "" {
		instanceName = e.option.Name
	}
	services, err := easytier.Services(e.dialer)
	if err != nil {
		return err
	}
	host, err := corehost.New(e.ctx, corehost.Options{
		Platform: services,
	})
	if err != nil {
		return err
	}
	e.mu.Lock()
	e.host = host
	e.mu.Unlock()

	instance, err := host.CreateInstanceTOML(e.ctx, instanceName, instanceID, e.configTOML)
	if err != nil {
		return err
	}
	e.mu.Lock()
	e.instance = instance
	e.instanceID = instance.ID()
	e.mu.Unlock()
	if err := writeInstanceID(e.stateDir, instance.ID()); err != nil {
		return err
	}
	if err := instance.Start(e.ctx); err != nil {
		return err
	}
	log.Infoln("[EasyTier](%s) instance %s running", e.Name(), instance.ID())
	return nil
}

func (e *EasyTier) currentInstance() (*corehost.Instance, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.option.WebClient != nil {
		return nil, fmt.Errorf("easytier %q: configuration-center networks use the shared TUN packet plane", e.Name())
	}
	if e.instance == nil {
		return nil, errors.New("easytier instance is not ready")
	}
	return e.instance, nil
}

func (e *EasyTier) OpenPacketSession(ctx context.Context) (easytier.PacketSession, error) {
	if !e.option.PacketMode {
		return easytier.PacketSession{}, fmt.Errorf("easytier: instance must be configured for the shared packet plane before startup")
	}
	if e.option.WebClient != nil {
		return easytier.PacketSession{}, fmt.Errorf("easytier %q: configuration center provides multiple packet sessions", e.Name())
	}
	if err := e.ensureStarted(ctx); err != nil {
		return easytier.PacketSession{}, err
	}
	instance, err := e.currentInstance()
	if err != nil {
		return easytier.PacketSession{}, err
	}
	return e.packetSession(ctx, instance, e.configMetadata, e, true)
}

func (e *EasyTier) packetSession(ctx context.Context, instance *corehost.Instance, metadata easytier.Config, endpoint easytier.PacketEndpoint, wait bool) (easytier.PacketSession, error) {
	var ipv6Address netip.Prefix
	if metadata.IPv6 != "" {
		var err error
		ipv6Address, err = easytier.ParseIPv6Prefix(metadata.IPv6)
		if err != nil {
			return easytier.PacketSession{}, fmt.Errorf("easytier %q instance %s: overlay IPv6: %w", e.Name(), instance.ID(), err)
		}
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var addresses, routes []netip.Prefix
	for {
		info, err := instance.ShowNodeInfo(ctx)
		if err != nil {
			return easytier.PacketSession{}, fmt.Errorf("easytier %q instance %s: packet address: %w", e.Name(), instance.ID(), err)
		}
		addresses, routes = nil, nil
		if value := info.GetIpv4Addr(); value != "" {
			address, err := netip.ParsePrefix(value)
			if err != nil || !address.Addr().Is4() {
				return easytier.PacketSession{}, fmt.Errorf("easytier %q: invalid runtime IPv4 CIDR %q", e.Name(), value)
			}
			addresses = append(addresses, address)
		}
		localPublicIPv6, _ := easyTierIPv6Inet(info.GetPublicIpv6Addr())
		coreRoutes, err := instance.ListRoute(ctx)
		if err != nil {
			return easytier.PacketSession{}, fmt.Errorf("easytier %q instance %s: packet routes: %w", e.Name(), instance.ID(), err)
		}
		localIPv6 := ipv6Address
		for _, route := range coreRoutes {
			if ip := route.GetIpv4Addr(); ip != nil && ip.GetAddress() != nil {
				routes = append(routes, netip.PrefixFrom(easytier.IPv4FromUint32(ip.GetAddress().GetAddr()), 32))
			}
			if ip := route.GetIpv6Addr(); ip != nil && ip.GetAddress() != nil {
				address := easytier.IPv6FromParts(ip.GetAddress().GetPart1(), ip.GetAddress().GetPart2(), ip.GetAddress().GetPart3(), ip.GetAddress().GetPart4())
				appendEasyTierPrefixUnique(&routes, netip.PrefixFrom(address, 128))
				if route.GetPeerId() == info.GetPeerId() {
					localIPv6 = netip.PrefixFrom(address, int(ip.GetNetworkLength()))
					if !localIPv6.IsValid() {
						return easytier.PacketSession{}, fmt.Errorf("easytier %q: invalid runtime IPv6 prefix length %d", e.Name(), ip.GetNetworkLength())
					}
				}
			}
			if publicIPv6, ok := easyTierIPv6Inet(route.GetPublicIpv6Addr()); ok {
				appendEasyTierPrefixUnique(&routes, netip.PrefixFrom(publicIPv6.Addr(), 128))
				if route.GetPeerId() == info.GetPeerId() {
					localPublicIPv6 = publicIPv6
				}
			}
			if metadata.Routes == nil {
				for _, cidr := range route.GetProxyCidrs() {
					prefix, err := netip.ParsePrefix(cidr)
					if err != nil {
						return easytier.PacketSession{}, fmt.Errorf("easytier %q instance %s: invalid peer proxy CIDR %q: %w", e.Name(), instance.ID(), cidr, err)
					}
					routes = append(routes, prefix.Masked())
				}
			}
		}
		waitIPv4 := metadata.IPv4 != "" || metadata.DHCP || metadata.IPv6 == "" && !localPublicIPv6.IsValid()
		if localPublicIPv6.IsValid() {
			// The provider lease routes public IPv6 traffic through EasyTier.
			// Keep it first so default routes use this address as their source.
			appendEasyTierPrefixUnique(&addresses, localPublicIPv6)
			appendEasyTierPrefixUnique(&routes, netip.MustParsePrefix("::/0"))
		}
		if localIPv6.IsValid() {
			appendEasyTierPrefixUnique(&addresses, localIPv6)
		}
		if (!waitIPv4 || info.GetIpv4Addr() != "") && (metadata.IPv6 == "" || localIPv6.IsValid()) && len(addresses) > 0 {
			break
		}
		if !wait {
			return easytier.PacketSession{}, fmt.Errorf("easytier %q instance %s: %w", e.Name(), instance.ID(), errEasyTierAddressesPending)
		}
		select {
		case <-ctx.Done():
			return easytier.PacketSession{}, fmt.Errorf("easytier %q: waiting for overlay addresses: %w", e.Name(), ctx.Err())
		case <-ticker.C:
		}
	}
	// A peer can advertise both families while this node only has one overlay
	// address family. Only learn routes with a usable local source address.
	routes = slices.DeleteFunc(routes, func(route netip.Prefix) bool {
		for _, address := range addresses {
			if route.Addr().BitLen() == address.Addr().BitLen() {
				return false
			}
		}
		return true
	})
	subnetRoutes := make([]netip.Prefix, 0, len(addresses)+len(routes))
	for _, address := range addresses {
		subnetRoutes = append(subnetRoutes, address.Masked())
	}
	routes = append(subnetRoutes, routes...)
	if metadata.Routes != nil {
		for _, value := range *metadata.Routes {
			prefix, err := netip.ParsePrefix(value)
			if err != nil || !prefix.Addr().Is4() {
				return easytier.PacketSession{}, fmt.Errorf("easytier %q instance %s: invalid explicit IPv4 route %q", e.Name(), instance.ID(), value)
			}
			routes = append(routes, prefix.Masked())
		}
	}
	for _, value := range metadata.ExitNodes {
		address, err := netip.ParseAddr(value)
		if err != nil {
			return easytier.PacketSession{}, fmt.Errorf("easytier %q: invalid exit-node address %q: %w", e.Name(), value, err)
		}
		routes = append(routes, netip.PrefixFrom(address.Unmap(), 0).Masked())
	}
	mtu := uint32(1380)
	if metadata.MTU > 0 {
		mtu = uint32(metadata.MTU)
	}
	return easytier.PacketSession{ID: instance.ID(), Endpoint: endpoint, Addresses: addresses, Routes: routes, MTU: mtu}, nil
}

func (e *EasyTier) SendPacket(ctx context.Context, packet []byte) error {
	instance, err := e.currentInstance()
	if err != nil {
		return err
	}
	if instance.State() != corehost.StateRunning {
		return fmt.Errorf("easytier: packet plane is restarting")
	}
	return instance.SendPacket(ctx, packet)
}

func (e *EasyTier) ReceivePacket(ctx context.Context) ([]byte, error) {
	for {
		if err := e.ensureStarted(ctx); err != nil {
			return nil, err
		}
		instance, err := e.currentInstance()
		if err == nil {
			packet, receiveErr := instance.ReceivePacket(ctx)
			if receiveErr == nil {
				return packet, nil
			}
			err = receiveErr
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if e.ctx.Err() != nil {
			return nil, errEasyTierClosed
		}
		// The instance owner recreates a stopped guest. Keep the TUN reader
		// attached to the outbound across that replacement.
		log.Warnln("[EasyTier](%s) packet receive interrupted: %v", e.Name(), err)
		timer := time.NewTimer(easyTierMinBackoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-e.ctx.Done():
			timer.Stop()
			return nil, errEasyTierClosed
		case <-timer.C:
		}
	}
}

func (e *EasyTier) serve() string {
	if e.option.WebClient != nil {
		e.mu.Lock()
		client := e.webClient
		e.mu.Unlock()
		if client == nil {
			return "configuration-center client is not ready"
		}
		if err := client.Wait(e.ctx); err != nil && e.ctx.Err() == nil {
			return fmt.Sprintf("configuration-center client stopped: %v", err)
		}
		return ""
	}
	e.mu.Lock()
	instance := e.instance
	e.mu.Unlock()
	if instance == nil {
		return "instance is not ready"
	}
	waitCtx, stopWait := context.WithCancel(e.ctx)
	defer stopWait()
	stopped := make(chan error, 1)
	go func() { stopped <- instance.Wait(waitCtx) }()
	// Manual connectors retry inside core (reconnect_interval, default 1s).
	// Events() is best-effort: a full host queue drops the event and does not
	// stall the guest. Drain it for logs; recreate only when the stream closes.
	events := instance.Events()
	if events == nil {
		if err := instance.Wait(e.ctx); err != nil && e.ctx.Err() == nil {
			return err.Error()
		}
		return "instance stopped"
	}
	for {
		select {
		case <-e.ctx.Done():
			return ""
		case err := <-stopped:
			if err != nil {
				return err.Error()
			}
			return "instance stopped"
		case event, ok := <-events:
			if !ok {
				return "instance stopped"
			}
			switch event.Kind {
			case "peer_added", "peer_removed":
				log.Infoln("[EasyTier](%s) %s: %s", e.Name(), event.Kind, event.Message)
			default:
				log.Debugln("[EasyTier](%s) %s: %s", e.Name(), event.Kind, event.Message)
			}
		}
	}
}

func (e *EasyTier) overlayNodes(ctx context.Context) ([]easytier.Node, error) {
	instance, err := e.currentInstance()
	if err != nil {
		return nil, err
	}
	return e.overlayInstanceNodes(ctx, instance, e.ipv6Address)
}

func (e *EasyTier) overlayInstanceNodes(ctx context.Context, instance *corehost.Instance, ipv6Address netip.Prefix) ([]easytier.Node, error) {
	var nodes []easytier.Node
	info, err := instance.ShowNodeInfo(ctx)
	if err == nil && info != nil {
		node := easytier.Node{Hostname: info.GetHostname()}
		if ip, parseErr := easytier.ParseNodeIPv4(info.GetIpv4Addr()); parseErr == nil {
			node.IPv4 = ip
		}
		if ipv6Address.IsValid() {
			node.IPv6 = ipv6Address.Addr()
		}
		if publicIPv6, ok := easyTierIPv6Inet(info.GetPublicIpv6Addr()); ok {
			node.PublicIPv6 = publicIPv6.Addr()
		}
		if node.Hostname != "" || node.IPv4.IsValid() || node.IPv6.IsValid() || node.PublicIPv6.IsValid() {
			nodes = append(nodes, node)
		}
	}
	routes, err := instance.ListRoute(ctx)
	if err != nil {
		if len(nodes) == 0 {
			return nil, err
		}
		return nodes, nil
	}
	for _, route := range routes {
		if route == nil {
			continue
		}
		node := easytier.Node{Hostname: route.GetHostname()}
		if inet := route.GetIpv4Addr(); inet != nil && inet.GetAddress() != nil {
			node.IPv4 = easytier.IPv4FromUint32(inet.GetAddress().GetAddr())
		}
		if inet := route.GetIpv6Addr(); inet != nil && inet.GetAddress() != nil {
			node.IPv6 = easytier.IPv6FromParts(inet.GetAddress().GetPart1(), inet.GetAddress().GetPart2(), inet.GetAddress().GetPart3(), inet.GetAddress().GetPart4())
		}
		if publicIPv6, ok := easyTierIPv6Inet(route.GetPublicIpv6Addr()); ok {
			node.PublicIPv6 = publicIPv6.Addr()
		}
		if info != nil && route.GetPeerId() == info.GetPeerId() && len(nodes) > 0 {
			nodes[0] = node
			continue
		}
		if node.Hostname != "" || node.IPv4.IsValid() || node.IPv6.IsValid() || node.PublicIPv6.IsValid() {
			nodes = append(nodes, node)
		}
	}
	return nodes, nil
}

func (e *EasyTier) resolveIPv4(ctx context.Context, host string) (netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		ip = ip.Unmap()
		if !ip.Is4() {
			return netip.Addr{}, fmt.Errorf("easytier: overlay dial supports IPv4 only")
		}
		return ip, nil
	}
	nodes, err := e.overlayNodes(ctx)
	if err != nil {
		return netip.Addr{}, err
	}
	if node, ok := easytier.LookupOverlayHost(host, e.zone, nodes); ok {
		if node.IPv4.IsValid() {
			return node.IPv4, nil
		}
		return netip.Addr{}, fmt.Errorf("easytier: overlay hostname %q has no IPv4 address", host)
	}
	if easytier.IsMagicDNS(host, e.zone) {
		return netip.Addr{}, fmt.Errorf("easytier: overlay hostname %q was not found", host)
	}
	ips, err := resolver.LookupIPv4WithResolver(ctx, host, resolver.ProxyServerHostResolver)
	if err != nil {
		return netip.Addr{}, err
	}
	if len(ips) == 0 {
		return netip.Addr{}, fmt.Errorf("easytier: resolve %q: no IPv4 address", host)
	}
	return ips[0], nil
}

func (e *EasyTier) DialContext(ctx context.Context, metadata *C.Metadata) (_ C.Conn, err error) {
	if err = e.ensureStarted(ctx); err != nil {
		return nil, err
	}
	host := metadata.Host
	if host == "" && metadata.DstIP.IsValid() {
		host = metadata.DstIP.String()
	}
	ip, err := e.resolveIPv4(ctx, host)
	if err != nil {
		return nil, err
	}
	instance, err := e.currentInstance()
	if err != nil {
		return nil, err
	}
	address := net.JoinHostPort(ip.String(), fmt.Sprintf("%d", metadata.DstPort))
	conn, err := instance.Dial(ctx, "tcp4", address)
	if err != nil {
		return nil, err
	}
	if conn == nil {
		return nil, errors.New("conn is nil")
	}
	return NewConn(conn, e), nil
}

func (e *EasyTier) ListenPacketContext(ctx context.Context, metadata *C.Metadata) (_ C.PacketConn, err error) {
	if err = e.ensureStarted(ctx); err != nil {
		return nil, err
	}
	if err = e.ResolveUDP(ctx, metadata); err != nil {
		return nil, err
	}
	instance, err := e.currentInstance()
	if err != nil {
		return nil, err
	}
	pc, err := instance.ListenPacket("udp4", ":0")
	if err != nil {
		return nil, err
	}
	if pc == nil {
		return nil, errors.New("packetConn is nil")
	}
	return NewPacketConn(pc, e), nil
}

func (e *EasyTier) ResolveUDP(ctx context.Context, metadata *C.Metadata) error {
	if metadata.Host != "" {
		ip, err := e.resolveIPv4(ctx, metadata.Host)
		if err != nil {
			return fmt.Errorf("can't resolve ip: %w", err)
		}
		metadata.DstIP = ip
		return nil
	}
	if metadata.DstIP.IsValid() && !metadata.DstIP.Is4() {
		return fmt.Errorf("easytier: overlay dial supports IPv4 only")
	}
	return nil
}

func (e *EasyTier) ProxyInfo() C.ProxyInfo {
	info := e.Base.ProxyInfo()
	info.DialerProxy = e.option.DialerProxy
	return info
}

func (e *EasyTier) IsL3Protocol(*C.Metadata) bool {
	return true
}

func (e *EasyTier) Close() error {
	e.cancel()
	if e.unregister != nil {
		e.unregister()
	}
	e.startMu.Lock()
	e.closed = true
	if e.readyCh != nil {
		close(e.readyCh)
		e.readyCh = nil
	}
	e.startMu.Unlock()
	// If startup already began, let its owner finish cancellation and teardown
	// before Close returns. Otherwise prevent any later call from starting it.
	e.loopOnce.Do(func() { close(e.loopDone) })
	<-e.loopDone
	return e.shutdown()
}

func (e *EasyTier) shutdown() error {
	ctx, cancel := context.WithTimeout(context.Background(), C.DefaultTCPTimeout)
	defer cancel()
	e.mu.Lock()
	instance := e.instance
	host := e.host
	e.instance = nil
	e.host = nil
	e.webClient = nil
	e.mu.Unlock()
	var err error
	if instance != nil {
		err = instance.Close(ctx)
	}
	if host != nil {
		if hostErr := host.Close(ctx); err == nil {
			err = hostErr
		}
	}
	return err
}

type easyTierDNSTransport struct {
	easytier *EasyTier
}

func (t easyTierDNSTransport) Address() string {
	return "easytier://" + t.easytier.Name()
}

func (t easyTierDNSTransport) ResetConnection() {}

func (t easyTierDNSTransport) ExchangeContext(ctx context.Context, msg *D.Msg) (*D.Msg, error) {
	if len(msg.Question) == 0 {
		return nil, errors.New("should have one question at least")
	}
	if err := t.easytier.ensureStarted(ctx); err != nil {
		return nil, err
	}
	if t.easytier.option.WebClient != nil {
		return t.exchangeWebClient(ctx, msg)
	}
	nodes, err := t.easytier.overlayNodes(ctx)
	if err != nil {
		return nil, err
	}
	return easyTierDNSReply(msg, t.easytier.zone, nodes), nil
}

func easyTierDNSReply(msg *D.Msg, zone string, nodes []easytier.Node) *D.Msg {
	q := msg.Question[0]
	reply := new(D.Msg)
	reply.SetReply(msg)
	reply.Authoritative = true
	reply.RecursionAvailable = true
	switch q.Qtype {
	case D.TypeA, D.TypeAAAA:
		node, ok := easytier.LookupOverlayHost(q.Name, zone, nodes)
		if !ok {
			reply.Rcode = D.RcodeNameError
			return reply
		}
		header := D.RR_Header{Name: q.Name, Rrtype: q.Qtype, Class: D.ClassINET, Ttl: easyTierDNSTTL}
		if q.Qtype == D.TypeA {
			if node.IPv4.IsValid() {
				reply.Answer = append(reply.Answer, &D.A{Hdr: header, A: node.IPv4.AsSlice()})
			}
		} else {
			for _, ip := range []netip.Addr{node.IPv6, node.PublicIPv6} {
				if ip.IsValid() {
					reply.Answer = append(reply.Answer, &D.AAAA{Hdr: header, AAAA: ip.AsSlice()})
				}
			}
			reply.Answer = D.Dedup(reply.Answer, nil)
		}
	case D.TypePTR:
		ip, ok := easytier.ParsePTR(q.Name)
		if !ok {
			reply.Rcode = D.RcodeNameError
			return reply
		}
		name, ok := easytier.LookupOverlayPTR(ip, zone, nodes)
		if !ok {
			reply.Rcode = D.RcodeNameError
			return reply
		}
		reply.Answer = append(reply.Answer, &D.PTR{
			Hdr: D.RR_Header{Name: q.Name, Rrtype: D.TypePTR, Class: D.ClassINET, Ttl: easyTierDNSTTL},
			Ptr: name,
		})
	default:
		if _, ok := easytier.LookupOverlayHost(q.Name, zone, nodes); !ok {
			reply.Rcode = D.RcodeNameError
		}
	}
	return reply
}

func loadInstanceID(stateDir string) string {
	contents, err := os.ReadFile(filepath.Join(stateDir, easyTierInstanceIDFile))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(contents))
}

func writeInstanceID(stateDir, id string) error {
	if id == "" {
		return nil
	}
	path := filepath.Join(stateDir, easyTierInstanceIDFile)
	return os.WriteFile(path, []byte(id+"\n"), 0o600)
}
