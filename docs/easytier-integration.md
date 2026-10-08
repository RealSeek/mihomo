# EasyTier single-TUN integration

## Objective and deployment

Run mihomo and the official EasyTier core in one process on Windows and on an
Android phone running the mihomo binary through root / su. Use one mihomo TUN
with `stack: mips`. Applications must reach peers by virtual LAN IP and Magic
DNS name, and peers must initiate connections to ordinary local application
sockets while preserving peer source addresses.

Complete upstream feature parity remains the goal, including discovery,
encryption, NAT traversal, relay, subnet routing, exit nodes, ACL, port
forwarding, and web management. The implementation and build checks below do
not establish complete parity. The physical Windows/root Android shared-TUN
results are recorded in [physical acceptance](easytier-physical-acceptance.md).
The supplied RealSeek network and its existing HTTP service also passed
[real-network acceptance](easytier-real-network-acceptance.md), including the
phone browser. That record identifies the unavailable listener transports.

## Architecture

- The pinned official `github.com/easytier/easytier/easytier-go` module at
  `v0.0.0-20261005082529-8b7f1f0196ed` hosts an embedded WASM build of the
  upstream Rust core. Its
  generated protobufs derive from `easytier-proto`; the peer protocol is not
  reimplemented in mihomo. The project-local copy under
  `third_party/easytier-go` retains the artifact and license, adding Go host
  policies, effective configuration snapshots, and WebClient termination
  observation. Its delta is recorded in `MIHOMO_PATCHES.md`.
  Official checkout `8b7f1f0196edb5b580829ed5f135050fb5853478` is the
  v2.7.0 tag. The embedded core is rebuilt from local staging commit
  `ecc0a81ce7400933874943aba3644d87a0a92165`, with WASM SHA256
  `d01bf1c861dd57dbf0d3fc526260270dc39e44888aa9f307ed6786a4c2d6649a`
  and schema SHA256
  `09d71b2964e418b15fcb42e6836f45c33f8c047aa1a93e71b83edf6e5622d454`.
  The rebuilt hash is verified; capability checks use the actual guest.
- `Instance.SendPacket` and `Instance.ReceivePacket` connect the upstream IP
  packet plane to the existing mihomo TUN. Overlay-bound packets enter EasyTier
  before mihomo's transport stack. Received packets go directly into that same
  TUN so the OS can deliver them to local sockets.
- Attached outbound names are configured in `tun.easytier`. The listener starts
  their instances, adds overlay addresses to its own TUN, and installs overlay
  routes. Lease and route updates are reconciled after startup. The desktop and
  Android examples below use static addresses for the first physical check.
  DHCP requires a reachable peer in the existing network; an isolated node
  without an assigned address cannot initialize this shared TUN. Startup waits
  up to 30 seconds for the attached networks to expose their runtime addresses.
- Underlay sockets use mihomo's dialer. `auto-detect-interface: true` binds them
  to the physical default interface. Android uses native TUN creation through
  `file-descriptor: 0`; this deployment requires root, a usable `/dev/net/tun`,
  and permission to manage addresses and routes. No Android client or
  `VpnService` is required for this deployment.
- Attached networks automatically handle Magic DNS A/AAAA/PTR and short hostnames
  before fake-IP processing. Overlay answers keep their real virtual addresses.
  Ordinary qualified DNS names continue through the configured mihomo
  resolvers. Networks sharing a DNS zone merge their records into unique
  RRsets. Short names present in different zones require a qualified name.
- The controller exposes `GET /easytier/` and `GET /easytier/{name}` behind its
  existing authentication. Status queries do not start idle instances. Runtime
  snapshots project node, peer, route, and embedded-core provenance fields;
  they omit raw configuration, private keys, and credential-bearing URLs.
- `config-toml` supplies native upstream configuration for options beyond the
  structured YAML fields. Parsing native TOML alone does not prove that every
  configured feature can operate through the WASM host. Native `routes`, when
  present, replace automatically discovered proxy CIDRs, including an explicit
  empty list; peer IP and local overlay subnet routes remain available. Actual
  embedded-core tests first confirm a peer proxy-CIDR advertisement, then check
  empty and explicit route overrides with the local subnet and peer host route
  preserved.
