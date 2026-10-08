//go:build mihomo_integration

package host

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/easytier/easytier/easytier-go/platform"
	"github.com/easytier/easytier/easytier-go/platform/netstd"
	apiconfig "github.com/easytier/easytier/easytier-go/proto/api/config"
	apiinstance "github.com/easytier/easytier/easytier-go/proto/api/instance"
	"github.com/easytier/easytier/easytier-go/proto/api/manage"
	"github.com/easytier/easytier/easytier-go/proto/common"
)

// Run from mihomo's root module for the existing tagged core helpers.
func TestWebManagementConnectorClearStopsReconnecting(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	peerSockets := &webMuxSocketFactory{SocketFactory: netstd.SocketFactory{}, ports: make(chan int, 2)}
	peerHost, err := New(ctx, Options{Platform: platform.Services{Sockets: peerSockets}})
	if err != nil {
		t.Fatal(err)
	}
	defer peerHost.Close(context.Background())
	peer, err := peerHost.CreateInstance(ctx, webMuxConfig(t, "connector-peer", "10.155.0.1/24", "", true))
	if err != nil {
		t.Fatal(err)
	}
	if err := peer.Start(ctx); err != nil {
		t.Fatal(err)
	}
	var port int
	select {
	case port = <-peerSockets.ports:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	endpoint := fmt.Sprintf("tcp://127.0.0.1:%d", port)
	ownerSockets := &connectorPatchSocketFactory{SocketFactory: netstd.SocketFactory{}}
	owner, err := New(ctx, Options{Platform: platform.Services{Sockets: ownerSockets}})
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close(context.Background())
	id := &common.UUID{Part1: 61, Part2: 62, Part3: 63, Part4: 64}
	idString, networkName := uuidString(id), "web-mux-integration"
	configTOML, err := encodeInstanceConfig(webMuxConfig(t, "connector-managed", "10.155.0.2/24", "", false))
	if err != nil {
		t.Fatal(err)
	}
	callManagement(t, owner, ctx, runNetworkInstanceMethod, &manage.RunNetworkInstanceRequest{
		InstId: id, Config: &manage.NetworkConfig{InstanceId: &idString, NetworkName: &networkName},
		Source: manage.ConfigSource_ConfigSourceWeb,
	}, bindInstanceIdentity(configTOML, idString, networkName), new(manage.RunNetworkInstanceResponse))
	managed := owner.InstanceConfigurations()[0].Instance
	identifier := &apiinstance.InstanceIdentifier{Selector: &apiinstance.InstanceIdentifier_Id{Id: id}}
	patch := func(action apiconfig.ConfigPatchAction) {
		t.Helper()
		callManagement(t, owner, ctx, patchConfigMethod, &apiconfig.PatchConfigRequest{
			Instance: identifier,
			Patch: &apiconfig.InstanceConfigPatch{Connectors: []*apiconfig.UrlPatch{
				{Action: action, Url: &common.Url{Url: endpoint}},
			}},
		}, "", new(apiconfig.PatchConfigResponse))
	}
	patch(apiconfig.ConfigPatchAction_ADD)
	webMuxWaitRoute(t, ctx, managed, "connector-peer")
	webMuxWaitRoute(t, ctx, peer, "connector-managed")
	routes := callManagement(t, owner, ctx, listRouteRPCMethod,
		&apiinstance.ListRouteRequest{Instance: identifier}, "", new(apiinstance.ListRouteResponse)).(*apiinstance.ListRouteResponse)
	if len(routes.Routes) != 1 || routes.Routes[0].InstId != peer.ID() || routes.Routes[0].Hostname != "connector-peer" {
		t.Fatalf("Web route query did not return the connected peer: %v", routes.Routes)
	}
	peerResponse := callManagement(t, owner, ctx, listPeerRPCMethod,
		&apiinstance.ListPeerRequest{Instance: identifier}, "", new(apiinstance.ListPeerResponse)).(*apiinstance.ListPeerResponse)
	if len(peerResponse.PeerInfos) != 1 || peerResponse.PeerInfos[0].PeerId != routes.Routes[0].PeerId || len(peerResponse.PeerInfos[0].Conns) == 0 {
		t.Fatalf("Web peer query did not return the connected transport: %v", peerResponse.PeerInfos)
	}
	connected := callManagement(t, owner, ctx, "api.instance.ConnectorManageRpc.ListConnector",
		&apiinstance.ListConnectorRequest{Instance: identifier}, "", new(apiinstance.ListConnectorResponse)).(*apiinstance.ListConnectorResponse)
	if len(connected.Connectors) != 1 || connected.Connectors[0].GetUrl().GetUrl() != endpoint || connected.Connectors[0].Status != apiinstance.ConnectorStatus_CONNECTED {
		t.Fatalf("Web connector query did not return the connected endpoint: %v", connected.Connectors)
	}
	packetCtx, done := context.WithTimeout(ctx, 5*time.Second)
	packet := webMuxIPv4Packet("10.155.0.2", "10.155.0.1", []byte("connector-add"))
	if err := managed.SendPacket(packetCtx, packet); err != nil {
		t.Fatal(err)
	}
	if received, err := peer.ReceivePacket(packetCtx); err != nil || !bytes.Equal(received, packet) {
		t.Fatalf("added connector forward packet: %v", err)
	}
	packet = webMuxIPv4Packet("10.155.0.1", "10.155.0.2", []byte("connector-reply"))
	if err := peer.SendPacket(packetCtx, packet); err != nil {
		t.Fatal(err)
	}
	if received, err := managed.ReceivePacket(packetCtx); err != nil || !bytes.Equal(received, packet) {
		t.Fatalf("added connector return packet: %v", err)
	}
	done()
	beforeClear := ownerSockets.manualConnects.Load()
	if beforeClear == 0 {
		t.Fatal("connector route did not use a real manual TCP connection")
	}
	patch(apiconfig.ConfigPatchAction_CLEAR)
	if err := peer.Close(ctx); err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		peers, err := managed.ListPeer(ctx)
		if err != nil {
			t.Fatal(err)
		}
		active := false
		for _, peer := range peers {
			for _, connection := range peer.Conns {
				active = active || !connection.GetIsClosed()
			}
		}
		if !active {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("cleared connector's previous peer remained connected")
		case <-ticker.C:
		}
	}
	restartedConfig, err := NewInstanceConfigBuilder(networkName).NetworkSecret("test").
		Hostname("connector-restarted-peer").IPv4(netip.MustParsePrefix("10.155.0.1/24")).
		AddListeners(endpoint).P2P(P2PPolicy{Disable: true}).STUNServers().STUNServersV6().Encryption(false).Build()
	if err != nil {
		t.Fatal(err)
	}
	peer, err = peerHost.CreateInstance(ctx, restartedConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := peer.Start(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case reopenedPort := <-peerSockets.ports:
		if reopenedPort != port {
			t.Fatal("restarted peer changed its underlay port")
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	observation := time.NewTimer(4 * time.Second)
	defer observation.Stop()
	select {
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	case <-observation.C:
	}
	connectors := new(apiinstance.ListConnectorResponse)
	if err := managed.callRPCRequest(ctx, "api.instance.ConnectorManageRpc.ListConnector", &apiinstance.ListConnectorRequest{Instance: identifier}, connectors); err != nil {
		t.Fatal(err)
	}
	t.Logf("connector list after CLEAR and peer restart: %v", connectors.Connectors)
	if afterClear := ownerSockets.manualConnects.Load(); afterClear != beforeClear {
		t.Errorf("cleared connector attempted reconnect: manual TCP connects %d -> %d", beforeClear, afterClear)
	}
	peers, err := peer.ListPeer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, peer := range peers {
		for _, connection := range peer.Conns {
			if !connection.GetIsClosed() {
				t.Errorf("cleared connector reconnected to the restarted real peer: %v", connection)
			}
		}
	}
	if owner.InstanceConfigurations()[0].Instance != managed {
		t.Fatal("connector hot patch replaced the managed endpoint")
	}
}

type connectorPatchSocketFactory struct {
	platform.SocketFactory
	manualConnects atomic.Uint64
}

func (s *connectorPatchSocketFactory) ConnectTCP(ctx context.Context, options platform.TCPConnectOptions) (net.Conn, error) {
	if options.Purpose == platform.TCPConnectManual {
		s.manualConnects.Add(1)
	}
	return s.SocketFactory.ConnectTCP(ctx, options)
}
