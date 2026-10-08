package host

import (
	"context"
	"strings"
	"testing"
	"time"

	apiinstance "github.com/easytier/easytier/easytier-go/proto/api/instance"
	"github.com/easytier/easytier/easytier-go/proto/api/manage"
	"github.com/easytier/easytier/easytier-go/proto/common"
	"google.golang.org/protobuf/proto"
)

func TestWebManagementInstanceQueries(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	host, err := New(ctx, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close(context.Background())
	id := &common.UUID{Part1: 71, Part2: 72, Part3: 73, Part4: 74}
	idString, networkName := uuidString(id), "management-queries"
	document, err := encodeInstanceConfig(managementTestConfig(t, networkName))
	if err != nil {
		t.Fatal(err)
	}
	callManagement(t, host, ctx, runNetworkInstanceMethod, &manage.RunNetworkInstanceRequest{
		InstId: id, Config: &manage.NetworkConfig{InstanceId: &idString, NetworkName: &networkName},
		Source: manage.ConfigSource_ConfigSourceWeb,
	}, bindInstanceIdentity(document, idString, networkName), new(manage.RunNetworkInstanceResponse))
	identifier := &apiinstance.InstanceIdentifier{Selector: &apiinstance.InstanceIdentifier_Id{Id: id}}
	queries := []struct {
		method   string
		request  proto.Message
		response proto.Message
	}{
		{listPeerRPCMethod, &apiinstance.ListPeerRequest{Instance: identifier}, new(apiinstance.ListPeerResponse)},
		{"api.instance.PeerManageRpc.ListPublicIpv6Info", &apiinstance.ListPublicIpv6InfoRequest{Instance: identifier}, new(apiinstance.ListPublicIpv6InfoResponse)},
		{listRouteRPCMethod, &apiinstance.ListRouteRequest{Instance: identifier}, new(apiinstance.ListRouteResponse)},
		{"api.instance.PeerManageRpc.DumpRoute", &apiinstance.DumpRouteRequest{Instance: identifier}, new(apiinstance.DumpRouteResponse)},
		{"api.instance.PeerManageRpc.ListForeignNetwork", &apiinstance.ListForeignNetworkRequest{Instance: identifier, IncludeTrustedKeys: true}, new(apiinstance.ListForeignNetworkResponse)},
		{"api.instance.PeerManageRpc.ListGlobalForeignNetwork", &apiinstance.ListGlobalForeignNetworkRequest{Instance: identifier}, new(apiinstance.ListGlobalForeignNetworkResponse)},
		{showNodeInfoRPCMethod, &apiinstance.ShowNodeInfoRequest{Instance: identifier}, new(apiinstance.ShowNodeInfoResponse)},
		{foreignNetworkSummaryRPCMethod, &apiinstance.GetForeignNetworkSummaryRequest{Instance: identifier}, new(apiinstance.GetForeignNetworkSummaryResponse)},
		{"api.instance.ConnectorManageRpc.ListConnector", &apiinstance.ListConnectorRequest{Instance: identifier}, new(apiinstance.ListConnectorResponse)},
		{listMappedListenerMethod, &apiinstance.ListMappedListenerRequest{Instance: identifier}, new(apiinstance.ListMappedListenerResponse)},
		{getVpnPortalInfoMethod, &apiinstance.GetVpnPortalInfoRequest{Instance: identifier}, new(apiinstance.GetVpnPortalInfoResponse)},
		{listTcpProxyEntryMethod, &apiinstance.ListTcpProxyEntryRequest{Instance: identifier}, new(apiinstance.ListTcpProxyEntryResponse)},
		{getACLStatsMethod, &apiinstance.GetAclStatsRequest{Instance: identifier}, new(apiinstance.GetAclStatsResponse)},
		{getWhitelistMethod, &apiinstance.GetWhitelistRequest{Instance: identifier}, new(apiinstance.GetWhitelistResponse)},
		{listPortForwardMethod, &apiinstance.ListPortForwardRequest{Instance: identifier}, new(apiinstance.ListPortForwardResponse)},
		{getStatsMethod, &apiinstance.GetStatsRequest{Instance: identifier}, new(apiinstance.GetStatsResponse)},
		{getPrometheusStatsMethod, &apiinstance.GetPrometheusStatsRequest{Instance: identifier}, new(apiinstance.GetPrometheusStatsResponse)},
	}
	snapshot := host.InstanceConfigurations()[0]
	for _, query := range queries {
		t.Run(query.method, func(t *testing.T) {
			response := callManagement(t, host, ctx, query.method, query.request, "", query.response)
			switch response := response.(type) {
			case *apiinstance.DumpRouteResponse:
				if !strings.Contains(response.Result, networkName+"-host") {
					t.Fatal("route dump did not contain the selected core")
				}
			case *apiinstance.ShowNodeInfoResponse:
				if response.GetNodeInfo().GetInstId() != idString || response.GetNodeInfo().GetIpv4Addr() != "10.144.0.1/24" {
					t.Fatal("node query did not return the selected core identity and address")
				}
			}
		})
	}
	if current := host.InstanceConfigurations()[0]; current.Instance != snapshot.Instance || current.ConfigTOML != snapshot.ConfigTOML {
		t.Fatal("read-only instance queries changed the managed configuration")
	}
	application, err := host.CreateInstance(ctx, managementTestConfig(t, "application-query"))
	if err != nil {
		t.Fatal(err)
	}
	if err := application.Start(ctx); err != nil {
		t.Fatal(err)
	}
	applicationID, _, err := parseInstanceUUID(application.ID())
	if err != nil {
		t.Fatal(err)
	}
	applicationInfo := callManagement(t, host, ctx, showNodeInfoRPCMethod,
		&apiinstance.ShowNodeInfoRequest{Instance: &apiinstance.InstanceIdentifier{Selector: &apiinstance.InstanceIdentifier_Id{Id: applicationID}}},
		"", new(apiinstance.ShowNodeInfoResponse)).(*apiinstance.ShowNodeInfoResponse)
	if applicationInfo.GetNodeInfo().GetInstId() != application.ID() {
		t.Fatal("application-owned instance was not available to read-only Web management")
	}
}
