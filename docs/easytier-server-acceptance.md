# Official configuration-server acceptance

The official portable server was downloaded and run with user approval on
2026-10-07. The physical Windows/root Android single-TUN acceptance remains
separate. Shared TUN still requires `stack: mips`, with GSO and AutoRedirect
disabled.

## Isolated server

Use the official Windows x86_64 v2.7.0 prerelease server, published on
2026-10-06. The v2.6.4 server has no internal `PUT` desired-network endpoint;
v2.7.0 includes that endpoint used by the pinned Go host's E2E test. The server
download is separate from the subsequent project-source update to the official
v2.7.0 Go host, WASM and generated protobufs.

- Source: <https://github.com/EasyTier/EasyTier/releases/tag/v2.7.0>.
- Archive: `easytier-windows-x86_64-v2.7.0.zip`, 39,616,733 bytes.
- Download: <https://github.com/EasyTier/EasyTier/releases/download/v2.7.0/easytier-windows-x86_64-v2.7.0.zip>.
- SHA256: `790fd9be347b00753570ac2b65f874a591ede9427603fe34e74a86b2b084847f`.
- Local directory: `output/easytier/server-v2.7.0`; this is ignored by Git.
- Extracted `easytier-web.exe`, its required `Packet.dll` and `wintun.dll`, and
  `easytier-core.exe` for a temporary no-TUN reference check. No `.sys` file,
  driver installation, service, PATH, Scoop, mise, firewall rule, or existing
  EasyTier installation was changed.
- Run under the current unprivileged user with a new SQLite database,
  random API authentication token, random machine identity, and random network
  identity. Close only the child process started for this test.

The REST API can bind `127.0.0.1`. The configuration protocol has no listen
address option in this release: it binds `0.0.0.0` and, when available, `::`.
The test connects through loopback, but the temporary TCP protocol port can
also be reachable through other interfaces under existing firewall policy.
No firewall exception is needed for loopback testing.

## Startup and test scope

The downloaded ZIP matched the stated size and SHA256. Its entries were
inspected before extracting the selected files. Running the executable alone
returned Windows loader error `0xc0000135`; the two bundled DLLs resolved that
dependency. The server's `--help` then succeeded. Unused TCP ports were selected
by binding temporary listeners to port zero. Tokens are kept in the ignored
local output directory. The server was started with:

```text
easytier-web.exe
  --db sqlite:./acceptance.db
  --config-server-protocol tcp
  --config-server-port <selected TCP port>
  --api-server-addr 127.0.0.1
  --api-server-port <selected API port>
  --internal-auth-token <random API token>
  --allow-auto-create-user
  --disable-registration
  --console-log-level warn
```

Run from the isolated local directory and capture stdout/stderr there.
SQLite automatically creates and migrates the new
database. Auto-creation lets the first heartbeat register an isolated token
without interacting with the CAPTCHA-based registration UI.

Set test-process environment variables, without printing token values:

```text
EASYTIER_WEB_E2E_ENDPOINT=tcp://127.0.0.1:<protocol port>/<random user token>
EASYTIER_WEB_E2E_API=http://127.0.0.1:<API port>
EASYTIER_WEB_E2E_AUTH=<random API token>
```

Resolve the user ID from the authenticated internal sessions endpoint after
the test machine connects. This test server assigned user ID 3. The copied
upstream E2E test's initial run assumed ID 1 and timed out querying its
application heartbeat; that timeout does not demonstrate protocol incompatibility.
The mihomo integration test discovers the matching machine's actual user ID.

The official v2.7.0 no-TUN native core registered against the same server and
token path. The mihomo WebClient also registered and its network creation
request returned HTTP 200. An initial `save:false` request was automatically
removed by v2.7.0 desired-state reconciliation. Persistent acceptance uses
`save:true` for this isolated machine and removes its networks during cleanup.

The real-server acceptance passed HTTP creation, live patching, replacement
under the same UUID, and deletion through mihomo's actual WebClient adapter.
Each lifecycle transition was verified using the shared raw packet mux, a
second embedded peer, and Magic DNS. The in-memory packet device proves the
server and adapter protocol workflow, not native TUN address/route setup.

Reconnection and desired-state recovery also passed against the official
portable v2.7.0 server. The recovery test creates a new database and identity,
persists a network, stops the owned server process, waits for disconnection,
and restarts the server on the same ports and database. The existing owner
authenticates again with the same machine ID. Closing and recreating the
owner without an explicit `machine-id` restores its persisted generated ID,
the same managed network UUID, and a new core endpoint from server state.
Each stage verifies bidirectional UDP through the same packet mux and Magic
DNS. The test removes its network, stops its child processes, and removes its
owned temporary database directory. The Windows/root Android physical
procedure is in `easytier-integration.md`.

The recovery scenario also persists manual route `10.211.0.0/24`, updates the
desired configuration to `10.212.0.0/24`, and confirms the server replaces the
core under the same UUID. Packet sessions publish the new route and withdraw
the old one. Both server restart and owner recreation retain the new route,
with bidirectional raw UDP and Magic DNS checked after every transition.
After the embedded module upgrade, the recovery scenario also changes
`prefer_peer_relay` from false to true through a desired-state `PUT`. The
effective guest TOML changes without replacing the endpoint. Manual-route
replacement, server restart, and owner recreation preserve the enabled value.
The extended scenario passed in 8.70 seconds, checking raw packets and DNS after
each transition. The upgraded multi-network scenario also passed in 10.85
seconds; copied-module and mihomo default suites passed against the new core.

