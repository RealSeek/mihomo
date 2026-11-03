# MIPS TCP recovery update

On 2026-11-03 the user confirmed normal GunZ gameplay after changing the
Windows TUN congestion controller from BBR to CUBIC. Mips, EasyTier, MTU 1360
and the existing routes remained in use. The dependency upgrade and close
diagnostics below supported the investigation; the additional clock-precision
patch is retained for upstream review and is not part of the running core.

The Windows core used during the GunZ investigation was built from `d05c5258`
with Go 1.26.10 and mipstack `961d4b1c1983` (2026-09-30). Minecraft's game
connections already selected `DIRECT`, which still traverses the userspace TUN
stack. Disabling TUN avoided that path. The game logs recorded disconnects but
did not include the TCP close reason, so they do not establish a specific stack
failure.

Two transport regressions were reproduced against that dependency in isolated
upstream checkouts:

| State | Old result | Updated result |
| --- | --- | --- |
| Window scale 5, receive sequence 100, previously promised right edge 131; application frees one 1460-byte MSS | Advertised window remains zero | Advertises a nonzero window |
| 4096 one-byte out-of-order pieces immediately after a missing byte; missing byte arrives | Gap-filling byte rejected, receive sequence stays at 100 | Contiguous data drains, receive sequence advances by 4097 |

