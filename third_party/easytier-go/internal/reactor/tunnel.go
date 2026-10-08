package reactor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"

	"github.com/easytier/easytier/easytier-go/platform"
)

func (reactor *Reactor) ConnectTunnelTCP(ctx context.Context, address string, purpose platform.TCPConnectPurpose) (net.Conn, error) {
	if reactor.services.Sockets == nil {
		return nil, fmt.Errorf("tunnel TCP connect: no socket factory configured")
	}
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		return nil, fmt.Errorf("parse tunnel TCP port: %w", err)
	}
	ip, err := netip.ParseAddr(host)
	addresses := []netip.Addr{ip}
	if err != nil {
		if reactor.services.DNS == nil {
			return nil, fmt.Errorf("tunnel TCP connect: no DNS resolver configured")
		}
		addresses, err = reactor.services.DNS.LookupIP(ctx, platform.DNSQuery{
			Host: host, IPVersion: uint8(platform.IPVersionBoth),
		})
		if err != nil {
			return nil, fmt.Errorf("resolve tunnel TCP host %s: %w", host, err)
		}
	}
	if len(addresses) == 0 {
		return nil, &net.DNSError{Name: host, Err: "no addresses", IsNotFound: true}
	}
	for _, ip := range addresses {
		version := platform.IPVersionV6
		if ip.Is4() {
			version = platform.IPVersionV4
		}
		var conn net.Conn
		conn, err = reactor.services.Sockets.ConnectTCP(ctx, platform.TCPConnectOptions{
			RemoteAddr: &net.TCPAddr{IP: net.IP(ip.AsSlice()), Zone: ip.Zone(), Port: int(port)},
			Bind:       platform.TCPBindOptions{Context: platform.SocketContext{IPVersion: version}},
			Purpose:    purpose,
		})
		if err == nil {
			return conn, nil
		}
		if ctx.Err() != nil {
			break
		}
	}
	return nil, fmt.Errorf("connect tunnel TCP %s: %w", address, err)
}

// TunnelSocket is a message-preserving transport used by EasyTier's external
// tunnel ABI. Each Receive returns one complete message, and each Send emits
// one complete message. WebSocket is the first implementation; keeping this
// interface below the reactor lets additional transports share the ABI.
type TunnelSocket interface {
	Receive(context.Context) ([]byte, error)
	Send(context.Context, []byte) error
	Close() error
}

type tunnelState struct {
	socket         TunnelSocket
	closed         bool
	receive        *tunnelReceiveOperation
	receiveRunning bool
	incoming       []byte
	receiveReady   bool
	receiveErr     error
	send           *tunnelSendOperation
	sendRunning    bool
}

type tunnelReceiveOperation struct {
	tunnel   *tunnelState
	capacity uint32
}

type tunnelSendOperation struct {
	tunnel *tunnelState
	data   []byte
	err    error
	done   bool
}

type tunnelConnectOperation struct {
	socket TunnelSocket
	err    error
	done   bool
	cancel context.CancelFunc
}

func (reactor *Reactor) RegisterTunnel(socket TunnelSocket) (uint64, error) {
	if socket == nil {
		return 0, ErrInvalid
	}
	reactor.mu.Lock()
	defer reactor.mu.Unlock()
	if reactor.closed {
		return 0, ErrInvalid
	}
	handle := reactor.allocateHandleLocked()
	reactor.tunnels[handle] = &tunnelState{socket: socket}
	return handle, nil
}

func (reactor *Reactor) StartTunnelConnect(
	operation uint64,
	endpoint string,
	dial func(context.Context, string) (TunnelSocket, error),
) error {
	if endpoint == "" || dial == nil {
		return ErrInvalid
	}
	reactor.mu.Lock()
	if err := reactor.claimOperationLocked(operation, operationTunnelConnect); err != nil {
		reactor.mu.Unlock()
		return err
	}
	ctx, cancel := context.WithCancel(reactor.ctx)
	waiter := &tunnelConnectOperation{cancel: cancel}
	reactor.tunnelConnects[operation] = waiter
	reactor.workers.Add(1)
	reactor.mu.Unlock()
	go reactor.runTunnelConnect(operation, waiter, ctx, endpoint, dial)
	return nil
}

