package host

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	apiconfig "github.com/easytier/easytier/easytier-go/proto/api/config"
	apiinstance "github.com/easytier/easytier/easytier-go/proto/api/instance"
	"github.com/easytier/easytier/easytier-go/proto/api/manage"
	"github.com/easytier/easytier/easytier-go/proto/common"
	"github.com/pelletier/go-toml/v2"
)

func TestWebManagementCoreCollectionPatches(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	host, err := New(ctx, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close(context.Background())
	id := &common.UUID{Part1: 51, Part2: 52, Part3: 53, Part4: 54}
	idString, networkName := uuidString(id), "collection-patches"
	configTOML, err := encodeInstanceConfig(managementTestConfig(t, networkName))
	if err != nil {
		t.Fatal(err)
	}
	callManagement(t, host, ctx, runNetworkInstanceMethod, &manage.RunNetworkInstanceRequest{
		InstId: id, Config: &manage.NetworkConfig{InstanceId: &idString, NetworkName: &networkName},
		Source: manage.ConfigSource_ConfigSourceWeb,
	}, bindInstanceIdentity(configTOML, idString, networkName), new(manage.RunNetworkInstanceResponse))
	instance := host.InstanceConfigurations()[0].Instance
	identifier := &apiinstance.InstanceIdentifier{Selector: &apiinstance.InstanceIdentifier_Id{Id: id}}
	const mappedURL = "tcp://192.0.2.1:11010"
	preferRelay, directRelay := true, false
	patches := []*apiconfig.InstanceConfigPatch{
		{
			PreferPeerRelay: &preferRelay,
			Routes: []*apiconfig.RoutePatch{{Action: apiconfig.ConfigPatchAction_ADD,
				Cidr: &common.Ipv4Inet{Address: &common.Ipv4Addr{Addr: 0xc0000200}, NetworkLength: 24}}},
			ExitNodes: []*apiconfig.ExitNodePatch{{Action: apiconfig.ConfigPatchAction_ADD,
				Node: &common.IpAddr{Ip: &common.IpAddr_Ipv4{Ipv4: &common.Ipv4Addr{Addr: 0x0a900003}}}}},
			MappedListeners: []*apiconfig.UrlPatch{{Action: apiconfig.ConfigPatchAction_ADD, Url: &common.Url{Url: mappedURL}}},
		},
		{
			PreferPeerRelay: &directRelay,
			Routes:          []*apiconfig.RoutePatch{{Action: apiconfig.ConfigPatchAction_CLEAR}},
			ExitNodes:       []*apiconfig.ExitNodePatch{{Action: apiconfig.ConfigPatchAction_CLEAR}},
			MappedListeners: []*apiconfig.UrlPatch{{Action: apiconfig.ConfigPatchAction_CLEAR}},
		},
	}
	for index, patch := range patches {
		phase := "add"
		if index == 1 {
			phase = "clear"
		}
		if _, err := host.manager.patchConfig(ctx, &apiconfig.PatchConfigRequest{
			Instance: identifier, Patch: patch,
		}); err != nil {
			t.Errorf("managed collection patch %s: %v", phase, err)
		}
		effective := new(apiconfig.GetConfigResponse)
		if err := instance.callRPCRequest(ctx, getConfigMethod, &apiconfig.GetConfigRequest{Instance: identifier}, effective); err != nil {
			t.Fatal(err)
		}
		if effective.Config == nil || effective.TomlConfig == "" {
			t.Fatal("guest returned an incomplete effective configuration")
		}
		desired := callManagement(t, host, ctx, getConfigMethod,
			&apiconfig.GetConfigRequest{Instance: identifier}, "", new(apiconfig.GetConfigResponse)).(*apiconfig.GetConfigResponse)
		if desired.TomlConfig != effective.TomlConfig || host.InstanceConfigurations()[0].ConfigTOML != effective.TomlConfig {
			t.Fatal("managed TOML is out of sync with the running guest")
		}
		var document struct {
			Routes          []string `toml:"routes"`
			ExitNodes       []string `toml:"exit_nodes"`
			MappedListeners []string `toml:"mapped_listeners"`
			Flags           struct {
				PreferPeerRelay bool `toml:"prefer_peer_relay"`
			} `toml:"flags"`
		}
		if err := toml.Unmarshal([]byte(effective.TomlConfig), &document); err != nil {
			t.Fatal(err)
		}
		var routes, exitNodes, mappedListeners []string
		if index == 0 {
			routes, exitNodes, mappedListeners = []string{"192.0.2.0/24"}, []string{"10.144.0.3"}, []string{mappedURL}
		}
		if !slices.Equal(document.Routes, routes) || !slices.Equal(effective.Config.GetRoutes(), routes) || !slices.Equal(desired.Config.GetRoutes(), routes) {
			t.Errorf("routes after %s: TOML=%v effective=%v desired=%v want=%v", phase, document.Routes, effective.Config.GetRoutes(), desired.Config.GetRoutes(), routes)
		}
		if effective.Config.GetEnableManualRoutes() != (index == 0) || desired.Config.GetEnableManualRoutes() != (index == 0) {
			t.Errorf("manual-route mode after %s: effective=%v desired=%v", phase, effective.Config.GetEnableManualRoutes(), desired.Config.GetEnableManualRoutes())
		}
		if document.Flags.PreferPeerRelay != (index == 0) || effective.Config.GetPreferPeerRelay() != (index == 0) || desired.Config.GetPreferPeerRelay() != (index == 0) {
			t.Errorf("peer relay preference after %s: TOML=%v effective=%v desired=%v", phase, document.Flags.PreferPeerRelay, effective.Config.GetPreferPeerRelay(), desired.Config.GetPreferPeerRelay())
		}
		if !slices.Equal(document.ExitNodes, exitNodes) || !slices.Equal(effective.Config.GetExitNodes(), exitNodes) || !slices.Equal(desired.Config.GetExitNodes(), exitNodes) {
			t.Errorf("exit nodes after %s: TOML=%v effective=%v desired=%v want=%v", phase, document.ExitNodes, effective.Config.GetExitNodes(), desired.Config.GetExitNodes(), exitNodes)
		}
		if !slices.Equal(document.MappedListeners, mappedListeners) || !slices.Equal(effective.Config.GetMappedListeners(), mappedListeners) || !slices.Equal(desired.Config.GetMappedListeners(), mappedListeners) {
			t.Errorf("mapped listeners after %s: TOML=%v effective=%v desired=%v want=%v", phase, document.MappedListeners, effective.Config.GetMappedListeners(), desired.Config.GetMappedListeners(), mappedListeners)
		}
		if host.InstanceConfigurations()[0].Instance != instance {
			t.Fatal("collection hot patch replaced the running endpoint")
		}
	}
}

func TestWebManagementAddressPatchPolicy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	host, err := New(ctx, Options{
		WebInstanceConfigPolicy: func(_ context.Context, _ string, document string, requested *manage.NetworkConfig) (string, error) {
			if requested != nil {
				return document, nil
			}
			var addresses struct {
				IPv4 string `toml:"ipv4"`
				IPv6 string `toml:"ipv6"`
			}
			if err := toml.Unmarshal([]byte(document), &addresses); err != nil {
				return "", err
			}
			if addresses.IPv4 == "10.145.0.2/24" {
				return "", errors.New("address overlaps another shared network")
			}
			if addresses.IPv4 != "10.144.0.2/24" || addresses.IPv6 != "fd00:144::2/64" {
				t.Fatalf("address patch policy candidate = %+v", addresses)
			}
			return document, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close(context.Background())
	id := &common.UUID{Part1: 61, Part2: 62, Part3: 63, Part4: 64}
	idString, networkName := uuidString(id), "address-patches"
	document, err := encodeInstanceConfig(managementTestConfig(t, networkName))
	if err != nil {
		t.Fatal(err)
	}
	callManagement(t, host, ctx, runNetworkInstanceMethod, &manage.RunNetworkInstanceRequest{
		InstId: id, Config: &manage.NetworkConfig{InstanceId: &idString, NetworkName: &networkName},
		Source: manage.ConfigSource_ConfigSourceWeb,
	}, bindInstanceIdentity(document, idString, networkName), new(manage.RunNetworkInstanceResponse))
	instance := host.Instances()[0]
	identifier := &apiinstance.InstanceIdentifier{Selector: &apiinstance.InstanceIdentifier_Id{Id: id}}
	hostname := "partially-applied-name"
	_, err = host.manager.patchConfig(ctx, &apiconfig.PatchConfigRequest{
		Instance: identifier,
		Patch: &apiconfig.InstanceConfigPatch{
			Hostname: &hostname,
			Ipv4:     &common.Ipv4Inet{Address: &common.Ipv4Addr{Addr: 0x0a910002}, NetworkLength: 24},
		},
	})
	if err == nil {
		t.Fatal("overlapping address patch was accepted")
	}
	info, err := instance.ShowNodeInfo(ctx)
	if err != nil || info.GetIpv4Addr() != "10.144.0.1/24" || info.GetHostname() != networkName+"-host" {
		t.Fatalf("rejected patch changed node info: %+v, %v", info, err)
	}
	_, err = host.manager.patchConfig(ctx, &apiconfig.PatchConfigRequest{
		Instance: identifier,
		Patch: &apiconfig.InstanceConfigPatch{
			Ipv4: &common.Ipv4Inet{Address: &common.Ipv4Addr{Addr: 0x0a900002}, NetworkLength: 24},
			Ipv6: &common.Ipv6Inet{Address: &common.Ipv6Addr{Part1: 0xfd000144, Part4: 2}, NetworkLength: 64},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	info, err = instance.ShowNodeInfo(ctx)
	if err != nil || info.GetIpv4Addr() != "10.144.0.2/24" {
		t.Fatalf("patched node address: %+v, %v", info, err)
	}
	snapshot := host.InstanceConfigurations()[0]
	var addresses struct {
		IPv4 string `toml:"ipv4"`
		IPv6 string `toml:"ipv6"`
	}
	if err := toml.Unmarshal([]byte(snapshot.ConfigTOML), &addresses); err != nil {
		t.Fatal(err)
	}
	if snapshot.Instance != instance || addresses.IPv4 != "10.144.0.2/24" || addresses.IPv6 != "fd00:144::2/64" {
		t.Fatalf("address patch snapshot = %+v", addresses)
	}
	desired, err := host.manager.getConfig(ctx, &apiconfig.GetConfigRequest{Instance: identifier})
	if err != nil || desired.GetConfig().GetVirtualIpv4() != "10.144.0.2" || desired.GetConfig().GetNetworkLength() != 24 {
		t.Fatalf("desired address patch: %+v, %v", desired, err)
	}
}
