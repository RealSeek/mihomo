# EasyTier configuration-center integration

This document records the implemented configuration-center host boundary and
the remaining end-to-end acceptance work for the full upstream feature-parity
goal. Keep the shared TUN limited to `stack: mips`, with GSO and AutoRedirect
disabled.

Use [the managed example](examples/easytier-managed.yaml) with this fork's
binary. Set the configuration-server endpoint/token and controller secret.
Keep a separate persistent state directory on each device; omitting
`machine-id` generates and persists its UUID. The local proxy must not mix
`web-client` with static network fields. Configure network identity, peers,
addresses and policies in the configuration center. Remote MTU must be at least
the shared TUN MTU. The pinned management schema has no persisted virtual IPv6
address or DNS-zone field. Remotely created networks use `et.net` and can
attach together with non-overlapping overlay address prefixes. Same-zone
DNS records form unique RRsets, including multiple addresses for one hostname.
Static networks can configure IPv6 and `tld-dns-zone`. Persisting and restoring
those additional Web-owned settings requires management schema support.

## Verified API boundaries

The pinned official Go host at
`v0.0.0-20261005082529-8b7f1f0196ed` contains the upstream WebClient protocol
and lifecycle. Its official v2.7.0 checkout is
`8b7f1f0196edb5b580829ed5f135050fb5853478`; the rebuilt WASM and protobufs
report local staging source
`b6b7d536c07ddfbe9e49bef969af944b064312db`. The verified WASM SHA256 is
`c429314b6a031d51b7865fab5af7c278c48db07d089bad3b659009e584eb13e2`,
and schema SHA256 is
`09d71b2964e418b15fcb42e6836f45c33f8c047aa1a93e71b83edf6e5622d454`.
These source revisions remain distinct.

- `Host.ConnectWebClient` accepts a TCP or UDP endpoint, stable machine UUID, hostname,
  and secure mode. The upstream Rust WebClient owns heartbeat, reconnect, and
  secure-tunnel behavior.
- `Host.Instances` lists both application-owned and Web-owned instances. A
  remote network can therefore use the same `SendPacket`/`ReceivePacket` data
  plane as a local network.
- Application instances created through `CreateInstanceTOML` are explicitly
  read-only to Web management. Remote overwrite, configuration reads, patches,
  retain, and delete do not transfer ownership of the existing mihomo outbound.
- Web-owned instances are created and started inside
  `internal/host/management.go`. The project copy adds a pre-create policy and
  typed effective-configuration snapshots to the pinned public host API.
- Runtime `ConfigRpc.GetConfig` and `PatchConfig` methods are internal. The
  host forwards the patch message intact to the core, including protobuf
  fields unknown to the pinned Go schema. Known patched fields are merged
  from the core's effective configuration into the saved desired view.
  Direct management tests verify hostname, routes, exit nodes, mapped
  listeners, and connector runtime changes. Address patches are validated
  against the shared TUN policy before any core partial commit.
  An actual field-17 wire probe showed that the previous artifact at
  `63519db2b5f2a6a1b9b7f20905f036dab54eb829` ignored the v2.7.0
  `prefer_peer_relay` patch despite returning success. The upgraded artifact
  passes typed true/false and replacement recovery checks. Official server
  desired-state tests confirm hot updates, server restart and owner recreation
  retain the effective value, packets and DNS. Credential management is now
  registered and typed-dispatched with focused core/Go permission tests, while
  persisted managed credentials have a real server recovery workflow. Raw
  credential RPC state is not claimed as persisted desired state. The official
  WASI `hosted_network_config` conversion filters `vpn_portal_config`, and the
  forwarded service registry still omits `VpnPortalRpc`.
- The official WASI WebClient forwards eight `PeerManageRpc` queries and
  `ConnectorManageRpc.ListConnector`. The Go host now explicitly decodes each
  request and response type, resolves its stable instance UUID, and holds the
  management mutation lock across the core query. Read-only queries can inspect
  both Web-owned and application-owned instances without changing ownership.
- The official v2.7.0 Web desired-state planner hot-patches hostname, ACL,
  port forwards, proxy CIDRs, and relay policies. Changes to manual routes,
  exit nodes, mapped listeners, static IPv4, and IPv6 provider settings use
  replacement. Connectors are only a runtime RPC option, absent from saved
  `NetworkConfig`; the host does not redefine the server planner's semantics.

The mihomo listener resolves owner names in `tun.easytier` at startup. Each
owner exposes its current packet sessions. `PacketMux.UpdateSessions` adds,
replaces, or removes their receivers by UUID. Local DNS zones are normalized
when the profile is parsed; Web-owned zones are read from current instances.