After adding all nine official forwarded read methods and the host's initial
interface snapshot, these scenarios passed again: recovery in 9.51 seconds
and multiple networks in 10.45 seconds. The selected node, route, peer and
connector queries are separately verified against actual embedded cores,
including a live peer connection. The new snapshot supplies system interface
addresses and IPv6 source indexes; it remains frozen at host creation and does
not establish Wi-Fi/mobile handover support.

The focused adapter regression initially exhausted the offline-owner test's
10-second total budget during its third successful WASM startup. Its three
starts and restart backoff now have a 30-second test context. The failed test
alone then passed in 8.22 seconds, retaining the generated machine ID across
driver restart and owner recreation. Production lifecycle timeouts were not
changed.

Multi-network acceptance also passed using two persisted Web-owned networks
with distinct `10.144.0.0/24` and `10.145.0.0/24` overlays, two real embedded
peers, and one shared packet mux. Both default `et.net` networks deliver
bidirectional raw UDP and resolve their hosts. A hostname-only desired-state
update now hot-patches the core without replacing its endpoint; giving both
networks the hostname `alpha` returns a unique two-address DNS RRset. Restoring
the second hostname, deleting the first network, and recreating the owner
preserve the second network's UUID, packet delivery, and DNS. The recreated
owner uses a new core endpoint. The test cleans up both saved networks, its
owned server process, and its temporary database directory.

The first multi-network run exposed a hostname-only update that the server
classified as a hot patch while the Go host discarded that field. The host
now forwards it and preserves the core's effective hostname for recovery.
An actual embedded-core management test also confirms the updated runtime
hostname survives replacement recovery.

Run the adapter acceptance from the root module with the variables above:

```powershell
mise.exe exec -- go test -tags mihomo_integration ./adapter/outbound -run '^TestEasyTierWebClientServerPacketMux$' -count=1 -v
```

The Windows recovery and multi-network tests start the already extracted
executable themselves, using fresh `_recovery-*` and `_multinetwork-*`
directories beside it. No installation or additional download is performed:

```powershell
$env:EASYTIER_WEB_E2E_SERVER_EXECUTABLE = (Resolve-Path output/easytier/server-v2.7.0/easytier-web.exe).Path
mise.exe exec -- go test -tags mihomo_integration ./adapter/outbound -run '^TestEasyTierWebClientServer(MultipleNetworks|Recovery)$' -count=1 -v
```

Persisted ACL and TCP port-forward behavior also passed against the official
server in 9.54 seconds. A saved Inbound ACL permits one UDP destination port
and drops another; receipt of the permitted control packet verifies the live
path before checking the rejected packet. A desired-state update swaps the
permitted port and replaces a TCP forward's bind without replacing the core.
Ordinary host TCP sockets exchange echo data through the forward to a real
embedded peer, and the removed bind can be acquired again. Server restart and
owner recreation preserve the ACL and forward; machine and network identities
stay stable. Clearing both through the desired-state API releases the bind and
allows the previously rejected UDP packet. The test cleans up its saved
network, child process and owned `_policy-*` directory.

```powershell
mise.exe exec -- go test -tags mihomo_integration ./adapter/outbound -run '^TestEasyTierWebClientServerACLAndPortForwardRecovery$' -count=1 -v
```

Managed credential recovery also passed against the same isolated v2.7.0
server. The workflow saves an X25519 credential, authenticates a second core,
checks credential identity and peer verification, revokes it, restores it,
then repeats authentication after server restart and owner recreation. The
test uses the persisted `managed_credentials` desired field; direct credential
RPC state is not represented as persisted desired configuration.

```powershell
mise.exe exec -- go test -tags mihomo_integration ./adapter/outbound -run '^TestEasyTierWebClientServerManagedCredentialRecovery$' -count=1 -v
```

The environment-refresh acceptance passes with two Web-owned networks. It
updates underlay addresses without replacing packet endpoints, retains UUIDs,
routes and Magic DNS while the server is offline, reauthenticates after server
restart, and applies a same-UUID route replacement with the newest environment
facts. The focused embedded-host tests also cover malformed/stale revisions,
static listener retention, concurrent packet/RPC/drive calls and race safety.

## Source evidence

- Server flags and binding behavior:
  <https://github.com/EasyTier/EasyTier/blob/v2.7.0/easytier-web/src/main.rs>.
- Internal desired-network `PUT`/`PATCH` responses are HTTP 204:
  <https://github.com/EasyTier/EasyTier/blob/v2.7.0/easytier-web/src/restful/network.rs>.
- Reconciliation deletes Web-owned instances absent from persisted desired state:
  <https://github.com/EasyTier/EasyTier/blob/v2.7.0/easytier-web/src/client_manager/session/runtime_revision.rs>.
- The pinned Go WASM host only accepts TCP and UDP configuration transports;
  native WebSocket support does not imply that this host supports it:
  <https://github.com/EasyTier/EasyTier/blob/3d0c9c3ca5e2/easytier-core/src/wasi/web_client.rs>.
