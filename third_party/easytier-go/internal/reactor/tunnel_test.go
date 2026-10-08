package reactor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

type blockingTunnel struct {
	reads   atomic.Int32
	entered chan struct{}
	payload chan []byte
	closed  chan struct{}
}

func (socket *blockingTunnel) Receive(ctx context.Context) ([]byte, error) {
	socket.reads.Add(1)
	socket.entered <- struct{}{}
	select {
	case payload := <-socket.payload:
		return payload, nil
	case <-socket.closed:
		return nil, net.ErrClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (*blockingTunnel) Send(context.Context, []byte) error { return nil }
func (socket *blockingTunnel) Close() error                { close(socket.closed); return nil }

func TestTunnelCanceledReceivePreservesSingleReadAndMessage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	reactor := New(ctx, Options{})
	defer reactor.Close()
	socket := &blockingTunnel{entered: make(chan struct{}, 2), payload: make(chan []byte, 1), closed: make(chan struct{})}
	handle, err := reactor.RegisterTunnel(socket)
	if err != nil {
		t.Fatal(err)
	}
	if err := reactor.StartTunnelReceive(handle, 1, 1024); err != nil {
		t.Fatal(err)
	}
	select {
	case <-socket.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := reactor.CancelOperation(1); err != nil {
		t.Fatal(err)
	}
	if err := reactor.StartTunnelReceive(handle, 2, 1024); err != nil {
		t.Fatal(err)
	}
	if socket.reads.Load() != 1 {
		t.Fatal("cancel started concurrent tunnel reads")
	}
	want := []byte("message retained after cancellation")
	socket.payload <- want
	for {
		got, err := reactor.TakeTunnelReceive(2)
		if err == nil {
			if !bytes.Equal(got, want) {
				t.Fatalf("receive = %q", got)
			}
			break
		}
		if !errors.Is(err, ErrPending) {
			t.Fatal(err)
		}
		select {
		case <-reactor.Completions():
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if socket.reads.Load() != 1 {
		t.Fatal("receive consumed more than one transport message")
	}
}

type contextTunnelListener struct{ ctx context.Context }

func (listener *contextTunnelListener) Accept(ctx context.Context) (TunnelSocket, TunnelMetadata, error) {
	<-ctx.Done()
	return nil, TunnelMetadata{}, ctx.Err()
}
func (*contextTunnelListener) LocalURL() string { return "ws://127.0.0.1:10000/" }
func (*contextTunnelListener) Close() error     { return nil }

func TestTunnelListenerHandleCancelsBindContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	reactor := New(ctx, Options{})
	defer reactor.Close()
	listener := &contextTunnelListener{}
	if err := reactor.StartTunnelBind(1, func(ctx context.Context) (TunnelListener, error) { listener.ctx = ctx; return listener, nil }); err != nil {
		t.Fatal(err)
	}
	var encoded []byte
	for {
		var err error
		encoded, err = reactor.TunnelBindResult(1, true)
		if err == nil {
			break
		}
		if !errors.Is(err, ErrPending) {
			t.Fatal(err)
		}
		select {
		case <-reactor.Completions():
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	var result struct {
		Handle uint64 `json:"handle"`
	}
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatal(err)
	}
	if listener.ctx.Err() != nil {
		t.Fatal("bind context canceled before listener close")
	}
	if err := reactor.CloseHandle(result.Handle); err != nil {
		t.Fatal(err)
	}
	select {
	case <-listener.ctx.Done():
	case <-ctx.Done():
		t.Fatal("listener close leaked bind context")
	}
}
