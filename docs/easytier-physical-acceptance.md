# Windows/root Android physical acceptance

The shared-TUN run on 2026-10-08 used Windows and a Redmi K40 (alioth), Android
16, with SukiSU root. Both nodes ran this fork's mihomo binary with the embedded
official EasyTier core. There was one shared `stack: mips` TUN for this test on
each device: `MihomoETTest` on Windows and `ettest0` on Android. GSO and
AutoRedirect were disabled; MTU was 1380.

## Isolated topology

| Setting | Windows | Android |
| --- | --- | --- |
| Overlay IPv4 | `10.144.0.1/24` | `10.144.0.2/24` |
| Overlay IPv6 | `fd00:144::1/64` | `fd00:144::2/64` |
| Hostname | `desktop` | `phone` |
| Controller | `127.0.0.1:19091` | `127.0.0.1:19090` |
| DNS listener | `127.0.0.1:15354` | `127.0.0.1:15353` |
| DNS fake range | `198.19.240.1/24` | `198.19.241.1/24` |
| Auto route | `false` | `true`, limited to the two overlay prefixes |
| Routing table / rule base | OS routes on the test interface | `42022` / `8000` |

The underlay used TCP through an ADB reverse mapping: the Windows listener was
`127.0.0.1:41110`, and Android connected to `tcp://127.0.0.1:41110`. This avoids
changing an existing daemon or firewall to make a LAN listener reachable. It
does not exercise independent NATs, public relay fallback, or Wi-Fi/mobile
handover. This test did not actively stop or reconfigure the existing mihomo or
clash deployments. The original Windows `mihomo-alpha` process remained
running; the final Android process listing did not contain the earlier clash
process, so its uninterrupted lifetime is not established by this run.

The independent fake ranges avoid assigning the existing TUN's
`198.18.0.1` address to the test interface. Top-level TUN IPv4 is derived from
`dns.fake-ip-range` and reduced to `/30`; a top-level `tun.inet4-address` value
does not override it. Android's `route-address` contains only `10.144.0.0/24`
and `fd00:144::/64`, so the test routing table has no default route. Both the
table and rule priorities were selected for the isolated run.

Test configurations and local logs are under the ignored
`output/easytier-physical` directory. Authentication and network secrets are
omitted from this record.

## Results

| Check | Result |
| --- | --- |
| Shared interface and overlay addresses | Both IPv4 and IPv6 assigned to each device's single test TUN; overlay routes installed |
| Core status | Both controllers report `running` with a peer connection whose `closed` field is `false` |
| Windows to Android ordinary TCP sockets | IPv4 and IPv6, 2 MiB echo payloads, matching SHA hashes |
| Android to Windows ordinary TCP sockets | IPv4 and IPv6, 2 MiB echo payloads, matching SHA hashes |
| Windows to Android ordinary UDP sockets | IPv4 and IPv6, 1280-byte echo payloads, matching SHA hashes |
| Android to Windows ordinary UDP sockets | IPv4 and IPv6, 1280-byte echo payloads, matching SHA hashes |
| ICMP | Remote overlay IPv4 and IPv6 reachable in both directions |
| Magic DNS | A, AAAA, PTR, and short-name queries pass through each node's explicit local DNS listener |

All four direction/address-family TCP runs returned the expected 2 MiB
payload SHA256
`9afe78d79ba8f41a92abb0b97932c9cc4e595f724ae8a4fdf002c057a7fc5fd3`.
The corresponding 1280-byte UDP runs returned
`56c1952ff0a32d40caf3fb924461723711d001e892be918df087d491573d0d2a`.

The socket checks used ordinary OS listeners and connections, with packets
passing through the real shared TUN. Explicit queries to the test DNS listeners
prove those Magic DNS records; they do not establish Android system resolver,
Private DNS, or application DoH behavior.

## Android IPv6 Reply Routing

The initial run exposed an Android-specific return-path failure: a TCP SYN
reached the phone, but its IPv6 SYN-ACK selected `wlan0` instead of `ettest0`.
The pinned sing-tun policy rules skip locally sourced IPv6 before reaching
their overlay source rules. Having the overlay route in table `42022` alone
did not make the ordinary accepted socket's reply use that route.

The fix adds a precise rule before sing-tun's rule base. For this run its
priority is `7999`, with `iif lo`, source `fd00:144::2/128`, and the relevant
overlay destination selecting table `42022`. The route manager owns these
rules along with its overlay routes. The rule is derived from each IPv6
overlay route and applies to Android auto-route deployments. It does not
introduce a global IPv6 default or a rule for unrelated source addresses.

After rebuilding with that fix, both directions of ordinary IPv6 TCP and UDP
passed. The root cause and rule ownership are implemented in
`listener/sing_tun/easytier_routes_linux.go`.

The focused reply-rule helper test executed once on Android and passed both
default and custom table/priority cases. Shell syntax, PowerShell parsing,
Go formatting, and diff whitespace checks also passed; the physical production
paths above supply the packet-delivery evidence.

## Scope and Cleanup

This run establishes the physical two-device shared-TUN paths listed above.
VPN Portal, FakeTCP, network namespaces, WebSocket configuration transport,
independent-NAT hole punching, public relay fallback, DHCP lease transitions,
subnet/exit-node deployment, and physical network handover were not tested by
this topology.

Cleanup was confirmed after the run:

- Android root reported UID 0. The `/proc/9666/exe` and `/proc/889/exe` paths
  were checked against the exact test directory before sending SIGTERM to the
  test mihomo and socket helper. The mihomo log records `Mihomo shutting down`,
  and the later process listing contains neither test process.
- Android `ettest0` no longer exists. IPv4 and IPv6 table `42022` are empty,
  with no test rules remaining at `7999` or within `8000..8010`. Public IPv4
  and IPv6 route lookups continue to select `wlan0`.
- The test ADB reverse mapping for TCP `41110` was removed, and `adb reverse
  --list` is empty. The exact Android test directory was confirmed with
  `realpath` before removal; `test ! -e` confirms it is gone.
- Android logs and the packet capture of the original IPv6 reply failure were
  pulled into `output/easytier-physical` before removing the device files.
- Windows disabled the test TUN through the controller before stopping test
  mihomo PID `69508`. Its overlay addresses `10.144.0.1` and `fd00:144::1`,
  and the two overlay prefix routes, disappeared when the route manager
  closed. The test socket helper PID `48640` was also stopped, and
  `MihomoETTest` no longer exists.
- The original Windows `mihomo-alpha` PID `54292` remains running. Its
  original mihomo adapter, interface index `42`, is still Up and retains
  `198.18.0.1`.

Phone Shell root authorization was intentionally retained for the user's
later use. No global installation, driver, or service changes were made for
this run.
