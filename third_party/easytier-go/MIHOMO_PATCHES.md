# Mihomo host delta

This directory is a project-owned copy of the pinned module
`github.com/easytier/easytier/easytier-go` at Go version
`v0.0.0-20261005082529-8b7f1f0196ed` (official v2.7.0 source commit
`8b7f1f0196edb5b580829ed5f135050fb5853478`). It includes the upstream
license, generated protobufs, and embedded WASM artifact. The protobufs retain
the official v2.7.0 schema. The WASM is rebuilt from local staging commit
`ecc0a81ce7400933874943aba3644d87a0a92165`, with SHA256
`d01bf1c861dd57dbf0d3fc526260270dc39e44888aa9f307ed6786a4c2d6649a`, and
schema SHA256
`09d71b2964e418b15fcb42e6836f45c33f8c047aa1a93e71b83edf6e5622d454`.
The staged WASM checksum has been verified. The Go source revision and embedded
source revision remain distinct; the embedded artifact includes the local Rust
dynamic-environment and management changes recorded by the staging commit.
The ordered Rust patches, including vendored portable transport libraries,
are retained in `../easytier-core-patches`; its README documents regeneration.

Historical evidence: the previous embedded artifact at
`63519db2b5f2a6a1b9b7f20905f036dab54eb829` accepted an actual management
wire probe of v2.7.0 `InstanceConfigPatch` field 17, but did not enable
`flags.prefer_peer_relay` in the guest TOML. The upgraded artifact passes typed
true/false, effective/recovery-view agreement, and replacement recovery tests.
The official server test also confirms a hot update and persistence across
route replacement, reconnect and owner recreation. Other newly generated fields
still require their own workflow acceptance.

The fork-only host changes are:

- `host.Options.WebInstanceConfigPolicy` and
  `host.WebInstanceConfigPolicy`, which validate or rewrite configuration
  center TOML before Web-owned create, overwrite, or recovery. The callback
  also receives a cloned original `manage.NetworkConfig`, allowing mihomo to
  restore manual routes and exit-node settings omitted by guest conversion
  without mutating the saved request used for recovery;
- `host.InstanceConfiguration` and `Host.InstanceConfigurations`, a fast
  synchronized snapshot containing the upstream UUID, ownership, final TOML,
  and instance pointer used by mihomo's shared packet plane;
- `WebClient.Wait`, plus its engine implementation, so mihomo can observe a
  terminal configuration-center failure and close its owned lifecycle;
- a context-aware management mutation lock, effective TOML refresh on
  `GetConfig`, and replacement preflight ordering that leaves the old instance
  alive when policy validation fails;
- explicit typed dispatch for all eight official forwarded `PeerManageRpc`
  queries and `ConnectorManageRpc.ListConnector`, resolving the stable UUID
  and holding the management mutation lock across the selected core query;
- typed dispatch and bound-instance registration for mapped-listener, VPN
  portal-info, TCP proxy entries, ACL stats/whitelist, port-forward-list and
  metrics queries. Eighteen query cases pass against the rebuilt guest. The
  actual WASI build keeps packet-proxy and smoltcp gateway support enabled;
  ACL relay/subnet/exit and WebClient TCP port-forward tests exercise those
  data paths;
- Web-owned patch messages are passed intact to the core, preserving unknown
  protobuf fields. Known hostname, address, routes, exit nodes, mapped
  listeners, IPv6 provider settings, port-forward, ACL, proxy-CIDR, relay
  preference, VPN portal clients and managed credential changes are merged
  from the effective snapshot into the recovery view;
- address hot patches run the existing Web configuration policy before the
  core begins partial commits, using a structured TOML candidate. This adds
  the existing `go-toml/v2` dependency already used by the mihomo module;
- `engine.Instance.State` reports `stopping` once close has been requested,
  matching RPC rejection before the driver finishes its shutdown.
- guest-driven host message-tunnel bind, accept, connect and I/O for WS/WSS
  and FakeTCP, with lifecycle-owned listeners and platform socket/DNS policy;
- WG and QUIC UDP protocol adapters, real KCP/QUIC wrapped TCP engines, and
  WASI smoltcp capability declarations for the raw packet proxy path;
- IPv6 underlay and static overlay support, public lease projection into
  shared addresses/routes and Magic DNS, and live host address reservations.

Application-owned instances remain read-only to Web management. The host does
not add a generic raw management RPC or change the upstream WebClient wire
protocol. Keep this file alongside future upstream updates and review the
delta before changing the pinned version.

The copied upstream license remains in `LICENSE`.

The copied `tests/web_client_e2e_test.go` expects HTTP 204 for the official
v2.7.0 desired-network `PUT` response. Its default user ID remains an upstream
test assumption; the mihomo server integration test instead discovers the
authenticated machine's user ID from `/api/internal/sessions`.

The `cmd/update-wasm` absolute Cargo-target fixture uses temporary absolute
paths so it exercises the same contract on Windows and Unix. Its production
path resolver is unchanged.

`internal/host/web_mux_integration_test.go` is a mihomo-specific test behind
the `mihomo_integration` build tag. Run it from the mihomo root module to
resolve the shared packet mux; the standalone module's default tests exclude
it. It exercises management creation, patching, replacement and deletion with
two embedded cores and an in-memory packet device.

`internal/host/management_patch_test.go` verifies collection ADD/CLEAR,
effective/recovery configuration agreement, and IPv4/IPv6 address policy
admission against an actual embedded core. The tagged
`management_connector_test.go` verifies bidirectional packets after connector
ADD and absence of reconnect attempts after CLEAR and a real peer restart.
It also confirms forwarded Web queries return the connected peer route,
matching transport connection, and `CONNECTED` connector endpoint.
`management_query_test.go` passes all nine forwarded read methods against the
actual embedded guest, verifies node identity and address, leaves the managed
endpoint and configuration intact, and covers application-owned read access.

The official v2.7.0 WASI `hosted_network_config` conversion filters
`vpn_portal_config`; the read-only VPN portal query is registered, but mihomo
does not yet provide the required `PortalHost` adapter. The WASI build script
enables `wasm-host-tunnel-outbound` in Full connectivity mode, retaining gateway
and packet-proxy data paths covered by the focused relay/forward tests.
Credential management is registered and explicitly typed-dispatched, with
focused core lifecycle and permission tests; managed credentials have a server
recovery workflow. Direct credential RPC state is not claimed as persisted
desired state. See upstream `easytier-core/src/wasi/runtime.rs` and
`easytier-core/src/wasi/web_client.rs` for the remaining boundary.

The separately reviewed `w568w/easytier-go` baseline is kept only as a local
comparison checkout under `.tools/easytier-w568w/` (commit
`06b3a7a0bc9503780842858f15acd771dab2420a`, 2026-10-02). It exposes a pure-Go
`HostNetwork` and standard `net.Conn`/`net.Listener`/`net.PacketConn` API. It is
not substituted for this module: mihomo needs the official v2.7.0 WASI guest,
its generated management protobufs, and the host ABI used by the WebClient and
shared packet plane. The comparison informed the socket and DNS adapter shape;
no incompatible second EasyTier module is added to the build.