- The structured outbound also exposes official transport and relay controls
  that are useful without a separate native TOML file: IPv6 public address
  discovery, TCP STUN servers, protocol/device selection, compression, relay
  whitelists and transport policy, TCP/UDP ACL whitelists, UPnP/data-relay
  policy, UDP broadcast relay, per-instance and foreign-relay limits, QUIC
  listen port, and socket-mark TOML. Direct Linux/Android sockets forward
  socket marks through the existing dialer; proxy/custom dialers reject marks
  they cannot honor, and non-Linux platforms retain their existing limitation.
  These fields render into the
  corresponding upstream top-level or
  `[flags]` keys. `no_tun` and `bind_device` remain owned by mihomo's shared
  packet plane; WebClient options must be configured in the configuration
  center and reject local static overrides.
- `web-client` connects a configuration-center owner independently of network
  creation. Web-owned UUIDs are reconciled into the same TUN, DNS, and route
  lifecycle. Offline/empty owners allow ordinary TUN startup; machine identity
  survives restarts. See [configuration-center integration](easytier-webclient-plan.md)
  for configuration and acceptance limits. The actual server protocol workflow
  has been tested against the isolated official v2.7.0 server; the result and
  command are recorded in `easytier-server-acceptance.md`.

Network-change notifications now refresh the complete underlay snapshot in
each already-started EasyTier Host, including WebClient owners. The versioned
guest update preserves static listener declarations, invalidates address
caches, wakes STUN discovery, and keeps instances, UUIDs, packet endpoints,
routes and DNS. Idle owners remain idle. ABI, race, offline reconnect, server
restart and same-UUID replacement tests pass; physical handover is pending
Windows and rooted Android devices.

Addresses currently assigned to EasyTier sessions on the shared TUN are
excluded from later underlay snapshots. This prevents a mobile or desktop
handover from advertising the overlay address as a physical source address;
the exclusion is exact-address based and is released when the listener closes.

References:

- https://github.com/EasyTier/EasyTier/tree/main/easytier-go
- https://github.com/EasyTier/EasyTier/tree/main/easytier-proto
- https://github.com/w568w/easytier-go

## Two-device configuration

Use [the Windows example](examples/easytier-desktop.yaml) and
[the root Android example](examples/easytier-android-root.yaml) with executables
built from this fork. An ordinary upstream mihomo executable does not include
these changes. Both examples use `stack: mips`, MTU 1380 in the TUN and the
EasyTier instance, `gso: false`, and `auto-redirect: false`.

| Setting | Windows desktop | Root Android |
| --- | --- | --- |
| EasyTier outbound name | `mesh` | `mesh` |
| Network name | `mihomo-mesh` | `mihomo-mesh` |
| Virtual IPv4 | `10.144.0.1/24` | `10.144.0.2/24` |
| Virtual IPv6 | `fd00:144::1/64` | `fd00:144::2/64` |
| Hostname | `desktop` | `phone` |
| Magic DNS name | `desktop.et.net` | `phone.et.net` |
| Bootstrap | Listens on TCP port 11010 | Connects to desktop port 11010 |
| DNS listener | `127.0.0.1:53` | `127.0.0.1:53` |
| Controller | `127.0.0.1:9090` | `127.0.0.1:9090` |

All three YAML examples passed `mihomo -t` using the Windows executable built from
this worktree. This validates the configuration schema, including the Android
settings, but does not execute Android device setup.

Before starting, replace `CHANGE_ME_NETWORK_SECRET` with the same private value
on both devices and choose a controller secret. Replace `203.0.113.10` in the
phone's peer URI with an address that can reach the desktop listener. That
documentation address is a placeholder. On the same LAN use the desktop's
physical LAN IP; across the Internet the endpoint must be publicly reachable,
with any required TCP port forwarding and firewall rule. This initial topology
depends on the desktop's reachable listener; a public relay is a later separate
configuration. Choose an overlay subnet that does not overlap existing LAN or
VPN routes.

The examples bind DNS and the controller to loopback and use `MATCH,DIRECT` for
ordinary mihomo traffic. Overlay IP packets are selected by the shared packet
plane before proxy rules. `allow-lan: false` controls mihomo proxy listeners;
it does not prevent a separately running application from accepting overlay
connections. Start that application on its virtual IP or `0.0.0.0` and permit
its service port in the device firewall.

