# FakeTCP Windows acceptance plan

The Windows backend uses the official WinDivert 2.2 ABI through a dynamically
loaded `WinDivert.dll`. It opens the network capture handle with
`WINDIVERT_FLAG_SNIFF | WINDIVERT_FLAG_NO_INSTALL`, and the injection handle
with `WINDIVERT_FLAG_SEND_ONLY | WINDIVERT_FLAG_NO_INSTALL`. `NO_INSTALL` is
intentional: opening mihomo must fail if the driver is not already running.

## Prepared and verified files

Downloaded on 2026-10-08 from the official
[WinDivert download page](https://reqrypt.org/windivert.html) and
[2.2.2 release](https://github.com/basil00/WinDivert/releases/tag/v2.2.2).
All files remain below `output/easytier/windivert`.

| File | Bytes | SHA-256 |
| --- | ---: | --- |
| `WinDivert-2.2.2-A.zip` | 405137 | `63CB41763BB4B20F600B6DE04E991A9C2BE73279E317D4D82F237B150C5F3F15` |
| `WinDivert-2.2.2-A/x64/WinDivert.dll` | 47616 | `C1E060EE19444A259B2162F8AF0F3FE8C4428A1C6F694DCE20DE194AC8D7D9A2` |
| `WinDivert-2.2.2-A/x64/WinDivert64.sys` | 94144 | `8DA085332782708D8767BCACE5327A6EC7283C17CFB85E40B03CD2323A90DDC2` |

Archive URL:
<https://github.com/basil00/WinDivert/releases/download/v2.2.2/WinDivert-2.2.2-A.zip>.
The official GitHub release API has no digest for this asset. The hashes above
are measured from the HTTPS release download, not publisher-signed checksums.
`Get-AuthenticodeSignature` reported the x64 SYS signature as `Valid`, with
signer thumbprint `043589F75FCE2795E7F2CC3E526D46784D5DDAB3` and timestamp
thumbprint `AB34013AAC4097319F081AF0B318E183F80F7881`. The DLL is unsigned;
its identity is pinned by the archive and DLL hashes.

Before runtime acceptance, the driver had not been loaded, no WinDivert DLL had
been opened, and no service had been created. Read-only `sc.exe query WinDivert`
returned 1060 (absent);
`Win32_SystemDriver` contained no WinDivert entry. Package extraction does not
change PATH, the firewall, Scoop, mise, or the existing TUN.

## Approved runtime steps

The user approved temporary kernel-driver startup on 2026-10-08. Run the
commands below from an elevated PowerShell process, retain the
normal project-local mihomo test build, and preserve the existing Android clash.

```powershell
$fakeTCPDriver = (Resolve-Path 'output/easytier/windivert/WinDivert-2.2.2-A/x64/WinDivert64.sys').Path
$fakeTCPDLL = (Resolve-Path 'output/easytier/windivert/WinDivert-2.2.2-A/x64/WinDivert.dll').Path
sc.exe query WinDivert
sc.exe create WinDivert type= kernel start= demand binPath= $fakeTCPDriver
sc.exe start WinDivert
sc.exe query WinDivert
$env:EASYTIER_WINDIVERT_DLL = $fakeTCPDLL
$env:EASYTIER_FAKETCP_RAW_TEST = '1'
& '.\output\easytier\faketcp-windows.test' -test.run '^TestFakeTCPRawBidirectional$' -test.v -test.timeout 25s
```

`WinDivert` is the official 2.2 service/device name. `start= demand` avoids
boot-time startup. The service points at the verified SYS inside the project;
no SYS or DLL is copied into Windows system directories. Abort if the initial
query finds an existing WinDivert service, so the test does not reuse or alter
someone else's installation. Do not disable signature enforcement or alter
Secure Boot if Windows declines this signed driver.

The production backend opens only existing driver handles using `NO_INSTALL`.
Its reader filter is restricted to the FakeTCP peer's IP pair and remote port,
and its sender handle uses the `false` filter with `SEND_ONLY`. The reader uses
priority 0 and the sender priority 1, so injection reaches the receiver when
both FakeTCP peers are on the same machine. Thus it neither
blocks ordinary packets nor captures unrelated peers. IPv4/IPv6 checksums are
computed before injection. Captured interface/subinterface indexes are recorded
and included in the injection address. WinDivert ignores those indexes for
outbound injection and routes the packet through the Windows stack, so physical
underlay endpoint routes must survive any EasyTier public IPv6 default route.

Test one isolated FakeTCP network against a matching official EasyTier native
peer and the rooted Android Go host. The shared device still uses `stack: mips`,
GSO disabled, AutoRedirect disabled, and one TUN. Check connection metadata,
both directions of application traffic, IPv4 and IPv6 underlays, overlay IPv6,
and `http://10.126.0.11:3366/` where appropriate. Exercise reconnect and closure;
save logs and results below `output/easytier`, without dumping network secrets.
No second TUN, firewall rule, global tool installation, or persistent service
is part of this plan.

## Cleanup

Close only the test-owned mihomo/native child processes first so all FakeTCP
handles release. Then remove only the `WinDivert` service created by the approved
steps. Never stop/delete an existing service that was not created for this test.

```powershell
Remove-Item Env:EASYTIER_WINDIVERT_DLL -ErrorAction SilentlyContinue
Remove-Item Env:EASYTIER_FAKETCP_RAW_TEST -ErrorAction SilentlyContinue
sc.exe stop WinDivert
sc.exe delete WinDivert
sc.exe query WinDivert
Get-CimInstance Win32_SystemDriver | Where-Object Name -Like '*WinDivert*'
```

Expected final state: query returns 1060, no running WinDivert driver, and the
shared TUN addresses/routes owned by the test have been removed. Keep the inert
downloaded archive and extracted files as verification artifacts. If driver
stop/delete fails, report the exact service state; do not reboot automatically.

Official runtime and cleanup semantics:
[WinDivert manual](https://reqrypt.org/windivert-doc.html#divert_open) and
[uninstall procedure](https://reqrypt.org/windivert-doc.html#uninstalling).
The source implementation and packet/ABI tests are complete. Runtime approval
is granted; record acceptance output and driver cleanup evidence under
`output/easytier`.

The retained runner `tools/easytier-faketcp-windows-acceptance.ps1` checks the
service's recorded ownership and exact project-local driver path. It runs the
raw/core and official-native fixtures, and stops/deletes the service in
`finally`. Its `-CleanupOnly` option removes an owned service without running
tests. Use administrator PowerShell; the script does not bypass UAC or alter
permissions. Test executables must already be built below `output/easytier`.

On 2026-10-08, the elevated production-backend fixture passed real IPv4 and
IPv6 loopback handshakes and 28-byte payloads in both directions. Output is
`output/easytier/windivert/windows-raw-test.log`. This validates raw packet
capture/injection; official-core peer and shared-TUN acceptance are separate.
