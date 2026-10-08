//go:build !no_easytier

package outbound

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"time"

	corehost "github.com/easytier/easytier/easytier-go"
	"github.com/easytier/easytier/easytier-go/proto/api/manage"
	"github.com/gofrs/uuid/v5"
	"github.com/metacubex/mihomo/component/easytier"
	"github.com/metacubex/mihomo/dns"
	"github.com/metacubex/mihomo/log"
	D "github.com/miekg/dns"
	"github.com/pelletier/go-toml/v2"
)

func validateEasyTierWebClient(option EasyTierOption) error {
	if !option.PacketMode {
		return fmt.Errorf("easytier %q: web-client requires attachment to the shared TUN", option.Name)
	}
	for _, field := range []struct {
		name string
		set  bool
	}{
		{"config-toml", option.ConfigTOML != ""},
		{"network-name", option.NetworkName != ""},
		{"network-secret", option.NetworkSecret != ""},
		{"hostname", option.Hostname != ""},
		{"ipv4", option.IPv4 != ""},
		{"ipv6", option.IPv6 != ""},
		{"ipv6-public-addr-provider", option.IPv6PublicAddrProvider != nil},
		{"ipv6-public-addr-auto", option.IPv6PublicAddrAuto != nil},
		{"ipv6-public-addr-prefix", option.IPv6PublicAddrPrefix != ""},
		{"dhcp", option.DHCP},
		{"peers", option.Peers != nil},
		{"listeners", option.Listeners != nil},
		{"no-listener", option.NoListener != nil},
		{"mapped-listeners", option.MappedListeners != nil},
		{"exit-nodes", option.ExitNodes != nil},
		{"routes", option.Routes != nil},
		{"proxy-networks", option.ProxyNetworks != nil},
		{"socks5-proxy", option.Socks5Proxy != ""},
		{"port-forwards", option.PortForwards != nil},
		{"instance-name", option.InstanceName != ""},
		{"accept-dns", option.AcceptDNS != nil},
		{"enable-exit-node", option.EnableExitNode != nil},
		{"enable-encryption", option.EnableEncryption != nil},
		{"encryption-algorithm", option.EncryptionAlgorithm != ""},
		{"private-mode", option.PrivateMode != nil},
		{"latency-first", option.LatencyFirst != nil},
		{"disable-p2p", option.DisableP2P != nil},
		{"enable-kcp-proxy", option.EnableKCPProxy != nil},
		{"disable-kcp-input", option.DisableKCPInput != nil},
		{"enable-quic-proxy", option.EnableQUICProxy != nil},
		{"disable-quic-input", option.DisableQUICInput != nil},
		{"mtu", option.MTU != 0},
		{"tld-dns-zone", option.TLDDNSZone != ""},
		{"secure-mode", option.SecureMode != nil},
		{"local-private-key", option.LocalPrivateKey != ""},
		{"local-public-key", option.LocalPublicKey != ""},
		{"stun-servers", option.STUNServers != nil},
		{"tcp-stun-servers", option.TCPSTUNServers != nil},
		{"stun-servers-v6", option.STUNServersV6 != nil},
		{"tcp-whitelist", option.TCPWhitelist != nil},
		{"udp-whitelist", option.UDPWhitelist != nil},
		{"default-protocol", option.DefaultProtocol != ""},
		{"dev-name", option.DevName != ""},
		{"enable-ipv6", option.EnableIPv6 != nil},
		{"proxy-forward-by-system", option.ProxyForwardBySystem != nil},
		{"relay-network-whitelist", option.RelayNetworkWhitelist != ""},
		{"p2p-only", option.P2POnly != nil},
		{"relay-all-peer-rpc", option.RelayAllPeerRPC != nil},
		{"disable-relay-kcp", option.DisableRelayKCP != nil},
		{"enable-relay-foreign-network-kcp", option.EnableRelayForeignNetworkKCP != nil},
		{"disable-relay-quic", option.DisableRelayQUIC != nil},
		{"enable-relay-foreign-network-quic", option.EnableRelayForeignNetworkQUIC != nil},
		{"foreign-relay-bps-limit", option.ForeignRelayBPSLimit != nil},
		{"instance-recv-bps-limit", option.InstanceRecvBPSLimit != nil},
		{"multi-thread", option.MultiThread != nil},
		{"multi-thread-count", option.MultiThreadCount != 0},
		{"data-compress-algo", option.DataCompressAlgo != ""},
		{"disable-upnp", option.DisableUPnP != nil},
		{"disable-relay-data", option.DisableRelayData != nil},
		{"prefer-peer-relay", option.PreferPeerRelay != nil},
		{"enable-udp-broadcast-relay", option.EnableUDPBroadcastRelay != nil},
		{"quic-listen-port", option.QUICListenPort != 0},
		{"socket-mark", option.SocketMark != nil},
		{"need-p2p", option.NeedP2P != nil},
		{"lazy-p2p", option.LazyP2P != nil},
		{"disable-tcp-hole-punch", option.DisableTCPHolePunch != nil},
		{"disable-udp-hole-punch", option.DisableUDPHolePunch != nil},
		{"disable-sym-hole-punch", option.DisableSymHolePunch != nil},
	} {
		if field.set {
			return fmt.Errorf("easytier %q: web-client cannot be combined with %s; configure remote networks in the configuration center", option.Name, field.name)
		}
	}
	endpoint := strings.TrimSpace(option.WebClient.Endpoint)
	if endpoint == "" {
		return fmt.Errorf("easytier %q: web-client endpoint is empty", option.Name)
	}
	if strings.Contains(endpoint, "://") {
		parsed, err := url.Parse(endpoint)
		if err != nil || (parsed.Scheme != "tcp" && parsed.Scheme != "udp") || parsed.Hostname() == "" {
			return fmt.Errorf("easytier %q: web-client endpoint must be a tcp:// or udp:// configuration-server URL or a token; the pinned Go WASM host does not support WebSocket transports", option.Name)
		}
	}
	if option.WebClient.MachineID != "" {
		if _, err := uuid.FromString(option.WebClient.MachineID); err != nil {
			return fmt.Errorf("easytier %q: invalid web-client machine-id: %w", option.Name, err)
		}
	}
	return nil
}

