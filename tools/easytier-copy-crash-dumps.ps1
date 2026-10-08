$ErrorActionPreference = 'Stop'
$destination = Join-Path (Split-Path -Parent $PSScriptRoot) 'output/easytier/debugger'
foreach ($name in @('100826-12156-01.dmp', '100826-12796-01.dmp')) {
    Copy-Item -LiteralPath (Join-Path 'C:\Windows\Minidump' $name) -Destination (Join-Path $destination $name)
}
Get-ChildItem -LiteralPath $destination -Filter '*.dmp' |
    Select-Object Name, Length, LastWriteTime | Out-File (Join-Path $destination 'dump-copy.log')
