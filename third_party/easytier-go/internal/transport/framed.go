package transport

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

const (
	tcpTunnelHeaderSize   = 4
	peerManagerHeaderSize = 16
	tcpTunnelMaxBody      = 2000
)

// StreamTunnel adapts an EasyTier TCP/faketcp byte stream to the message
// tunnel ABI. The wire header is TCPTunnelHeader: one little-endian u32 body
// length; the body contains the peer-manager header and packet payload.
type StreamTunnel struct {
	conn    net.Conn
	readMu  sync.Mutex
	writeMu sync.Mutex
}

func NewStreamTunnel(conn net.Conn) *StreamTunnel {
	return &StreamTunnel{conn: conn}
}

func (t *StreamTunnel) Receive(ctx context.Context) ([]byte, error) {
	if t == nil || t.conn == nil {
		return nil, net.ErrClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	t.readMu.Lock()
	defer t.readMu.Unlock()
	stop := t.watchContext(ctx, true)
	defer stop()
	var header [tcpTunnelHeaderSize]byte
	if _, err := io.ReadFull(t.conn, header[:]); err != nil {
		return nil, streamTunnelContextError(ctx, err)
	}
	bodyLength := int(binary.LittleEndian.Uint32(header[:]))
	if bodyLength < peerManagerHeaderSize {
		return nil, fmt.Errorf("easytier: TCP tunnel body too short: %d", bodyLength)
	}
	if bodyLength > tcpTunnelMaxBody {
		return nil, fmt.Errorf("easytier: TCP tunnel body too long: %d", bodyLength)
	}
	body := make([]byte, bodyLength)
	if _, err := io.ReadFull(t.conn, body); err != nil {
		return nil, streamTunnelContextError(ctx, err)
	}
	return body, nil
}

func (t *StreamTunnel) Send(ctx context.Context, body []byte) error {
	if t == nil || t.conn == nil {
		return net.ErrClosed
	}
	if len(body) < peerManagerHeaderSize {
		return fmt.Errorf("easytier: TCP tunnel body too short: %d", len(body))
	}
	if len(body) > tcpTunnelMaxBody {
		return fmt.Errorf("easytier: TCP tunnel body too long: %d", len(body))
	}
	if ctx == nil {
		ctx = context.Background()
	}
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	stop := t.watchContext(ctx, false)
	defer stop()
	frame := make([]byte, tcpTunnelHeaderSize+len(body))
	binary.LittleEndian.PutUint32(frame[:tcpTunnelHeaderSize], uint32(len(body)))
	copy(frame[tcpTunnelHeaderSize:], body)
	if err := writeAll(t.conn, frame); err != nil {
		return streamTunnelContextError(ctx, err)
	}
	return nil
}

func (t *StreamTunnel) Close() error {
	if t == nil || t.conn == nil {
		return nil
	}
	return t.conn.Close()
}

func (t *StreamTunnel) watchContext(ctx context.Context, read bool) func() {
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		select {
		case <-ctx.Done():
			_ = t.setDeadline(read, time.Now())
		case <-done:
		}
	}()
	return func() {
		close(done)
		<-finished
		_ = t.setDeadline(read, time.Time{})
	}
}

func (t *StreamTunnel) setDeadline(read bool, deadline time.Time) error {
	if read {
		return t.conn.SetReadDeadline(deadline)
	}
	return t.conn.SetWriteDeadline(deadline)
}

func streamTunnelContextError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := writer.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}
