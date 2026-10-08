package engine

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/easytier/easytier/easytier-go/internal/coreabi"
	"github.com/easytier/easytier/easytier-go/platform"
	"github.com/easytier/easytier/easytier-go/platform/netstd"
	apiinstance "github.com/easytier/easytier/easytier-go/proto/api/instance"
	"github.com/easytier/easytier/easytier-go/proto/common"
	"google.golang.org/protobuf/proto"
)

func environmentNodeInfo(ctx context.Context, instance *Instance) (*apiinstance.NodeInfo, error) {
	request, err := proto.Marshal(&common.DirectRpcRequest{
		FullMethodName: "api.instance.PeerManageRpc.ShowNodeInfo",
	})
	if err != nil {
		return nil, err
	}
	encoded, err := instance.RPC(ctx, request)
	if err != nil {
		return nil, err
	}
	var envelope common.RpcResponse
	if err := proto.Unmarshal(encoded, &envelope); err != nil {
		return nil, err
	}
	if envelope.Error != nil {
		return nil, fmt.Errorf("ShowNodeInfo: %s", envelope.Error)
	}
	var response apiinstance.ShowNodeInfoResponse
	if err := proto.Unmarshal(envelope.Response, &response); err != nil {
		return nil, err
	}
	if response.NodeInfo == nil {
		return nil, fmt.Errorf("ShowNodeInfo returned no node info")
	}
	return response.NodeInfo, nil
}

