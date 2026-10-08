package hostabi

import (
	"context"
	"net/url"

	"github.com/easytier/easytier/easytier-go/internal/reactor"
	"github.com/easytier/easytier/easytier-go/internal/transport"
	"github.com/metacubex/wazero/api"
)

var (
	tunnelBindStartTypes   = []api.ValueType{api.ValueTypeI64, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32}
	tunnelResultTypes      = []api.ValueType{api.ValueTypeI64, api.ValueTypeI32, api.ValueTypeI32}
	tunnelAcceptStartTypes = []api.ValueType{api.ValueTypeI64, api.ValueTypeI64}
)

func (adapter *Adapter) startTunnelBind(_ context.Context, module api.Module, operation uint64, pointer, length, contextPointer, contextLength uint32) int32 {
	if length == 0 || length > 4096 {
		return statusInvalid
	}
	encoded, ok := module.Memory().Read(pointer, length)
	if !ok {
		return statusMemory
	}
	encodedContext, ok := module.Memory().Read(contextPointer, contextLength)
	if !ok {
		return statusMemory
	}
	socketContext, remainder, err := decodeSocketContext(encodedContext)
	if err != nil || len(remainder) != 0 {
		return statusInvalid
	}
	endpoint := string(encoded)
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "ws" && parsed.Scheme != "wss" && parsed.Scheme != "faketcp") {
		return statusInvalid
	}
	return operationStatus(adapter.reactor.StartTunnelBind(operation, func(ctx context.Context) (reactor.TunnelListener, error) {
		listener, err := adapter.reactor.ListenTunnelTCP(ctx, endpoint, socketContext)
		if err != nil {
			return nil, err
		}
		if parsed.Scheme == "faketcp" {
			return transport.ListenMessageStream(endpoint, listener)
		}
		return transport.ListenMessageWebSocket(ctx, endpoint, listener)
	}))
}

func (adapter *Adapter) takeTunnelBind(_ context.Context, module api.Module, operation uint64, pointer, capacity uint32) int32 {
	return adapter.copyTunnelResult(module, operation, pointer, capacity, adapter.reactor.TunnelBindResult)
}

func (adapter *Adapter) startTunnelAccept(_ context.Context, _ api.Module, handle, operation uint64) int32 {
	return operationStatus(adapter.reactor.StartTunnelAccept(handle, operation))
}

func (adapter *Adapter) takeTunnelAccept(_ context.Context, module api.Module, operation uint64, pointer, capacity uint32) int32 {
	return adapter.copyTunnelResult(module, operation, pointer, capacity, adapter.reactor.TunnelAcceptResult)
}

func (adapter *Adapter) copyTunnelResult(module api.Module, operation uint64, pointer, capacity uint32, take func(uint64, bool) ([]byte, error)) int32 {
	encoded, err := take(operation, false)
	if err != nil {
		return operationStatus(err)
	}
	if pointer == 0 && capacity == 0 {
		return int32(len(encoded))
	}
	if uint32(len(encoded)) > capacity {
		return statusMemory
	}
	if _, ok := module.Memory().Read(pointer, capacity); !ok {
		return statusMemory
	}
	encoded, err = take(operation, true)
	if err != nil {
		return operationStatus(err)
	}
	if !module.Memory().Write(pointer, encoded) {
		return statusMemory
	}
	return int32(len(encoded))
}

func (adapter *Adapter) startTunnelBindFunction() api.GoModuleFunction {
	return api.GoModuleFunc(func(ctx context.Context, module api.Module, stack []uint64) {
		stack[0] = api.EncodeI32(adapter.startTunnelBind(ctx, module, stack[0], api.DecodeU32(stack[1]), api.DecodeU32(stack[2]), api.DecodeU32(stack[3]), api.DecodeU32(stack[4])))
	})
}

func (adapter *Adapter) takeTunnelBindFunction() api.GoModuleFunction {
	return api.GoModuleFunc(func(ctx context.Context, module api.Module, stack []uint64) {
		stack[0] = api.EncodeI32(adapter.takeTunnelBind(ctx, module, stack[0], api.DecodeU32(stack[1]), api.DecodeU32(stack[2])))
	})
}

func (adapter *Adapter) startTunnelAcceptFunction() api.GoModuleFunction {
	return api.GoModuleFunc(func(ctx context.Context, module api.Module, stack []uint64) {
		stack[0] = api.EncodeI32(adapter.startTunnelAccept(ctx, module, stack[0], stack[1]))
	})
}

func (adapter *Adapter) takeTunnelAcceptFunction() api.GoModuleFunction {
	return api.GoModuleFunc(func(ctx context.Context, module api.Module, stack []uint64) {
		stack[0] = api.EncodeI32(adapter.takeTunnelAccept(ctx, module, stack[0], api.DecodeU32(stack[1]), api.DecodeU32(stack[2])))
	})
}