func (e *EasyTier) initWebClient() error {
	option := *e.option.WebClient
	machineID := option.MachineID
	if machineID == "" {
		machineID = loadInstanceID(e.stateDir)
		if machineID == "" {
			id, err := uuid.NewV4()
			if err != nil {
				return fmt.Errorf("easytier %q: create machine ID: %w", e.Name(), err)
			}
			machineID = id.String()
		}
	}
	if err := writeInstanceID(e.stateDir, machineID); err != nil {
		return fmt.Errorf("easytier %q: persist machine ID: %w", e.Name(), err)
	}
	services, err := easytier.Services(e.dialer)
	if err != nil {
		return err
	}
	host, err := corehost.New(e.ctx, corehost.Options{
		Platform:                services,
		WebInstanceConfigPolicy: e.prepareWebInstanceConfig,
	})
	if err != nil {
		return err
	}
	e.mu.Lock()
	e.host, e.instanceID = host, machineID
	e.mu.Unlock()
	client, err := host.ConnectWebClient(e.ctx, corehost.WebClientOptions{
		Endpoint: option.Endpoint, MachineID: machineID, Hostname: option.Hostname, SecureMode: option.SecureMode,
	})
	if err != nil {
		return fmt.Errorf("easytier %q: start configuration-center client: %w", e.Name(), err)
	}
	e.mu.Lock()
	e.webClient = client
	e.mu.Unlock()
	log.Infoln("[EasyTier](%s) configuration-center client %s started", e.Name(), machineID)
	return nil
}

