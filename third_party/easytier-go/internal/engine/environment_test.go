package engine

import (
	"context"
	"encoding/json"
	"net/netip"
	"reflect"
	"strings"
	"testing"

	"github.com/easytier/easytier/easytier-go/internal/coreabi"
	"github.com/easytier/easytier/easytier-go/platform"
	"github.com/metacubex/wazero/api"
	"github.com/metacubex/wazero/experimental/wazerotest"
)

func TestHostEnvironmentUpdateCommitsAfterGuestSuccess(t *testing.T) {
	memory := wazerotest.NewMemory(64 * 1024)
	nextPointer := uint32(1024)
	freed := 0
	var updateStatus int32
	var received []environmentUpdateEnvelope
	function := func(name string, call any) *wazerotest.Function {
		fn := wazerotest.NewFunction(call)
		fn.ExportNames = []string{name}
		return fn
	}
	module := wazerotest.NewModule(memory,
		function("easytier_data_plane_abi_version", func(context.Context, api.Module) uint32 {
			return coreabi.DataPlaneABIVersion
		}),
		function("easytier_data_plane_capabilities", func(context.Context, api.Module) uint64 {
			return coreabi.DataPlaneCapability | coreabi.DataPlaneTCPCapability | coreabi.DataPlaneUDPCapability
		}),
		function("easytier_rpc_abi_version", func(context.Context, api.Module) uint32 {
			return coreabi.RPCABIVersion
		}),
		function("easytier_buffer_alloc", func(_ context.Context, _ api.Module, length uint32) uint32 {
			pointer := nextPointer
			nextPointer += length + 8
			return pointer
		}),
		function("easytier_buffer_free", func(context.Context, api.Module, uint32) int32 {
			freed++
			return 0
		}),
		function("easytier_environment_update", func(_ context.Context, _ api.Module, pointer, length uint32) int32 {
			encoded, ok := memory.Read(pointer, length)
			if !ok {
				t.Fatal("guest environment payload is outside memory")
			}
			var envelope environmentUpdateEnvelope
			if err := json.Unmarshal(encoded, &envelope); err != nil {
				t.Fatalf("decode guest environment payload: %v", err)
			}
			received = append(received, envelope)
			return updateStatus
		}),
		function("easytier_instance_error_len", func(context.Context, api.Module, uint64) uint32 {
			return 0
		}),
	)
	initial := platform.EnvironmentSnapshot{
		InterfaceIPv4s:    []netip.Addr{netip.MustParseAddr("192.0.2.1")},
		MappedListeners:   []string{"tcp://192.0.2.1:11010"},
		ProtectedTCPPorts: []uint16{11010},
	}
	instance := &Instance{completions: make(chan struct{}, 1)}
	webClient := &WebClient{completions: make(chan struct{}, 1)}
	host := &Host{
		module:    module,
		options:   Options{Services: platform.Services{Snapshot: initial}},
		instances: map[*Instance]struct{}{instance: {}},
		webClient: webClient,
	}
	ipv4 := netip.MustParseAddr("192.0.2.2")
	ipv6 := netip.MustParseAddr("2001:db8::2")
	updated := platform.EnvironmentSnapshot{
		PublicIPv4:        &ipv4,
		InterfaceIPv4s:    []netip.Addr{ipv4},
		InterfaceIPv6s:    []netip.Addr{ipv6},
		LocalIPs:          []netip.Addr{ipv4, ipv6},
		MappedListeners:   []string{"tcp://discarded:12000"},
		ProtectedTCPPorts: []uint16{12000},
		PreferredIPv6Sources: []platform.PreferredIPv6Source{{
			IP: ipv6, IfIndex: 7,
		}},
	}
	if err := host.UpdateEnvironment(context.Background(), updated); err != nil {
		t.Fatalf("update environment: %v", err)
	}
	if len(received) != 1 || received[0].Version != 1 || received[0].Revision != 1 {
		t.Fatalf("guest envelopes = %+v", received)
	}
	if got := received[0].Environment.InterfaceIPv4s; !reflect.DeepEqual(got, []string{"192.0.2.2"}) {
		t.Fatalf("guest interface IPv4s = %v", got)
	}
	if got := received[0].Environment.MappedListeners; !reflect.DeepEqual(got, initial.MappedListeners) {
		t.Fatalf("guest mapped listeners = %v", got)
	}
	if host.environmentRevision != 1 || len(instance.completions) != 1 || len(webClient.completions) != 1 {
		t.Fatal("successful environment update was not committed and signalled")
	}
	committed := host.options.Services.Snapshot
	ipv4 = netip.MustParseAddr("192.0.2.3")
	updated.InterfaceIPv4s[0] = ipv4
	updated.LocalIPs[0] = ipv4
	updated.PreferredIPv6Sources[0].IfIndex = 8
	if committed.PublicIPv4.String() != "192.0.2.2" || committed.InterfaceIPv4s[0].String() != "192.0.2.2" || committed.PreferredIPv6Sources[0].IfIndex != 7 {
		t.Fatal("committed environment aliases the caller's snapshot")
	}
	<-instance.completions
	<-webClient.completions
	updateStatus = -3
	err := host.UpdateEnvironment(context.Background(), updated)
	if err == nil || !strings.Contains(err.Error(), "revision 2") || !strings.Contains(err.Error(), "status=-3") {
		t.Fatalf("failed guest update error = %v", err)
	}
	if host.environmentRevision != 1 || !reflect.DeepEqual(host.options.Services.Snapshot, committed) {
		t.Fatal("failed update changed the committed snapshot")
	}
	if len(instance.completions) != 0 || len(webClient.completions) != 0 || freed != 2 {
		t.Fatalf("failed update signals or buffer cleanup: instance=%d web=%d free=%d", len(instance.completions), len(webClient.completions), freed)
	}
	updateStatus = 0
	if err := host.UpdateEnvironment(context.Background(), updated); err != nil {
		t.Fatalf("retry uncommitted environment: %v", err)
	}
	if host.environmentRevision != 2 || received[2].Revision != 2 {
		t.Fatalf("retry revision = %d / %d", host.environmentRevision, received[2].Revision)
	}
}
