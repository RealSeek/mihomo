[CmdletBinding()]
param([string]$Serial = 'eb20be6a')

$ErrorActionPreference = 'Stop'
$projectDirectory = Split-Path -Parent $PSScriptRoot
$adb = 'D:\Scoop\apps\adb\current\platform-tools\adb.exe'
$remoteDirectory = '/data/local/tmp/mihomo-et-transport-test'
$remoteTest = "$remoteDirectory/faketcp-android.test"
$testResult = 1
Start-Transcript -Path (Join-Path $projectDirectory 'output/easytier/device-acceptance-runner.log') -Append | Out-Null
try {
    & $adb -s $Serial get-state
    if ($LASTEXITCODE -ne 0) { throw 'Android adb connection unavailable.' }
    & $adb -s $Serial shell 'su -c id'
    if ($LASTEXITCODE -ne 0) { throw 'Android root shell unavailable.' }
    & $adb -s $Serial shell "mkdir -p $remoteDirectory"
    if ($LASTEXITCODE -ne 0) { throw 'Create Android test directory failed.' }
    try {
        & $adb -s $Serial push (Join-Path $projectDirectory 'output/easytier/faketcp-android.test') $remoteTest
        if ($LASTEXITCODE -ne 0) { throw 'Push Android test executable failed.' }
        & $adb -s $Serial shell "chmod 700 $remoteTest"
        if ($LASTEXITCODE -ne 0) { throw 'Set Android test executable permission failed.' }
        foreach ($testName in @('TestFakeTCPRawBidirectional', 'TestFakeTCPCoreUnderlayAndOverlay')) {
            & $adb -s $Serial shell "su -c 'EASYTIER_FAKETCP_RAW_TEST=1 $remoteTest -test.run=^$testName`$ -test.v -test.timeout=150s'" 2>&1 |
                Tee-Object -FilePath (Join-Path $projectDirectory "output/easytier/android-$testName.log")
            $testResult = $LASTEXITCODE
            if ($testResult -ne 0) { break }
        }
    } finally {
        & $adb -s $Serial shell "rm -f $remoteTest; rmdir $remoteDirectory"
        if ($LASTEXITCODE -ne 0) { throw 'Android test executable cleanup failed.' }
    }
    if ($testResult -ne 0) { throw "Android FakeTCP test failed with exit code $testResult." }
    Write-Output 'Android acceptance complete. Windows runtime tests are suspended pending crash-dump analysis.'
} finally {
    Stop-Transcript | Out-Null
}
exit $testResult
