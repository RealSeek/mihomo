//go:build !no_easytier

package outbound

import (
	"context"
	"encoding/json"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corehost "github.com/easytier/easytier/easytier-go"
	"github.com/easytier/easytier/easytier-go/proto/api/manage"
	"github.com/gofrs/uuid/v5"
	"github.com/metacubex/mihomo/common/structure"
	"github.com/metacubex/mihomo/component/easytier"
	C "github.com/metacubex/mihomo/constant"
	D "github.com/miekg/dns"
	"github.com/pelletier/go-toml/v2"
)

func TestEasyTierWebClientOptionValidation(t *testing.T) {
	valid := EasyTierOption{
		Name: "managed", PacketMode: true,
		WebClient: &EasyTierWebClientOption{Endpoint: "tcp://config.example.test:22020"},
	}
	adapter, err := NewEasyTier(valid)
	if err != nil {
		t.Fatalf("valid web-client option: %v", err)
	}
	defer adapter.Close()
	disabled := false
	for _, test := range []struct {
		name   string
		option EasyTierOption
	}{
		{name: "requires-packet-mode", option: EasyTierOption{Name: "managed", WebClient: valid.WebClient}},
		{name: "cannot-use-static-network", option: EasyTierOption{Name: "managed", PacketMode: true, NetworkName: "network", WebClient: valid.WebClient}},
		{name: "cannot-use-toml", option: EasyTierOption{Name: "managed", PacketMode: true, ConfigTOML: "ipv4 = '10.0.0.1/24'", WebClient: valid.WebClient}},
		{name: "requires-endpoint", option: EasyTierOption{Name: "managed", PacketMode: true, WebClient: &EasyTierWebClientOption{}}},
		{name: "invalid-machine-id", option: EasyTierOption{Name: "managed", PacketMode: true, WebClient: &EasyTierWebClientOption{Endpoint: "token", MachineID: "not-a-uuid"}}},
		{name: "invalid-endpoint-scheme", option: EasyTierOption{Name: "managed", PacketMode: true, WebClient: &EasyTierWebClientOption{Endpoint: "https://config.example.test"}}},
		{name: "cannot-use-static-ipv4", option: EasyTierOption{Name: "managed", PacketMode: true, IPv4: "10.144.0.1/24", WebClient: valid.WebClient}},
		{name: "cannot-use-static-dhcp", option: EasyTierOption{Name: "managed", PacketMode: true, DHCP: true, WebClient: valid.WebClient}},
		{name: "cannot-use-empty-stun", option: EasyTierOption{Name: "managed", PacketMode: true, STUNServers: []string{}, WebClient: valid.WebClient}},
		{name: "cannot-use-tcp-stun", option: EasyTierOption{Name: "managed", PacketMode: true, TCPSTUNServers: []string{"stun.example.test:3478"}, WebClient: valid.WebClient}},
		{name: "cannot-use-static-policy", option: EasyTierOption{Name: "managed", PacketMode: true, DisableP2P: &disabled, WebClient: valid.WebClient}},
		{name: "cannot-use-relay-policy", option: EasyTierOption{Name: "managed", PacketMode: true, DisableRelayData: &disabled, WebClient: valid.WebClient}},
		{name: "cannot-use-port-forwards", option: EasyTierOption{Name: "managed", PacketMode: true, PortForwards: []easytier.PortForwardOption{{Protocol: easytier.PortForwardTCP, Bind: "127.0.0.1:0", Destination: "10.144.0.1:8080"}}, WebClient: valid.WebClient}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewEasyTier(test.option); err == nil {
				t.Fatal("expected option validation error")
			}
		})
	}
}

