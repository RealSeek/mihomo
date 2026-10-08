# EasyTier transport acceptance

Windows FakeTCP core acceptance is suspended after two 0x133 watchdog blue
screens. The test-owned driver service was deleted at 14:44:53. See
[crash investigation](easytier-faketcp-windows-crash.md); earlier raw results
and the artifact hashes below do not validate the latest source corrections.

The transport upgrade was checked on 2026-10-08. The embedded source is
`ecc0a81ce7400933874943aba3644d87a0a92165`, based on official v2.7.0. Optimized
WASM SHA-256 is
`d01bf1c861dd57dbf0d3fc526260270dc39e44888aa9f307ed6786a4c2d6649a`.
The ordered Rust patches and regeneration instructions are retained in
`third_party/easytier-core-patches`.

| Path | Evidence |
| --- | --- |
| WG and QUIC underlay | Each protocol exchanges bidirectional IPv4 and IPv6 overlay packets through real loopback IPv4 and IPv6 UDP sockets; packet sizes are 1220 and 1248 bytes. |
| WG/QUIC/WS/WSS native interoperability | Official `easytier-core 2.7.0-8b7f1f01` and the mihomo adapter pass both directions of TCP forwarding, raw UDP to an ordinary host socket, and native-initiated UDP with its reply through IPv4 and IPv6 underlays. |
| Configured WS/WSS listeners | Configuration alone binds the platform listener on IPv4 and IPv6. A named peer uses platform DNS and TCP connect; each underlay carries bidirectional IPv4/IPv6 overlay packets. `/overlay` paths, ephemeral WSS certificates, packet boundaries, and port release after closure pass. |
| KCP and QUIC wrapped TCP | Each engine independently passes raw NIC SYN/SYN-ACK/ACK and real socket echo. Both source and destination management entries have the correct original addresses, transport type and Connected state. |
| KCP and QUIC native engines | Each engine passes embedded raw NIC TCP to the official native core's ordinary host socket, and native port forwarding to the embedded core's ordinary host socket. The embedded source and destination entries are strictly KCP/QUIC Connected with the original endpoints. QUIC's native userspace source is its virtual IPv4 and allocated source port; KCP preserves the host caller endpoint. |
| Android FakeTCP raw sockets | Rooted Android production AF_PACKET backend passes IPv4 and IPv6 handshake and 28-byte payloads in both directions. |
| Windows FakeTCP raw sockets | Elevated production WinDivert backend passes IPv4 and IPv6 handshake and 28-byte payloads in both directions. Sender priority 1 lets the priority-0 capture receive injected packets. |
| Android configured FakeTCP core | Configuration binds the listener and connects two embedded cores. Each IPv4/IPv6 underlay carries 1036-byte IPv4 and 1056-byte IPv6 overlay UDP packets in both directions. Tunnel metadata reports faketcp. |

The raw FakeTCP tests open the production backend, without a simulated packet
socket. Android uses `su`; Windows uses the verified project-local WinDivert
2.2.2 package. The backend opens only an already-running driver with
NO_INSTALL. The lifecycle and raw output are retained below
`output/easytier/windivert`.

After those raw runtime checks, FakeTCP dialing was corrected to disable TFO
for the decoy connection and reserve an exact source port before capture. This
prevents lazy TFO handshakes and overlapping WinDivert filters for connections
to one remote endpoint. The updated raw fixture enables TFO and keeps two
connections open per address family. Compilation passes, but that new runtime
case must be executed with administrator/root privileges before final
acceptance; the earlier single-connection result does not validate this change.

The initial Windows driver window was stopped and its test-owned service
deleted after the raw checks; `sc query WinDivert` returned 1060. At 14:10 in
the subsequent window, the service was STOPPED but still registered. The core
log contained only NUL bytes and establishes no result. The execution account
had changed to CodexSandboxOffline: service deletion returned Access denied,
and the normal UAC launch failed with 0xc0000142. The Windows core check and
service deletion remain pending; an earlier cleanup is not evidence for this
second window.

Both the root `go test ./...` and standalone Go host suite passed with the
upgraded artifact. Later host cancellation changes were checked with focused
tests: a canceled receive preserves one pending read and its unread message;
closing a listener cancels its lifetime context. The configured WS/WSS test
also passed after those changes.

The final host correction was rebuilt with CGO disabled, `with_gvisor` and
`with_ebpf`, `-trimpath`, and `-ldflags '-s -w'` on 2026-10-08. No tool or
dependency installation was needed for these builds.

| Artifact | SHA-256 |
| --- | --- |
| `output/mihomo-windows-amd64.exe` | `58d23010ceec3045fb1c4b3914a0b6ff12a2b5e2995fe52fcc6e1f1fa07bd081` |
| `output/mihomo-android-arm64` | `96e77041015854f4b26cb57ef13f3dbda8b46da9b7eaf88a558ece4750d910cd` |
| `output/mihomo-linux-amd64` | `e105e731cfab4805ce2336b307d6cfd0522108bd7995892e781d80dc95aa9a47` |
| `output/mihomo-darwin-arm64` | `2dce8773cd4c18aa61c8ab5ce2126b4fe2d6cd8f526448d73bb2a74ff7d4ed5f` |

The rebuilt Windows core and native fixtures are retained as
`output/easytier/faketcp-windows.test.exe` and
`output/easytier/native-interop-windows.test.exe`. The approved driver test can
be run from administrator PowerShell using
`tools/easytier-faketcp-windows-acceptance.ps1`; it includes owned-service
cleanup in `finally`. The `-CleanupOnly` option completes service removal
without opening the driver or running tests.

These checks do not establish independent-NAT hole punching or WAN packet-loss
performance. Wrapped engines retain the upstream IPv4 TCP proxy scope; IPv6
uses the ordinary overlay path. Static IPv6 shared-TUN communication was
already verified on Windows and Android. Public IPv6 client leases are
projected into addresses, routes, status and Magic DNS, but a real provider
lease was not obtained in the supplied network. Provider/NDP hosting is not
implemented in this WASI host.

The shared TUN remains limited to `stack: mips`, with GSO and AutoRedirect
disabled. The original RealSeek network's TCP HTTP/browser acceptance is
recorded in `easytier-real-network-acceptance.md`; that run is not evidence for
the newer transport engines.
