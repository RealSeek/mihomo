package reactor

import (
	"context"
	"fmt"
	"net"
	"net/url"

	"github.com/easytier/easytier/easytier-go/platform"
)

func (reactor *Reactor) ListenTunnelTCP(ctx context.Context, endpoint string, socketContext platform.SocketContext) (net.Listener, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	address, err := net.ResolveTCPAddr("tcp", parsed.Host)
	if err != nil {
		return nil, err
	}
	options := platform.TCPListenOptions{Bind: platform.TCPBindOptions{Context: socketContext, LocalAddr: address}, Purpose: platform.TCPListenDirect}
	if parsed.Scheme == "faketcp" {
		factory, ok := reactor.services.Sockets.(interface {
			ListenFakeTCP(context.Context, platform.TCPListenOptions) (net.Listener, error)
		})
		if !ok {
			return nil, fmt.Errorf("platform socket factory does not support FakeTCP listeners")
		}
		return factory.ListenFakeTCP(ctx, options)
	}
	return reactor.services.Sockets.ListenTCP(ctx, options)
}