func (e *EasyTier) prepareWebInstanceConfig(ctx context.Context, instanceID, configTOML string, requested *manage.NetworkConfig) (string, error) {
	if requested != nil {
		document := make(map[string]any)
		if err := toml.Unmarshal([]byte(configTOML), &document); err != nil {
			return "", fmt.Errorf("easytier %q instance %s: configuration-center config: %w", e.Name(), instanceID, err)
		}
		// The hosted guest converts only a subset of NetworkConfig to TOML.
		// Restore routing fields from the original management request.
		if requested.GetEnableManualRoutes() {
			document["routes"] = requested.GetRoutes()
		} else {
			delete(document, "routes")
		}
		document["exit_nodes"] = requested.GetExitNodes()
		if requested.EnableExitNode != nil {
			flags, ok := document["flags"].(map[string]any)
			if !ok {
				if _, exists := document["flags"]; exists {
					return "", fmt.Errorf("easytier %q instance %s: configuration-center flags must be a table", e.Name(), instanceID)
				}
				flags = make(map[string]any)
				document["flags"] = flags
			}
			flags["enable_exit_node"] = requested.GetEnableExitNode()
		}
		encoded, err := toml.Marshal(document)
		if err != nil {
			return "", fmt.Errorf("easytier %q instance %s: encode configuration-center routing fields: %w", e.Name(), instanceID, err)
		}
		configTOML = string(encoded)
	}
	prepared, candidate, err := easytier.PrepareNativeConfig(configTOML, true)
	if err != nil {
		return "", fmt.Errorf("easytier %q instance %s: configuration-center config: %w", e.Name(), instanceID, err)
	}
	addresses, err := candidate.OverlayAddresses()
	if err != nil {
		return "", fmt.Errorf("easytier %q instance %s: configuration-center addresses: %w", e.Name(), instanceID, err)
	}
	e.mu.Lock()
	host, policy := e.host, e.packetConfigPolicy
	e.mu.Unlock()
	if host == nil {
		return "", fmt.Errorf("easytier %q: configuration-center host is not ready", e.Name())
	}
	for _, snapshot := range host.InstanceConfigurations() {
		if snapshot.InstanceID == instanceID {
			continue
		}
		_, existing, err := easytier.PrepareNativeConfig(snapshot.ConfigTOML, true)
		if err != nil {
			return "", fmt.Errorf("easytier %q instance %s: existing config: %w", e.Name(), snapshot.InstanceID, err)
		}
		previousAddresses, err := existing.OverlayAddresses()
		if err != nil {
			return "", fmt.Errorf("easytier %q instance %s: existing addresses: %w", e.Name(), snapshot.InstanceID, err)
		}
		if existing.DHCP && snapshot.Instance.State() == corehost.StateRunning {
			info, err := snapshot.Instance.ShowNodeInfo(ctx)
			if err != nil {
				return "", fmt.Errorf("easytier %q instance %s: existing DHCP address: %w", e.Name(), snapshot.InstanceID, err)
			}
			if info.GetIpv4Addr() != "" {
				lease, err := netip.ParsePrefix(info.GetIpv4Addr())
				if err != nil {
					return "", fmt.Errorf("easytier %q instance %s: invalid DHCP address %q: %w", e.Name(), snapshot.InstanceID, info.GetIpv4Addr(), err)
				}
				previousAddresses = append(previousAddresses, lease)
			}
		}
		for _, address := range addresses {
			for _, previous := range previousAddresses {
				if address.Overlaps(previous) {
					return "", fmt.Errorf("easytier %q instance %s: overlay address %s overlaps instance %s address %s", e.Name(), instanceID, address, snapshot.InstanceID, previous)
				}
			}
		}
	}
	if policy != nil {
		if err := policy(ctx, instanceID, candidate); err != nil {
			return "", fmt.Errorf("easytier %q instance %s: shared TUN config: %w", e.Name(), instanceID, err)
		}
	}
	return prepared, nil
}

func (e *EasyTier) SetPacketConfigPolicy(policy easytier.PacketConfigPolicy) {
	e.mu.Lock()
	e.packetConfigPolicy = policy
	e.mu.Unlock()
}

func (e *EasyTier) PacketNetworks() ([]easytier.PacketNetwork, error) {
	e.mu.Lock()
	host, instanceID := e.host, e.instanceID
	e.mu.Unlock()
	if e.option.WebClient == nil {
		return []easytier.PacketNetwork{{ID: instanceID, Config: e.configMetadata}}, nil
	}
	if host == nil {
		return nil, nil
	}
	networks := make([]easytier.PacketNetwork, 0)
	for _, snapshot := range host.InstanceConfigurations() {
		if !snapshot.WebOwned {
			continue
		}
		_, metadata, err := easytier.PrepareNativeConfig(snapshot.ConfigTOML, true)
		if err != nil {
			return nil, fmt.Errorf("easytier %q instance %s: packet network config: %w", e.Name(), snapshot.InstanceID, err)
		}
		networks = append(networks, easytier.PacketNetwork{ID: snapshot.InstanceID, Config: metadata})
	}
	return networks, nil
}

func (e *EasyTier) OpenPacketSessions(ctx context.Context) ([]easytier.PacketSession, error) {
	if e.option.WebClient == nil {
		session, err := e.OpenPacketSession(ctx)
		if err != nil {
			return nil, err
		}
		return []easytier.PacketSession{session}, nil
	}
	if err := e.ensureStarted(ctx); err != nil {
		return nil, err
	}
	e.mu.Lock()
	host := e.host
	e.mu.Unlock()
	if host == nil {
		return nil, fmt.Errorf("easytier %q: configuration-center host is not ready", e.Name())
	}
	var sessions []easytier.PacketSession
	var failures []error
	for _, snapshot := range host.InstanceConfigurations() {
		if !snapshot.WebOwned {
			continue
		}
		if snapshot.Instance.State() != corehost.StateRunning {
			continue
		}
		_, metadata, err := easytier.PrepareNativeConfig(snapshot.ConfigTOML, true)
		if err != nil {
			failures = append(failures, fmt.Errorf("easytier %q instance %s: effective config: %w", e.Name(), snapshot.InstanceID, err))
			continue
		}
		networkCtx, cancel := context.WithTimeout(ctx, time.Second)
		session, err := e.packetSession(networkCtx, snapshot.Instance, metadata, snapshot.Instance, false)
		cancel()
		if errors.Is(err, net.ErrClosed) && snapshot.Instance.State() != corehost.StateRunning {
			continue
		}
		if errors.Is(err, errEasyTierAddressesPending) {
			continue
		}
		if err != nil {
			failures = append(failures, err)
			continue
		}
		sessions = append(sessions, session)
	}
	return sessions, errors.Join(failures...)
}

