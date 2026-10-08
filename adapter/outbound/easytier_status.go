//go:build !no_easytier

package outbound

import (
	"context"
	"fmt"
	"net/netip"

	corehost "github.com/easytier/easytier/easytier-go"
	"github.com/metacubex/mihomo/component/easytier"
)

func (e *EasyTier) statusSnapshot() (easytier.Summary, *corehost.Instance) {
	e.startMu.Lock()
	closed, starting := e.closed, e.readyCh != nil
	e.startMu.Unlock()
	e.mu.Lock()
	instance, instanceID, host, webClient := e.instance, e.instanceID, e.host, e.webClient
	e.mu.Unlock()
	state := "idle"
	switch {
	case closed || e.ctx.Err() != nil:
		state = "closed"
	case e.option.WebClient != nil && host != nil && webClient != nil:
		state = "running"
	case instance != nil:
		instanceID = instance.ID()
		state = easyTierInstanceState(instance.State())
	case instanceID != "":
		state = "restarting"
	case starting:
		state = "starting"
	}
	summary := easytier.Summary{
		Name: e.Name(), State: state, InstanceID: instanceID,
		PacketMode: e.option.PacketMode, DNSZone: e.zone,
		WebManaged: e.option.WebClient != nil,
	}
	if summary.WebManaged {
		summary.DNSZone = ""
		summary.WebClientConnected = webClient != nil && webClient.Connected()
	}
	return summary, instance
}

func (e *EasyTier) EasyTierSummary() easytier.Summary {
	summary, _ := e.statusSnapshot()
	return summary
}

func (e *EasyTier) EasyTierStatus(ctx context.Context) (easytier.Status, error) {
	summary, instance := e.statusSnapshot()
	coreInfo := corehost.CoreInfo()
	status := easytier.Status{
		Summary: summary, Peers: []easytier.PeerStatus{}, Routes: []easytier.RouteStatus{},
		Provenance: easytier.Provenance{Commit: coreInfo.EasyTierCommit, SHA256: coreInfo.SHA256},
	}
	if err := ctx.Err(); err != nil {
		return status, err
	}
	if summary.State != "running" {
		return status, nil
	}
	if summary.WebManaged {
		e.mu.Lock()
		host := e.host
		e.mu.Unlock()
		if host == nil {
			return status, nil
		}
		for _, snapshot := range host.InstanceConfigurations() {
			if !snapshot.WebOwned {
				continue
			}
			_, metadata, err := easytier.PrepareNativeConfig(snapshot.ConfigTOML, true)
			if err != nil {
				return status, fmt.Errorf("easytier %q instance %s: status config: %w", e.Name(), snapshot.InstanceID, err)
			}
			var ipv6Address netip.Prefix
			if metadata.IPv6 != "" {
				ipv6Address, err = easytier.ParseIPv6Prefix(metadata.IPv6)
				if err != nil {
					return status, fmt.Errorf("easytier %q instance %s: status IPv6: %w", e.Name(), snapshot.InstanceID, err)
				}
			}
			network := easytier.NetworkStatus{
				InstanceID: snapshot.InstanceID, NetworkName: metadata.NetworkName,
				DNSZone: easytier.NormalizeZone(metadata.TLDDNSZone), State: easyTierInstanceState(snapshot.Instance.State()),
				Peers: []easytier.PeerStatus{}, Routes: []easytier.RouteStatus{},
			}
			if snapshot.Instance.State() == corehost.StateRunning {
				data, err := e.instanceStatus(ctx, snapshot.Instance, ipv6Address)
				if err != nil {
					return status, err
				}
				network.Node, network.Peers, network.Routes = data.Node, data.Peers, data.Routes
			}
			status.Networks = append(status.Networks, network)
		}
		return status, nil
	}
	data, err := e.instanceStatus(ctx, instance, e.ipv6Address)
	status.Node, status.Peers, status.Routes = data.Node, data.Peers, data.Routes
	return status, err
}

func easyTierInstanceState(state corehost.State) string {
	switch state {
	case corehost.StateCreated:
		return "created"
	case corehost.StateStarting:
		return "starting"
	case corehost.StateRunning:
		return "running"
	case corehost.StateStopping:
		return "stopping"
	case corehost.StateStopped:
		return "stopped"
	}
	return "unknown"
}

