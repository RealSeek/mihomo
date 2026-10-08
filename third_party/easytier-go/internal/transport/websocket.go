package transport

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// WebSocket is the host side of EasyTier's message-preserving tunnel ABI.
// Binary frames map one-to-one to host tunnel messages.
type WebSocket struct {
	conn *websocket.Conn
}

type WebSocketServerOptions struct {
	TLSConfig   *tls.Config
	NetListener net.Listener
}

type webSocketServer struct {
	server   *http.Server
	listener net.Listener
	done     chan struct{}
	once     sync.Once
}

func (server *webSocketServer) Close() error {
	var err error
	server.once.Do(func() {
		close(server.done)
		err = server.server.Close()
		_ = server.listener.Close()
	})
	return err
}

// ListenWebSocket binds one ws/wss endpoint and invokes accept for every
// binary WebSocket connection. The callback owns the returned socket after a
// nil error; an error closes it.
func ListenWebSocket(
	ctx context.Context,
	endpoint string,
	options WebSocketServerOptions,
	accept func(context.Context, *WebSocket, string, string, string) error,
) (io.Closer, error) {
	if ctx == nil {
		return nil, fmt.Errorf("listen EasyTier WebSocket with nil context")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "ws" && parsed.Scheme != "wss") {
		return nil, fmt.Errorf("invalid EasyTier WebSocket listener URL %q", endpoint)
	}
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	if parsed.Scheme == "wss" && options.TLSConfig == nil {
		options.TLSConfig, err = selfSignedTLSConfig()
		if err != nil {
			return nil, fmt.Errorf("create wss listener certificate: %w", err)
		}
	}
	listener := options.NetListener
	if listener == nil {
		listener, err = net.Listen("tcp", parsed.Host)
		if err != nil {
			return nil, fmt.Errorf("listen EasyTier WebSocket %s: %w", endpoint, err)
		}
	}
	if options.TLSConfig != nil {
		listener = tls.NewListener(listener, options.TLSConfig)
	}
	server := &http.Server{}
	server.Handler = http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != parsed.Path {
			http.NotFound(writer, request)
			return
		}
		socket, acceptErr := AcceptWebSocket(writer, request)
		if acceptErr != nil {
			return
		}
		remote := ""
		if request.RemoteAddr != "" {
			remote = fmt.Sprintf("%s://%s", parsed.Scheme, request.RemoteAddr)
		}
		resolved := remote
		if accept != nil {
			if acceptErr = accept(ctx, socket, endpoint, remote, resolved); acceptErr == nil {
				return
			}
		}
		_ = socket.Close()
	})
	closer := &webSocketServer{server: server, listener: listener, done: make(chan struct{})}
	go func() {
		select {
		case <-ctx.Done():
			_ = closer.Close()
		case <-closer.done:
		}
	}()
	go func() {
		_ = server.Serve(listener)
		_ = closer.Close()
	}()
	return closer, nil
}

func DialWebSocket(ctx context.Context, endpoint string) (*WebSocket, error) {
	return DialWebSocketWithDialer(ctx, endpoint, nil)
}

func DialWebSocketWithDialer(
	ctx context.Context,
	endpoint string,
	dialContext func(context.Context, string, string) (net.Conn, error),
) (*WebSocket, error) {
	options := &websocket.DialOptions{}
	parsed, parseErr := url.Parse(endpoint)
	if parseErr != nil {
		return nil, parseErr
	}
	transportConfig := &http.Transport{}
	if parsed.Scheme == "wss" {
		transportConfig.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // EasyTier peers may use ephemeral certificates.
	}
	if dialContext != nil {
		transportConfig.DialContext = dialContext
	}
	options.HTTPClient = &http.Client{Transport: transportConfig}
	conn, _, err := websocket.Dial(ctx, endpoint, options)
	if err != nil {
		return nil, fmt.Errorf("dial EasyTier WebSocket %s: %w", endpoint, err)
	}
	conn.SetReadLimit(1024 * 1024)
	return &WebSocket{conn: conn}, nil
}

func selfSignedTLSConfig() (*tls.Config, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "easytier"},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	privateKey, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	certificate, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateKey}),
	)
	if err != nil {
		return nil, err
	}
	certificate.Leaf = template
	return &tls.Config{Certificates: []tls.Certificate{certificate}}, nil
}

func AcceptWebSocket(w http.ResponseWriter, r *http.Request) (*WebSocket, error) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return nil, fmt.Errorf("accept EasyTier WebSocket: %w", err)
	}
	conn.SetReadLimit(1024 * 1024)
	return &WebSocket{conn: conn}, nil
}

func (socket *WebSocket) Receive(ctx context.Context) ([]byte, error) {
	kind, payload, err := socket.conn.Read(ctx)
	if err != nil {
		return nil, err
	}
	if kind != websocket.MessageBinary {
		return nil, fmt.Errorf("EasyTier WebSocket received non-binary frame")
	}
	return payload, nil
}

func (socket *WebSocket) Send(ctx context.Context, payload []byte) error {
	return socket.conn.Write(ctx, websocket.MessageBinary, payload)
}

func (socket *WebSocket) Close() error {
	if socket == nil || socket.conn == nil {
		return nil
	}
	return socket.conn.CloseNow()
}
