package host

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"testing"
	"time"

	apiconfig "github.com/easytier/easytier/easytier-go/proto/api/config"
	apiinstance "github.com/easytier/easytier/easytier-go/proto/api/instance"
	"github.com/easytier/easytier/easytier-go/proto/api/manage"
	"github.com/easytier/easytier/easytier-go/proto/common"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

func TestWebManagementConfigPolicyAndRecovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var calls []string
	host, err := New(ctx, Options{
		WebInstanceConfigPolicy: func(_ context.Context, instanceID, configTOML string, requested *manage.NetworkConfig) (string, error) {
			calls = append(calls, instanceID)
			if requested.GetEnableManualRoutes() != true || len(requested.GetRoutes()) != 1 || requested.GetRoutes()[0] != "192.0.2.0/24" ||
				len(requested.GetExitNodes()) != 1 || requested.GetExitNodes()[0] != "10.0.0.2" || requested.EnableExitNode == nil || requested.GetEnableExitNode() {
				t.Fatalf("policy requested config lost routing fields: %+v", requested)
			}
			requested.Routes[0] = "198.51.100.0/24"
			requested.ExitNodes[0] = "10.0.0.3"
			if strings.Contains(configTOML, "reject-policy") {
				return "", errors.New("policy rejected test configuration")
			}
			return configTOML + "\n# policy-applied\n", nil
		},
	})
	if err != nil {
		t.Fatalf("create host: %v", err)
	}
	defer host.Close(ctx)

	id := &common.UUID{Part1: 11, Part2: 12, Part3: 13, Part4: 14}
	idString := uuidString(id)
	config := managementTestConfig(t, "policy")
	configTOML, err := encodeInstanceConfig(config)
	if err != nil {
		t.Fatalf("encode config: %v", err)
	}
	networkName := "policy"
	enableManualRoutes, enableExitNode := true, false
	networkConfig := &manage.NetworkConfig{
		InstanceId: &idString, NetworkName: &networkName,
		EnableManualRoutes: &enableManualRoutes, Routes: []string{"192.0.2.0/24"},
		ExitNodes: []string{"10.0.0.2"}, EnableExitNode: &enableExitNode,
	}
	callManagement(
		t,
		host,
		ctx,
		runNetworkInstanceMethod,
		&manage.RunNetworkInstanceRequest{
			InstId: id,
			Config: networkConfig,
			Source: manage.ConfigSource_ConfigSourceWeb,
		},
		bindInstanceIdentity(configTOML, idString, networkName),
		new(manage.RunNetworkInstanceResponse),
	)
	snapshot := host.InstanceConfigurations()
	if len(snapshot) != 1 || !snapshot[0].WebOwned || snapshot[0].Instance == nil ||
		snapshot[0].InstanceID != idString || !strings.Contains(snapshot[0].ConfigTOML, "policy-applied") {
		t.Fatalf("policy snapshot = %+v", snapshot)
	}
	oldInstance := snapshot[0].Instance

	_, err = host.manager.runNetworkInstance(
		ctx,
		&manage.RunNetworkInstanceRequest{
			InstId:    id,
			Config:    proto.Clone(networkConfig).(*manage.NetworkConfig),
			Overwrite: true,
			Source:    manage.ConfigSource_ConfigSourceWeb,
		},
		"reject-policy",
		id,
	)
	if err == nil || !strings.Contains(err.Error(), "policy rejected") {
		t.Fatalf("policy rejection error = %v", err)
	}
	snapshot = host.InstanceConfigurations()
	if len(snapshot) != 1 || snapshot[0].Instance != oldInstance {
		t.Fatalf("policy rejection removed old instance: %+v", snapshot)
	}

	_, err = host.manager.runNetworkInstance(
		ctx,
		&manage.RunNetworkInstanceRequest{
			InstId:    id,
			Config:    proto.Clone(networkConfig).(*manage.NetworkConfig),
			Overwrite: true,
			Source:    manage.ConfigSource_ConfigSourceWeb,
		},
		"[",
		id,
	)
	if err == nil {
		t.Fatal("invalid replacement unexpectedly succeeded")
	}
	snapshot = host.InstanceConfigurations()
	if len(snapshot) != 1 || snapshot[0].Instance == oldInstance ||
		!strings.Contains(snapshot[0].ConfigTOML, "policy-applied") {
		t.Fatalf("recovery snapshot = %+v", snapshot)
	}
	if len(calls) < 4 {
		t.Fatalf("policy calls = %d, want create, rejected overwrite, replacement and recovery", len(calls))
	}
}

