package easytier

import (
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestPrepareNativeConfig(t *testing.T) {
	const input = `ipv4 = '10.144.0.1/24'
ipv6 = 'fd00:144::1/64'
hostname = 'desktop'
dhcp = false
routes = ['192.0.2.0/24']
listeners = ['tcp://0.0.0.0:11010']
[network_identity]
network_name = 'example'
network_secret = 'secret'
[flags]
mtu = 1380
tld_dns_zone = 'mesh.example.'
no_tun = true
bind_device = true
disable_p2p = true
enable_exit_node = false
[[peer]]
uri = 'udp://192.0.2.1:11010'
[acl.acl_v1]
[[acl.acl_v1.chains]]
name = 'inbound'
chain_type = 1
enabled = true
default_action = 2
`
	output, metadata, err := PrepareNativeConfig(input, true)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.IPv4 != "10.144.0.1/24" || metadata.IPv6 != "fd00:144::1/64" || metadata.NetworkName != "example" || metadata.TLDDNSZone != "mesh.example." || metadata.MTU != 1380 {
		t.Fatalf("incorrect metadata: %+v", metadata)
	}
	if metadata.Routes == nil || len(*metadata.Routes) != 1 || (*metadata.Routes)[0] != "192.0.2.0/24" {
		t.Fatalf("manual routes metadata = %+v", metadata.Routes)
	}
	if metadata.EnableExitNode == nil || *metadata.EnableExitNode {
		t.Fatalf("explicit disabled exit-node metadata = %+v", metadata.EnableExitNode)
	}
	var document map[string]any
	if err := toml.Unmarshal([]byte(output), &document); err != nil {
		t.Fatal(err)
	}
	flags := document["flags"].(map[string]any)
	if flags["no_tun"] != false || flags["bind_device"] != false || flags["disable_p2p"] != true {
		t.Fatalf("incorrect packet flags: %+v", flags)
	}
	acl := document["acl"].(map[string]any)["acl_v1"].(map[string]any)
	chain := acl["chains"].([]any)[0].(map[string]any)
	if chain["default_action"] != int64(2) || len(document["peer"].([]any)) != 1 {
		t.Fatal("upstream fields were lost")
	}
}

func TestPrepareNativeConfigEmptyManualRoutes(t *testing.T) {
	_, omitted, err := PrepareNativeConfig("", true)
	if err != nil || omitted.Routes != nil {
		t.Fatalf("omitted routes = %+v, %v", omitted.Routes, err)
	}
	_, empty, err := PrepareNativeConfig("routes = []", true)
	if err != nil || empty.Routes == nil || len(*empty.Routes) != 0 {
		t.Fatalf("explicit empty routes = %+v, %v", empty.Routes, err)
	}
}

func TestPrepareNativeConfigMetadataError(t *testing.T) {
	if _, _, err := PrepareNativeConfig("ipv4 = 42", true); err == nil {
		t.Fatal("expected typed metadata error")
	}
}