func (t easyTierDNSTransport) EasyTierDNSNetworks(ctx context.Context) ([]dns.EasyTierNetwork, error) {
	if t.easytier.option.WebClient == nil {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	t.easytier.mu.Lock()
	host := t.easytier.host
	t.easytier.mu.Unlock()
	if host == nil {
		return nil, nil
	}
	var networks []dns.EasyTierNetwork
	for _, snapshot := range host.InstanceConfigurations() {
		if !snapshot.WebOwned || snapshot.Instance.State() != corehost.StateRunning {
			continue
		}
		_, metadata, err := easytier.PrepareNativeConfig(snapshot.ConfigTOML, true)
		if err != nil {
			return nil, fmt.Errorf("easytier %q instance %s: DNS config: %w", t.easytier.Name(), snapshot.InstanceID, err)
		}
		networks = append(networks, dns.EasyTierNetwork{Name: snapshot.InstanceID, Zone: easytier.NormalizeZone(metadata.TLDDNSZone)})
	}
	return networks, nil
}

func (t easyTierDNSTransport) ExchangeEasyTierDNS(ctx context.Context, instanceID string, msg *D.Msg) (*D.Msg, error) {
	if len(msg.Question) == 0 {
		return nil, errors.New("should have one question at least")
	}
	if t.easytier.option.WebClient == nil {
		return t.ExchangeContext(ctx, msg)
	}
	if err := t.easytier.ensureStarted(ctx); err != nil {
		return nil, err
	}
	t.easytier.mu.Lock()
	host := t.easytier.host
	t.easytier.mu.Unlock()
	if host == nil {
		return nil, fmt.Errorf("easytier %q: configuration-center host is not ready", t.easytier.Name())
	}
	for _, snapshot := range host.InstanceConfigurations() {
		if !snapshot.WebOwned || snapshot.InstanceID != instanceID {
			continue
		}
		_, metadata, err := easytier.PrepareNativeConfig(snapshot.ConfigTOML, true)
		if err != nil {
			return nil, fmt.Errorf("easytier %q instance %s: DNS config: %w", t.easytier.Name(), instanceID, err)
		}
		var ipv6Address netip.Prefix
		if metadata.IPv6 != "" {
			ipv6Address, err = easytier.ParseIPv6Prefix(metadata.IPv6)
			if err != nil {
				return nil, fmt.Errorf("easytier %q instance %s: DNS IPv6: %w", t.easytier.Name(), instanceID, err)
			}
		}
		nodes, err := t.easytier.overlayInstanceNodes(ctx, snapshot.Instance, ipv6Address)
		if err != nil {
			return nil, fmt.Errorf("easytier %q instance %s: DNS nodes: %w", t.easytier.Name(), instanceID, err)
		}
		return easyTierDNSReply(msg, easytier.NormalizeZone(metadata.TLDDNSZone), nodes), nil
	}
	return nil, fmt.Errorf("easytier %q: configuration-center instance %s is no longer available", t.easytier.Name(), instanceID)
}

func (t easyTierDNSTransport) exchangeWebClient(ctx context.Context, msg *D.Msg) (*D.Msg, error) {
	networks, err := t.EasyTierDNSNetworks(ctx)
	if err != nil {
		return nil, err
	}
	qualifiedZone := ""
	for _, network := range networks {
		if easytier.IsMagicDNS(msg.Question[0].Name, network.Zone) && len(network.Zone) > len(qualifiedZone) {
			qualifiedZone = network.Zone
		}
	}
	var reply *D.Msg
	var replyZone string
	for _, network := range networks {
		if qualifiedZone != "" && network.Zone != qualifiedZone {
			continue
		}
		candidate, err := t.ExchangeEasyTierDNS(ctx, network.Name, msg)
		if err != nil {
			return nil, err
		}
		if candidate.Rcode != D.RcodeSuccess {
			continue
		}
		if reply == nil {
			reply, replyZone = candidate, network.Zone
			continue
		}
		if replyZone != network.Zone {
			return nil, fmt.Errorf("easytier %q: Magic DNS name %q is ambiguous across zones %q and %q", t.easytier.Name(), msg.Question[0].Name, replyZone, network.Zone)
		}
		reply.Answer = D.Dedup(append(reply.Answer, candidate.Answer...), nil)
	}
	if reply != nil {
		return reply, nil
	}
	return easyTierDNSReply(msg, "", nil), nil
}