func TestEasyTierWebConfigPolicyExistingInstances(t *testing.T) {
	previousHome := C.Path.HomeDir()
	C.SetHomeDir(t.TempDir())
	t.Cleanup(func() { C.SetHomeDir(previousHome) })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	host, err := corehost.New(ctx, corehost.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close(ctx)
	existing, err := host.CreateInstanceTOML(ctx, "existing", "", `
ipv4 = '10.144.0.1'
ipv6 = 'fd00:144::1/64'
listeners = []
stun_servers = []
stun_servers_v6 = []
[network_identity]
network_name = 'existing'
network_secret = 'private-secret'
[flags]
tld_dns_zone = 'existing.et.net.'
no_tun = true
bind_device = false
`)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := NewEasyTier(EasyTierOption{Name: "managed-policy", PacketMode: true, WebClient: &EasyTierWebClientOption{Endpoint: "token"}})
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	adapter.host = host
	for _, test := range []struct {
		name, config string
	}{
		{"ipv4-prefix", "ipv4 = '10.144.0.2'\n[flags]\ntld_dns_zone = 'other.et.net.'"},
		{"ipv6-prefix", "ipv6 = 'fd00:144::2/64'\n[flags]\ntld_dns_zone = 'other.et.net.'"},
		{"ipv6-bare-prefix", "ipv6 = 'fd00:144::2'\n[flags]\ntld_dns_zone = 'other.et.net.'"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := adapter.prepareWebInstanceConfig(ctx, "new-instance", test.config, nil)
			if err == nil || !strings.Contains(err.Error(), existing.ID()) {
				t.Fatalf("conflicting candidate error = %v", err)
			}
			if snapshots := host.InstanceConfigurations(); len(snapshots) != 1 || snapshots[0].Instance != existing {
				t.Fatal("candidate validation replaced the existing instance")
			}
		})
	}
	if _, err := adapter.prepareWebInstanceConfig(ctx, "new-instance", "ipv4 = '10.145.0.1/24'\n[flags]\ntld_dns_zone = 'EXISTING.et.net.'", nil); err != nil {
		t.Fatalf("same-zone network with a distinct overlay prefix rejected: %v", err)
	}
	prepared, err := adapter.prepareWebInstanceConfig(ctx, existing.ID(), `
ipv4 = '10.144.0.2/24'
[flags]
tld_dns_zone = 'existing.et.net.'
no_tun = true
bind_device = true
`, nil)
	if err != nil {
		t.Fatalf("self replacement rejected: %v", err)
	}
	var document struct {
		Flags struct {
			NoTun      bool `toml:"no_tun"`
			BindDevice bool `toml:"bind_device"`
		} `toml:"flags"`
	}
	if err := toml.Unmarshal([]byte(prepared), &document); err != nil {
		t.Fatal(err)
	}
	if document.Flags.NoTun || document.Flags.BindDevice {
		t.Fatalf("remote config bypassed shared packet ownership: %+v", document.Flags)
	}
	if _, err := adapter.prepareWebInstanceConfig(ctx, "new-instance", "ipv6 = 'fd00:145::9'\n[flags]\ntld_dns_zone = 'other.et.net.'", nil); err != nil {
		t.Fatalf("valid bare IPv6 configuration-center candidate rejected: %v", err)
	}
}

