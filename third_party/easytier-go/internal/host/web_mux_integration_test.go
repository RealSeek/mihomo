//go:build mihomo_integration

package host

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/easytier/easytier/easytier-go/platform"
	"github.com/easytier/easytier/easytier-go/platform/netstd"
	apiconfig "github.com/easytier/easytier/easytier-go/proto/api/config"
	apiinstance "github.com/easytier/easytier/easytier-go/proto/api/instance"
	"github.com/easytier/easytier/easytier-go/proto/api/manage"
	"github.com/easytier/easytier/easytier-go/proto/common"
	et "github.com/metacubex/mihomo/component/easytier"
)

// Run from mihomo's root module so the tagged test resolves its shared mux.
func TestWebManagementSharedPacketMux(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	sockets := &webMuxSocketFactory{
		SocketFactory: netstd.SocketFactory{},
		ports:         make(chan int, 1),
	}
	peerHost, err := New(ctx, Options{Platform: platform.Services{Sockets: sockets}})
	if err != nil {
		t.Fatalf("create peer host: %v", err)
	}
	defer peerHost.Close(context.Background())
	peer, err := peerHost.CreateInstance(ctx, webMuxConfig(t, "mux-peer", "10.155.0.1/24", "", true))
	if err != nil {
		t.Fatalf("create peer core: %v", err)
	}
	if err := peer.Start(ctx); err != nil {
		t.Fatalf("start peer core: %v", err)
	}
	var port int
	select {
	case port = <-sockets.ports:
	case <-ctx.Done():
		t.Fatalf("wait for peer listener: %v", ctx.Err())
	}
	owner, err := New(ctx, Options{
		WebInstanceConfigPolicy: func(_ context.Context, _ string, configTOML string, _ *manage.NetworkConfig) (string, error) {
			prepared, _, err := et.PrepareNativeConfig(configTOML, true)
			return prepared, err
		},
	})
	if err != nil {
		t.Fatalf("create managed host: %v", err)
	}
	defer owner.Close(context.Background())
	device := &webMuxDevice{input: make(chan []byte, 2), output: make(chan []byte, 2), done: make(chan struct{})}
	mux, err := et.NewPacketMux(device, nil, false, nil, func(err error) {
		t.Logf("shared mux endpoint lifecycle: %v", err)
	})
	if err != nil {
		t.Fatalf("create shared mux: %v", err)
	}
	defer mux.Close()
	deadline := time.AfterFunc(45*time.Second, func() { _ = mux.Close() })
	defer deadline.Stop()

	id := &common.UUID{Part1: 101, Part2: 102, Part3: 103, Part4: 104}
	idString := uuidString(id)
	networkName := "web-mux-integration"
	endpoint := fmt.Sprintf("tcp://127.0.0.1:%d", port)
	run := func(hostname, address string, overwrite bool) *Instance {
		t.Helper()
		configTOML, err := encodeInstanceConfig(webMuxConfig(t, hostname, address, endpoint, false))
		if err != nil {
			t.Fatalf("encode managed config: %v", err)
		}
		callManagement(t, owner, ctx, runNetworkInstanceMethod, &manage.RunNetworkInstanceRequest{
			InstId: id, Config: &manage.NetworkConfig{InstanceId: &idString, NetworkName: &networkName},
			Overwrite: overwrite, Source: manage.ConfigSource_ConfigSourceWeb,
		}, bindInstanceIdentity(configTOML, idString, networkName), new(manage.RunNetworkInstanceResponse))
		snapshot := owner.InstanceConfigurations()
		if len(snapshot) != 1 || snapshot[0].InstanceID != idString || !snapshot[0].WebOwned {
			t.Fatalf("managed snapshot = %+v", snapshot)
		}
		_, metadata, err := et.PrepareNativeConfig(snapshot[0].ConfigTOML, true)
		if err != nil {
			t.Fatalf("read effective packet metadata: %v", err)
		}
		addresses, err := metadata.OverlayAddresses()
		if err != nil || len(addresses) != 1 || addresses[0].String() != address {
			t.Fatalf("effective addresses = %v, error = %v; want %s", addresses, err, address)
		}
		if err := mux.UpdateSessions([]et.PacketSession{{
			ID: snapshot[0].InstanceID, Endpoint: snapshot[0].Instance,
			Addresses: addresses, Routes: []netip.Prefix{addresses[0].Masked()},
		}}); err != nil {
			t.Fatalf("publish managed packet session: %v", err)
		}
		webMuxWaitRoute(t, ctx, snapshot[0].Instance, "mux-peer")
		webMuxWaitRoute(t, ctx, peer, hostname)
		return snapshot[0].Instance
	}

	initial := run("mux-managed", "10.155.0.2/24", false)
	webMuxExchange(t, ctx, mux, device, peer, "10.155.0.2", "created")
	disableRelayData := true
	callManagement(t, owner, ctx, patchConfigMethod, &apiconfig.PatchConfigRequest{
		Instance: &apiinstance.InstanceIdentifier{Selector: &apiinstance.InstanceIdentifier_Id{Id: id}},
		Patch:    &apiconfig.InstanceConfigPatch{DisableRelayData: &disableRelayData},
	}, "", new(apiconfig.PatchConfigResponse))
	patched := owner.InstanceConfigurations()
	if len(patched) != 1 || patched[0].Instance != initial ||
		!bytes.Contains([]byte(patched[0].ConfigTOML), []byte("disable_relay_data = true")) {
		t.Fatalf("hot patch did not retain the attached effective configuration: %+v", patched)
	}
	webMuxExchange(t, ctx, mux, device, peer, "10.155.0.2", "hot-patched")

	replacement := run("mux-replacement", "10.155.0.3/24", true)
	if replacement == initial || initial.State() != StateStopped {
		t.Fatal("overwrite did not replace and stop the original core")
	}
	webMuxExchange(t, ctx, mux, device, peer, "10.155.0.3", "replaced")
	callManagement(t, owner, ctx, deleteNetworkInstanceMethod,
		&manage.DeleteNetworkInstanceRequest{InstIds: []*common.UUID{id}}, "", new(manage.DeleteNetworkInstanceResponse))
	if snapshot := owner.InstanceConfigurations(); len(snapshot) != 0 {
		t.Fatalf("deleted managed core still published: %+v", snapshot)
	}
	if err := mux.UpdateSessions(nil); err != nil {
		t.Fatalf("withdraw managed session: %v", err)
	}
	packet := webMuxIPv4Packet("10.155.0.3", "10.155.0.1", []byte("deleted"))
	device.input <- packet
	buffer := make([]byte, 1500)
	n, err := mux.Read(buffer)
	if err != nil || !bytes.Equal(buffer[:n], packet) {
		t.Fatalf("deleted overlay route did not return to mihomo: packet=%x, error=%v", buffer[:n], err)
	}
}

