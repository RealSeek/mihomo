//go:build !no_easytier

package outbound

import (
	"context"
	"net/netip"
	"testing"

	"github.com/easytier/easytier/easytier-go/platform"
	C "github.com/metacubex/mihomo/constant"
)

func TestEasyTierEnvironmentUpdateKeepsIdleOwner(t *testing.T) {
	previousHome := C.Path.HomeDir()
	C.SetHomeDir(t.TempDir())
	t.Cleanup(func() { C.SetHomeDir(previousHome) })
	for _, option := range []EasyTierOption{
		{Name: "idle-static", NetworkName: "idle", IPv4: "10.143.0.1", PacketMode: true, Peers: []string{"tcp://127.0.0.1:11010"}},
		{Name: "idle-web", PacketMode: true, WebClient: &EasyTierWebClientOption{Endpoint: "token"}},
	} {
		t.Run(option.Name, func(t *testing.T) {
			owner, err := NewEasyTier(option)
			if err != nil {
				t.Fatal(err)
			}
			defer owner.Close()
			if err := owner.UpdateEnvironment(context.Background(), platform.EnvironmentSnapshot{
				InterfaceIPv4s: []netip.Addr{netip.MustParseAddr("192.0.2.10")},
			}); err != nil {
				t.Fatal(err)
			}
			owner.mu.Lock()
			started := owner.host != nil || owner.instance != nil || owner.webClient != nil
			owner.mu.Unlock()
			if started {
				t.Fatal("environment notification started an idle owner")
			}
		})
	}
}