The mihomo adapter now owns the WebClient and consumes the typed host snapshot
for Web-owned packet-mode instances. The shared mux identifies instances by
their upstream UUID, adds/replaces/removes receivers atomically, and updates
Magic DNS and metadata with the same lifecycle. Local application-owned
instances remain fixed and read-only to Web management. A connected WebClient
alone still does not prove the physical shared-TUN objective.

## Implementation sequence

1. Keep a project-owned copy of the pinned official Go host, preserving its
   module path, embedded artifact, protobufs, provenance, and upstream license.
   Resolve it using a project-local `replace`. This requires no installation
   and no Rust rebuild for Go-only host changes. Track the small host delta
   against the pinned source so a future upstream update can be reviewed.
2. Add one host-level policy that receives the prepared native TOML before a
   Web-owned instance is created. The pinned host now exposes
   `host.WebInstanceConfigPolicy`:

   ```go
   type WebInstanceConfigPolicy func(
       context.Context, string, string, *manage.NetworkConfig,
   ) (string, error)
   ```

   Configure it through `host.Options.WebInstanceConfigPolicy`. The callback
   receives the stable upstream instance UUID, the prepared TOML, and a cloned
   original protobuf configuration. It restores manual routes, exit nodes, and
   the explicit exit-node flag omitted by the guest conversion. Its returned
   TOML is the document passed to the embedded core. The callback is
   run before an old instance is closed, including the replacement recovery
   preflight, so a rejected candidate cannot remove a working old instance.
   A typed `Host.InstanceConfigurations()` snapshot contains
   `InstanceID`, `WebOwned`, `ConfigTOML`, and the corresponding `*Instance`.
   `ConfigTOML` is refreshed from the core's effective `GetConfig` response
   after a hot patch. Apply the existing structured TOML parser to preserve
   upstream settings, force `no_tun = false` and `bind_device = false`, and
   validate the candidate against the shared TUN. Do not add a generic raw-RPC
   facade. This host delta is implemented in `third_party/easytier-go`; see
   `MIHOMO_PATCHES.md` there.
3. Add a process-level configuration-center owner in mihomo. Persist its
   machine UUID under the selected state directory, connect through mihomo's
   physical-interface dialer, and start it independently of network creation.
   An empty remote network list must still allow the ordinary mihomo TUN to
   start. The controller can report connected state and remote instance IDs
   without exposing the endpoint token or raw secrets.
4. Reconcile Web-owned network UUIDs into the existing shared TUN. Publish
   newly prepared endpoints before installing captured OS routes. Refresh
   IPv4/IPv6 addresses, MTU requirements, peer routes, proxy CIDRs, and exit
   routes from the effective runtime configuration. On replacement, move
   packet send and receive ownership to the new instance of the same UUID.
   On deletion, withdraw routes and DNS records, stop its receiver, and remove
   only addresses/routes owned by this integration.
5. Register and remove automatic Magic DNS networks with that same lifecycle.
   Preserve each upstream DNS zone and aggregate records from networks in the
   same zone. Validate address overlap before accepting a remote candidate,
   and return the concrete conflict through the configuration-center
   operation. Updating a remote zone must also update the DNS routing registry.
6. Retain upstream Web management ownership and hot-patch semantics. Remote
   hostname, port forwards, ACL, proxy-network, and relay changes must affect
   the same running instance whose packet plane is attached to the shared TUN. A
   profile reload or mihomo shutdown must close the WebClient and all of its
   attached network resources in one owned lifecycle.

The adapter implementation now covers items 3-6 for local lifecycle and
offline/restart handling. The actual host management handler and two embedded
cores now cover create, hot patch, same-UUID replacement, and delete with
bidirectional raw UDP through the shared mux. The dynamic DNS metadata path has
separate contract coverage. The official v2.7.0 server acceptance also covers
that lifecycle through the real WebClient adapter. Physical Windows/root
Android deployment remains an outstanding acceptance stage alongside the core
capability gaps above.

The mihomo platform supplies an interface snapshot when each host is created.
Started hosts also receive a revisioned snapshot after a network-change
notification; the guest invalidates its address cache and wakes STUN without
replacing the instance or WebClient. Direct TCP STUN probes disable TCP Fast
Open and close with `linger = 0`, allowing the core to inspect an established
source socket and release it for subsequent NAT traversal. Physical interface
handover remains unverified on Windows and rooted Android.

Remote candidates are checked against the same owner's static address
prefixes, existing DHCP leases, the mihomo TUN addresses, and its MTU.
Candidates also check published configurations of other attached owners without
issuing RPCs. An unavailable DHCP address is omitted until it becomes ready.
Concurrent creates and leases assigned after creation can still conflict; route
synchronization reports those errors rather than returning them to the original
remote create operation. Duplicate dynamic DNS zones share an aggregated
RRset; ambiguous short names across different zones produce a concrete query
error. Bare IPv6 addresses are accepted as `/128`, while explicit CIDRs retain
their prefix. Real embedded peers confirm both structured and native bare IPv6
configurations in runtime routes, session addresses, DNS, and status.