func webMuxConfig(t *testing.T, hostname, address, peer string, listen bool) InstanceConfig {
	t.Helper()
	builder := NewInstanceConfigBuilder("web-mux-integration").NetworkSecret("test").
		Hostname(hostname).IPv4(netip.MustParsePrefix(address)).P2P(P2PPolicy{Disable: true}).
		STUNServers().STUNServersV6().Encryption(false)
	if peer != "" {
		builder.AddPeers(peer)
	}
	if listen {
		builder.AddListeners("tcp://127.0.0.1:0")
	}
	config, err := builder.Build()
	if err != nil {
		t.Fatalf("build packet core config: %v", err)
	}
	return config
}

func webMuxWaitRoute(t *testing.T, ctx context.Context, instance *Instance, hostname string) {
	t.Helper()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		routes, err := instance.ListRoute(ctx)
		if err != nil {
			t.Fatalf("list core routes: %v", err)
		}
		for _, route := range routes {
			if route.Hostname == hostname {
				return
			}
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatalf("wait for route to %s: %v", hostname, ctx.Err())
		}
	}
}

func webMuxExchange(t *testing.T, ctx context.Context, mux *et.PacketMux, device *webMuxDevice, peer *Instance, address, phase string) {
	t.Helper()
	packet := webMuxIPv4Packet(address, "10.155.0.1", []byte(phase))
	ordinary := webMuxIPv4Packet("198.18.0.1", "192.0.2.1", []byte("ordinary-"+phase))
	device.input <- packet
	device.input <- ordinary
	buffer := make([]byte, 1500)
	n, err := mux.Read(buffer)
	if err != nil || !bytes.Equal(buffer[:n], ordinary) {
		t.Fatalf("%s ordinary packet dispatch: packet=%x, error=%v", phase, buffer[:n], err)
	}
	receiveCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	received, err := peer.ReceivePacket(receiveCtx)
	if err != nil || !bytes.Equal(received, packet) {
		t.Fatalf("%s shared mux to peer: packet=%x, error=%v", phase, received, err)
	}
	response := webMuxIPv4Packet("10.155.0.1", address, []byte("reply-"+phase))
	if err := peer.SendPacket(receiveCtx, response); err != nil {
		t.Fatalf("%s peer to shared mux: %v", phase, err)
	}
	select {
	case received := <-device.output:
		if !bytes.Equal(received, response) {
			t.Fatalf("%s shared mux to device: packet=%x, want=%x", phase, received, response)
		}
	case <-receiveCtx.Done():
		t.Fatalf("%s shared mux to device: %v", phase, receiveCtx.Err())
	}
}

