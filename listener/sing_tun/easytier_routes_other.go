//go:build !linux && !android && !windows && !darwin

package sing_tun

import (
	"fmt"
	"runtime"

	et "github.com/metacubex/mihomo/component/easytier"
	tun "github.com/metacubex/sing-tun"
)

func newEasyTierRoutes(tun.Options, []et.PacketSession) (*easyTierRoutes, error) {
	return nil, fmt.Errorf("easytier: TUN route management is unsupported on %s", runtime.GOOS)
}
