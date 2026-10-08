//go:build !no_easytier

package outbound

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	C "github.com/metacubex/mihomo/constant"
)

func TestEasyTierIdleStatusDoesNotStartCore(t *testing.T) {
	previousHome := C.Path.HomeDir()
	C.SetHomeDir(t.TempDir())
	t.Cleanup(func() { C.SetHomeDir(previousHome) })
	instance, err := NewEasyTier(EasyTierOption{
		Name: "idle-status", PacketMode: true, NetworkName: "status-test",
		NetworkSecret: "status-network-secret", IPv4: "10.144.0.1/24",
		Peers: []string{"tcp://127.0.0.1:11010"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	status, err := instance.EasyTierStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "idle" || status.Node != nil || instance.host != nil || instance.instance != nil {
		t.Fatalf("idle status started the core: %+v", status)
	}
	if len(status.Provenance.Commit) != 40 || len(status.Provenance.SHA256) != 64 {
		t.Fatalf("embedded core provenance missing: %+v", status.Provenance)
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "status-network-secret") || strings.Contains(string(encoded), "config") {
		t.Fatalf("status exposed private configuration: %s", encoded)
	}
	if err := instance.Close(); err != nil {
		t.Fatal(err)
	}
	if summary := instance.EasyTierSummary(); summary.State != "closed" {
		t.Fatalf("closed state = %q", summary.State)
	}
}