func TestHostEnvironmentUpdateGuestABIAndLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	initialIP := netip.MustParseAddr("192.0.2.21")
	services := netstd.Services()
	services.Snapshot = platform.EnvironmentSnapshot{
		InterfaceIPv4s:    []netip.Addr{initialIP},
		LocalIPs:          []netip.Addr{initialIP},
		MappedListeners:   []string{"tcp://198.51.100.21:11010"},
		ProtectedTCPPorts: []uint16{11010},
	}
	host, err := NewHost(ctx, Options{Services: services})
	if err != nil {
		t.Fatalf("create environment host: %v", err)
	}
	defer host.Close(ctx)
	instance, err := host.CreateInstance(ctx, minimalConfig)
	if err != nil {
		t.Fatalf("create environment instance: %v", err)
	}
	if err := instance.Start(ctx); err != nil {
		t.Fatalf("start environment instance: %v", err)
	}
	initialCore, initialSink := instance.core, instance.packetSink
	before, err := environmentNodeInfo(ctx, instance)
	if err != nil {
		t.Fatal(err)
	}
	check := func(target *Instance, wantIP netip.Addr, wantID string) {
		t.Helper()
		info, err := environmentNodeInfo(ctx, target)
		if err != nil {
			t.Fatal(err)
		}
		addresses := info.GetIpList().GetInterfaceIpv4S()
		if len(addresses) != 1 || addresses[0].GetAddr() != binary.BigEndian.Uint32(wantIP.AsSlice()) {
			t.Fatalf("ShowNodeInfo interfaces = %v, want %s", addresses, wantIP)
		}
		if wantID != "" && info.InstId != wantID {
			t.Fatalf("instance ID = %s, want %s", info.InstId, wantID)
		}
		if target.State() != coreabi.StateRunning {
			t.Fatalf("instance state = %v", target.State())
		}
		if target == instance && (target.core != initialCore || target.packetSink != initialSink) {
			t.Fatal("environment update replaced the running core or packet sink")
		}
	}
	check(instance, initialIP, before.InstId)
	rawUpdate := func(encoded []byte) error {
		host.mu.Lock()
		defer host.mu.Unlock()
		host.guestMu.Lock()
		defer host.guestMu.Unlock()
		core, err := coreabi.New(host.module)
		if err != nil {
			return err
		}
		return core.UpdateEnvironment(ctx, encoded)
	}
	encoded, err := encodeEnvironmentUpdateEnvelope(0, services.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := rawUpdate(encoded); err == nil {
		t.Fatal("guest accepted revision zero")
	}
	check(instance, initialIP, before.InstId)
	var invalidVersion environmentUpdateEnvelope
	if err := json.Unmarshal(encoded, &invalidVersion); err != nil {
		t.Fatal(err)
	}
	invalidVersion.Version++
	invalidVersion.Revision = 1
	encoded, err = json.Marshal(invalidVersion)
	if err != nil {
		t.Fatal(err)
	}
	if err := rawUpdate(encoded); err == nil {
		t.Fatal("guest accepted unsupported environment version")
	}
	check(instance, initialIP, before.InstId)
	updated := platform.EnvironmentSnapshot{
		InterfaceIPv4s: []netip.Addr{netip.MustParseAddr("192.0.2.22")},
		LocalIPs:       []netip.Addr{netip.MustParseAddr("192.0.2.22")},
	}
	if err := host.UpdateEnvironment(ctx, updated); err != nil {
		t.Fatal(err)
	}
	updated.InterfaceIPv4s[0] = netip.MustParseAddr("192.0.2.23")
	updated.LocalIPs[0] = updated.InterfaceIPv4s[0]
	if err := host.UpdateEnvironment(ctx, updated); err != nil {
		t.Fatal(err)
	}
	check(instance, updated.InterfaceIPv4s[0], before.InstId)
	host.mu.Lock()
	if got := host.options.Services.Snapshot.MappedListeners; len(got) != 1 || got[0] != services.Snapshot.MappedListeners[0] {
		host.mu.Unlock()
		t.Fatalf("static mapped listeners changed: %v", got)
	}
	host.mu.Unlock()
	encoded, err = encodeEnvironmentUpdateEnvelope(1, services.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := rawUpdate(encoded); err != nil {
		t.Fatalf("stale update must be a no-op: %v", err)
	}
	check(instance, updated.InterfaceIPv4s[0], before.InstId)
	invalidIPv4 := invalidVersion.Environment
	invalidIPv4.InterfaceIPv4s = []string{"2001:db8::23"}
	invalidIPv6 := invalidVersion.Environment
	invalidIPv6.InterfaceIPv6s = []string{"::ffff:192.0.2.23"}
	for _, invalid := range []struct {
		name        string
		environment environmentSnapshot
	}{
		{"IPv6 as interface IPv4", invalidIPv4},
		{"IPv4-mapped interface IPv6", invalidIPv6},
	} {
		encoded, err = json.Marshal(environmentUpdateEnvelope{
			Version: environmentUpdateVersion, Revision: 3, Environment: invalid.environment,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := rawUpdate(encoded); err == nil {
			t.Fatalf("guest accepted %s", invalid.name)
		}
		check(instance, updated.InterfaceIPv4s[0], before.InstId)
	}
	secondConfig := strings.Replace(minimalConfig, "018f4fb1-7a2c-7d1f-9d89-935b0ad7e135", "018f4fb1-7a2c-7d1f-9d89-935b0ad7e136", 1)
	second, err := host.CreateInstance(ctx, secondConfig)
	if err != nil {
		t.Fatalf("create after update: %v", err)
	}
	if err := second.Start(ctx); err != nil {
		t.Fatal(err)
	}
	check(second, updated.InterfaceIPv4s[0], "018f4fb1-7a2c-7d1f-9d89-935b0ad7e136")
	check(instance, updated.InterfaceIPv4s[0], before.InstId)
}

func TestHostEnvironmentUpdateConcurrentDrivers(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	host, err := NewHost(ctx, Options{Services: netstd.Services()})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close(ctx)
	instance, err := host.CreateInstance(ctx, minimalConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.Start(ctx); err != nil {
		t.Fatal(err)
	}
	before, err := environmentNodeInfo(ctx, instance)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	offlineEndpoint := "tcp://" + listener.Addr().String() + "/environment-test"
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	client, err := host.CreateWebClient(ctx, WebClientOptions{
		Endpoint: offlineEndpoint, MachineID: "018f4fb1-7a2c-7d1f-9d89-935b0ad7e137", Hostname: "environment-test", OSType: "windows",
	})
	if err != nil {
		t.Fatalf("create offline WebClient: %v", err)
	}
	errors := make(chan error, 3)
	start := make(chan struct{})
	var workers sync.WaitGroup
	workers.Add(3)
	go func() {
		defer workers.Done()
		<-start
		for index := 0; index < 12; index++ {
			address := netip.AddrFrom4([4]byte{192, 0, 2, byte(30 + index)})
			if err := host.UpdateEnvironment(ctx, platform.EnvironmentSnapshot{InterfaceIPv4s: []netip.Addr{address}, LocalIPs: []netip.Addr{address}}); err != nil {
				errors <- err
				return
			}
		}
	}()
	go func() {
		defer workers.Done()
		<-start
		packet := []byte{0x45, 0, 0, 20, 0, 0, 0, 0, 64, 17, 0, 0, 10, 88, 0, 1, 10, 88, 0, 2}
		for index := 0; index < 12; index++ {
			if err := instance.SendPacket(ctx, packet); err != nil {
				errors <- err
				return
			}
		}
	}()
	go func() {
		defer workers.Done()
		<-start
		for index := 0; index < 12; index++ {
			info, err := environmentNodeInfo(ctx, instance)
			if err != nil {
				errors <- err
				return
			}
			if info.InstId != before.InstId {
				errors <- fmt.Errorf("instance changed during update: %s", info.InstId)
				return
			}
		}
	}()
	close(start)
	workers.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	if instance.State() != coreabi.StateRunning || client.Connected() {
		t.Fatalf("driver states after refresh: instance=%v web_connected=%v", instance.State(), client.Connected())
	}
	select {
	case <-client.done:
		t.Fatalf("offline WebClient stopped during refresh: %v", client.terminalError())
	default:
	}
}