type webMuxSocketFactory struct {
	platform.SocketFactory
	ports chan int
}

func (factory *webMuxSocketFactory) ListenTCP(ctx context.Context, options platform.TCPListenOptions) (net.Listener, error) {
	listener, err := factory.SocketFactory.ListenTCP(ctx, options)
	if err == nil {
		factory.ports <- listener.Addr().(*net.TCPAddr).Port
	}
	return listener, err
}

type webMuxDevice struct {
	input, output chan []byte
	done          chan struct{}
	close         sync.Once
}

func (device *webMuxDevice) Read(buffer []byte) (int, error) {
	select {
	case packet := <-device.input:
		return copy(buffer, packet), nil
	case <-device.done:
		return 0, net.ErrClosed
	}
}

func (device *webMuxDevice) Write(packet []byte) (int, error) {
	select {
	case device.output <- bytes.Clone(packet):
		return len(packet), nil
	case <-device.done:
		return 0, net.ErrClosed
	}
}

func (device *webMuxDevice) Close() error {
	device.close.Do(func() { close(device.done) })
	return nil
}

func webMuxIPv4Packet(source, destination string, payload []byte) []byte {
	packet := make([]byte, 28+len(payload))
	packet[0], packet[8], packet[9] = 0x45, 64, 17
	binary.BigEndian.PutUint16(packet[2:4], uint16(len(packet)))
	copy(packet[12:16], net.ParseIP(source).To4())
	copy(packet[16:20], net.ParseIP(destination).To4())
	binary.BigEndian.PutUint16(packet[20:22], 40000)
	binary.BigEndian.PutUint16(packet[22:24], 40001)
	binary.BigEndian.PutUint16(packet[24:26], uint16(8+len(payload)))
	copy(packet[28:], payload)
	var checksum uint32
	for index := 0; index < 20; index += 2 {
		checksum += uint32(binary.BigEndian.Uint16(packet[index : index+2]))
	}
	for checksum > 0xffff {
		checksum = checksum&0xffff + checksum>>16
	}
	binary.BigEndian.PutUint16(packet[10:12], ^uint16(checksum))
	return packet
}
