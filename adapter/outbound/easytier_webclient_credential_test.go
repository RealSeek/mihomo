//go:build windows && mihomo_integration && !no_easytier

package outbound

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"slices"
	"testing"
	"time"

	corehost "github.com/easytier/easytier/easytier-go"
	apiinstance "github.com/easytier/easytier/easytier-go/proto/api/instance"
	"github.com/easytier/easytier/easytier-go/proto/peer_rpc"
	"github.com/gofrs/uuid/v5"
	et "github.com/metacubex/mihomo/component/easytier"
	C "github.com/metacubex/mihomo/constant"
	"github.com/pelletier/go-toml/v2"
)

func TestEasyTierWebClientServerManagedCredentialRecovery(t *testing.T) {
	previousHome := C.Path.HomeDir()
	C.SetHomeDir(t.TempDir())
	defer C.SetHomeDir(previousHome)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	server := newEasyTierWebTestServer(t, ctx, "_credential-")
	request := server.request
	wait := func(description string, condition func() bool) {
		t.Helper()
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for !condition() {
			select {
			case <-ctx.Done():
				t.Fatalf("wait for %s: %v", description, ctx.Err())
			case <-ticker.C:
			}
		}
	}
	serverReady := func() bool {
		_, status, err := request(http.MethodGet, "/api/internal/sessions", nil)
		return err == nil && status == http.StatusOK
	}
	server.start(t)
	wait("isolated configuration-server API", serverReady)
	networkID := uuid.Must(uuid.NewV4()).String()
	option := EasyTierOption{
		Name: "credential-" + networkID, StateDir: "credential-state-" + networkID, PacketMode: true,
		WebClient: &EasyTierWebClientOption{Endpoint: server.endpoint, Hostname: "mihomo-credential"},
	}
	owner, err := NewEasyTier(option)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = owner.Close() }()
	if sessions, err := owner.OpenPacketSessions(ctx); err != nil || len(sessions) != 0 {
		t.Fatalf("start fresh managed owner: sessions=%d err=%v", len(sessions), err)
	}
	machineID := owner.EasyTierSummary().InstanceID
	userID := 0
	authenticated := func() bool {
		body, status, err := request(http.MethodGet, "/api/internal/sessions", nil)
		if err != nil || status != http.StatusOK {
			return false
		}
		var sessions []struct {
			MachineID string `json:"machine_id"`
			UserID    int    `json:"user_id"`
		}
		if err := json.Unmarshal(body, &sessions); err != nil {
			t.Fatal("decode authenticated session identities")
		}
		for _, session := range sessions {
			if session.MachineID == machineID {
				if userID != 0 && session.UserID != userID {
					t.Fatal("reconnect changed the server user identity")
				}
				userID = session.UserID
				return owner.EasyTierSummary().WebClientConnected
			}
		}
		return false
	}
	wait("machine authentication", authenticated)
	adminKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	credentialKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	credentialID := uuid.Must(uuid.NewV4()).String()
	adminPrivate := base64.StdEncoding.EncodeToString(adminKey.Bytes())
	adminPublic := base64.StdEncoding.EncodeToString(adminKey.PublicKey().Bytes())
	credentialSecret := base64.StdEncoding.EncodeToString(credentialKey.Bytes())
	credentials := []map[string]any{{
		"credential_id": credentialID, "credential_secret": credentialSecret, "groups": []string{"ops"},
		"allow_relay": false, "allowed_proxy_cidrs": []string{}, "expiry_unix": time.Now().Add(time.Hour).Unix(), "reusable": true,
	}}
	adminAddress, _ := easyTierWebTestAddress(t)
	adminURL := "tcp://" + adminAddress
	config := map[string]any{
		"instance_id": networkID, "dhcp": false, "virtual_ipv4": "10.144.0.2", "network_length": 24,
		"hostname": "managed-admin", "network_name": networkID, "network_secret": networkID, "networking_method": "Manual",
		"peer_urls": []string{}, "public_server_url": adminURL, "listener_urls": []string{adminURL}, "advanced_settings": true,
		"disable_p2p": true, "no_tun": true, "bind_device": true, "mtu": 1380,
		"secure_mode":         map[string]any{"enabled": true, "local_private_key": adminPrivate, "local_public_key": adminPublic},
		"managed_credentials": credentials,
	}
	base := fmt.Sprintf("/api/internal/users/%d/machines/%s/networks", userID, machineID)
	_, status, err := request(http.MethodPost, base, map[string]any{"save": true, "config": config})
	if err != nil || status != http.StatusOK {
		t.Fatalf("persist managed credential: HTTP %d err=%v", status, err)
	}
	var session et.PacketSession
	refresh := func() {
		t.Helper()
		wait("managed administrator packet network", func() bool {
			sessions, err := owner.OpenPacketSessions(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(sessions) != 1 || sessions[0].ID != networkID || !slices.Contains(sessions[0].Addresses, netip.MustParsePrefix("10.144.0.2/24")) {
				return false
			}
			session = sessions[0]
			return true
		})
	}
	refresh()
	// A credential client has no network_secret; its private key is the issued credential.
	peer, err := NewEasyTier(EasyTierOption{
		Name: "credential-peer-" + networkID, PacketMode: true,
		ConfigTOML: fmt.Sprintf(`
hostname = 'credential-peer'
ipv4 = '10.144.0.1/24'
listeners = []
stun_servers = []
stun_servers_v6 = []
[network_identity]
network_name = %q
[secure_mode]
enabled = true
local_private_key = %q
[[peer]]
uri = %q
peer_public_key = %q
[flags]
disable_p2p = true
`, networkID, credentialSecret, adminURL, adminPublic),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	if _, err := peer.OpenPacketSession(ctx); err != nil {
		t.Fatal(err)
	}
	peerCore, err := peer.currentInstance()
	if err != nil {
		t.Fatal(err)
	}
	device := &easyTierMemoryTun{read: make(chan []byte, 4), write: make(chan []byte, 4), closed: make(chan struct{})}
	mux, err := et.NewPacketMux(device, nil, false, nil, func(err error) { t.Logf("managed packet receiver: %v", err) })
	if err != nil {
		t.Fatal(err)
	}
	defer mux.Close()
	stopMux := context.AfterFunc(ctx, func() { _ = mux.Close() })
	defer stopMux()
	verifyAuthentication := func(phase string) []string {
		t.Helper()
		refresh()
		managed := session.Endpoint.(*corehost.Instance)
		connectionIDs := []string{}
		for _, pair := range []struct {
			local, remote *corehost.Instance
			identity      peer_rpc.PeerIdentityType
			publicKey     []byte
		}{
			{managed, peerCore, peer_rpc.PeerIdentityType_Credential, credentialKey.PublicKey().Bytes()},
			{peerCore, managed, peer_rpc.PeerIdentityType_Admin, adminKey.PublicKey().Bytes()},
		} {
			info, err := pair.remote.ShowNodeInfo(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var connection *apiinstance.PeerConnInfo
			wait("authenticated peer connection and route after "+phase, func() bool {
				peers, err := pair.local.ListPeer(ctx)
				if err != nil {
					t.Fatal(err)
				}
				connection = nil
				for _, p := range peers {
					if p.GetPeerId() == info.GetPeerId() {
						for _, c := range p.GetConns() {
							if !c.GetIsClosed() {
								connection = c
							}
						}
					}
				}
				routes, err := pair.local.ListRoute(ctx)
				if err != nil {
					t.Fatal(err)
				}
				return connection != nil && slices.ContainsFunc(routes, func(route *corehost.Route) bool {
					return route.GetPeerId() == info.GetPeerId() && route.GetNextHopPeerId() == info.GetPeerId()
				})
			})
			if connection.GetPeerIdentityType() != pair.identity || connection.GetSecureAuthLevel() != peer_rpc.SecureAuthLevel_PeerVerified ||
				!bytes.Equal(connection.GetNoiseRemoteStaticPubkey(), pair.publicKey) {
				t.Fatalf("%s authenticated connection: identity=%s auth=%s remote-key-match=%v", phase,
					connection.GetPeerIdentityType(), connection.GetSecureAuthLevel(), bytes.Equal(connection.GetNoiseRemoteStaticPubkey(), pair.publicKey))
			}
			connectionIDs = append(connectionIDs, connection.GetConnId())
		}
		if err := mux.UpdateSessions([]et.PacketSession{session}); err != nil {
			t.Fatal(err)
		}
		if phase == "created" {
			go func() {
				_, _ = mux.Read(make([]byte, 65535))
			}()
		}
		packetCtx, done := context.WithTimeout(ctx, 5*time.Second)
		defer done()
		outgoing := easyTierRoutingUDP(netip.MustParseAddr("10.144.0.2"), netip.MustParseAddr("10.144.0.1"), 41001, 40001, []byte("admin to credential "+phase))
		device.read <- outgoing
		if got, err := peerCore.ReceivePacket(packetCtx); err != nil || !bytes.Equal(got, outgoing) {
			t.Fatalf("%s shared TUN -> credential: %v", phase, err)
		}
		incoming := easyTierRoutingUDP(netip.MustParseAddr("10.144.0.1"), netip.MustParseAddr("10.144.0.2"), 41002, 40002, []byte("credential to admin "+phase))
		if err := peerCore.SendPacket(packetCtx, incoming); err != nil {
			t.Fatal(err)
		}
		select {
		case got := <-device.write:
			if !bytes.Equal(got, incoming) {
				t.Fatalf("%s credential -> shared TUN packet changed", phase)
			}
		case <-packetCtx.Done():
			t.Fatalf("%s credential -> shared TUN: %v", phase, packetCtx.Err())
		}
		return connectionIDs
	}
	update := func(revision string) {
		t.Helper()
		_, status, err := request(http.MethodPut, base, map[string]any{
			"managed_network_configs": []map[string]any{{"instance_id": networkID, "network_config": config}}, "config_revision": revision,
		})
		if err != nil || status != http.StatusNoContent {
			t.Fatalf("update desired credential: HTTP %d err=%v", status, err)
		}
	}
	waitCredential := func(present bool) {
		t.Helper()
		wait("effective managed credential configuration", func() bool {
			owner.mu.Lock()
			host := owner.host
			owner.mu.Unlock()
			configurations := host.InstanceConfigurations()
			if len(configurations) != 1 {
				return false
			}
			var effective struct {
				Credentials []struct {
					ID string `toml:"credential_id"`
				} `toml:"managed_credentials"`
			}
			if err := toml.Unmarshal([]byte(configurations[0].ConfigTOML), &effective); err != nil {
				t.Fatal(err)
			}
			if !present {
				return len(effective.Credentials) == 0
			}
			return len(effective.Credentials) == 1 && effective.Credentials[0].ID == credentialID
		})
	}
	waitCredential(true)
	initialConnections := verifyAuthentication("created")
	initial := session.Endpoint
	config["managed_credentials"] = []any{}
	update("revoke-" + networkID)
	waitCredential(false)
	refresh()
	if session.Endpoint != initial {
		t.Fatal("credential revoke replaced the managed administrator core")
	}
	managed := session.Endpoint.(*corehost.Instance)
	for _, pair := range [][2]*corehost.Instance{{managed, peerCore}, {peerCore, managed}} {
		info, err := pair[1].ShowNodeInfo(ctx)
		if err != nil {
			t.Fatal(err)
		}
		wait("revoked credential connections closed and route withdrawn", func() bool {
			peers, err := pair[0].ListPeer(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range peers {
				if p.GetPeerId() == info.GetPeerId() && slices.ContainsFunc(p.GetConns(), func(c *apiinstance.PeerConnInfo) bool { return !c.GetIsClosed() }) {
					return false
				}
			}
			routes, err := pair[0].ListRoute(ctx)
			if err != nil {
				t.Fatal(err)
			}
			return !slices.ContainsFunc(routes, func(route *corehost.Route) bool { return route.GetPeerId() == info.GetPeerId() })
		})
	}
	t.Log("hot revoke closed the authenticated connection and withdrew both peer routes")
	config["managed_credentials"] = credentials
	update("restore-" + networkID)
	waitCredential(true)
	restoredConnections := verifyAuthentication("hot-restored")
	if session.Endpoint != initial {
		t.Fatal("credential restore replaced the managed administrator core")
	}
	for _, connection := range restoredConnections {
		if slices.Contains(initialConnections, connection) {
			t.Fatal("credential restore retained a revoked authenticated connection")
		}
	}
	server.stop(t)
	wait("WebClient disconnect", func() bool { return !owner.EasyTierSummary().WebClientConnected })
	server.start(t)
	wait("restarted server API", serverReady)
	wait("same machine reauthentication", authenticated)
	waitCredential(true)
	verifyAuthentication("server-restarted")
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	if err := mux.UpdateSessions(nil); err != nil {
		t.Fatal(err)
	}
	owner, err = NewEasyTier(option)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.OpenPacketSessions(ctx); err != nil {
		t.Fatal(err)
	}
	if owner.EasyTierSummary().InstanceID != machineID {
		t.Fatal("owner recreation changed the persisted machine identity")
	}
	wait("recreated owner authentication", authenticated)
	waitCredential(true)
	verifyAuthentication("owner-recreated")
	if session.Endpoint == initial {
		t.Fatal("owner recreation retained the previous administrator core")
	}
	_, status, err = request(http.MethodDelete, base+"/"+networkID, nil)
	if err != nil || status != http.StatusOK {
		t.Fatalf("remove persisted credential network: HTTP %d err=%v", status, err)
	}
	t.Log("managed credential authentication and bidirectional UDP passed create, hot revoke/restore, server restart, and owner recreation")
}
