package host

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/easytier/easytier/easytier-go/platform"
	"github.com/easytier/easytier/easytier-go/platform/netstd"
	apiinstance "github.com/easytier/easytier/easytier-go/proto/api/instance"
)

// Use NIC packets so a successful public Dial cannot mask an unused proxy engine.
func TestWrappedProxyRawNICTCP(t *testing.T) {
	for _, transport := range []apiinstance.TcpProxyEntryTransportType{
		apiinstance.TcpProxyEntryTransportType_KCP,
		apiinstance.TcpProxyEntryTransportType_QUIC,
	} {
		t.Run(transport.String(), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			sockets := &wrappedProxySocketFactory{port: make(chan int, 1)}
			host, err := New(ctx, Options{Platform: platform.Services{Sockets: sockets}})
			if err != nil {
				t.Fatalf("create wrapped proxy host: %v", err)
			}
			defer host.Close(context.Background())
			server := startWrappedProxyInstance(t, ctx, host, transport, "destination", "10.162.0.1", 0, false)
			defer server.Close(context.Background())
			var port int
			select {
			case port = <-sockets.port:
			case <-ctx.Done():
				t.Fatalf("wait for wrapped proxy underlay listener: %v", ctx.Err())
			}
			client := startWrappedProxyInstance(t, ctx, host, transport, "source", "10.162.0.2", port, true)
			defer client.Close(context.Background())
			waitWrappedProxyRoute(t, ctx, client, "wrapped-destination")
			waitWrappedProxyRoute(t, ctx, server, "wrapped-source")

			testWrappedRawTCPEcho(t, ctx, client, server, transport)
		})
	}
}

func testWrappedRawTCPEcho(t *testing.T, ctx context.Context, client, server *Instance, transport apiinstance.TcpProxyEntryTransportType) {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen wrapped destination echo: %v", err)
	}
	defer listener.Close()
	echo := make(chan net.Conn, 1)
	echoDone := make(chan error, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			echoDone <- acceptErr
			return
		}
		echo <- connection
		_, copyErr := io.Copy(connection, connection)
		echoDone <- copyErr
	}()

	src := netip.MustParseAddrPort("10.162.0.2:45000")
	dst := netip.AddrPortFrom(netip.MustParseAddr("10.162.0.1"), uint16(listener.Addr().(*net.TCPAddr).Port))
	const initialSequence = uint32(1000)
	if err := client.SendPacket(ctx, wrappedTCPPacket(src, dst, initialSequence, 0, 0x02, nil)); err != nil {
		t.Fatalf("send raw NIC SYN: %v", err)
	}
	synAck := receiveWrappedTCP(t, ctx, client, dst, src)
	if synAck.flags&0x12 != 0x12 || synAck.ack != initialSequence+1 {
		t.Fatalf("wrapped raw NIC SYN-ACK = %+v", synAck)
	}
	sequence, acknowledgment := initialSequence+1, synAck.sequence+1
	if err := client.SendPacket(ctx, wrappedTCPPacket(src, dst, sequence, acknowledgment, 0x10, nil)); err != nil {
		t.Fatalf("complete raw NIC handshake: %v", err)
	}
	var destination net.Conn
	select {
	case destination = <-echo:
		defer destination.Close()
	case err := <-echoDone:
		t.Fatalf("accept wrapped destination socket: %v", err)
	case <-ctx.Done():
		t.Fatalf("accept wrapped destination socket: %v", ctx.Err())
	}
	payload := []byte("raw-nic-" + strings.ToLower(transport.String()))
	if err := client.SendPacket(ctx, wrappedTCPPacket(src, dst, sequence, acknowledgment, 0x18, payload)); err != nil {
		t.Fatalf("write raw NIC payload: %v", err)
	}
	sequence += uint32(len(payload))
	var response []byte
	for len(response) < len(payload) {
		packet := receiveWrappedTCP(t, ctx, client, dst, src)
		if len(packet.payload) == 0 || packet.sequence != acknowledgment {
			continue
		}
		response = append(response, packet.payload...)
		acknowledgment += uint32(len(packet.payload))
		if err := client.SendPacket(ctx, wrappedTCPPacket(src, dst, sequence, acknowledgment, 0x10, nil)); err != nil {
			t.Fatalf("acknowledge wrapped response: %v", err)
		}
	}
	if !bytes.Equal(response, payload) {
		t.Fatalf("wrapped TCP echo = %q, want %q", response, payload)
	}
	assertWrappedProxyEntry(t, ctx, client, "source", transport, src, dst)
	if server != nil {
		assertWrappedProxyEntry(t, ctx, server, "destination", transport, src, dst)
	}
}