func TestApplicationInstanceIsReadOnlyToWebManagement(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	host, err := New(ctx, Options{})
	if err != nil {
		t.Fatalf("create host: %v", err)
	}
	defer host.Close(ctx)

	instance, err := host.CreateInstance(ctx, managementTestConfig(t, "application"))
	if err != nil {
		t.Fatalf("create application instance: %v", err)
	}
	if instance.ID() == "" {
		t.Fatal("application instance ID is empty")
	}
	if got := host.Instances(); len(got) != 1 || got[0] != instance {
		t.Fatalf("host instances = %v, want application instance", got)
	}
	entry := host.manager.snapshot()[0]
	meta := host.manager.listNetworkInstanceMeta(
		&manage.ListNetworkInstanceMetaRequest{
			InstIds: []*common.UUID{entry.id},
		},
	)
	if len(meta.Metas) != 1 || meta.Metas[0].ConfigPermission != 3 {
		t.Fatalf("application metadata = %v, want read-only/no-delete", meta.Metas)
	}
	if _, err := host.manager.getNetworkInstanceConfig(
		&manage.GetNetworkInstanceConfigRequest{InstId: entry.id},
	); err == nil {
		t.Fatal("application config was writable through Web management")
	}
	response, err := host.manager.deleteNetworkInstances(
		ctx,
		&manage.DeleteNetworkInstanceRequest{
			InstIds: []*common.UUID{entry.id},
		},
	)
	if err != nil {
		t.Fatalf("delete application instance through Web management: %v", err)
	}
	if len(response.RemainInstIds) != 1 {
		t.Fatalf("remaining IDs = %v, want application instance", response.RemainInstIds)
	}
	if err := instance.Close(ctx); err != nil {
		t.Fatalf("close application instance: %v", err)
	}
	if len(host.Instances()) != 0 {
		t.Fatal("closed application instance remained registered")
	}
}

