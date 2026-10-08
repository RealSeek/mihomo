[CmdletBinding()]
param([switch]$CleanupOnly)

$ErrorActionPreference = 'Stop'
if (-not $CleanupOnly) {
    throw 'Windows FakeTCP runtime tests are suspended after two DPC_WATCHDOG_VIOLATION crashes. Use -CleanupOnly; analyze the crash dumps before enabling another driver run.'
}
$projectDirectory = Split-Path -Parent $PSScriptRoot
$driverDirectory = Join-Path $projectDirectory 'output/easytier/windivert'
$ownershipFile = Join-Path $driverDirectory 'test-owned-service.json'
$driverFile = (Resolve-Path (Join-Path $driverDirectory 'WinDivert-2.2.2-A/x64/WinDivert64.sys')).Path
$principal = [Security.Principal.WindowsPrincipal]::new([Security.Principal.WindowsIdentity]::GetCurrent())
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw 'Run this acceptance script from an administrator PowerShell.'
}

$service = Get-CimInstance Win32_SystemDriver -Filter "Name='WinDivert'"
if ($null -ne $service) {
    if (-not (Test-Path -LiteralPath $ownershipFile)) { throw 'WinDivert is not owned by this test.' }
    $ownership = Get-Content -LiteralPath $ownershipFile -Raw | ConvertFrom-Json
    if ($ownership.path -ne $driverFile -or $service.PathName -notin @($driverFile, ('\??\' + $driverFile))) {
        throw 'WinDivert service path differs from the test-owned driver.'
    }
} elseif ($CleanupOnly) {
    sc.exe query WinDivert
    exit 0
}

$testResult = 0
$owned = $null -ne $service
Start-Transcript -Path (Join-Path $driverDirectory 'windows-acceptance-runner.log') -Append | Out-Null
try {
    if (-not $CleanupOnly) {
        if (-not $owned) {
            sc.exe create WinDivert type= kernel start= demand binPath= $driverFile
            if ($LASTEXITCODE -ne 0) { throw 'Create WinDivert service failed.' }
            $owned = $true
            @{ path = $driverFile; created = [DateTime]::UtcNow.ToString('o') } |
                ConvertTo-Json | Set-Content -LiteralPath $ownershipFile -Encoding utf8
        }
        if ($null -eq $service -or $service.State -ne 'Running') {
            sc.exe start WinDivert
            if ($LASTEXITCODE -ne 0) { throw 'Start WinDivert driver failed.' }
        }
        $env:EASYTIER_WINDIVERT_DLL = Join-Path $driverDirectory 'WinDivert-2.2.2-A/x64/WinDivert.dll'
        $env:EASYTIER_FAKETCP_RAW_TEST = '1'
        & (Join-Path $projectDirectory 'output/easytier/faketcp-windows.test.exe') '-test.run=^TestFakeTCP(RawBidirectional|CoreUnderlayAndOverlay)$' '-test.v' '-test.timeout=150s' 2>&1 |
            Tee-Object -FilePath (Join-Path $driverDirectory 'windows-core-test.log')
        $testResult = $LASTEXITCODE
        if ($testResult -eq 0) {
            $env:EASYTIER_NATIVE_EXECUTABLE = Join-Path $projectDirectory 'output/easytier/server-v2.7.0/easytier-core.exe'
            & (Join-Path $projectDirectory 'output/easytier/native-interop-windows.test.exe') '-test.run=^TestEasyTierNativeInterop/faketcp$' '-test.v' '-test.timeout=120s' 2>&1 |
                Tee-Object -FilePath (Join-Path $driverDirectory 'windows-native-test.log')
            $testResult = $LASTEXITCODE
        }
    }
} finally {
    Remove-Item Env:EASYTIER_WINDIVERT_DLL, Env:EASYTIER_FAKETCP_RAW_TEST, Env:EASYTIER_NATIVE_EXECUTABLE -ErrorAction SilentlyContinue
    try {
        if ($owned) {
            sc.exe stop WinDivert
            if ($LASTEXITCODE -ne 0 -and $LASTEXITCODE -ne 1062) { throw 'Stop WinDivert driver failed.' }
            sc.exe delete WinDivert
            if ($LASTEXITCODE -ne 0 -and $LASTEXITCODE -ne 1060) { throw 'Delete WinDivert service failed.' }
            sc.exe query WinDivert
            if ($LASTEXITCODE -ne 1060) { throw 'WinDivert service is still registered.' }
            Move-Item -LiteralPath $ownershipFile -Destination (Join-Path $driverDirectory 'cleaned-service.json') -Force
        }
    } finally {
        Stop-Transcript | Out-Null
    }
}
exit $testResult
