//go:build windows && amd64 && !no_fake_tcp

package easytier

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/metacubex/mihomo/common/net/deadline"
	"github.com/metacubex/mihomo/component/iface"
	"golang.org/x/sys/windows"
)

const (
	winDivertSniff     = 0x0001
	winDivertSendOnly  = 0x0008
	winDivertNoInstall = 0x0010
)

// This is the WinDivert 2.x WINDIVERT_ADDRESS ABI, including its network union.
type fakeTCPWinDivertAddress struct {
	Timestamp int64
	Flags     uint32
	Reserved  uint32
	Union     [64]byte
}

func (a *fakeTCPWinDivertAddress) networkIfIndex() uint32 {
	return binary.LittleEndian.Uint32(a.Union[:4])
}
func (a *fakeTCPWinDivertAddress) networkSubIndex() uint32 {
	return binary.LittleEndian.Uint32(a.Union[4:8])
}
func (a *fakeTCPWinDivertAddress) setNetwork(ifindex, subindex uint32) {
	binary.LittleEndian.PutUint32(a.Union[:4], ifindex)
	binary.LittleEndian.PutUint32(a.Union[4:8], subindex)
}

type fakeTCPWinDivert struct {
	dll                               *windows.DLL
	open, recv, send, shutdown, close *windows.Proc
	reader, sender                    windows.Handle
	ifindex, subindex                 atomic.Uint32
	writeDeadline                     deadline.PipeDeadline
	closeSignal                       chan struct{}
	closeOnce                         sync.Once
	readMu, writeMu                   sync.Mutex
	closed                            atomic.Bool
}

func newFakeTCPPacketSocket(local, remote netip.AddrPort) (fakeTCPPacketIO, error) {
	local = netip.AddrPortFrom(local.Addr().Unmap().WithZone(""), local.Port())
	remote = netip.AddrPortFrom(remote.Addr().Unmap().WithZone(""), remote.Port())
	networkInterface, err := iface.ResolveInterfaceByAddr(local.Addr())
	if err != nil {
		return nil, fmt.Errorf("easytier: FakeTCP interface for %s: %w", local.Addr(), err)
	}
	dllPath := os.Getenv("EASYTIER_WINDIVERT_DLL")
	if dllPath == "" {
		executable, err := os.Executable()
		if err != nil {
			return nil, err
		}
		dllPath = filepath.Join(filepath.Dir(executable), "WinDivert.dll")
	}
	if !filepath.IsAbs(dllPath) {
		return nil, fmt.Errorf("easytier: EASYTIER_WINDIVERT_DLL must be an absolute path")
	}
	dll, err := windows.LoadDLL(dllPath)
	if err != nil {
		return nil, fmt.Errorf("easytier: load FakeTCP WinDivert DLL %s: %w", dllPath, err)
	}
	backend := &fakeTCPWinDivert{dll: dll, writeDeadline: deadline.MakePipeDeadline(), closeSignal: make(chan struct{})}
	backend.ifindex.Store(uint32(networkInterface.Index))
	procedures := []struct {
		name   string
		target **windows.Proc
	}{{"WinDivertOpen", &backend.open}, {"WinDivertRecv", &backend.recv}, {"WinDivertSendEx", &backend.send}, {"WinDivertShutdown", &backend.shutdown}, {"WinDivertClose", &backend.close}}
	for _, procedure := range procedures {
		*procedure.target, err = dll.FindProc(procedure.name)
		if err != nil {
			_ = dll.Release()
			return nil, fmt.Errorf("easytier: FakeTCP WinDivert procedure %s: %w", procedure.name, err)
		}
	}
	backend.reader, err = backend.openHandle(fakeTCPWinDivertFilter(local, remote), 0, winDivertSniff|winDivertNoInstall)
	if err != nil {
		_ = dll.Release()
		return nil, err
	}
	// Inject before capture priority so local FakeTCP peers can receive injected
	// packets. WinDivert does not divert a packet twice at the same priority.
	backend.sender, err = backend.openHandle("false", 1, winDivertSendOnly|winDivertNoInstall)
	if err != nil {
		_, _, _ = backend.close.Call(uintptr(backend.reader))
		_ = dll.Release()
		return nil, err
	}
	return backend, nil
}

func (s *fakeTCPWinDivert) openHandle(filter string, priority uint16, flags uintptr) (windows.Handle, error) {
	encoded, err := windows.BytePtrFromString(filter)
	if err != nil {
		return 0, err
	}
	handle, _, callError := s.open.Call(uintptr(unsafe.Pointer(encoded)), 0, uintptr(priority), flags)
	if windows.Handle(handle) == windows.InvalidHandle {
		return 0, fmt.Errorf("easytier: open FakeTCP WinDivert capture (NO_INSTALL, requires administrator and an already running WinDivert driver): %w", callError)
	}
	return windows.Handle(handle), nil
}