The corresponding upstream fixes are
[receive-window reopening](https://github.com/MetaCubeX/mipstack/commit/45b3e68d3a)
and [receive progress/recovery accounting](https://github.com/MetaCubeX/mipstack/commit/d909ee5e899adca8afba8d0f1ff0d36c89b5fe4c).
The dependency is pinned to `v0.0.0-20261019124225-9a80a07eaeb9`, which includes
these changes and subsequent RTO/RACK recovery fixes. No Minecraft-specific
routing exception or protocol modification is required by this update.

Validation performed with the existing Go toolchains:

- Each focused transport regression failed on `961d4b1c1983` and passed on
  `9a80a07eaeb9`.
- `go test -tags with_gvisor ./listener/sing_tun ./component/easytier -count=1`
  passed using Go 1.26.10.
- A Windows native TCP client transferred and verified 22,287,320 bytes per
  connection through the original TUN stack, both normally and with paused
  reads/small receive buffers. Native socket controls also passed. This did not
  reproduce the game failure and is not evidence that either regression caused
  the observed disconnects.

The subsequent gameplay test below showed that the dependency upgrade alone
did not resolve the failure. The synthetic transfers did not reproduce it.

## Follow-up: reset persists on 2026-11-03

The running API confirmed `easytier-mips-tcpfix-9a80a07` before the next
disconnect. Minecraft exited its world at 12:05:55 with a displayed
`java.net.SocketException: Connection reset`; the core had established its
direct TUN connection to the game server at 12:02:23. TUN was disabled at
12:06:33, after the failure. No preceding interface change, configuration
reload or core crash appeared in the retained logs. The user reported no
interaction with Sparkle when the failure occurred.

Therefore the dependency update does not resolve the reported game failure.
A five-minute paced transfer with large bursts, small receive buffers and
paused reads also passed byte-integrity checks on both native sockets and
the updated Windows TUN. Neither synthetic check attributes the real reset.

The relay previously discarded both copy errors, so established-connection
failures were absent from the core logs. Relay now reports each direction's
byte count and original error, plus the first direction to finish before
cleanup can interrupt the other copy. Abnormal TUN closes produce a warning;
all closes are available to debug log subscribers. A mipstack final snapshot
adds receive/send windows, retransmissions and queue drops. Because snapshots
follow cleanup, their `LastError` can reflect cleanup rather than the cause;
the ordered original copy errors take precedence. API-driven connection
closures are recorded separately.

The diagnostic build is `easytier-mips-resetdiag-20261103`. It adds observation
without altering copy, half-close, routing or TCP recovery behavior. The
following sections describe actual game reproductions with this build.

## Live reproduction at 16:51:53

The diagnostic build captured game connection
`cab449a4-f9c6-4ced-87eb-e9aaea10df97`, from `198.18.0.1:64465` through the
physical socket `10.0.0.138:64466 -> 61.164.246.74:1224`. It lasted 5m21.606s.
The remote-to-client copy ended first with a nil error (EOF), after accepting
11,756,022 download bytes. The upload copy subsequently failed with Windows
`WSAECONNABORTED` (10053), after forwarding 567,746 bytes. No management API
close or TUN restart was recorded around the failure.

The final mipstack snapshot was `ESTABLISHED`, with no last error, one
retransmission and zero inbound queue drops. Its send window was 524,032;
11,517,200 bytes were both sent and acknowledged, with zero bytes in flight.
The remaining 238,822 bytes exactly account for the accepted download bytes
not yet sent to the local game. This single post-cleanup snapshot does not
establish a persistent send stall.

The Windows sing read-wait path maps a zero-byte successful socket receive to
EOF; reset and abort errors remain non-nil. The wrapper chain also preserves
mipstack `CloseWrite`, which queues FIN after accepted data. No buffer
ownership or half-close defect was established by inspecting this path.
The evidence therefore points to the physical socket's receive direction
ending first, but does not yet explain why enabling TUN triggers it. Physical
and TUN FIN/RST packet evidence is needed before attributing the close to the
server, a local filter or stack behavior.

## Packet reproduction at 17:04:12

A Windows pktmon capture covered both the physical flow
`10.0.0.138:60218 <-> 61.164.246.74:1224` and the TUN flow
`198.18.0.1:60217 <-> 198.18.0.22:1224`. The capture was stopped and its two
temporary port filters removed after the failure.

| Time (+08:00) | Observation |
| --- | --- |
| 17:04:05.353328 | Server sends its final 83 payload bytes on the physical connection |
| 17:04:05.353581 | Server sends FIN/ACK |
| 17:04:05.353594 | Windows acknowledges the FIN |
| 17:04:05.380814 | Server sends RST |
| 17:04:12.103 | Relay's upload first fails with Windows 10054; closing that socket interrupts the other copy |
| 17:04:12.103650 | TUN sends RST/ACK to the game |

Physical downstream sequence coverage contains 12,572,776 unique payload
bytes without gaps. The TUN had sent and received acknowledgements for only
11,399,309 bytes when it closed. The copy had accepted 12,108,021 bytes, so
708,712 accepted bytes had not yet been sent to the game. The physical
connection continued acknowledging upstream traffic before its close.

The local delivery delay precedes that server close. After about 17:03:37,
the download copy advanced by just 32 KiB every six to eight seconds, while
upload counters continued advancing. TUN sequence/ACK progress shows roughly
4.7 KB/s delivery in this interval: pairs of 1320-byte segments about 558 ms
apart, with Java acknowledging a pair within about six microseconds and still
advertising approximately 1 MiB of receive space. This is a local sending
delay, rather than an exhausted game receive window or a whole-network outage.
The server's precise application reason for closing remains unknown.

The current mitigation changes only the TUN congestion controller from BBR to
CUBIC. It is stored in Sparkle's existing local global YAML override and was
applied through PATCH /configs, preserving Mips, EasyTier, MTU 1360 and the
existing routes. The generated config passed the core's configuration check;
GET /configs confirms enabled TUN with `congestion-controller: cubic`.
The diagnostic core is unchanged. The user subsequently confirmed normal
gameplay without further disconnects after this configuration change.

## Delivery-rate clock precision regression

A focused dependency test reproduced a separate, concrete BBR recovery defect
consistent with the observed low-speed behavior. The rate sampler compresses
send/ACK timestamps to whole microseconds but compared its resulting interval
to a minimum RTT that retains nanosecond precision. An actual 1.3-microsecond
interval can become one microsecond and incorrectly fail a 1.2-microsecond
minimum-RTT check. Repeating these fast ACKs leaves an existing low bandwidth
model unchanged. Shifting the identical ordered events by 800 ns makes their
compressed interval two microseconds, allowing immediate bandwidth recovery.

The minimal dependency patch compares the minimum RTT in the same microsecond
precision, while retaining the zero-interval check and full RTT estimator
precision. The [dependency patch and focused regression tests](patches/mipstack-delivery-rate-precision.patch)
apply to mipstack `9a80a07eaeb9`. The original phase-zero test failed; after the
patch both clock phases recover bandwidth, genuinely short intervals remain
invalid, and the existing compact-clock wrap test passes. This regression
was tested in an isolated mipstack checkout; the
current running core does not contain this additional patch. The live
capture did not record `MinimumRTT` or rate-sample validity, so this specific
defect is not yet established as the sole cause of the game failure.
