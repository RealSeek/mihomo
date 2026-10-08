package easytier

import (
	"strings"
	"testing"

	"github.com/metacubex/mihomo/common/structure"
	"github.com/pelletier/go-toml/v2"
)

func TestStructuredPortForwardConfig(t *testing.T) {
	var option struct {
		PortForwards []PortForwardOption `proxy:"port-forwards"`
	}
	decoder := structure.NewDecoder(structure.Option{TagName: "proxy", WeaklyTypedInput: true})
	if err := decoder.Decode(map[string]any{"port-forwards": []any{
		map[string]any{"protocol": "tcp", "bind": "127.0.0.1:18080", "destination": "10.144.0.2:8080"},
		map[string]any{"protocol": "udp", "bind": "127.0.0.1:0", "destination": "10.144.0.2:5353"},
	}}, &option); err != nil {
		t.Fatal(err)
	}
	encoded, err := (Config{
		NetworkName: "port-forward-test", Peers: []string{"tcp://127.0.0.1:11010"},
		PacketMode: true, PortForwards: option.PortForwards,
	}).RenderTOML()
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		PortForwards []struct {
			Protocol    string `toml:"proto"`
			Bind        string `toml:"bind_addr"`
			Destination string `toml:"dst_addr"`
		} `toml:"port_forward"`
		Flags struct {
			NoTun      bool `toml:"no_tun"`
			BindDevice bool `toml:"bind_device"`
		} `toml:"flags"`
	}
	if err := toml.Unmarshal([]byte(encoded), &document); err != nil {
		t.Fatal(err)
	}
	if len(document.PortForwards) != 2 || document.PortForwards[0].Protocol != "tcp" || document.PortForwards[0].Bind != "127.0.0.1:18080" || document.PortForwards[0].Destination != "10.144.0.2:8080" || document.PortForwards[1].Protocol != "udp" || document.PortForwards[1].Bind != "127.0.0.1:0" {
		t.Fatalf("decoded port forwards = %+v", document.PortForwards)
	}
	if document.Flags.NoTun || document.Flags.BindDevice {
		t.Fatalf("port forwards changed shared packet ownership: %+v", document.Flags)
	}
	for _, test := range []struct {
		name  string
		rule  PortForwardOption
		field string
	}{
		{"protocol", PortForwardOption{"sctp", "127.0.0.1:18080", "10.144.0.2:8080"}, "protocol"},
		{"bind-hostname", PortForwardOption{PortForwardTCP, "localhost:18080", "10.144.0.2:8080"}, "bind"},
		{"destination-hostname", PortForwardOption{PortForwardTCP, "127.0.0.1:18080", "phone.et.net:8080"}, "destination"},
		{"destination-zero-port", PortForwardOption{PortForwardUDP, "127.0.0.1:18080", "10.144.0.2:0"}, "destination"},
		{"destination-ipv6", PortForwardOption{PortForwardUDP, "127.0.0.1:18080", "[fd00::2]:5353"}, "destination"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := (Config{NetworkName: "port-forward-test", Peers: []string{"tcp://127.0.0.1:11010"}, PortForwards: []PortForwardOption{test.rule}}).RenderTOML()
			if err == nil || !strings.Contains(err.Error(), "port-forwards[0]."+test.field) {
				t.Fatalf("invalid port-forward error = %v", err)
			}
		})
	}
}
