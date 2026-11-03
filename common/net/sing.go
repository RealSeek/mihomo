package net

import (
	"io"
	"net"
	"sync/atomic"

	"github.com/metacubex/mihomo/common/net/deadline"

	"github.com/metacubex/sing/common"
	"github.com/metacubex/sing/common/bufio"
	"github.com/metacubex/sing/common/network"
)

var NewExtendedConn = bufio.NewExtendedConn
var NewExtendedWriter = bufio.NewExtendedWriter
var NewExtendedReader = bufio.NewExtendedReader

type ExtendedConn = network.ExtendedConn
type ExtendedWriter = network.ExtendedWriter
type ExtendedReader = network.ExtendedReader

var WriteBuffer = bufio.WriteBuffer

type ReadWaitOptions = network.ReadWaitOptions

var NewReadWaitOptions = network.NewReadWaitOptions
var CalculateFrontHeadroom = network.CalculateFrontHeadroom
var CalculateRearHeadroom = network.CalculateRearHeadroom

type ReaderWithUpstream = network.ReaderWithUpstream
type WithUpstreamReader = network.WithUpstreamReader
type WriterWithUpstream = network.WriterWithUpstream
type WithUpstreamWriter = network.WithUpstreamWriter
type WithUpstream = common.WithUpstream

var UnwrapReader = network.UnwrapReader
var UnwrapWriter = network.UnwrapWriter

func NewDeadlineConn(conn net.Conn) ExtendedConn {
	if deadline.IsPipe(conn) || deadline.IsPipe(UnwrapReader(conn)) {
		return NewExtendedConn(conn) // pipe always have correctly deadline implement
	}
	if deadline.IsConn(conn) || deadline.IsConn(UnwrapReader(conn)) {
		return NewExtendedConn(conn) // was a *deadline.Conn
	}
	return deadline.NewConn(conn)
}

func NeedHandshake(conn any) bool {
	if earlyConn, isEarlyConn := common.Cast[network.EarlyConn](conn); isEarlyConn && earlyConn.NeedHandshake() {
		return true
	}
	return false
}

type CountFunc = network.CountFunc

var Pipe = deadline.Pipe

func closeWrite(writer io.Closer) error {
	if c, ok := common.Cast[network.WriteCloser](writer); ok {
		return c.CloseWrite()
	}
	return writer.Close()
}

// Relay copies between left and right bidirectionally.
// like [bufio.CopyConn] but remove unneeded [context.Context] handle and the cost of [task.Group]
// The first copy result is recorded before its cleanup can interrupt the other copy.
func Relay(leftConn, rightConn net.Conn) (result RelayResult) {
	defer func() {
		_ = leftConn.Close()
		_ = rightConn.Close()
	}()

	var first atomic.Uint32
	ch := make(chan struct{})
	go func() {
		n, err := bufio.Copy(leftConn, rightConn)
		result.RightToLeft = RelayCopyResult{Bytes: n, Err: err}
		first.CompareAndSwap(0, uint32(RelayRightToLeft))
		if err == nil {
			_ = closeWrite(leftConn)
		} else {
			_ = leftConn.Close()
		}
		close(ch)
	}()

	n, err := bufio.Copy(rightConn, leftConn)
	result.LeftToRight = RelayCopyResult{Bytes: n, Err: err}
	first.CompareAndSwap(0, uint32(RelayLeftToRight))
	if err == nil {
		_ = closeWrite(rightConn)
	} else {
		_ = rightConn.Close()
	}
	<-ch
	result.First = RelayDirection(first.Load())
	return
}

type RelayDirection uint8

const (
	RelayLeftToRight RelayDirection = iota + 1
	RelayRightToLeft
)

type RelayCopyResult struct {
	Bytes int64
	// Err is nil for EOF, which bufio.Copy treats as a successful copy.
	Err error
}

type RelayResult struct {
	LeftToRight RelayCopyResult
	RightToLeft RelayCopyResult
	First       RelayDirection
}
