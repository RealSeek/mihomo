# Windows FakeTCP crash investigation

Windows runtime testing is suspended. Do not start WinDivert or rerun the
Windows fixture while investigating the reported blue screens.

## Confirmed evidence

- System event 1001 at 2026-10-08 14:05:38 reports bugcheck 0x133, argument 1
  equal to 1. Dump: `C:\Windows\Minidump\100826-12796-01.dmp`.
- System event 1001 at 14:40:44 reports bugcheck 0x133, argument 1 equal to 0.
  Dump: `C:\Windows\Minidump\100826-12156-01.dmp`.
- The second fixture log reaches the configured IPv4 FakeTCP connection and
  one 1036-byte overlay IPv4 packet. Its subsequent IPv6 overlay receive times
  out; the machine then crashes. This is not successful core acceptance.
- The Go WINDIVERT_ADDRESS layout matches the bundled official 2.2 header:
  80 bytes, flag bits at offset 8, network union at offset 16. Open/Recv/Send
  argument order and Shutdown(Recv=1) match the header. Offline ABI test passes.
- Official WinDivert 2.2.2 `windivert_write` does not read the Loopback flag.
  The earlier proposed Loopback metadata change therefore cannot correct
  injection behavior and has been removed. It is not a demonstrated bug.
- The sender uses priority 1 and reader priority 0, allowing local injected
  packets to reach the reader. This differs from upstream's priority-0 sender.
  Priority interactions with other WFP callout drivers require dump analysis;
  no injection-loop cause has been established.
- At 14:44:53, the cleanup-only elevated runner deleted the test-owned stopped
  WinDivert service. `sc query WinDivert` returned 1060. No driver is restarted.

## Android correction and acceptance

The latest Android raw fixture enables TFO in its caller and keeps two
connections open to each IPv4/IPv6 listener. It exposed a route-probe type
assertion panic before the raw handshake. Disabling TFO only at the final
decoy dial was too late; it is now disabled before route probing as well.
After that fix, the dual-connection raw fixture passes in 0.04 seconds and
the dual-underlay/dual-overlay configured-core fixture passes in 2.80 seconds.
Logs are retained in `output/easytier/android-TestFakeTCP*.log`. The temporary
Android executable and its test directory were removed.

## Offline dump analysis

With the user's portable-debugger authorization, Microsoft DbgEng and Symsrv
NuGet packages were extracted under `output/easytier/debugger`. The DbgEng DLL
has a valid Microsoft Authenticode signature. DbgEng package SHA256:
`875678516f9ceed4a1c8b9b106d165106fcaa38723ad7c584c47b95b231600de`.
The large WinDbg bundle download was aborted; no global debugger was installed.
The local reader is `tools/easytier-read-crash-dump.cpp`. Dump copies stay local.

Both dumps were read with Microsoft kernel symbols and the matching signed
WinDivert driver image (timestamp `632912C2`, checksum `0001D693`). Logs:
`output/easytier/debugger/12156-driver-stack.txt` and
`output/easytier/debugger/12796-driver-stack.txt`.

The latest interrupted stack contains:
`NETIO!ProcessCallout -> WinDivert64+0x2a92 -> WinDivert64+0x9441 ->
WinDivert64+0x9cb9 -> Wdf01000!imp_WdfWorkItemEnqueue ->
nt!IoQueueWorkItem -> nt!ExQueueWorkItemFromIo`.
The earlier stack also contains `NETIO!ProcessCallout -> WinDivert64+0x2a92
-> WinDivert64+0x958b`. This places both watchdog failures in WinDivert's WFP
packet classification/queue path. Driver disassembly at `0x9cb9` follows its
WDF work-item enqueue call. No WinDivert private PDB or `!analyze` extension
was available, so this is stack analysis, not a complete debugger diagnosis.

## Remaining work

The blocking-send cleanup defect found in review is corrected: sends now use
`WinDivertSendEx` with pinned buffers and an OVERLAPPED event. Write deadlines
and connection close cancel pending I/O using `CancelIoEx`, then drain its
completion before releasing memory and handles. Close no longer sends a
synchronous RST before closing the decoy and packet handles. Offline tests use
real pending Windows named-pipe I/O to verify deadline and close cancellation;
wire, stream and ABI tests also pass. No WinDivert runtime retry was performed.
Cancellation still relies on the kernel driver completing a cancelled request;
it cannot recover an already hung kernel or establish blue-screen safety.

The crash trigger is unresolved. The stacks do not prove an injection loop,
exclude a defect in our sender behavior, or distinguish a WinDivert defect
from interaction with another WFP driver. Priority handling and the injected
packet path need further offline investigation before any runtime retry.

The retained Windows runner permits only `-CleanupOnly` until this
investigation is resolved. The Android runner no longer chains Windows tests.
Previously built release artifacts precede these latest source corrections
and must be rebuilt before deployment.