func startWrappedProxyInstance(t *testing.T, ctx context.Context, host *Host, transport apiinstance.TcpProxyEntryTransportType, role, ip string, port int, source bool) *Instance {
	t.Helper()
	var peer string
	if source {
		peer = fmt.Sprintf("\n[[peer]]\nuri = \"tcp://127.0.0.1:%d\"\n", port)
	}
	listeners := "[]"
	if !source {
		listeners = "[\"tcp://127.0.0.1:0\"]"
	}
	config := fmt.Sprintf(`hostname = "wrapped-%s"
ipv4 = "%s/24"
listeners = %s
stun_servers = []
stun_servers_v6 = []

[network_identity]
network_name = "wrapped-%s"
network_secret = "test"
%s
[flags]
no_tun = true
use_smoltcp = true
bind_device = false
disable_p2p = true
enable_encryption = false
enable_kcp_proxy = %t
disable_kcp_input = %t
enable_quic_proxy = %t
disable_quic_input = %t
`, role, ip, listeners, transport, peer,
		source && transport == apiinstance.TcpProxyEntryTransportType_KCP,
		transport != apiinstance.TcpProxyEntryTransportType_KCP,
		source && transport == apiinstance.TcpProxyEntryTransportType_QUIC,
		transport != apiinstance.TcpProxyEntryTransportType_QUIC)
	instance, err := host.CreateInstanceTOML(ctx, "wrapped-"+role, "", config)
	if err != nil {
		t.Fatalf("create wrapped %s: %v", role, err)
	}
	if err := instance.Start(ctx); err != nil {
		instance.Close(context.Background())
		t.Fatalf("start wrapped %s: %v", role, err)
	}
	return instance
}

func waitWrappedProxyRoute(t *testing.T, ctx context.Context, instance *Instance, hostname string) {
	t.Helper()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		routes, err := instance.ListRoute(ctx)
		if err != nil {
			t.Fatalf("list wrapped proxy routes: %v", err)
		}
		for _, route := range routes {
			if route.Hostname == hostname {
				return
			}
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatalf("wait for wrapped proxy route to %s: %v", hostname, ctx.Err())
		}
	}
}

func assertWrappedProxyEntry(t *testing.T, ctx context.Context, instance *Instance, role string, transport apiinstance.TcpProxyEntryTransportType, src, dst netip.AddrPort) {
	t.Helper()
	response := new(apiinstance.ListTcpProxyEntryResponse)
	if err := instance.callRPC(ctx, listTcpProxyEntryMethod, response); err != nil {
		t.Fatalf("list wrapped %s entries: %v", role, err)
	}
	for _, entry := range response.Entries {
		if entry.GetSrc().GetIpv4().GetAddr() != binary.BigEndian.Uint32(src.Addr().AsSlice()) ||
			entry.GetSrc().GetPort() != uint32(src.Port()) ||
			entry.GetDst().GetIpv4().GetAddr() != binary.BigEndian.Uint32(dst.Addr().AsSlice()) ||
			entry.GetDst().GetPort() != uint32(dst.Port()) {
			continue
		}
		if entry.TransportType != transport || entry.State != apiinstance.TcpProxyEntryState_Connected {
			t.Fatalf("wrapped %s entry = %v; want %s Connected", role, entry, transport)
		}
		t.Logf("wrapped %s %s -> %s: %s %s", role, src, dst, entry.TransportType, entry.State)
		return
	}
	t.Fatalf("wrapped %s has no entry for %s -> %s: %v", role, src, dst, response.Entries)
}

type wrappedProxySocketFactory struct {
	netstd.SocketFactory
	port chan int
}

