param([string]$EasyTierSource = 'output/easytier/upstream-v2.7.0')
$ErrorActionPreference = 'Stop'
$projectDirectory = Split-Path $PSScriptRoot -Parent
$buildTools = Join-Path $projectDirectory '.tools/easytier-upgrade'
$toolchain = Join-Path $buildTools 'rustup/toolchains/1.95.0-x86_64-pc-windows-gnu/bin'
$env:RUSTUP_HOME = Join-Path $buildTools 'rustup'
$env:CARGO_HOME = Join-Path $buildTools 'cargo'
$env:CARGO_TARGET_DIR = Join-Path $buildTools 'target'
$env:CARGO_NET_OFFLINE = 'true'
$env:PROTOC = Join-Path $buildTools 'tools/protoc-35.1/bin/protoc.exe'
$env:CC_wasm32_wasip1 = Join-Path $buildTools 'tools/llvm-23.1.2/bin/clang.exe'
$env:AR_wasm32_wasip1 = Join-Path $buildTools 'tools/llvm-23.1.2/bin/llvm-ar.exe'
$env:WASI_SYSROOT = Join-Path $buildTools 'tools/wasi-sysroot-34.0'
$env:WASM_OPT = Join-Path $buildTools 'tools/binaryen-version_131/bin/wasm-opt.exe'
$gitExecutable = (Get-Command git.exe).Source
$gitBashDirectory = Join-Path (Split-Path $gitExecutable -Parent) '../bin'
$env:PATH = "$gitBashDirectory;$toolchain;$(Join-Path $buildTools 'tools/protoc-35.1/bin');$(Join-Path $buildTools 'bin');$env:PATH"
$sourceDirectory = (Resolve-Path (Join-Path $projectDirectory $EasyTierSource)).Path
Push-Location (Join-Path $projectDirectory 'third_party/easytier-go')
try {
    go run ./cmd/update-wasm -easytier $sourceDirectory
    if ($LASTEXITCODE -ne 0) { throw 'EasyTier artifact generation failed.' }
} finally {
    Pop-Location
}
