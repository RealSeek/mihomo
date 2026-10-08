package easytier

import (
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestIPv6StructuredConfig(t *testing.T) {
	config := Config{NetworkName: "ipv6", IPv6: "fd00:144::1/64", Listeners: []string{"tcp://127.0.0.1:0"}}
	encoded, err := config.RenderTOML()
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := toml.Unmarshal([]byte(encoded), &document); err != nil {
		t.Fatal(err)
	}
	if document["ipv6"] != config.IPv6 || document["dhcp"] == true {
		t.Fatalf("IPv6-only config implicitly enabled IPv4 DHCP: %v", document)
	}
}