func TestWebManagementRunsCollectsAndDeletesInstance(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	host, err := New(ctx, Options{})
	if err != nil {
		t.Fatalf("create host: %v", err)
	}
	defer host.Close(ctx)

	id := &common.UUID{Part1: 1, Part2: 2, Part3: 3, Part4: 4}
	idString := uuidString(id)
	config := managementTestConfig(t, "managed")
	configTOML, err := encodeInstanceConfig(config)
	if err != nil {
		t.Fatalf("encode managed config: %v", err)
	}
	networkName := "managed"
	networkConfig := &manage.NetworkConfig{
		InstanceId:  &idString,
		NetworkName: &networkName,
	}
	unknownConfig := protowire.AppendTag(nil, 1000, protowire.BytesType)
	unknownConfig = protowire.AppendString(unknownConfig, "future-config")
	networkConfig.ProtoReflect().SetUnknown(unknownConfig)
	run := callManagement(
		t,
		host,
		ctx,
		runNetworkInstanceMethod,
		&manage.RunNetworkInstanceRequest{
			InstId: id,
			Config: networkConfig,
			Source: manage.ConfigSource_ConfigSourceWeb,
		},
		bindInstanceIdentity(configTOML, idString, networkName),
		new(manage.RunNetworkInstanceResponse),
	)
	if run.(*manage.RunNetworkInstanceResponse).GetInstId() == nil {
		t.Fatal("run response omitted instance ID")
	}
	if instances := host.Instances(); len(instances) != 1 ||
		instances[0].ID() != idString {
		t.Fatalf("host instances = %v, want managed instance %s", instances, idString)
	}
	listed := callManagement(
		t,
		host,
		ctx,
		listNetworkInstanceMethod,
		new(manage.ListNetworkInstanceRequest),
		"",
		new(manage.ListNetworkInstanceResponse),
	).(*manage.ListNetworkInstanceResponse)
	if len(listed.InstIds) != 1 {
		t.Fatalf("listed IDs = %v, want managed instance", listed.InstIds)
	}
	metas := callManagement(
		t,
		host,
		ctx,
		listNetworkInstanceMetaMethod,
		&manage.ListNetworkInstanceMetaRequest{
			InstIds: []*common.UUID{id},
		},
		"",
		new(manage.ListNetworkInstanceMetaResponse),
	).(*manage.ListNetworkInstanceMetaResponse)
	if len(metas.Metas) != 1 || metas.Metas[0].ConfigPermission != 0 {
		t.Fatalf("managed metadata = %v, want writable instance", metas.Metas)
	}
	retained := callManagement(
		t,
		host,
		ctx,
		retainNetworkInstanceMethod,
		&manage.RetainNetworkInstanceRequest{
			InstIds: []*common.UUID{id},
		},
		"",
		new(manage.RetainNetworkInstanceResponse),
	).(*manage.RetainNetworkInstanceResponse)
	if len(retained.RemainInstIds) != 1 {
		t.Fatalf("retained IDs = %v, want managed instance", retained.RemainInstIds)
	}

	collected := callManagement(
		t,
		host,
		ctx,
		collectNetworkInfoMethod,
		&manage.CollectNetworkInfoRequest{InstIds: []*common.UUID{id}},
		"",
		new(manage.CollectNetworkInfoResponse),
	).(*manage.CollectNetworkInfoResponse)
	info := collected.GetInfo().GetMap()[idString]
	if info == nil || !info.Running || info.MyNodeInfo == nil {
		t.Fatalf("managed instance status = %v, want running node info", info)
	}
	if info.MyNodeInfo.Hostname != "managed-host" ||
		info.MyNodeInfo.PeerId == 0 {
		t.Fatalf("managed node info = %v, want live node identity", info.MyNodeInfo)
	}

	managedConfig := callManagement(
		t,
		host,
		ctx,
		getNetworkInstanceConfigMethod,
		&manage.GetNetworkInstanceConfigRequest{InstId: id},
		"",
		new(manage.GetNetworkInstanceConfigResponse),
	).(*manage.GetNetworkInstanceConfigResponse)
	if managedConfig.GetConfig().GetNetworkName() != networkName ||
		managedConfig.Source != manage.ConfigSource_ConfigSourceWeb {
		t.Fatalf("managed config response = %v", managedConfig)
	}
	if unknown := managedConfig.Config.ProtoReflect().GetUnknown(); !bytes.Equal(
		unknown,
		unknownConfig,
	) {
		t.Fatalf("managed config unknown fields = %x, want %x", unknown, unknownConfig)
	}

	disableRelayData := true
	preferPeerRelay := true
	patchedHostname := "patched-hostname"
	identifier := &apiinstance.InstanceIdentifier{
		Selector: &apiinstance.InstanceIdentifier_Id{Id: id},
	}
	callManagement(
		t,
		host,
		ctx,
		patchConfigMethod,
		&apiconfig.PatchConfigRequest{
			Instance: identifier,
			Patch: &apiconfig.InstanceConfigPatch{
				Hostname:         &patchedHostname,
				DisableRelayData: &disableRelayData,
				PreferPeerRelay:  &preferPeerRelay,
			},
		},
		"",
		new(apiconfig.PatchConfigResponse),
	)
	patchedConfig := callManagement(
		t,
		host,
		ctx,
		getConfigMethod,
		&apiconfig.GetConfigRequest{Instance: identifier},
		"",
		new(apiconfig.GetConfigResponse),
	).(*apiconfig.GetConfigResponse).Config
	if patchedConfig == nil || !patchedConfig.GetDisableRelayData() {
		t.Fatalf("patched config = %v, want relay data disabled", patchedConfig)
	}
	if !patchedConfig.GetPreferPeerRelay() {
		t.Fatal("patched config lost peer relay preference")
	}
	if patchedConfig.GetHostname() != patchedHostname {
		t.Fatalf("patched hostname = %q, want %q", patchedConfig.GetHostname(), patchedHostname)
	}
	nodeInfo, err := host.Instances()[0].ShowNodeInfo(ctx)
	if err != nil || nodeInfo.GetHostname() != patchedHostname {
		t.Fatalf("runtime patched hostname = %q, err=%v", nodeInfo.GetHostname(), err)
	}
	if unknown := patchedConfig.ProtoReflect().GetUnknown(); !bytes.Equal(
		unknown,
		unknownConfig,
	) {
		t.Fatalf("patched config unknown fields = %x, want %x", unknown, unknownConfig)
	}

	originalInstance := host.manager.snapshot()[0].instance
	replacementConfig := proto.Clone(networkConfig).(*manage.NetworkConfig)
	_, err = host.manager.runNetworkInstance(
		ctx,
		&manage.RunNetworkInstanceRequest{
			InstId:    id,
			Config:    replacementConfig,
			Overwrite: true,
			Source:    manage.ConfigSource_ConfigSourceWeb,
		},
		"[",
		id,
	)
	if err == nil {
		t.Fatal("invalid replacement config unexpectedly succeeded")
	}
	restoredEntries := host.manager.snapshot()
	if len(restoredEntries) != 1 || restoredEntries[0].instance == originalInstance {
		t.Fatalf("restored entries = %v, want one replacement instance", restoredEntries)
	}
	effectiveConfig := new(apiconfig.GetConfigResponse)
	if err := restoredEntries[0].instance.callRPCRequest(
		ctx,
		getConfigMethod,
		&apiconfig.GetConfigRequest{Instance: identifier},
		effectiveConfig,
	); err != nil {
		t.Fatalf("get restored runtime config: %v", err)
	}
	if effectiveConfig.Config == nil ||
		!effectiveConfig.Config.GetDisableRelayData() || !effectiveConfig.Config.GetPreferPeerRelay() || effectiveConfig.Config.GetHostname() != patchedHostname {
		t.Fatalf("restored runtime config = %v, want relay data disabled and patched hostname", effectiveConfig.Config)
	}
	if effectiveConfig.TomlConfig == "" ||
		effectiveConfig.TomlConfig != restoredEntries[0].configTOML {
		t.Fatal("restored TOML config does not match the running Core")
	}

	deleted := callManagement(
		t,
		host,
		ctx,
		deleteNetworkInstanceMethod,
		&manage.DeleteNetworkInstanceRequest{InstIds: []*common.UUID{id}},
		"",
		new(manage.DeleteNetworkInstanceResponse),
	).(*manage.DeleteNetworkInstanceResponse)
	if len(deleted.RemainInstIds) != 0 || len(host.Instances()) != 0 {
		t.Fatalf("managed instance remained after delete: %v", deleted.RemainInstIds)
	}
}