The TUN intercepts ordinary UDP/TCP DNS on port 53. Android Private DNS and
application DoH can bypass it. For the initial name-resolution check, use plain
DNS through this listener or disable the relevant encrypted DNS setting.
Querying `desktop.et.net` or `phone.et.net` avoids application search-suffix
differences; short `desktop` and `phone` names are supported by the DNS service.
No `nameserver-policy` or `fake-ip-filter` entry is required for attached Magic
DNS names. If port 53 is already occupied, resolve the listener conflict before
starting these exact examples.

### Run existing executables

From an elevated Windows PowerShell, with this fork's `mihomo.exe` already
present and a writable state directory:

```powershell
.\mihomo.exe -t -d .\state-desktop -f .\docs\examples\easytier-desktop.yaml
.\mihomo.exe -d .\state-desktop -f .\docs\examples\easytier-desktop.yaml
```

On Android, assuming this fork's executable and the Android example are
already present under `/data/local/tmp`, and the state directory already
exists and is writable by the root process:

```sh
su -c '/data/local/tmp/mihomo -t -d /data/local/tmp/mihomo-state -f /data/local/tmp/easytier-android-root.yaml'
su -c '/data/local/tmp/mihomo -d /data/local/tmp/mihomo-state -f /data/local/tmp/easytier-android-root.yaml'
```

`-t` validates configuration without creating a TUN or proving that the peer
endpoint is reachable. Keep each device's state directory across restarts so
its EasyTier instance ID is retained. These commands install no tools and do
not require a separate EasyTier process. Stop the foreground process normally
when checking route cleanup.

### Read-only device probe

The repository includes read-only probes for the physical stage. They do not
start or stop mihomo, change routes or addresses, alter firewall state, or
install tools. The Windows probe checks the authenticated `/easytier/{name}`
status, one shared interface carrying the expected overlay addresses, overlay
routes, optional Magic DNS records, and an optional ICMP probe. Run it from an
elevated PowerShell after both nodes are running:

```powershell
.\tools\easytier-physical-acceptance.ps1 `
  -Controller http://127.0.0.1:9090 `
  -Secret $env:MIHOMO_CONTROLLER_SECRET `
  -Instance mesh `
  -ExpectedIPv4 10.144.0.1 `
  -ExpectedIPv6 'fd00:144::1/64' `
  -TunInterface Mihomo `
  -ExpectedIPv4Route 10.144.0.0/24 `
  -ExpectedIPv6Route 'fd00:144::/64' `
  -RemoteIPv4 10.144.0.2 `
  -RemoteIPv6 'fd00:144::2' `
  -MagicDnsNames @('phone.et.net')
```

The expected addresses belong to the local node. Remote addresses and Magic DNS
names belong to its peer; ICMP probes must target that peer rather than the
local overlay address. Both address families must select the same TUN interface.
The Android probe is a POSIX shell script and must be invoked through the existing
root mechanism. It uses an already present `curl` or `wget`, `ip`, `grep`, and
optionally `nslookup`; it never installs a replacement:

```powershell
adb push tools/easytier-physical-acceptance.sh /data/local/tmp/
adb shell su -c "chmod 0755 /data/local/tmp/easytier-physical-acceptance.sh"
adb shell su -c "CONTROLLER=http://127.0.0.1:9090 SECRET=$env:MIHOMO_CONTROLLER_SECRET INSTANCE=mesh TUN_INTERFACE=Mihomo EXPECTED_IPV4=10.144.0.2 EXPECTED_IPV6='fd00:144::2/64' EXPECTED_IPV4_ROUTE=10.144.0.0/24 EXPECTED_IPV6_ROUTE='fd00:144::/64' REMOTE_IPV4=10.144.0.1 REMOTE_IPV6='fd00:144::1' MAGIC_DNS_NAMES='desktop.et.net' /data/local/tmp/easytier-physical-acceptance.sh"
```

If the phone has no ADB connection, copy the same script through the existing
root file-transfer path and run it locally with `su -c`; a missing `curl` or
`wget` is reported as an unmet prerequisite rather than installed. A successful
probe is evidence for status, address, route, DNS, and ICMP checks only. TCP,
UDP, source-address, handover, cleanup, NAT/relay, and feature-parity checks
remain the manual steps below.

