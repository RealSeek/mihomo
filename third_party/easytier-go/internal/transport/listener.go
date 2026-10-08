package transport

import (
	"context"
	"io"
	"net"
	"net/url"
	"sync"

	"github.com/easytier/easytier/easytier-go/internal/reactor"
)

type acceptedTunnel struct {
	socket   reactor.TunnelSocket
	metadata reactor.TunnelMetadata
}

type MessageListener struct {
	mu       sync.Mutex
	closed   bool
	localURL string
	resource io.Closer
	accepted chan acceptedTunnel
	done     chan struct{}
}

func ListenMessageWebSocket(ctx context.Context, endpoint string, listener net.Listener) (*MessageListener, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	parsed.Host = listener.Addr().String()
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	state := &MessageListener{localURL: parsed.String(), accepted: make(chan acceptedTunnel, 256), done: make(chan struct{})}
	resource, err := ListenWebSocket(ctx, state.localURL, WebSocketServerOptions{NetListener: listener}, func(_ context.Context, socket *WebSocket, local, remote, resolved string) error {
		state.mu.Lock()
		defer state.mu.Unlock()
		if state.closed {
			return net.ErrClosed
		}
		select {
		case state.accepted <- acceptedTunnel{socket, reactor.TunnelMetadata{LocalURL: local, RemoteURL: remote, ResolvedRemoteURL: resolved}}:
			return nil
		default:
			return reactor.ErrWouldBlock
		}
	})
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	state.resource = resource
	return state, nil
}

func (listener *MessageListener) LocalURL() string { return listener.localURL }

func (listener *MessageListener) Accept(ctx context.Context) (reactor.TunnelSocket, reactor.TunnelMetadata, error) {
	select {
	case <-ctx.Done():
		return nil, reactor.TunnelMetadata{}, ctx.Err()
	case <-listener.done:
		return nil, reactor.TunnelMetadata{}, net.ErrClosed
	case accepted := <-listener.accepted:
		return accepted.socket, accepted.metadata, nil
	}
}

func (listener *MessageListener) Close() error {
	listener.mu.Lock()
	if listener.closed {
		listener.mu.Unlock()
		return nil
	}
	listener.closed = true
	close(listener.done)
	var pending []reactor.TunnelSocket
	for len(listener.accepted) != 0 {
		pending = append(pending, (<-listener.accepted).socket)
	}
	listener.mu.Unlock()
	for _, socket := range pending {
		_ = socket.Close()
	}
	return listener.resource.Close()
}

type StreamListener struct {
	listener net.Listener
	localURL string
	scheme   string
}

func ListenMessageStream(endpoint string, listener net.Listener) (*StreamListener, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	parsed.Host = listener.Addr().String()
	return &StreamListener{listener: listener, localURL: parsed.String(), scheme: parsed.Scheme}, nil
}

func (listener *StreamListener) LocalURL() string { return listener.localURL }
func (listener *StreamListener) Close() error     { return listener.listener.Close() }
func (listener *StreamListener) Accept(_ context.Context) (reactor.TunnelSocket, reactor.TunnelMetadata, error) {
	connection, err := listener.listener.Accept()
	if err != nil {
		return nil, reactor.TunnelMetadata{}, err
	}
	remote := listener.scheme + "://" + connection.RemoteAddr().String()
	return NewStreamTunnel(connection), reactor.TunnelMetadata{LocalURL: listener.localURL, RemoteURL: remote, ResolvedRemoteURL: remote}, nil
}