func (e *EasyTier) instanceStatus(ctx context.Context, instance *corehost.Instance, ipv6Address netip.Prefix) (easytier.Status, error) {
	status := easytier.Status{Peers: []easytier.PeerStatus{}, Routes: []easytier.RouteStatus{}}
	info, err := instance.ShowNodeInfo(ctx)
	if err != nil {
		return status, fmt.Errorf("easytier %q: show node info: %w", e.Name(), err)
	}
	// NodeInfo.Config contains network secrets and private keys. Project the
	// public runtime fields rather than exposing the upstream message.
	status.Node = &easytier.NodeStatus{
		PeerID: info.GetPeerId(), Hostname: info.GetHostname(), IPv4: info.GetIpv4Addr(),
		InstanceID: info.GetInstId(), Version: info.GetVersion(), ProxyCIDRs: info.GetProxyCidrs(),
	}
	if ipv6Address.IsValid() {
		status.Node.IPv6 = ipv6Address.String()
	}
	if publicIPv6, ok := easyTierIPv6Inet(info.GetPublicIpv6Addr()); ok {
		status.Node.PublicIPv6 = publicIPv6.String()
	}
	peers, err := instance.ListPeer(ctx)
	if err != nil {
		return status, fmt.Errorf("easytier %q: list peers: %w", e.Name(), err)
	}
	for _, peer := range peers {
		if peer == nil {
			continue
		}
		peerStatus := easytier.PeerStatus{PeerID: peer.GetPeerId(), Connections: []easytier.ConnectionStatus{}}
		for _, conn := range peer.GetConns() {
			if conn == nil {
				continue
			}
			stats := conn.GetStats()
			peerStatus.Connections = append(peerStatus.Connections, easytier.ConnectionStatus{
				ID: conn.GetConnId(), Transport: conn.GetTunnel().GetTunnelType(),
				Client: conn.GetIsClient(), Closed: conn.GetIsClosed(),
				LatencyUS: stats.GetLatencyUs(), LossRate: conn.GetLossRate(),
				RXBytes: stats.GetRxBytes(), TXBytes: stats.GetTxBytes(),
				RXPackets: stats.GetRxPackets(), TXPackets: stats.GetTxPackets(),
				SecureAuthLevel: conn.GetSecureAuthLevel().String(), PeerIdentityType: conn.GetPeerIdentityType().String(),
			})
		}
		status.Peers = append(status.Peers, peerStatus)
	}
	routes, err := instance.ListRoute(ctx)
	if err != nil {
		return status, fmt.Errorf("easytier %q: list routes: %w", e.Name(), err)
	}
	for _, route := range routes {
		if route == nil {
			continue
		}
		routeStatus := easytier.RouteStatus{
			PeerID: route.GetPeerId(), Hostname: route.GetHostname(),
			NextHopPeerID: route.GetNextHopPeerId(), Cost: route.GetCost(), PathLatency: route.GetPathLatency(),
			NextHopPeerIDLatencyFirst: route.NextHopPeerIdLatencyFirst, CostLatencyFirst: route.CostLatencyFirst,
			PathLatencyLatencyFirst: route.PathLatencyLatencyFirst,
			ProxyCIDRs:              route.GetProxyCidrs(), InstanceID: route.GetInstId(), Version: route.GetVersion(),
		}
		if address := route.GetIpv4Addr(); address != nil && address.GetAddress() != nil {
			ip := easytier.IPv4FromUint32(address.GetAddress().GetAddr())
			routeStatus.IPv4 = netip.PrefixFrom(ip, int(address.GetNetworkLength())).String()
		}
		if address := route.GetIpv6Addr(); address != nil && address.GetAddress() != nil {
			ip := address.GetAddress()
			routeStatus.IPv6 = netip.PrefixFrom(easytier.IPv6FromParts(ip.GetPart1(), ip.GetPart2(), ip.GetPart3(), ip.GetPart4()), int(address.GetNetworkLength())).String()
			if route.GetPeerId() == info.GetPeerId() {
				status.Node.IPv6 = routeStatus.IPv6
			}
		}
		if address, ok := easyTierIPv6Inet(route.GetPublicIpv6Addr()); ok {
			routeStatus.PublicIPv6 = address.String()
			if route.GetPeerId() == info.GetPeerId() {
				status.Node.PublicIPv6 = routeStatus.PublicIPv6
			}
		}
		status.Routes = append(status.Routes, routeStatus)
	}
	return status, nil
}

var _ easytier.StatusProvider = (*EasyTier)(nil)