func callManagement(
	t *testing.T,
	host *Host,
	ctx context.Context,
	method string,
	request proto.Message,
	preparedConfig string,
	response proto.Message,
) proto.Message {
	t.Helper()
	encodedRequest, err := proto.Marshal(request)
	if err != nil {
		t.Fatalf("encode %s request: %v", method, err)
	}
	var prepared *string
	var preparedInstanceID *common.UUID
	if preparedConfig != "" {
		prepared = &preparedConfig
	}
	if request, ok := request.(*manage.RunNetworkInstanceRequest); ok {
		preparedInstanceID = request.InstId
	}
	encodedEnvelope, err := proto.Marshal(&common.HostManagementRequest{
		Rpc: &common.DirectRpcRequest{
			FullMethodName: method,
			Request:        encodedRequest,
		},
		PreparedConfig:     prepared,
		PreparedInstanceId: preparedInstanceID,
	})
	if err != nil {
		t.Fatalf("encode %s envelope: %v", method, err)
	}
	var result common.RpcResponse
	if err := proto.Unmarshal(host.manager.handle(ctx, encodedEnvelope), &result); err != nil {
		t.Fatalf("decode %s envelope: %v", method, err)
	}
	if result.Error != nil {
		t.Fatalf("%s failed: %v", method, result.Error)
	}
	if err := proto.Unmarshal(result.Response, response); err != nil {
		t.Fatalf("decode %s response: %v", method, err)
	}
	return response
}

func managementTestConfig(t *testing.T, name string) InstanceConfig {
	t.Helper()
	config, err := NewInstanceConfigBuilder(name).
		NetworkSecret("test").
		Hostname(fmt.Sprintf("%s-host", name)).
		IPv4(netip.MustParsePrefix("10.144.0.1/24")).
		P2P(P2PPolicy{Disable: true}).
		Encryption(false).
		Build()
	if err != nil {
		t.Fatalf("build management test config: %v", err)
	}
	return config
}