The Windows/root Android run on 2026-10-08 passed bidirectional IPv4/IPv6
ordinary system-socket TCP/UDP, ICMP, and explicit Magic DNS queries through
the shared `mips` TUN. The isolated topology, Android IPv6 reply-route fix,
evidence boundaries, and cleanup status are recorded in
[physical acceptance](easytier-physical-acceptance.md).

## Execution plan and evidence

| Work item | Current implementation and evidence | Remaining acceptance |
| --- | --- | --- |
| Upstream host selection | Official pinned core and generated protobufs are used. Existing Go 1.27.1 is selected in project-local mise. | Track capability differences from the native official binary. |
| Single TUN packet path | Raw packet dispatch, OS injection, native checksum completion, MTU selection, and platform framing are implemented. The 2026-10-08 physical run passed bidirectional IPv4/IPv6 ordinary TCP/UDP sockets and ICMP through each shared TUN. | Broader physical topologies and lifecycle checks; see the physical acceptance record for scope. |
| Lifecycle and routes | Attached startup, core replacement, runtime address/route refresh, and platform route helpers are implemented. A connected DHCP client obtained an actual `10.144.0.2/24` lease from the embedded official core. | DHCP lease changes, Wi-Fi/mobile handover, shutdown cleanup, subnet/exit-node behavior on devices. |
| Automatic Magic DNS | Focused DNS/config tests pass for A/AAAA/PTR, IPv6 reverse lookup, short names, longest-zone selection, same-zone RRset aggregation and deduplication, fake-IP bypass, normal DNS fallback, and cross-zone ambiguity errors. Real Web-owned networks confirm same-name two-address aggregation. | OS/application resolution on both real devices. |
| Management API | Focused HTTP/idle-state tests pass with authentication, wrapper lookup, bounded queries, safe fields, and `no_easytier`. Web owner status includes connection state and safe per-network summaries. Both physical nodes report running and a live peer connection. | Remote WebClient instance mapping on devices. |
| Configuration center | Host policy tests exercise actual embedded-core management creation, rejected replacement, and recovery. The official v2.7.0 server tests verify create, hot patch, same-UUID replacement, delete, server restart/reconnect, persisted machine identity and desired-network recovery after owner recreation, bidirectional raw UDP through the shared mux, and Magic DNS. A persisted manual route changes from `10.211.0.0/24` to `10.212.0.0/24` through instance replacement; reconnect and owner recreation retain the new route and withdraw the old one. Two distinct Web networks also pass simultaneous packet delivery, hostname hot patches, same-zone RRset aggregation, deletion isolation, and owner recreation. | Physical shared-TUN acceptance and the Web management schema gaps listed below. |
| Native configuration and feature coverage | Structured options include STUN/P2P/hole-punch policies, SOCKS5 gateway portals, and TCP/UDP port forwards; native `config-toml` retains additional upstream fields. Actual management tests verify routes, exit-node and mapped-listener ADD/CLEAR, IPv4/IPv6 address policy admission, and connector ADD packet delivery with CLEAR preventing reconnect after a peer restart. The upgraded guest confirms typed `prefer_peer_relay` true/false, replacement recovery, and official server hot updates and desired-state restoration. | Demonstrate each required upstream feature end to end, including unsupported host capabilities. VPN portal and managed-credential fields now have matching generated types but their complete workflows remain unverified. |
| Core interoperability | Two embedded official cores exchange raw IPv4 and IPv6 TCP/UDP/ICMP packets through the shared mux. IPv6-only and dual-stack session/DNS tests pass. Interoperation with the installed native `easytier-core 2.6.4-8428a89d~` passed real TCP port forwards in both directions, raw UDP to an ordinary host socket, and the first native-initiated UDP packet plus its reply. The physical Windows/Android run passed ordinary dual-stack OS socket traffic. | Wider physical native-node topologies. |
| NAT socket capabilities | Direct mihomo sockets preserve EasyTier local TCP/UDP binding and address/port reuse requests. TCP STUN probes disable TFO and use zero linger; a real socket test confirms reset on close and immediate source-port reuse. Proxy/custom dialers report unsupported binding/reuse requests. | Confirm real hole punching across independent NATs and relay fallback on devices. |
| Host interface environment | Both static and WebClient hosts supply a system interface snapshot through mihomo's existing Android-aware interface API. Started hosts receive revisioned refreshes after network-change notifications; the guest invalidates address caches and wakes STUN without replacing instances. IPv4 discovery applies desktop virtual-interface filtering while Android retains point-to-point mobile interfaces, and active shared-TUN overlay addresses are excluded from later underlay snapshots. | Physical Wi-Fi/mobile handover and source-address behavior on Windows/root Android. |
| Relay, ACL, subnet and exit node | Three actual embedded cores passed a two-hop topology with Inbound/Forward ACL Drop/Allow, mapped proxy CIDR delivery, and exit-node UDP requests/replies through ordinary OS sockets. | Physical routing, firewall and source-address behavior on both devices. |
| Platform builds | Windows amd64, Linux amd64, Android arm64, and Darwin arm64 binaries were rebuilt with CGO disabled after the official v2.7.0 embedded module update. Desktop, root Android, and managed examples passed `mihomo -t` earlier. Windows and a rooted Android 16 phone executed the shared TUN on 2026-10-08. | Physical Linux and Darwin deployment. |
| Final audit | Both full `go test ./...` suites passed after the official v2.7.0 module update. Tagged connector and management/shared-mux tests passed, as did the real official server multi-network and extended reconnect tests. The reconnect scenario verifies relay preference hot updates, persisted manual-route replacement, server restart, owner identity and desired-state restoration, packets and DNS. Eighteen instance-management query cases pass against the rebuilt WASM, including all eight newly added service queries. Four CGO-disabled `with_gvisor with_ebpf` binaries were rebuilt with the updated host, WASM and protobufs. Physical acceptance remains separate. | Builds and in-memory/server tests do not substitute for physical TUN acceptance or full upstream parity. The upgraded WASI artifact enables `wasm-host-tunnel-outbound` in Full connectivity mode and retains packet-proxy and smoltcp gateway support; ACL relay/subnet/exit, WebClient TCP port-forward and wrapped TCP tests exercise those paths. Direct Linux/Android sockets now forward socket marks; proxy/custom dialers and unsupported host platforms remain explicit limitations. |

