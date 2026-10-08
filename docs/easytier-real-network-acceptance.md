# RealSeek network acceptance

The supplied RealSeek network was tested on 2026-10-08 using the embedded
EasyTier 2.7.0 core. The Windows node was named `RealSeek-Mihomo-PC` and the
rooted Android node `RealSeek-Mihomo-Phone`. Both used separate state
directories, `dhcp = true`, `mtu = 1360`, the supplied network name/secret, and
the public TCP peer `easytier.weiai.org.cn:11010`. The original static `.10`
address was not claimed; DHCP assigned `10.126.0.1` to Windows and
`10.126.0.2` to Android. The existing `RealSeek-Server` appeared as
`10.126.0.11/24`.

## Passed

- Both mihomo instances reported `running` with live TCP peer connections.
- The shared `mips` TUN carried the assigned overlay address on each device.
- Windows and Android reached `10.126.0.11` with ICMP and HTTP.
- `GET http://10.126.0.11:3366/` returned `HTTP/1.1 200 OK` and 1981 bytes
  from both nodes. The HTML SHA256 was
  `3be1cc5fcfa79dc2499b6b795e06f490a4b1733476fef2b803f4a970241aea96`.
- `GET /index.js` returned HTTP 200 and 1750 bytes on both devices, with SHA256
  `5157f135a6b61f86ac6a2835468f3e083a46b1a9d00c1df93f4aca338ce45221`.
- The phone browser displayed the Sub Store page at that overlay URL.
- `RealSeek-Server.et.net` resolved to `10.126.0.11`; its PTR resolved back to
  `realseek-server.et.net` from both explicit mihomo DNS listeners.
- The Windows read-only physical probe and Android root probe passed status,
  interface, address, route, and ICMP checks.

## Original Run Boundaries

The supplied listener list was tested except for the previously unsupported
FakeTCP entry. TCP and UDP listeners on port 11010 were registered; live peer
connections in this run used TCP. The embedded WASI
artifact at the time logged `listener_plan_failed` for `wg://`, `ws://`, `wss://`, and
`quic://`; those transports are not available in this host build. The supplied
`enable_kcp_proxy` and `enable_quic_proxy` flags are retained in TOML but do not
prove working proxy engines: this WASI runtime does not inject the native KCP
or QUIC transport engines at that point. These limitations were subsequently
addressed in the transport upgrade; the original run itself remains evidence
for TCP access to the existing network. `enable_udp_broadcast_relay` was accepted but was
not exercised by this unicast HTTP test.

`ipv6_public_addr_auto = true` was accepted. Neither test node reported a
public overlay IPv6 address, and no provider appeared in the network status.
Automatic addressing requires an available provider; this WASI host does not
implement the native provider adapter. No static IPv6 address was invented for
the existing network.

The test used the real public TCP peer and the existing overlay, so it did not
change the server, install drivers, or modify global firewall/service state.
UPnP was disabled to avoid adding router port mappings. The server advertised
version `2.6.4-8428a89d~`; the embedded clients advertised `2.7.0`.
The existing server also established direct TCP connections on the local
network. This run therefore does not establish independent-NAT hole punching
or relay-only delivery. The Windows public-peer socket initially selected the
existing mihomo TUN address `198.18.0.1`; that daemon remained in place, so
the public underlay was not isolated from the user's existing routing policy.

## Cleanup

Android test PID 19720 exited through SIGTERM. Its shared `etreal0` interface,
table 42023 routes, and policy rules disappeared; logs, HTTP responses, and
status were copied to `output/easytier-real-network` before the exact temporary
device directory was removed. No ADB reverse mapping was used in this run.

Windows disabled its test TUN through the controller, removing `10.126.0.1`
and the overlay routes. Automatic command approval rejected terminating test
PID 70084 with `blocked by policy`. Reloading a configuration without the
EasyTier proxy exposed a lifecycle defect: the old instance kept port 11010
listeners alive after it was removed from the current proxy table. The process
subsequently exited when its long-running launcher session ended. Final checks
found no test PID, port 11010/19091 listeners, overlay address, or test TUN.
The configuration-removal lifecycle is now fixed in
`hub/executor/executor.go`: replaced or removed EasyTier adapters are closed
synchronously, including provider-owned instances, while retained adapters
are kept. A focused regression test starts the embedded official core,
checks that its old TCP listener is released, starts a replacement on the
same address, and verifies that final removal releases that listener. The
test passed, and the executor also compiled with `no_easytier`. This repair
was verified locally; it was not redeployed for another physical network run.
The original Windows mihomo PID 54292 and its interface were not stopped or
reconfigured.