Do not force remote instances into a local outbound's UUID, close unrelated
user EasyTier processes, or recreate an independent EasyTier TUN. A remote
network should be identified by its upstream UUID rather than a mutable
hostname or list position.

## Required verification

Focused host tests should prove the policy is applied on remote creation and
replacement, errors are returned to the upstream management call, and ordinary
application ownership stays read-only. Shared-plane tests should use actual
embedded cores to prove adding a remote endpoint, replacing that instance, and
deleting it change packet dispatch and DNS together.

The official configuration-center protocol was run against an isolated v2.7.0
server and network identity. The official Go host test
`tests/web_client_e2e_test.go` documents the server's create, update, query,
and delete API. Extend this workflow to assert that the remotely created
instance uses `no_tun = false`, enters the shared packet plane, and reaches
ordinary applications by virtual IP and Magic DNS. Check a hot port-forward
or ACL change and an overwrite that replaces the core while keeping the same
network UUID. The real-server recovery test now confirms reconnection after
server restart and recovery of persisted desired networks after owner
recreation. Machine identity and network UUID remain stable, while the
recreated owner uses a new core endpoint. Bidirectional UDP and Magic DNS are
checked after each transition.

The recovery acceptance now starts with a saved manual route, changes it
through the official desired-state API, and verifies replacement under the
same UUID with the new route present and the old one removed. The new route
also survives server restart and owner recreation. Direct management tests
verify routes, exit nodes, and mapped listeners ADD/CLEAR with effective TOML,
desired configuration, and host snapshots in agreement. A real peer test
verifies connector ADD carries bidirectional packets and CLEAR stops future
connection attempts after that peer is stopped and restarted.

`internal/host/management_query_test.go` passes all nine official forwarded
read methods against the actual embedded guest, checks the selected node UUID
and address, and confirms queries leave the endpoint and configuration intact.
It also verifies application-owned instances remain available to read-only
queries. The real connector test confirms Web queries return the connected
peer's route, matching transport connection, and `CONNECTED` connector endpoint.

The real server's multi-network acceptance now verifies two independent
Web-owned UUIDs in distinct subnets through one packet mux. Same-zone DNS
names return aggregated unique addresses, hostname-only updates retain their
core endpoint, and deleting one network leaves the other network's packets
and DNS available. Recreating the owner restores the surviving desired UUID
with a new endpoint and working bidirectional UDP.

The physical Windows/root Android acceptance still needs to confirm one TUN,
correct virtual addresses and routes, application TCP/UDP traffic in both
directions, peer source addresses, DNS resolution, and cleanup. A WebClient
connection flag, static HTTP status fixture, or successful Go compilation is
insufficient evidence for this feature.

## Source references

- Official host public API and management ownership:
  <https://github.com/EasyTier/EasyTier/tree/main/easytier-go>.
- Official management messages:
  <https://github.com/EasyTier/EasyTier/tree/main/easytier-proto>.
- Native per-machine DNS aggregation flattens clients within each zone:
  <https://github.com/EasyTier/EasyTier/blob/v2.7.0/easytier-core/src/gateway/magic_dns/records.rs>.
- The native DNS server constructs one authority from those routes:
  <https://github.com/EasyTier/EasyTier/blob/v2.7.0/easytier/src/instance/dns_server/server_instance.rs>.
- Hickory's RRset insertion keeps distinct RDATA and ignores duplicate records:
  <https://github.com/hickory-dns/hickory-dns/blob/v0.25.2/crates/proto/src/rr/rr_set.rs>.
- Official desired-state patch/replacement planning:
  <https://github.com/EasyTier/EasyTier/blob/v2.7.0/easytier-web/src/client_manager/runtime_reconcile.rs>.
- Official WASI configuration conversion and forwarded service registry:
  <https://github.com/EasyTier/EasyTier/blob/v2.7.0/easytier-core/src/wasi/web_client.rs>
  and <https://github.com/EasyTier/EasyTier/blob/v2.7.0/easytier-core/src/management/forwarded_rpc.rs>.
- Pinned local source: `internal/host/host.go`,
  `internal/host/web_client.go`, `internal/host/management.go`,
  `internal/host/rpc.go`, and `tests/web_client_e2e_test.go` in
  `github.com/easytier/easytier/easytier-go@v0.0.0-20261005082529-8b7f1f0196ed`.

The isolated acceptance is recorded in
[easytier-server-acceptance.md](easytier-server-acceptance.md). It does not
change the pinned embedded core or establish physical TUN behavior.