Physical Windows/root Android address, route, and bidirectional packet checks
passed on 2026-10-08; see [the physical run](easytier-physical-acceptance.md).
Handover, independent-NAT/relay, and the broader upstream feature workflows
still require their own acceptance. Compilation alone does not establish those
results.

After adding forwarded Web queries and the initial interface snapshot, the
official server scenarios passed again: recovery in 9.51 seconds and multiple
networks in 10.45 seconds. Recovery retains machine identity, the managed UUID,
relay preference and the replaced manual route through server restart and
owner recreation, with raw UDP and Magic DNS after each transition. All nine
official forwarded read methods pass actual-core tests; a real connected peer
also confirms consistent route, peer and connector query results. The four
CGO-disabled platform binaries were rebuilt with these changes.

The official-server ACL and TCP port-forward scenario also passed in 9.54
seconds. Saved creation and hot updates change actual permitted/denied UDP
delivery and ordinary host TCP echo traffic through an embedded peer. Server
restart and owner recreation retain the policies, while CLEAR restores UDP
permission and releases the removed forward's local TCP bind. These tests use
the shared in-memory raw mux and do not claim physical TUN delivery.

Run the management/shared-mux integration test from the mihomo repository root:

```powershell
mise.exe exec -- go test -tags mihomo_integration github.com/easytier/easytier/easytier-go/internal/host -run '^TestWebManagementSharedPacketMux$' -count=1
```

The explicit build tag lets this copied-host test import mihomo's packet mux
without adding a dependency to the upstream module. It uses two embedded cores,
an in-memory device, and an isolated loopback listener. It does not connect a
configuration server, create an OS TUN, or install tools.

### Repeat native interoperability

The Windows integration test uses an already installed native core. Set its
path explicitly; an EasyTier GUI executable that supports CLI arguments also
works. This test starts a temporary no-TUN instance with an isolated network
identity and loopback listeners, then terminates only its own child process.
It does not install tools or modify another EasyTier instance.

