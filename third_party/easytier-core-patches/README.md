# Embedded EasyTier core source

These ordered patches contain the Rust changes used by the mihomo Go host.
Apply them to the official EasyTier v2.7.0 commit
`8b7f1f0196edb5b580829ed5f135050fb5853478` in a separate checkout:

```powershell
git clone --branch v2.7.0 https://github.com/EasyTier/EasyTier.git output/easytier/source
git -C output/easytier/source rev-parse HEAD
$corePatches = Get-ChildItem third_party/easytier-core-patches/*.patch | Sort-Object Name
foreach ($corePatch in $corePatches) {
    git -C output/easytier/source am $corePatch.FullName
    if ($LASTEXITCODE -ne 0) { throw "Apply patch failed: $($corePatch.Name)" }
}
```

The Go host fork lives in `third_party/easytier-go`. Its embedded artifact and
protobuf bindings are regenerated together from the clean patched checkout:

```powershell
$env:EASYTIER_SOURCE = (Resolve-Path output/easytier/source).Path
Push-Location third_party/easytier-go
try { go generate ./... } finally { Pop-Location }
```

Generation requires project-local Rust 1.95 with `wasm32-wasip1`, a C compiler
and WASI sysroot for ring and KCP, protoc 35.1, protoc-gen-go 1.36.11, and
Binaryen 131. On Windows use Git's Bash and the GNU Rust toolchain. Configure
`CC_wasm32_wasip1`, `AR_wasm32_wasip1`, `WASI_SYSROOT`, `PROTOC`, and
`WASM_OPT` to the isolated tool locations. The build script enables WG,
QUIC, KCP, proxy smoltcp, management RPC, host encryption, and host message
tunnels. No tool installation is performed by the Go consumer build.

With the isolated Windows tools already prepared for this workspace, run
`./tools/build-easytier-core.ps1` from the project root. That script runs the
same generator using the project-local toolchain and dependency cache.

Vendored protocol libraries in the patch series retain their upstream
licenses. The WASI changes supply abstract sockets and registered timers;
the WireGuard, KCP, and QUIC algorithms remain in their upstream libraries.

`internal/artifact/provenance.go` identifies the exact source commit and
optimized WASM SHA-256. `proto/provenance.go` records the same source commit
and the management schema digest. Git `am` creates a new source commit when
applied elsewhere, so regeneration updates both provenance files to that
checkout's identity.