func fakeTCPWinDivertFilter(local, remote netip.AddrPort) string {
	family := "ip"
	if local.Addr().Is6() {
		family = "ipv6"
	}
	filter := fmt.Sprintf("tcp and %s.DstAddr == %s and %s.SrcAddr == %s and tcp.SrcPort == %d", family, local.Addr(), family, remote.Addr(), remote.Port())
	if local.Port() != 0 {
		filter += fmt.Sprintf(" and tcp.DstPort == %d", local.Port())
	}
	return filter
}

func (s *fakeTCPWinDivert) ReadPacket() (fakeTCPFrame, error) {
	s.readMu.Lock()
	defer s.readMu.Unlock()
	if s.closed.Load() {
		return fakeTCPFrame{}, os.ErrClosed
	}
	packet := make([]byte, 65535)
	var size uint32
	var address fakeTCPWinDivertAddress
	ok, _, callError := s.recv.Call(uintptr(s.reader), uintptr(unsafe.Pointer(&packet[0])), uintptr(len(packet)), uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&address)))
	if ok == 0 {
		return fakeTCPFrame{}, fmt.Errorf("easytier: FakeTCP WinDivert receive: %w", callError)
	}
	s.ifindex.Store(address.networkIfIndex())
	s.subindex.Store(address.networkSubIndex())
	return fakeTCPFrame{packet: packet[:size]}, nil
}

func (s *fakeTCPWinDivert) WritePacket(packet []byte, _ [8]byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.closed.Load() {
		return os.ErrClosed
	}
	select {
	case <-s.writeDeadline.Wait():
		return os.ErrDeadlineExceeded
	default:
	}
	address := fakeTCPWinDivertAddress{Flags: 1<<17 | 1<<21 | 1<<22}
	address.setNetwork(s.ifindex.Load(), s.subindex.Load())
	if packet[0]>>4 == 6 {
		address.Flags |= 1 << 20
	}
	var size uint32
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return fmt.Errorf("easytier: FakeTCP send event: %w", err)
	}
	defer windows.CloseHandle(event)
	overlapped := windows.Overlapped{HEvent: event}
	// The driver retains these pointers until asynchronous I/O completes.
	var pinned runtime.Pinner
	pinned.Pin(&packet[0])
	pinned.Pin(&size)
	pinned.Pin(&address)
	pinned.Pin(&overlapped)
	defer pinned.Unpin()
	ok, _, callError := s.send.Call(uintptr(s.sender), uintptr(unsafe.Pointer(&packet[0])), uintptr(len(packet)), uintptr(unsafe.Pointer(&size)), 0, uintptr(unsafe.Pointer(&address)), unsafe.Sizeof(address), uintptr(unsafe.Pointer(&overlapped)))
	if ok == 0 && !errors.Is(callError, windows.ERROR_IO_PENDING) {
		return fmt.Errorf("easytier: FakeTCP WinDivert send: %w", callError)
	}
	if ok == 0 {
		err = s.waitSend(&overlapped, &size)
		if err != nil {
			return fmt.Errorf("easytier: FakeTCP WinDivert send completion: %w", err)
		}
	}
	if int(size) != len(packet) {
		return fmt.Errorf("easytier: FakeTCP WinDivert partial packet write: %d/%d", size, len(packet))
	}
	return nil
}

func (s *fakeTCPWinDivert) waitSend(overlapped *windows.Overlapped, size *uint32) error {
	completed := make(chan error, 1)
	go func() { completed <- windows.GetOverlappedResult(s.sender, overlapped, size, true) }()
	var interrupted error
	select {
	case err := <-completed:
		return err
	case <-s.closeSignal:
		interrupted = os.ErrClosed
	case <-s.writeDeadline.Wait():
		interrupted = os.ErrDeadlineExceeded
	}
	cancelErr := windows.CancelIoEx(s.sender, overlapped)
	// Drain completion before releasing pinned memory or the handle.
	<-completed
	if cancelErr != nil && !errors.Is(cancelErr, windows.ERROR_NOT_FOUND) {
		return errors.Join(interrupted, fmt.Errorf("easytier: FakeTCP cancel send: %w", cancelErr))
	}
	return interrupted
}

func (s *fakeTCPWinDivert) SetWriteDeadline(value time.Time) error {
	s.writeDeadline.Set(value)
	return nil
}

func (s *fakeTCPWinDivert) Close() error {
	var closeErrors []error
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		close(s.closeSignal)
		_, _, _ = s.shutdown.Call(uintptr(s.reader), 1)
		s.readMu.Lock()
		defer s.readMu.Unlock()
		s.writeMu.Lock()
		defer s.writeMu.Unlock()
		for _, handle := range []windows.Handle{s.reader, s.sender} {
			if ok, _, err := s.close.Call(uintptr(handle)); ok == 0 {
				closeErrors = append(closeErrors, err)
			}
		}
		closeErrors = append(closeErrors, s.dll.Release())
		s.writeDeadline.Set(time.Time{})
	})
	return errors.Join(closeErrors...)
}