```powershell
$env:EASYTIER_NATIVE_EXECUTABLE = 'D:\easytier-gui\easytier-gui.exe'
mise.exe exec -- go test ./adapter/outbound -run '^TestEasyTierNativeInterop$' -count=1 -v
```

Without this environment variable, the explicit native integration check is
skipped. Its packet device is in memory, so this proves native protocol and
host-socket interoperability, not Windows TUN address or route setup.

## Physical acceptance procedure

1. Start the desktop, then the phone, with the configurations above. Inspect
   `/easytier/mesh` on each local controller using its bearer secret. Expect
   state `running`, a live peer connection, and routes for the other node.
2. Verify exactly one mihomo TUN per device, with its own overlay addresses and
   routes to `10.144.0.0/24` and `fd00:144::/64`. Windows
   `Get-NetIPAddress -AddressFamily IPv4,IPv6` and `Get-NetRoute` expose this
   state. On Android
   use the already available `ip addr` and `ip route show table all` under root.
3. Ping `10.144.0.2` and `fd00:144::2` from Windows, and `10.144.0.1` and
   `fd00:144::1` from Android. Query
   `phone.et.net` and `desktop.et.net` through each local DNS listener. On Windows
   use `Resolve-DnsName phone.et.net -Type A -Server 127.0.0.1`; on Android use
   an already present DNS client or an application using the hijacked plain DNS
   path. Verify real `10.144.0.x` answers rather than `198.18.x.x` fake addresses,
   then verify A, AAAA, PTR, and short-hostname lookup. The controller's `/dns/query`
   endpoint uses the raw resolver and is not an acceptance check of the DNS
   service's automatic EasyTier middleware.
4. Use already available TCP and UDP applications on both devices. Connect to
   each by overlay IP and by Magic DNS name in both directions. Inspect the
   receiving application's peer address to confirm the other overlay IP.
   Include a remote-initiated first UDP datagram and an MTU-sized transfer.
5. Change the phone's underlay between Wi-Fi and mobile data, restart a core,
   and stop/restart mihomo normally. Check reconnection, DNS refresh, stable
   instance ID, and removal of owned addresses/routes without removing unrelated
   routes. Repeat using DHCP when that path is accepted.
6. Add a native official EasyTier node, a subnet router, and an exit node, then
   exercise routing, NAT traversal/relay, encryption, ACL, port forwarding, and
   WebClient management with the same network identity. Record per-feature
   evidence and host limitations before claiming full upstream equivalence.

## Known gaps

- Physical Wi-Fi/mobile handover on Windows and rooted Android remains pending.
  The Go/WASI refresh path is implemented and tested, but device acceptance
  still needs one shared TUN, route, Magic DNS, source-address and NAT/relay
  run on each platform.
- Official WASI Web configuration conversion filters VPN portal configuration.
  Its read-only management query is registered, but the portal feature remains
  disabled. Credential management is registered and typed-dispatched, with core
  in-memory lifecycle and permission tests. Persisted managed credentials are
  accepted and recovered through the configuration-center desired schema; raw
  credential RPC state is not claimed as persisted recovery.
- The patched WASI build enables `proxy-smoltcp-stack` and host message tunnels
  together. Connectivity uses Full mode, with host smoltcp capabilities declared
  explicitly, so packet-proxy and smoltcp gateway paths remain active. The three-core ACL/subnet/exit
  test and the WebClient ACL/TCP port-forward recovery test exercise those
  paths. The VPN portal still has no mihomo `PortalHost` adapter. Direct
  Linux/Android sockets apply socket marks through mihomo's dialer; proxy/custom
  dialers reject marks they cannot honor.
- The host rejects network namespaces. FakeTCP uses real raw capture/injection
  on Linux/root Android and WinDivert on Windows amd64. WinDivert must already
  be running; the backend uses NO_INSTALL and never installs its own driver.
- The current WASI runtime injects WG/QUIC UDP adapters, WS/WSS host message
  transports and actual KCP/QUIC wrapped TCP engines. WG/QUIC IPv4/IPv6 underlay
  packet tests and WG/QUIC/WS/WSS official v2.7.0 native interoperability passed.
  KCP and QUIC raw NIC TCP tests confirm handshake, echo and both ends' precise
  Connected entries. The original real-network run used TCP; see
  [real-network acceptance](easytier-real-network-acceptance.md).