func (factory *wrappedProxySocketFactory) ListenTCP(ctx context.Context, options platform.TCPListenOptions) (net.Listener, error) {
	listener, err := factory.SocketFactory.ListenTCP(ctx, options)
	if err == nil && options.Purpose == platform.TCPListenDirect {
		factory.port <- listener.Addr().(*net.TCPAddr).Port
	}
	return listener, err
}

type wrappedTCPSegment struct {
	sequence, ack uint32
	flags         byte
	payload       []byte
}

func receiveWrappedTCP(t *testing.T, ctx context.Context, instance *Instance, src, dst netip.AddrPort) wrappedTCPSegment {
	t.Helper()
	receiveCtx, cancelReceive := context.WithTimeout(ctx, 5*time.Second)
	defer cancelReceive()
	for {
		packet, err := instance.ReceivePacket(receiveCtx)
		if err != nil {
			queryCtx, cancel := context.WithTimeout(context.Background(), time.Second)
			response := new(apiinstance.ListTcpProxyEntryResponse)
			queryErr := instance.callRPC(queryCtx, listTcpProxyEntryMethod, response)
			cancel()
			t.Logf("raw NIC receive failure snapshot: entries=%v query_error=%v", response.Entries, queryErr)
			t.Fatalf("receive wrapped TCP from NIC: %v", err)
		}
		if len(packet) < 40 || packet[0]>>4 != 4 || packet[9] != 6 ||
			!bytes.Equal(packet[12:16], src.Addr().AsSlice()) || !bytes.Equal(packet[16:20], dst.Addr().AsSlice()) {
			continue
		}
		ipLength := int(packet[0]&15) * 4
		tcp := packet[ipLength:]
		if binary.BigEndian.Uint16(tcp[:2]) != src.Port() || binary.BigEndian.Uint16(tcp[2:4]) != dst.Port() {
			continue
		}
		if tcp[13]&0x04 != 0 {
			t.Fatalf("wrapped TCP reset: %x", packet)
		}
		return wrappedTCPSegment{
			sequence: binary.BigEndian.Uint32(tcp[4:8]), ack: binary.BigEndian.Uint32(tcp[8:12]),
			flags: tcp[13], payload: tcp[int(tcp[12]>>4)*4:],
		}
	}
}

func wrappedTCPPacket(src, dst netip.AddrPort, sequence, ack uint32, flags byte, payload []byte) []byte {
	packet := make([]byte, 40+len(payload))
	packet[0], packet[8], packet[9] = 0x45, 64, 6
	binary.BigEndian.PutUint16(packet[2:4], uint16(len(packet)))
	copy(packet[12:16], src.Addr().AsSlice())
	copy(packet[16:20], dst.Addr().AsSlice())
	tcp := packet[20:]
	binary.BigEndian.PutUint16(tcp[:2], src.Port())
	binary.BigEndian.PutUint16(tcp[2:4], dst.Port())
	binary.BigEndian.PutUint32(tcp[4:8], sequence)
	binary.BigEndian.PutUint32(tcp[8:12], ack)
	tcp[12], tcp[13] = 5<<4, flags
	binary.BigEndian.PutUint16(tcp[14:16], 65535)
	copy(tcp[20:], payload)
	pseudoHeader := make([]byte, 12+len(tcp))
	copy(pseudoHeader[:8], packet[12:20])
	pseudoHeader[9] = 6
	binary.BigEndian.PutUint16(pseudoHeader[10:12], uint16(len(tcp)))
	copy(pseudoHeader[12:], tcp)
	binary.BigEndian.PutUint16(tcp[16:18], wrappedChecksum(pseudoHeader))
	binary.BigEndian.PutUint16(packet[10:12], wrappedChecksum(packet[:20]))
	return packet
}

func wrappedChecksum(data []byte) uint16 {
	var sum uint32
	for len(data) >= 2 {
		sum += uint32(binary.BigEndian.Uint16(data[:2]))
		data = data[2:]
	}
	if len(data) != 0 {
		sum += uint32(data[0]) << 8
	}
	for sum > 0xffff {
		sum = sum&0xffff + sum>>16
	}
	return ^uint16(sum)
}
