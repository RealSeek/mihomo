//go:build windows && amd64 && !no_fake_tcp

package easytier

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/metacubex/mihomo/common/net/deadline"
	"golang.org/x/sys/windows"
)

func TestFakeTCPPendingIOCancellation(t *testing.T) {
	for _, closing := range []bool{false, true} {
		t.Run(fmt.Sprintf("close=%t", closing), func(t *testing.T) {
			name, err := windows.UTF16PtrFromString(fmt.Sprintf(`\\.\pipe\mihomo-faketcp-%d-%d`, os.Getpid(), time.Now().UnixNano()))
			if err != nil {
				t.Fatal(err)
			}
			handle, err := windows.CreateNamedPipe(name, windows.PIPE_ACCESS_OUTBOUND|windows.FILE_FLAG_OVERLAPPED, windows.PIPE_TYPE_BYTE|windows.PIPE_WAIT, 1, 4096, 4096, 0, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer windows.CloseHandle(handle)
			event, err := windows.CreateEvent(nil, 1, 0, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer windows.CloseHandle(event)
			overlapped := windows.Overlapped{HEvent: event}
			if err := windows.ConnectNamedPipe(handle, &overlapped); !errors.Is(err, windows.ERROR_IO_PENDING) {
				t.Fatalf("pending connection: %v", err)
			}
			s := fakeTCPWinDivert{sender: handle, writeDeadline: deadline.MakePipeDeadline(), closeSignal: make(chan struct{})}
			result := make(chan error, 1)
			go func() { var size uint32; result <- s.waitSend(&overlapped, &size) }()
			want := os.ErrDeadlineExceeded
			if closing {
				close(s.closeSignal)
				want = os.ErrClosed
			} else {
				s.writeDeadline.Set(time.Now().Add(10 * time.Millisecond))
			}
			select {
			case err := <-result:
				if !errors.Is(err, want) {
					t.Fatalf("cancel: %v, want %v", err, want)
				}
			case <-time.After(time.Second):
				_ = windows.CancelIoEx(handle, &overlapped)
				<-result
				t.Fatal("pending I/O was not cancelled")
			}
			var size uint32
			if err := windows.GetOverlappedResult(handle, &overlapped, &size, false); !errors.Is(err, windows.ERROR_OPERATION_ABORTED) {
				t.Fatalf("completion: %v", err)
			}
		})
	}
}

func TestFakeTCPWinDivertABI(t *testing.T) {
	address := fakeTCPWinDivertAddress{}
	if unsafe.Sizeof(address) != 80 || unsafe.Offsetof(address.Flags) != 8 || unsafe.Offsetof(address.Union) != 16 {
		t.Fatalf("WinDivert address ABI: size=%d flags=%d union=%d", unsafe.Sizeof(address), unsafe.Offsetof(address.Flags), unsafe.Offsetof(address.Union))
	}
	address.setNetwork(11, 12)
	if address.networkIfIndex() != 11 || address.networkSubIndex() != 12 {
		t.Fatalf("WinDivert network union: %d/%d", address.networkIfIndex(), address.networkSubIndex())
	}
	local, remote := netip.MustParseAddrPort("[2001:db8::1]:0"), netip.MustParseAddrPort("[2001:db8::2]:23456")
	filter := fakeTCPWinDivertFilter(local, remote)
	if filter != "tcp and ipv6.DstAddr == 2001:db8::1 and ipv6.SrcAddr == 2001:db8::2 and tcp.SrcPort == 23456" || strings.Contains(filter, "tcp.DstPort") {
		t.Fatalf("IPv6 pre-handshake filter: %s", filter)
	}
	filter = fakeTCPWinDivertFilter(netip.AddrPortFrom(local.Addr(), 12345), remote)
	if !strings.HasSuffix(filter, " and tcp.DstPort == 12345") {
		t.Fatalf("IPv6 established socket filter: %s", filter)
	}
}