- The pinned Go host accepts only TCP/UDP configuration-center endpoints.
  Native EasyTier's WebSocket and secure WebSocket configuration transports
  require additional guest support; accepting their URLs in Go alone does not
  implement them.
- Passing unknown protobuf fields through the Go host does not add support
  to the embedded core. A historical direct `InstanceConfigPatch` field-17
  wire probe against artifact
  `63519db2b5f2a6a1b9b7f20905f036dab54eb829` returned success but left
  `flags.prefer_peer_relay` unset in the guest's effective TOML. The current
  official v2.7.0 artifact and generated schema have been upgraded together.
  Actual tests confirm the typed field becomes true and false in effective
  TOML and the desired recovery view. Official server tests confirm hot
  updates retain the endpoint and the value survives route replacement,
  server restart and owner recreation. Other newly generated fields still
  require their own runtime workflow acceptance.
  The new field is defined in the official
  [v2.7.0 configuration RPC schema](https://github.com/EasyTier/EasyTier/blob/v2.7.0/easytier-proto/proto/api_config.proto).
- The pinned Web management schema has no persisted virtual IPv6 address or
  DNS-zone field. Remote networks share the default `et.net` zone and can
  attach together when their overlay address prefixes do not overlap.
  Same-zone records are aggregated, including multiple addresses for the
  same hostname, matching the native Magic DNS RRset behavior. Static networks
  support explicit IPv6 and configurable `tld-dns-zone` values. Runtime IPv6
  patches are forwarded and validated before the core applies them; actual
  management tests confirm IPv4/IPv6 values in the running configuration.
  The server desired schema still cannot restore a static virtual IPv6
  address when it creates a fresh owner or replaces its network.
- IPv6 shared-packet, route, and Magic DNS behavior is covered by embedded-core
  tests. The 2026-10-08 physical Windows/Android run also passed IPv6
  address/route delivery and ordinary bidirectional TCP/UDP after the Android
  reply-policy fix described in [physical acceptance](easytier-physical-acceptance.md).
  Bare IPv6 addresses and explicit CIDRs are supported. A real peer's
  route snapshot confirms that bare addresses retain the upstream `/128`.
  An IPv4-only node with a dual-stack peer now learns only IPv4 routes; an
  actual two-core test confirms that unusable IPv6 advertisements do not block
  its IPv4 route synchronization. Explicit route/exit configuration still
  reaches the existing validation rather than being silently filtered.
- Configuration-center contract tests and the isolated official v2.7.0 server
  acceptance have passed for create, patch, same-UUID replacement, delete,
  shared raw packet dispatch, and Magic DNS. Candidates are checked against
  published configurations of other attached owners. Concurrent creation and
  newly assigned DHCP leases can still produce conflicts discovered by route
  synchronization after the original create operation.
- The v2.7.0 desired-state planner replaces instances for manual-route,
  exit-node, mapped-listener, static-IPv4, and IPv6-provider changes, while
  direct core RPC can hot-patch those settings. Host tests verify collection
  ADD/CLEAR and address policy rejection before partial commits. Connector
  runtime ADD carries actual bidirectional packets; CLEAR prevents reconnect
  after a live peer is restarted. These runtime connector changes are not
  persisted in the configuration center's `NetworkConfig` schema.
- Windows routes do not expose a per-route preferred source address. Overlay
  subnet destinations can select matching assigned addresses, but arbitrary
  proxy CIDRs and exit-node destinations need source-selection validation on
  the real Windows device, especially with multiple attached networks.
- The pinned host's `KNOWN_LIMITATIONS.md` describes maximum UDP fragment
  reassembly limits and the first inbound `ListenPacket` flow issue. The latter
  does not apply to the raw TUN plane, but the socket outbound retains the
  upstream limitation.
- A passing in-memory core test does not prove delivery to an OS socket through
  a native TUN. Windows administrative permissions and Android root/network
  policy remain required for that verification.
- No tools are installed automatically. Any necessary installation requires a
  concrete proposal; mise configuration remains project-local.
