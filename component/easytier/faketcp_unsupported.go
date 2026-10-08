//go:build (!linux && !android && (!windows || !amd64)) || no_fake_tcp

package easytier

import (
	"context"
	"fmt"
	"net"

	"github.com/easytier/easytier/easytier-go/platform"
)

func (SocketFactory) ConnectFakeTCP(context.Context, platform.TCPConnectOptions) (net.Conn, error) {
	return nil, fmt.Errorf("easytier: FakeTCP requires an enabled raw packet capture backend; Windows requires WinDivert")
}

func (SocketFactory) ListenFakeTCP(context.Context, platform.TCPListenOptions) (net.Listener, error) {
	return nil, fmt.Errorf("easytier: FakeTCP requires an enabled raw packet capture backend; Windows requires WinDivert")
}