func TestEasyTierWebConfigPolicyRequestedRouting(t *testing.T) {
	previousHome := C.Path.HomeDir()
	C.SetHomeDir(t.TempDir())
	t.Cleanup(func() { C.SetHomeDir(previousHome) })
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	adapter, err := NewEasyTier(EasyTierOption{Name: "managed-routing", PacketMode: true, WebClient: &EasyTierWebClientOption{Endpoint: "token"}})
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	host, err := corehost.New(ctx, corehost.Options{})
	if err != nil {
		t.Fatal(err)
	}
	adapter.host = host
	const instanceID = "00112233-4455-6677-8899-aabbccddeeff"
	const input = `
ipv4 = '10.144.0.1/24'
listeners = []
stun_servers = []
stun_servers_v6 = []
routes = ['198.51.100.0/24']
exit_nodes = ['10.144.0.2']
[network_identity]
network_name = 'managed-routing'
network_secret = 'test-secret'
[flags]
enable_exit_node = true
disable_p2p = true
`
	enabled, disabled := true, false
	requested := &manage.NetworkConfig{
		EnableManualRoutes: &enabled, Routes: []string{"192.0.2.0/24"},
		ExitNodes: []string{"10.144.0.9"}, EnableExitNode: &disabled,
	}
	prepared, err := adapter.prepareWebInstanceConfig(ctx, instanceID, input, requested)
	if err != nil {
		t.Fatal(err)
	}
	_, metadata, err := easytier.PrepareNativeConfig(prepared, true)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Routes == nil || len(*metadata.Routes) != 1 || (*metadata.Routes)[0] != "192.0.2.0/24" ||
		len(metadata.ExitNodes) != 1 || metadata.ExitNodes[0] != "10.144.0.9" || metadata.EnableExitNode == nil || *metadata.EnableExitNode {
		t.Fatalf("requested routing metadata lost: %+v", metadata)
	}
	var document struct {
		Flags struct {
			DisableP2P bool `toml:"disable_p2p"`
		} `toml:"flags"`
	}
	if err := toml.Unmarshal([]byte(prepared), &document); err != nil || !document.Flags.DisableP2P {
		t.Fatalf("unrelated native field lost: %+v, %v", document, err)
	}
	instance, err := host.CreateInstanceTOML(ctx, "managed-routing", instanceID, prepared)
	if err != nil {
		t.Fatalf("create core from restored routing config: %v", err)
	}
	if err := instance.Start(ctx); err != nil {
		t.Fatalf("start core from restored routing config: %v", err)
	}
	session, err := adapter.packetSession(ctx, instance, metadata, instance, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("0.0.0.0/0")} {
		found := false
		for _, route := range session.Routes {
			found = found || route == expected
		}
		if !found {
			t.Fatalf("restored route %s missing from actual core packet session: %+v", expected, session)
		}
	}
	for _, manual := range []*bool{nil, &disabled} {
		prepared, err := adapter.prepareWebInstanceConfig(ctx, instanceID, input, &manage.NetworkConfig{EnableManualRoutes: manual, Routes: requested.Routes})
		if err != nil {
			t.Fatal(err)
		}
		_, metadata, err := easytier.PrepareNativeConfig(prepared, true)
		if err != nil || metadata.Routes != nil || len(metadata.ExitNodes) != 0 {
			t.Fatalf("automatic routing retained manual routes or stale exits: %+v, %v", metadata, err)
		}
	}
	prepared, err = adapter.prepareWebInstanceConfig(ctx, instanceID, input, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, metadata, err = easytier.PrepareNativeConfig(prepared, true)
	if err != nil || metadata.Routes == nil || len(*metadata.Routes) != 1 || (*metadata.Routes)[0] != "198.51.100.0/24" ||
		len(metadata.ExitNodes) != 1 || metadata.ExitNodes[0] != "10.144.0.2" {
		t.Fatalf("native TOML without a requested protobuf was changed: %+v, %v", metadata, err)
	}
}

func TestEasyTierDNSReplyUnsupportedTypeNameExistence(t *testing.T) {
	nodes := []easytier.Node{{Hostname: "desktop", IPv4: netip.MustParseAddr("10.144.0.1")}}
	for _, recordType := range []uint16{D.TypeTXT, D.TypeHTTPS} {
		for _, test := range []struct {
			name  string
			rcode int
		}{
			{"desktop.et.net.", D.RcodeSuccess},
			{"missing.et.net.", D.RcodeNameError},
		} {
			request := new(D.Msg)
			request.SetQuestion(test.name, recordType)
			reply := easyTierDNSReply(request, "et.net", nodes)
			if reply.Rcode != test.rcode || len(reply.Answer) != 0 {
				t.Fatalf("%s %s = rcode %d answers %v", test.name, D.TypeToString[recordType], reply.Rcode, reply.Answer)
			}
		}
	}
}

func TestEasyTierWebClientOfflineOwner(t *testing.T) {
	previousHome := C.Path.HomeDir()
	C.SetHomeDir(t.TempDir())
	t.Cleanup(func() { C.SetHomeDir(previousHome) })
	reservation, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := "tcp://" + reservation.Addr().String() + "/private-test-token"
	if err := reservation.Close(); err != nil {
		t.Fatal(err)
	}
	var option EasyTierOption
	decoder := structure.NewDecoder(structure.Option{TagName: "proxy", WeaklyTypedInput: true})
	if err := decoder.Decode(map[string]any{
		"name": "managed-offline", "packet-mode": true,
		"web-client": map[string]any{"endpoint": endpoint, "hostname": "managed-test-host"},
	}, &option); err != nil {
		t.Fatal(err)
	}
	adapter, err := NewEasyTier(option)
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	idle, err := adapter.EasyTierStatus(context.Background())
	if err != nil || idle.State != "idle" || !idle.WebManaged || adapter.host != nil {
		t.Fatalf("idle managed state = %+v, err = %v", idle, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sessions, err := adapter.OpenPacketSessions(ctx)
	if err != nil || len(sessions) != 0 {
		t.Fatalf("offline owner blocked an empty shared TUN: sessions=%+v err=%v", sessions, err)
	}
	if adapter.instance != nil || len(adapter.host.Instances()) != 0 || adapter.webClient == nil {
		t.Fatal("managed owner created a static application instance")
	}
	status, err := adapter.EasyTierStatus(ctx)
	if err != nil || status.State != "running" || status.WebClientConnected || len(status.Networks) != 0 {
		t.Fatalf("offline owner state = %+v, err = %v", status, err)
	}
	if _, err := uuid.FromString(status.InstanceID); err != nil {
		t.Fatalf("invalid persisted machine ID: %v", err)
	}
	identity, err := os.ReadFile(filepath.Join(adapter.stateDir, easyTierInstanceIDFile))
	if err != nil || strings.TrimSpace(string(identity)) != status.InstanceID {
		t.Fatalf("persisted machine ID = %q, err = %v", identity, err)
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private-test-token") || strings.Contains(string(encoded), endpoint) {
		t.Fatalf("managed status exposed endpoint: %s", encoded)
	}
	originalClient := adapter.webClient
	if err := originalClient.Close(ctx); err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		adapter.mu.Lock()
		replaced := adapter.webClient != nil && adapter.webClient != originalClient
		adapter.mu.Unlock()
		if replaced {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("owner did not restart a stopped configuration-center driver: %v", ctx.Err())
		case <-ticker.C:
		}
	}
	if err := adapter.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewEasyTier(option)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.OpenPacketSessions(ctx); err != nil {
		t.Fatal(err)
	}
	if reopened.instanceID != status.InstanceID {
		t.Fatalf("restart changed machine ID: %s -> %s", status.InstanceID, reopened.instanceID)
	}
}
