package easytier

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"
)

// PacketEndpoint is the official core's raw IP plane, independent of its sockets.
type PacketEndpoint interface {
	SendPacket(context.Context, []byte) error
	ReceivePacket(context.Context) ([]byte, error)
}

type PacketSession struct {
	ID        string
	Endpoint  PacketEndpoint
	Addresses []netip.Prefix
	Routes    []netip.Prefix
	MTU       uint32
}

type PacketProvider interface {
	OpenPacketSession(context.Context) (PacketSession, error)
}

type PacketSessionsProvider interface {
	OpenPacketSessions(context.Context) ([]PacketSession, error)
	PacketNetworks() ([]PacketNetwork, error)
	SetPacketConfigPolicy(PacketConfigPolicy)
}

type PacketNetwork struct {
	ID     string
	Config Config
}

type PacketConfigPolicy func(context.Context, string, Config) error

type packetReceiver struct {
	endpoint PacketEndpoint
	cancel   context.CancelFunc
}

// PacketMux owns the shared device. Only unmatched packets reach mihomo's stack.
// Overlay receive packets go to the OS, never back into the proxy stack.
type PacketMux struct {
	device    io.ReadWriteCloser
	sessions  atomic.Pointer[[]PacketSession]
	receivers map[string]*packetReceiver
	updates   sync.Mutex
	ctx       context.Context
	cancel    context.CancelFunc
	header    int
	bypass    func([]byte) bool
	onError   func(error)
	writes    sync.Mutex
	close     sync.Once
	wg        sync.WaitGroup
	closeErr  error
}

func NewPacketMux(device io.ReadWriteCloser, sessions []PacketSession, darwinHeader bool, bypass func([]byte) bool, onError func(error)) (*PacketMux, error) {
	ctx, cancel := context.WithCancel(context.Background())
	m := &PacketMux{device: device, receivers: make(map[string]*packetReceiver), ctx: ctx, cancel: cancel, bypass: bypass, onError: onError}
	if darwinHeader {
		m.header = 4
	}
	if err := m.UpdateSessions(sessions); err != nil {
		cancel()
		return nil, err
	}
	return m, nil
}

func (m *PacketMux) Read(buffer []byte) (int, error) {
	// The native macOS device includes a four-byte family header. The proxy
	// stack sees this mux as a plain IP device on every platform.
	storage := buffer
	if m.header != 0 {
		storage = make([]byte, len(buffer)+m.header)
	}
	for {
		n, err := m.device.Read(storage)
		if n <= m.header {
			return 0, err
		}
		packet := storage[m.header:n]
		endpoint := m.route(packet)
		if endpoint == nil {
			if m.header != 0 {
				copy(buffer, packet)
			}
			return len(packet), err
		}
		sendCtx, cancel := context.WithTimeout(m.ctx, time.Second)
		sendErr := endpoint.SendPacket(sendCtx, packet)
		cancel()
		if sendErr != nil && m.ctx.Err() == nil {
			m.onError(sendErr)
		}
		if err != nil {
			return 0, err
		}
	}
}

func (m *PacketMux) route(packet []byte) PacketEndpoint {
	if len(packet) == 0 || m.bypass != nil && m.bypass(packet) {
		return nil
	}
	var destination netip.Addr
	switch packet[0] >> 4 {
	case 4:
		if len(packet) < 20 {
			return nil
		}
		var address [4]byte
		copy(address[:], packet[16:20])
		destination = netip.AddrFrom4(address)
	case 6:
		if len(packet) < 40 {
			return nil
		}
		var address [16]byte
		copy(address[:], packet[24:40])
		destination = netip.AddrFrom16(address)
	default:
		return nil
	}
	var endpoint PacketEndpoint
	bits := -1
	for _, session := range *m.sessions.Load() {
		for _, prefix := range session.Routes {
			if prefix.Bits() > bits && prefix.Contains(destination) {
				endpoint, bits = session.Endpoint, prefix.Bits()
			}
		}
	}
	return endpoint
}

// UpdateSessions reconciles network UUIDs and their current packet endpoints.
// Replacement or deletion cancels the previous receiver on the same TUN.
func (m *PacketMux) UpdateSessions(sessions []PacketSession) error {
	current := make(map[string]PacketSession, len(sessions))
	snapshot := make([]PacketSession, len(sessions))
	for i, session := range sessions {
		if session.ID == "" {
			return fmt.Errorf("easytier: shared packet session requires a network ID")
		}
		if _, exists := current[session.ID]; exists {
			return fmt.Errorf("easytier: duplicate shared network ID %q", session.ID)
		}
		current[session.ID] = session
		snapshot[i] = session
		snapshot[i].Routes = append([]netip.Prefix(nil), session.Routes...)
	}
	m.updates.Lock()
	defer m.updates.Unlock()
	if m.ctx.Err() != nil {
		return m.ctx.Err()
	}
	for id, receiver := range m.receivers {
		session, keep := current[id]
		if keep && receiver.endpoint == session.Endpoint {
			continue
		}
		receiver.cancel()
		delete(m.receivers, id)
	}
	for _, session := range snapshot {
		if _, exists := m.receivers[session.ID]; exists {
			continue
		}
		ctx, cancel := context.WithCancel(m.ctx)
		receiver := &packetReceiver{endpoint: session.Endpoint, cancel: cancel}
		m.receivers[session.ID] = receiver
		m.wg.Add(1)
		go m.receive(ctx, session.ID, receiver)
	}
	m.sessions.Store(&snapshot)
	return nil
}

func (m *PacketMux) Write(packet []byte) (int, error) {
	m.writes.Lock()
	defer m.writes.Unlock()
	if m.header == 0 {
		return m.device.Write(packet)
	}
	framed := make([]byte, len(packet)+4)
	family := uint32(2) // macOS AF_INET
	if len(packet) > 0 && packet[0]>>4 == 6 {
		family = 30 // macOS AF_INET6
	}
	binary.BigEndian.PutUint32(framed, family)
	copy(framed[4:], packet)
	n, err := m.device.Write(framed)
	if n < 4 {
		return 0, err
	}
	return n - 4, err
}

func (m *PacketMux) receive(ctx context.Context, id string, receiver *packetReceiver) {
	defer func() {
		m.updates.Lock()
		if m.receivers[id] == receiver {
			delete(m.receivers, id)
		}
		m.updates.Unlock()
		m.wg.Done()
	}()
	for {
		packet, err := receiver.endpoint.ReceivePacket(ctx)
		if err != nil {
			if ctx.Err() == nil {
				m.onError(err)
			}
			return
		}
		m.updates.Lock()
		if m.receivers[id] != receiver || ctx.Err() != nil {
			m.updates.Unlock()
			return
		}
		_, err = m.Write(packet)
		m.updates.Unlock()
		if err != nil {
			if ctx.Err() == nil {
				m.onError(err)
			}
			return
		}
	}
}

func (m *PacketMux) Close() error {
	m.close.Do(func() {
		m.cancel()
		m.closeErr = m.device.Close()
		// The update barrier prevents further goroutines joining the wait group.
		m.updates.Lock()
		m.updates.Unlock()
		m.wg.Wait()
	})
	return m.closeErr
}
