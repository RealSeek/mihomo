# EasyTier embedded core v2.7.0 upgrade plan

Status: source-build tools authorized and prepared on 2026-10-08. The official source/library upgrade is within
the existing project task authorization; the pinned source has now been staged
at `output/easytier/upstream-v2.7.0`. Its host, WASM and generated protobufs
have been migrated into the project copy. The downloaded WASM SHA256 was
verified against official provenance. The user has explicitly authorized the
isolated source-build toolchain below. Rust 1.95 GNU, `wasm32-wasip1`, LLVM
23.1.2, protoc 35.1, Binaryen 131, protoc-gen-go 1.36.11 and Cargo dependencies
are now available under `.tools/easytier-upgrade`. The initial locked WASI
compile check passed. Keep shared TUN
support restricted to `mips`.

## Preferred Path: Official Committed Go Module

The project now uses the `easytier-go` files committed in official tag `v2.7.0`
at module pin `v0.0.0-20261005082529-8b7f1f0196ed`, with the mihomo host
delta retained. Both module suites, tagged connector/shared-mux checks and
official server reconnect/multiple-network checks passed. Four platform binaries
were rebuilt. This path does not require Rust, a WASI target, protoc or Binaryen.

The tag resolves to commit
`8b7f1f0196edb5b580829ed5f135050fb5853478`, verified through the official
[commit API](https://api.github.com/repos/EasyTier/EasyTier/commits/v2.7.0).
The release is currently marked a prerelease on the official
[v2.7.0 release page](https://github.com/EasyTier/EasyTier/releases/tag/v2.7.0).
Pin the full commit when fetching, rather than relying on a movable tag.

The tag's checked-in artifact and protobufs describe an earlier, matching core
revision. Preserve this distinction in the upgrade record:

| Item | Official v2.7.0 value |
| --- | --- |
| Go source checkout | `8b7f1f0196edb5b580829ed5f135050fb5853478` |
| Artifact/protobuf source | `599e4eacaa9c9a6f84b8d6439418af9d860f9aa3` |
| WASM SHA256 | `f5cc76a30d4c6128f7ce77d494dd4e8130aa7e3ce9d5b9ede7604745064bed8d` |
| Protobuf schema SHA256 | `7de60ee229e6ee2f17e3673cff6f9d9d4d4bece13ed74e46b1999277b364db67` |
| WASM repository blob | `af27a3bda47167cb016c51a7165f8dc53ac7bd7d`, 6,995,836 bytes |

The authorized local rebuild is staging commit
`ecc0a81ce7400933874943aba3644d87a0a92165`, with WASM SHA256
`d01bf1c861dd57dbf0d3fc526260270dc39e44888aa9f307ed6786a4c2d6649a` and
protobuf schema SHA256
`09d71b2964e418b15fcb42e6836f45c33f8c047aa1a93e71b83edf6e5622d454`.

These values come from official
[artifact provenance](https://raw.githubusercontent.com/EasyTier/EasyTier/v2.7.0/easytier-go/internal/artifact/provenance.go),
[protobuf provenance](https://raw.githubusercontent.com/EasyTier/EasyTier/v2.7.0/easytier-go/proto/provenance.go)
and [repository tree metadata](https://api.github.com/repos/EasyTier/EasyTier/git/trees/v2.7.0?recursive=1).
The tree contains an ordinary WASM blob, rather than only a release download or
an LFS pointer. The staged bytes have been downloaded and their WASM SHA256
verified against the official value above.

Historical evidence: the previous artifact at
`63519db2b5f2a6a1b9b7f20905f036dab54eb829` accepted a real guest management
probe of unknown patch field 17 but left `flags.prefer_peer_relay` disabled.
The new matching artifact/schema has been migrated. Real tests now confirm
typed true/false, effective/recovery views and replacement recovery. The official
server scenario confirms a hot update without endpoint replacement and retains
the setting after route replacement, server restart and owner recreation.
Successful RPC decoding alone is not a capability check; other new generated
fields still require workflow acceptance.

### Staging And Delta Migration

1. Fetch the pinned official source into a new project-local staging directory.
   The current checkout is `output/easytier/upstream-v2.7.0`; its verified revision
   is the full tag commit above. Fetching the repository/source and embedded
   artifact is the only download required by this preferred path. Record the
   checkout SHA and source archive hash if using an archive. Keep staging outside
   the active `third_party/easytier-go` copy.
2. Stage the entire official `easytier-go` directory, including its license,
   WASM, generated protobufs, both provenance files, host, engine and tests.
   Verify the actual WASM SHA256 against the value above before running it.
   Inspect ABI/create-schema compatibility and require artifact/protobuf
   provenance to agree. Do not transplant only the newer WASM into the old host.
3. Compare the staged host against `third_party/easytier-go/MIHOMO_PATCHES.md` and
   reapply only the production delta still needed: typed cloned-original Web
   config policy; ownership/effective configuration snapshots; terminal WebClient
   wait; context-aware mutation serialization; preflight before replacement;
   application-owned management protection; full patch forwarding; effective
   patch/recovery merge and address admission; `StateStopping` during close.
   Preserve the existing `go-toml/v2` dependency if those edits still need it.
4. Bring across mihomo's actual management, connector, shared-mux and lifecycle
   tests, the Windows absolute-path fixture and official desired-network PUT 204
   expectation. Resolve changed generated field types directly. Do not retain a
   compatibility forwarding layer or hand-edit generated protobufs.
5. Copy the validated staged module into the active project as one reviewable
   change containing host, artifact, generated protobufs and provenance together.
   Update `MIHOMO_PATCHES.md` with the new Go source revision, embedded revision,
   hashes, retained delta and measured capability results. Keep root's existing
   local `replace` ownership and inspect dependency changes explicitly.

The official [Go module](https://raw.githubusercontent.com/EasyTier/EasyTier/v2.7.0/easytier-go/go.mod)
uses Go 1.20 and the same wazero, x/sys and protobuf dependency versions as the
current copied module. Existing project-selected Go 1.27.1 is available; no Go
installation is proposed.

### Third-party Go baseline review

The requested `w568w/easytier-go` repository was inspected at commit
`06b3a7a0bc9503780842858f15acd771dab2420a` (2026-10-02) and retained as a
local, ignored comparison checkout in `.tools/easytier-w568w`. That project is
a pure-Go embedded networking library centered on `HostNetwork` and standard
Go socket interfaces. The active implementation keeps the official v2.7.0
`github.com/easytier/easytier/easytier-go` module because it supplies the
WASI guest, generated management API, WebClient lifecycle, and shared raw
packet ABI required by mihomo. The comparison API informed the native socket
adapter, SOCKS5 gateway, and Magic DNS integration; its incompatible module is
not vendored or placed on the root module's dependency path.

### Focused Acceptance

Run checks once after the staged migration, expanding only for a failure or a
newly changed contract:

- Assert typed `PreferPeerRelay=true` through the real manager and direct guest
  `GetConfig`; require effective TOML to become true. This now passes for true
  and false through the embedded core, including effective/desired/recovery
  agreement and same-UUID replacement. The official-server scenario also
  passes a false-to-true hot update without endpoint replacement, then retains
  the value through route replacement, server restart and owner recreation.
- Run existing real-core management patch/address/collection tests, lifecycle
  replacement/recovery tests, and connector test when touched. Run the root's
  tagged shared-mux test against the staged module to validate packet endpoints.
- Run the existing official v2.7.0 server owner/recovery/multiple-network test
  against the upgraded module, checking desired PUT, effective snapshots, DNS
  and packet-session routes. Use the already authorized portable server.
- The copied module suite, root affected suites, tagged connector/shared-mux
  checks, official-server recovery/multiple-network checks, and four CGO-
  disabled `with_gvisor with_ebpf` binary builds have now passed. Android
  root/su physical shared-TUN verification remains unavailable until a phone
  can connect; do not describe embedded-core tests as that evidence.

Other newly generated fields still need measurement. The official Rust
[hosted config conversion](https://github.com/EasyTier/EasyTier/blob/v2.7.0/easytier-core/src/wasi/web_client.rs)
has a selective Web configuration mapping. Existing Go restoration of manual
routes and exit-node settings must remain until the new guest proves equivalent
behavior. Upgrading alone does not establish support for every transport, VPN
portal, compression or provider setting, nor align guest-only preview validation
with the mihomo admission policy. Document any remaining specific gap.

## Fallback: Rebuild A Known Rust Revision

Use this only if the committed artifact fails a required capability, or an
explicit Rust conversion/preview fix is necessary. Start from the pinned official
v2.7.0 checkout above. Apply the necessary small Rust changes in that checkout,
commit them locally, and generate both WASM and protobufs from that exact clean
commit. Record its SHA rather than claiming an unmodified v2.7.0 artifact.
No local Rust patch series is currently available to replay; the current delta
inventory is Go host/application code in `MIHOMO_PATCHES.md`.

### Existing Tools And Authorized Preparation Scope

Read-only inventory on this Windows x86_64 machine:

| Tool | Existing state | Rebuild action |
| --- | --- | --- |
| Go | Project mise selects 1.27.1; `GOTOOLCHAIN=local` | Reuse |
| Rust | 1.95.0 MSVC and 1.96.0 GNU installed; project has no Rust selection | Isolate 1.95.0 GNU locally |
| rustc/cargo 1.95 | Existing 1.95.0 uses host `x86_64-pc-windows-msvc` | Keep Rust 1.95 and use the existing MinGW host linker |
| WASI standard library | Rust 1.95 has only host target installed | `wasm32-wasip1` must be added after authorization |
| protoc | Not found on PATH | Portable protoc 35.1 |
| protoc-gen-go | Not found on PATH | Project-local v1.36.11 |
| Binaryen/wasm-opt | Not found on PATH | Portable Binaryen 131 |
| C tools | Scoop MinGW gcc and CMake exist; clang/llvm-ar not found on PATH | Confirm suitable WASM C compiler; portable LLVM if absent |
| Shell tools | Git, Windows tar/curl and `D:/Scoop/apps/git/current/bin/bash.exe` exist | Reuse Git bash explicitly |

The official [Rust selection](https://raw.githubusercontent.com/EasyTier/EasyTier/v2.7.0/rust-toolchain.toml)
and workspace require Rust 1.95. Use project-local `mise.toml` configuration,
never `mise use -g` or a global installation. For isolated target installation,
propose `.tools/easytier-upgrade/rustup` as `RUSTUP_HOME` and
`.tools/easytier-upgrade/cargo` as `CARGO_HOME`. The current machine has no
MSVC linker or Windows SDK on the inspected paths, but already has Scoop MinGW
GCC. Reuse the existing rustup executable to install a separate minimal
`1.95.0-x86_64-pc-windows-gnu` toolchain with `wasm32-wasip1`, using that existing
MinGW linker for host build scripts. Do not install Visual Studio or a Windows
SDK. This duplicates the host toolchain but avoids modifying the shared target
set. Cargo registry/git fetches also need authorization and remain inside that
project-local cache.

Place portable protoc, Binaryen and LLVM under
`.tools/easytier-upgrade/tools`, and put generated `protoc-gen-go` in a local
`GOBIN`. Set `PROTOC` and `WASM_OPT` to the verified absolute executables. The
[proto generator](https://raw.githubusercontent.com/EasyTier/EasyTier/v2.7.0/easytier-go/script/generate-proto.sh)
requires exactly `libprotoc 35.1` and `protoc-gen-go v1.36.11`. The Rust
[proto build](https://github.com/EasyTier/EasyTier/blob/v2.7.0/easytier-proto/build/main.rs)
can download a different protoc on Windows when none is found; setting the
approved protoc explicitly avoids that automatic download path.

Binaryen Windows x86_64 archive, pinned by the official build script:

- URL: `https://github.com/WebAssembly/binaryen/releases/download/version_131/binaryen-version_131-x86_64-windows.tar.gz`
- SHA256: `2f4edac1703a2f695254d6ff52ede03481e67db1f094915763d863158c17d9bc`

Verify the archive hash before extraction. Setting `WASM_OPT` prevents the
script's automatic Binaryen download; its executable version check does not
replace archive verification. Additional official release assets were resolved
on 2026-10-07 without downloading or installing them:

| Portable payload | Download bytes | SHA256 |
| --- | --- | --- |
| protoc 35.1 Windows x64 ZIP | 3,518,494 | `5d3ff218d7d91eea95f7569bcb5a98f3030f8996d44151279d9772edcff76082` |
| LLVM 23.1.2 Windows x64 MSI payload | 639,465,201 | `e9d9141524f2c9fe4bf9f37012efe34f0aa745617be9e21fface9104703c55e5` |
| Binaryen 131 Windows x64 tar.gz | 108,545,916 | `2f4edac1703a2f695254d6ff52ede03481e67db1f094915763d863158c17d9bc` |

The LLVM payload is extracted with the existing 7-Zip/Scoop extraction tools,
not run as a system installer. These three payloads total about 752 MB decimal
before adding isolated Rust/WASI and Cargo dependencies; their extracted size
is larger. Source URLs and digests are from the official release APIs:
[protoc](https://api.github.com/repos/protocolbuffers/protobuf/releases/tags/v35.1),
[LLVM](https://api.github.com/repos/llvm/llvm-project/releases/tags/llvmorg-23.1.2),
and [Binaryen](https://api.github.com/repos/WebAssembly/binaryen/releases/tags/version_131).
Pin these exact payloads and verify their SHA256 before extraction. This is a
record of authorized project-local preparation. The archives were verified
against these digests before extraction. LLVM was extracted directly from the
MSI payload; no Windows Installer installation, service, driver, global mise
configuration or shared Rust target set was modified.

### Build And Atomic Regeneration

The official [WASI build script](https://raw.githubusercontent.com/EasyTier/EasyTier/v2.7.0/script/build-wasi-core.sh)
builds release `easytier-core` for `wasm32-wasip1` with additional features
`management-rpc,proxy-smoltcp-stack,ring-crypto,wasi-crypto-offload`. It keeps
default features enabled; do not substitute `--no-default-features` or `full`.
It optimizes with Binaryen 131 using `-O4`, bulk-memory/bulk-memory-opt and
nontrapping-float-to-int. The output is
`wasm32-wasip1/release/easytier_core_go_host.wasm` under `CARGO_TARGET_DIR`.

The lockfile selects ring 0.17.14 and cc-rs 1.4.6. Ring compiles C through cc-rs. Its
[build script](https://docs.rs/crate/ring/0.17.14/source/build.rs)
supports wasm32 Clang compilation without a target libc sysroot. The
[cc-rs compiler selection](https://docs.rs/crate/cc/1.4.6/source/src/lib.rs)
uses Clang for WASI and llvm-ar for wasm32 archives. Existing MinGW
gcc or MSVC availability does not establish WASM C compilation. Neither
EasyTier nor ring pins a Clang version here. Whether additional host linker
tools or a WASI SDK are necessary remains unverified; identify the actual error
during authorized staging rather than preinstalling a native release build stack.
Frontend/node tooling and the Windows Web-server build are outside this WASM
package build.

Run the existing `cmd/update-wasm` from a staged copied Go module, with
`EASYTIER_SOURCE` pointing at the clean full Rust checkout. Its checked-in
source requires the root `script/build-wasi-core.sh`, which is absent from the
current copied Go module. It sets commit-time `SOURCE_DATE_EPOCH`, clears ambient
Rust flags and remaps source paths. Keep the committed Cargo.lock unchanged;
the upstream build script does not pass `--locked`, so inspect any lock change.

The current generator writes protobufs before replacing the artifact and its
provenance. A failure can therefore leave a partial update. Generate and verify
in staging, then move the full validated host/generated/artifact set into the
active copy in one reviewable change. Require matching artifact/protobuf source
SHA, recomputed WASM SHA256 and schema digest, unchanged unintended dependencies,
and the focused acceptance checks above. Record any Rust patch in the new source
commit and upgrade documentation before final review.