func (reactor *Reactor) runTunnelConnect(
	operation uint64,
	waiter *tunnelConnectOperation,
	ctx context.Context,
	endpoint string,
	dial func(context.Context, string) (TunnelSocket, error),
) {
	defer reactor.workers.Done()
	socket, err := dial(ctx, endpoint)
	reactor.mu.Lock()
	if reactor.tunnelConnects[operation] != waiter {
		reactor.mu.Unlock()
		if socket != nil {
			_ = socket.Close()
		}
		return
	}
	waiter.socket = socket
	waiter.err = err
	waiter.done = true
	reactor.mu.Unlock()
	reactor.signalCompletion()
}

func (reactor *Reactor) TakeTunnelConnect(operation uint64) (uint64, error) {
	reactor.mu.Lock()
	waiter, exists := reactor.tunnelConnects[operation]
	if !exists || reactor.operations[operation] != operationTunnelConnect {
		reactor.mu.Unlock()
		return 0, ErrInvalid
	}
	if !waiter.done {
		reactor.mu.Unlock()
		return 0, ErrPending
	}
	if waiter.err != nil {
		err := waiter.err
		delete(reactor.tunnelConnects, operation)
		reactor.releaseOperationLocked(operation, operationTunnelConnect)
		reactor.mu.Unlock()
		waiter.cancel()
		return 0, err
	}
	handle, err := reactor.registerTunnelLocked(waiter.socket)
	delete(reactor.tunnelConnects, operation)
	reactor.releaseOperationLocked(operation, operationTunnelConnect)
	reactor.mu.Unlock()
	waiter.cancel()
	if err != nil && waiter.socket != nil {
		_ = waiter.socket.Close()
	}
	return handle, err
}

func (reactor *Reactor) registerTunnelLocked(socket TunnelSocket) (uint64, error) {
	if socket == nil || reactor.closed {
		return 0, ErrInvalid
	}
	handle := reactor.allocateHandleLocked()
	reactor.tunnels[handle] = &tunnelState{socket: socket}
	return handle, nil
}

func (reactor *Reactor) StartTunnelReceive(handle, operation uint64, capacity uint32) error {
	if capacity == 0 {
		return ErrInvalid
	}
	reactor.mu.Lock()
	tunnel, exists := reactor.tunnels[handle]
	if !exists || tunnel.closed || tunnel.receive != nil {
		reactor.mu.Unlock()
		return ErrInvalid
	}
	if err := reactor.claimOperationLocked(operation, operationTunnelReceive); err != nil {
		reactor.mu.Unlock()
		return err
	}
	waiter := &tunnelReceiveOperation{tunnel: tunnel, capacity: capacity}
	tunnel.receive = waiter
	reactor.tunnelReceives[operation] = waiter
	startWorker := !tunnel.receiveReady && !tunnel.receiveRunning
	if startWorker {
		tunnel.receiveRunning = true
		reactor.workers.Add(1)
	}
	reactor.mu.Unlock()
	if startWorker {
		go reactor.runTunnelReceive(tunnel)
	} else {
		reactor.signalCompletion()
	}
	return nil
}

func (reactor *Reactor) runTunnelReceive(tunnel *tunnelState) {
	defer reactor.workers.Done()
	data, err := tunnel.socket.Receive(reactor.ctx)
	if data != nil {
		data = append([]byte(nil), data...)
	}
	reactor.mu.Lock()
	tunnel.receiveRunning = false
	if tunnel.closed {
		reactor.mu.Unlock()
		return
	}
	tunnel.incoming = data
	tunnel.receiveErr = err
	tunnel.receiveReady = true
	reactor.mu.Unlock()
	reactor.signalCompletion()
}

