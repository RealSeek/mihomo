package hostabi

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"

	"github.com/easytier/easytier/easytier-go/internal/reactor"
	"github.com/easytier/easytier/easytier-go/internal/transport"
	"github.com/easytier/easytier/easytier-go/platform"
	"github.com/metacubex/wazero/api"
)

const maxTunnelPayload = 1024 * 1024

var (
	tunnelReceiveParameterTypes = []api.ValueType{api.ValueTypeI64, api.ValueTypeI64, api.ValueTypeI32}
	tunnelSendParameterTypes    = []api.ValueType{api.ValueTypeI64, api.ValueTypeI64, api.ValueTypeI32, api.ValueTypeI32}
	tunnelConnectStartTypes     = []api.ValueType{api.ValueTypeI64, api.ValueTypeI32, api.ValueTypeI32}
	tunnelConnectTakeTypes      = []api.ValueType{api.ValueTypeI64}
	hostI64ResultTypes          = []api.ValueType{api.ValueTypeI64}
)

func (adapter *Adapter) startTunnelReceiveFunction() api.GoModuleFunction {
	return api.GoModuleFunc(func(ctx context.Context, module api.Module, stack []uint64) {
		stack[0] = api.EncodeI32(adapter.startTunnelReceive(ctx, module, stack[0], stack[1], api.DecodeU32(stack[2])))
	})
}

func (adapter *Adapter) takeTunnelReceiveFunction() api.GoModuleFunction {
	return api.GoModuleFunc(func(ctx context.Context, module api.Module, stack []uint64) {
		stack[0] = api.EncodeI32(adapter.takeTunnelReceive(ctx, module, stack[0], api.DecodeU32(stack[1]), api.DecodeU32(stack[2])))
	})
}

func (adapter *Adapter) startTunnelSendFunction() api.GoModuleFunction {
	return api.GoModuleFunc(func(ctx context.Context, module api.Module, stack []uint64) {
		stack[0] = api.EncodeI32(adapter.startTunnelSend(ctx, module, stack[0], stack[1], api.DecodeU32(stack[2]), api.DecodeU32(stack[3])))
	})
}

func (adapter *Adapter) takeTunnelSendFunction() api.GoModuleFunction {
	return api.GoModuleFunc(func(ctx context.Context, module api.Module, stack []uint64) {
		stack[0] = api.EncodeI32(adapter.takeTunnelSend(ctx, module, stack[0]))
	})
}

func (adapter *Adapter) startTunnelConnectFunction() api.GoModuleFunction {
	return api.GoModuleFunc(func(ctx context.Context, module api.Module, stack []uint64) {
		stack[0] = api.EncodeI32(adapter.startTunnelConnect(ctx, module, stack[0], api.DecodeU32(stack[1]), api.DecodeU32(stack[2])))
	})
}

func (adapter *Adapter) takeTunnelConnectFunction() api.GoModuleFunction {
	return api.GoModuleFunc(func(ctx context.Context, module api.Module, stack []uint64) {
		stack[0] = uint64(adapter.takeTunnelConnect(ctx, module, stack[0]))
	})
}

func (adapter *Adapter) startTunnelReceive(
	_ context.Context,
	_ api.Module,
	handle, operation uint64,
	capacity uint32,
) int32 {
	if capacity == 0 || capacity > maxTunnelPayload {
		return statusInvalid
	}
	return operationStatus(adapter.reactor.StartTunnelReceive(handle, operation, capacity))
}

func (adapter *Adapter) takeTunnelReceive(
	_ context.Context,
	module api.Module,
	operation uint64,
	destination, capacity uint32,
) int32 {
	if destination == 0 && capacity == 0 {
		length, err := adapter.reactor.PeekTunnelReceive(operation)
		if err != nil {
			if !errors.Is(err, reactor.ErrPending) {
				_, _ = adapter.reactor.TakeTunnelReceive(operation)
			}
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				return -10
			}
			return operationStatus(err)
		}
		return int32(length)
	}
	data, err := adapter.reactor.TakeTunnelReceive(operation)
	if err != nil {
		return operationStatus(err)
	}
	if uint32(len(data)) > capacity || !module.Memory().Write(destination, data) {
		_ = adapter.reactor.CancelOperation(operation)
		return statusMemory
	}
	return int32(len(data))
}

func (adapter *Adapter) startTunnelSend(
	_ context.Context,
	module api.Module,
	handle, operation uint64,
	source, length uint32,
) int32 {
	if length == 0 || length > maxTunnelPayload {
		return statusInvalid
	}
	data, ok := module.Memory().Read(source, length)
	if !ok {
		return statusMemory
	}
	return operationStatus(adapter.reactor.StartTunnelSend(handle, operation, data))
}

func (adapter *Adapter) takeTunnelSend(
	_ context.Context,
	_ api.Module,
	operation uint64,
) int32 {
	return operationStatus(adapter.reactor.TakeTunnelSend(operation))
}

func (adapter *Adapter) startTunnelConnect(
	_ context.Context,
	module api.Module,
	operation uint64,
	pointer, length uint32,
) int32 {
	if length == 0 || length > 4096 {
		return statusInvalid
	}
	encoded, ok := module.Memory().Read(pointer, length)
	if !ok {
		return statusMemory
	}
	endpoint := string(encoded)
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "ws" && parsed.Scheme != "wss" && parsed.Scheme != "faketcp") {
		return statusInvalid
	}
	return operationStatus(adapter.reactor.StartTunnelConnect(
		operation,
		endpoint,
		func(ctx context.Context, endpoint string) (reactor.TunnelSocket, error) {
			if parsed.Scheme == "faketcp" {
				connection, err := adapter.reactor.ConnectTunnelTCP(ctx, parsed.Host, platform.TCPConnectFake)
				if err != nil {
					return nil, err
				}
				return transport.NewStreamTunnel(connection), nil
			}
			return transport.DialWebSocketWithDialer(
				ctx,
				endpoint,
				func(ctx context.Context, _, address string) (net.Conn, error) {
					return adapter.reactor.ConnectTunnelTCP(ctx, address, platform.TCPConnectDirect)
				},
			)
		},
	))
}

func (adapter *Adapter) takeTunnelConnect(
	_ context.Context,
	_ api.Module,
	operation uint64,
) int64 {
	handle, err := adapter.reactor.TakeTunnelConnect(operation)
	if err != nil {
		return int64(operationStatus(err))
	}
	return int64(handle)
}
