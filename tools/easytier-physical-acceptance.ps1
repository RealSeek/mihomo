[CmdletBinding()]
param(
    [ValidateSet('windows', 'android')]
    [string]$Platform = 'windows',

    [string]$Controller = 'http://127.0.0.1:9090',
    [string]$Secret = '',
    [string]$Instance = 'mesh',
    [string]$ExpectedIPv4 = '',
    [string]$ExpectedIPv6 = '',
    [string]$ExpectedIPv4Route = '',
    [string]$ExpectedIPv6Route = '',
    [string]$RemoteIPv4 = '',
    [string]$RemoteIPv6 = '',
    [string]$TunInterface = '',
    [string[]]$MagicDnsNames = @(),
    [string]$DnsServer = '127.0.0.1',
    [switch]$SkipPacketProbe
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Add-CheckFailure {
    param([string]$Message)
    $script:Failures += $Message
    Write-Host "FAIL: $Message" -ForegroundColor Red
}

function Assert-Condition {
    param([bool]$Condition, [string]$Message)
    if ($Condition) {
        Write-Host "PASS: $Message" -ForegroundColor Green
    }
    else {
        Add-CheckFailure $Message
    }
}

function Get-StatusValue {
    param([object]$Status, [string]$Property)
    if ($null -eq $Status) { return $null }
    $entry = $Status.PSObject.Properties[$Property]
    if ($null -eq $entry) { return $null }
    return $entry.Value
}

function Get-NetworkStatuses {
    param([object]$Status)
    $networks = @()
    $managedNetworks = Get-StatusValue $Status 'networks'
    if ($null -ne $managedNetworks) { $networks += @($managedNetworks) }
    else { $networks += $Status }
    return $networks
}

function Invoke-ControllerGet {
    param([string]$Path)
    $headers = @{}
    if ($Secret -ne '') { $headers.Authorization = "Bearer $Secret" }
    try {
        return Invoke-RestMethod -Method Get -Uri ($Controller.TrimEnd('/') + $Path) -Headers $headers -TimeoutSec 8
    }
    catch {
        throw "GET $Path failed: $($_.Exception.Message)"
    }
}

function Test-DnsRecord {
    param([string]$Name)
    try {
        $records = @(Resolve-DnsName -Name $Name -Server $DnsServer -DnsOnly -ErrorAction Stop)
        Assert-Condition ($records.Count -gt 0) "DNS returned a record for $Name through $DnsServer"
        foreach ($record in $records) {
            if ($record.Type -in @('A', 'AAAA', 'PTR')) {
                Write-Host ("  {0} {1} {2}" -f $record.Name, $record.Type, $record.IPAddress)
            }
        }
    }
    catch {
        Add-CheckFailure "DNS query $Name through $DnsServer failed: $($_.Exception.Message)"
    }
}

function Test-WindowsNetwork {
    $addresses = @(Get-NetIPAddress -AddressFamily IPv4, IPv6 -ErrorAction Stop)
    $routes = @(Get-NetRoute -AddressFamily IPv4, IPv6 -ErrorAction Stop)
    $expectedAddresses = @(@($ExpectedIPv4, $ExpectedIPv6) | Where-Object { $_ -ne '' })
    $ifIndexes = [System.Collections.Generic.HashSet[int]]::new()

    if ($TunInterface -ne '') {
        $namedAdapters = @(Get-NetAdapter -Name $TunInterface -IncludeHidden -ErrorAction SilentlyContinue)
        Assert-Condition ($namedAdapters.Count -eq 1) "the named TUN interface $TunInterface exists exactly once"
        if ($namedAdapters.Count -eq 1) {
            Assert-Condition ($namedAdapters[0].Status -eq 'Up') "the named TUN interface $TunInterface is up"
            [void]$ifIndexes.Add([int]$namedAdapters[0].ifIndex)
        }
    }

    foreach ($expected in $expectedAddresses) {
        $address = $expected.Split('/')[0]
        $matches = @($addresses | Where-Object { $_.IPAddress -eq $address })
        Assert-Condition ($matches.Count -eq 1) "overlay address $address is assigned exactly once"
        foreach ($match in $matches) {
            [void]$ifIndexes.Add([int]$match.InterfaceIndex)
            Write-Host ("  {0} on ifIndex {1} ({2})" -f $match.IPAddress, $match.InterfaceIndex, $match.InterfaceAlias)
        }
    }
    if ($expectedAddresses.Count -gt 0) {
        Assert-Condition ($ifIndexes.Count -eq 1) "overlay addresses share one Windows TUN interface"
        if ($ifIndexes.Count -eq 1) {
            $ifIndex = $ifIndexes | Select-Object -First 1
            $adapter = Get-NetAdapter -IncludeHidden -ErrorAction SilentlyContinue |
                Where-Object { $_.ifIndex -eq $ifIndex }
            Assert-Condition ($null -ne $adapter -and $adapter.Status -eq 'Up') "the shared TUN interface is up"
        }
    }

    foreach ($expectedRoute in @($ExpectedIPv4Route, $ExpectedIPv6Route)) {
        if ($expectedRoute -eq '') { continue }
        $routeMatches = @($routes | Where-Object { $_.DestinationPrefix -eq $expectedRoute -and ($ifIndexes.Count -eq 0 -or $ifIndexes.Contains([int]$_.InterfaceIndex)) })
        Assert-Condition ($routeMatches.Count -ge 1) "route $expectedRoute selects the shared TUN interface"
    }
}

function Test-AndroidNetwork {
    throw 'The PowerShell checker is intended for Windows. Run tools/easytier-physical-acceptance.sh as root on Android.'
}

$script:Failures = @()
Write-Host "Read-only EasyTier physical acceptance probe ($Platform)"
Write-Host "Controller: $Controller; instance: $Instance"

try {
    $status = Invoke-ControllerGet ("/easytier/{0}" -f [uri]::EscapeDataString($Instance))
    $networks = @(Get-NetworkStatuses $status)
    Assert-Condition ($networks.Count -gt 0) "controller returned at least one EasyTier network"

    foreach ($network in $networks) {
        $summary = Get-StatusValue $network 'summary'
        if ($null -eq $summary) { $summary = $network }
        $state = [string](Get-StatusValue $summary 'state')
        Assert-Condition ($state -eq 'running') "EasyTier instance state is running"

        $peers = @((Get-StatusValue $network 'peers'))
        $connections = @($peers | ForEach-Object { @((Get-StatusValue $_ 'connections')) })
        $live = @($connections | Where-Object { -not [bool](Get-StatusValue $_ 'closed') })
        Assert-Condition ($live.Count -gt 0) "at least one EasyTier peer connection is live"

        $node = Get-StatusValue $network 'node'
        if ($ExpectedIPv4 -ne '') {
            $actualIPv4 = [string](Get-StatusValue $node 'ipv4')
            Assert-Condition ($actualIPv4.Split('/')[0] -eq $ExpectedIPv4.Split('/')[0]) "node IPv4 is $($ExpectedIPv4.Split('/')[0])"
        }
        if ($ExpectedIPv6 -ne '') {
            $actualIPv6 = [string](Get-StatusValue $node 'ipv6')
            Assert-Condition ($actualIPv6 -eq $ExpectedIPv6 -or $actualIPv6 -eq ($ExpectedIPv6.Split('/')[0])) "node IPv6 is $ExpectedIPv6"
        }

    }
}
catch {
    Add-CheckFailure $_.Exception.Message
}

if ($Platform -eq 'windows') {
    try { Test-WindowsNetwork } catch { Add-CheckFailure $_.Exception.Message }
}
else {
    try { Test-AndroidNetwork } catch { Add-CheckFailure $_.Exception.Message }
}

foreach ($name in $MagicDnsNames) { Test-DnsRecord $name }

if (-not $SkipPacketProbe) {
    foreach ($address in @($RemoteIPv4, $RemoteIPv6)) {
        if ($address -eq '') { continue }
        try {
            $reply = Test-Connection -TargetName $address -Count 2 -Quiet -ErrorAction Stop
            Assert-Condition ([bool]$reply) "ICMP reaches remote overlay address $address"
        }
        catch {
            Add-CheckFailure "ICMP probe to $address failed: $($_.Exception.Message)"
        }
    }
}

if ($script:Failures.Count -gt 0) {
    Write-Host ("{0} acceptance check(s) failed." -f $script:Failures.Count) -ForegroundColor Red
    exit 1
}
Write-Host 'All requested read-only acceptance checks passed.' -ForegroundColor Green