func (reactor *Reactor) PeekTunnelReceive(operation uint64) (int, error) {
	reactor.mu.Lock()
	waiter, exists := reactor.tunnelReceives[operation]
	if !exists || reactor.operations[operation] != operationTunnelReceive {
		reactor.mu.Unlock()
		return 0, ErrInvalid
	}
	if !waiter.tunnel.receiveReady {
		reactor.mu.Unlock()
		return 0, ErrPending
	}
	if waiter.tunnel.receiveErr != nil && len(waiter.tunnel.incoming) == 0 {
		err := waiter.tunnel.receiveErr
		reactor.mu.Unlock()
		return 0, err
	}
	length := len(waiter.tunnel.incoming)
	reactor.mu.Unlock()
	return length, nil
}

func (reactor *Reactor) TakeTunnelReceive(operation uint64) ([]byte, error) {
	reactor.mu.Lock()
	waiter, exists := reactor.tunnelReceives[operation]
	if !exists || reactor.operations[operation] != operationTunnelReceive {
		reactor.mu.Unlock()
		return nil, ErrInvalid
	}
	if !waiter.tunnel.receiveReady {
		reactor.mu.Unlock()
		return nil, ErrPending
	}
	tunnel := waiter.tunnel
	data := append([]byte(nil), tunnel.incoming...)
	err := tunnel.receiveErr
	delete(reactor.tunnelReceives, operation)
	reactor.releaseOperationLocked(operation, operationTunnelReceive)
	if tunnel.receive == waiter {
		tunnel.receive = nil
		tunnel.receiveReady = false
		tunnel.incoming = nil
		tunnel.receiveErr = nil
	}
	if errors.Is(err, io.EOF) {
		err = net.ErrClosed
	}
	reactor.mu.Unlock()
	return data, err
}

func (reactor *Reactor) StartTunnelSend(handle, operation uint64, data []byte) error {
	data = append([]byte(nil), data...)
	if len(data) == 0 {
		return ErrInvalid
	}
	reactor.mu.Lock()
	tunnel, exists := reactor.tunnels[handle]
	if !exists || tunnel.closed || tunnel.send != nil || tunnel.sendRunning {
		reactor.mu.Unlock()
		return ErrInvalid
	}
	if err := reactor.claimOperationLocked(operation, operationTunnelSend); err != nil {
		reactor.mu.Unlock()
		return err
	}
	waiter := &tunnelSendOperation{tunnel: tunnel, data: data}
	tunnel.send = waiter
	tunnel.sendRunning = true
	reactor.tunnelSends[operation] = waiter
	reactor.workers.Add(1)
	reactor.mu.Unlock()
	go reactor.runTunnelSend(operation, waiter)
	return nil
}

func (reactor *Reactor) runTunnelSend(operation uint64, waiter *tunnelSendOperation) {
	defer reactor.workers.Done()
	err := waiter.tunnel.socket.Send(reactor.ctx, waiter.data)
	reactor.mu.Lock()
	waiter.tunnel.sendRunning = false
	if reactor.tunnelSends[operation] != waiter {
		reactor.mu.Unlock()
		return
	}
	waiter.err = err
	waiter.done = true
	reactor.mu.Unlock()
	reactor.signalCompletion()
}

func (reactor *Reactor) TakeTunnelSend(operation uint64) error {
	reactor.mu.Lock()
	waiter, exists := reactor.tunnelSends[operation]
	if !exists || reactor.operations[operation] != operationTunnelSend {
		reactor.mu.Unlock()
		return ErrInvalid
	}
	if !waiter.done {
		reactor.mu.Unlock()
		return ErrPending
	}
	err := waiter.err
	tunnel := waiter.tunnel
	delete(reactor.tunnelSends, operation)
	reactor.releaseOperationLocked(operation, operationTunnelSend)
	if tunnel.send == waiter {
		tunnel.send = nil
	}
	reactor.mu.Unlock()
	return err
}
