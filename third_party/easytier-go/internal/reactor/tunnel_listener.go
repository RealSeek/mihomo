package reactor

import (
	"context"
	"encoding/json"
)

type TunnelMetadata struct {
	LocalURL          string `json:"local_url"`
	RemoteURL         string `json:"remote_url"`
	ResolvedRemoteURL string `json:"resolved_remote_url,omitempty"`
}

type TunnelListener interface {
	Accept(context.Context) (TunnelSocket, TunnelMetadata, error)
	LocalURL() string
	Close() error
}

type tunnelListenerState struct {
	listener TunnelListener
	cancel   context.CancelFunc
}

type tunnelBindOperation struct {
	listener TunnelListener
	err      error
	done     bool
	cancel   context.CancelFunc
}

type tunnelAcceptOperation struct {
	socket   TunnelSocket
	metadata TunnelMetadata
	err      error
	done     bool
	cancel   context.CancelFunc
}

func (reactor *Reactor) StartTunnelBind(operation uint64, bind func(context.Context) (TunnelListener, error)) error {
	reactor.mu.Lock()
	if err := reactor.claimOperationLocked(operation, operationTunnelBind); err != nil {
		reactor.mu.Unlock()
		return err
	}
	ctx, cancel := context.WithCancel(reactor.ctx)
	waiter := &tunnelBindOperation{cancel: cancel}
	reactor.tunnelBinds[operation] = waiter
	reactor.workers.Add(1)
	reactor.mu.Unlock()
	go func() {
		defer reactor.workers.Done()
		listener, err := bind(ctx)
		reactor.mu.Lock()
		if reactor.tunnelBinds[operation] != waiter {
			reactor.mu.Unlock()
			if listener != nil {
				_ = listener.Close()
			}
			return
		}
		waiter.listener, waiter.err, waiter.done = listener, err, true
		reactor.mu.Unlock()
		reactor.signalCompletion()
	}()
	return nil
}

// Tunnel result queries preserve resources until the copy succeeds.
func (reactor *Reactor) TunnelBindResult(operation uint64, consume bool) ([]byte, error) {
	reactor.mu.Lock()
	defer reactor.mu.Unlock()
	waiter := reactor.tunnelBinds[operation]
	if waiter == nil {
		return nil, ErrInvalid
	}
	if !waiter.done {
		return nil, ErrPending
	}
	if waiter.err != nil {
		delete(reactor.tunnelBinds, operation)
		reactor.releaseOperationLocked(operation, operationTunnelBind)
		waiter.cancel()
		return nil, waiter.err
	}
	handle := reactor.nextHandle + 1
	encoded, err := json.Marshal(struct {
		Handle   uint64 `json:"handle"`
		LocalURL string `json:"local_url"`
	}{handle, waiter.listener.LocalURL()})
	if consume && err == nil {
		handle = reactor.allocateHandleLocked()
		reactor.tunnelListeners[handle] = &tunnelListenerState{listener: waiter.listener, cancel: waiter.cancel}
		delete(reactor.tunnelBinds, operation)
		reactor.releaseOperationLocked(operation, operationTunnelBind)
	}
	return encoded, err
}

func (reactor *Reactor) StartTunnelAccept(handle, operation uint64) error {
	reactor.mu.Lock()
	state := reactor.tunnelListeners[handle]
	if state == nil {
		reactor.mu.Unlock()
		return ErrInvalid
	}
	if err := reactor.claimOperationLocked(operation, operationTunnelAccept); err != nil {
		reactor.mu.Unlock()
		return err
	}
	ctx, cancel := context.WithCancel(reactor.ctx)
	waiter := &tunnelAcceptOperation{cancel: cancel}
	reactor.tunnelAccepts[operation] = waiter
	reactor.workers.Add(1)
	reactor.mu.Unlock()
	go func() {
		defer reactor.workers.Done()
		socket, metadata, err := state.listener.Accept(ctx)
		reactor.mu.Lock()
		if reactor.tunnelAccepts[operation] != waiter {
			reactor.mu.Unlock()
			if socket != nil {
				_ = socket.Close()
			}
			return
		}
		waiter.socket, waiter.metadata, waiter.err, waiter.done = socket, metadata, err, true
		reactor.mu.Unlock()
		reactor.signalCompletion()
	}()
	return nil
}

func (reactor *Reactor) TunnelAcceptResult(operation uint64, consume bool) ([]byte, error) {
	reactor.mu.Lock()
	defer reactor.mu.Unlock()
	waiter := reactor.tunnelAccepts[operation]
	if waiter == nil {
		return nil, ErrInvalid
	}
	if !waiter.done {
		return nil, ErrPending
	}
	if waiter.err != nil {
		delete(reactor.tunnelAccepts, operation)
		reactor.releaseOperationLocked(operation, operationTunnelAccept)
		waiter.cancel()
		return nil, waiter.err
	}
	encoded, err := json.Marshal(struct {
		Handle uint64 `json:"handle"`
		TunnelMetadata
	}{reactor.nextHandle + 1, waiter.metadata})
	if consume && err == nil {
		if _, err = reactor.registerTunnelLocked(waiter.socket); err != nil {
			return nil, err
		}
		delete(reactor.tunnelAccepts, operation)
		reactor.releaseOperationLocked(operation, operationTunnelAccept)
		waiter.cancel()
	}
	return encoded, err
}
