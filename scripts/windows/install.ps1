[CmdletBinding()]
param(
    [string]$Executable = "",
    [string]$ServiceName = "Halo",
    [string]$InstallDirectory = "",
    [string]$DataDirectory = "",
    [int]$FirewallPort = 0,
    [switch]$OpenFirewall,
    [switch]$NoStart
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

function Assert-Administrator {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = [Security.Principal.WindowsPrincipal]::new($identity)
    if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
        throw "Run PowerShell as Administrator to install the Halo service."
    }
}

function Invoke-Sc {
    param([Parameter(Mandatory)][string[]]$Arguments)
    & "$env:SystemRoot\System32\sc.exe" @Arguments | Out-Null
    if ($LASTEXITCODE -ne 0) {
        throw "sc.exe failed while configuring the Windows service (exit $LASTEXITCODE)."
    }
}

function Get-ListenEndpoint {
    param([string]$Address)
    $value = $Address.Trim()
    if ($value -match '^\[(?<host>[^]]+)\]:(?<port>\d+)$') {
        return @{ Host = $Matches.host; Port = [int]$Matches.port }
    }
    if ($value -match '^(?<host>[^:]*):(?<port>\d+)$') {
        return @{ Host = $Matches.host; Port = [int]$Matches.port }
    }
    throw "VOCAT_ADDR must contain a TCP host and port, for example 127.0.0.1:7575."
}

Assert-Administrator
if ($ServiceName -notmatch '^[A-Za-z0-9_.-]{1,256}$') {
    throw "Invalid Windows service name."
}

$repositoryRoot = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
if ([string]::IsNullOrWhiteSpace($Executable)) {
    $candidates = @(
        (Join-Path $repositoryRoot "dist\halo-windows-amd64.exe"),
        (Join-Path $repositoryRoot "halo-windows-amd64.exe"),
        (Join-Path $repositoryRoot "dist\vocat-windows-amd64.exe")
    )
    $Executable = $candidates | Where-Object { Test-Path -LiteralPath $_ -PathType Leaf } | Select-Object -First 1
}
if ([string]::IsNullOrWhiteSpace($Executable) -or -not (Test-Path -LiteralPath $Executable -PathType Leaf)) {
    throw "Halo executable not found. Pass -Executable with the built Windows .exe path."
}
$sourceExecutable = (Resolve-Path -LiteralPath $Executable).Path

if ([string]::IsNullOrWhiteSpace($InstallDirectory)) {
    $InstallDirectory = Join-Path $env:ProgramFiles "Halo"
}
if ([string]::IsNullOrWhiteSpace($DataDirectory)) {
    $DataDirectory = Join-Path $env:ProgramData "Halo\data"
}
$InstallDirectory = [IO.Path]::GetFullPath($InstallDirectory)
$DataDirectory = [IO.Path]::GetFullPath($DataDirectory)
$installedExecutable = Join-Path $InstallDirectory "vocat.exe"

New-Item -ItemType Directory -Force -Path $InstallDirectory | Out-Null
New-Item -ItemType Directory -Force -Path $DataDirectory | Out-Null

$existing = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
if ($null -ne $existing) {
    if ($existing.Status -ne [ServiceProcess.ServiceControllerStatus]::Stopped) {
        Stop-Service -Name $ServiceName -Force
        $existing.WaitForStatus([ServiceProcess.ServiceControllerStatus]::Stopped, [TimeSpan]::FromSeconds(45))
    }
    Invoke-Sc -Arguments @("delete", $ServiceName)
    $deadline = [DateTime]::UtcNow.AddSeconds(15)
    while ((Get-Service -Name $ServiceName -ErrorAction SilentlyContinue) -and [DateTime]::UtcNow -lt $deadline) {
        Start-Sleep -Milliseconds 250
    }
    if (Get-Service -Name $ServiceName -ErrorAction SilentlyContinue) {
        throw "Windows service $ServiceName is pending deletion; close Services consoles and rerun the installer."
    }
}

if (-not [string]::Equals($sourceExecutable, $installedExecutable, [StringComparison]::OrdinalIgnoreCase)) {
    Copy-Item -LiteralPath $sourceExecutable -Destination $installedExecutable -Force
}

$serviceEnvironment = [ordered]@{}
Get-ChildItem Env: | Where-Object { $_.Name -like "VOCAT_*" } | ForEach-Object {
    $serviceEnvironment[$_.Name] = $_.Value
}
if (-not $serviceEnvironment.Contains("VOCAT_DATABASE_PATH")) {
    $serviceEnvironment["VOCAT_DATABASE_PATH"] = Join-Path $DataDirectory "vocat.db"
}
if (-not $serviceEnvironment.Contains("VOCAT_ADDR")) {
    $serviceEnvironment["VOCAT_ADDR"] = "127.0.0.1:7575"
}
$serviceEnvironment["VOCAT_WINDOWS_SERVICE_NAME"] = $ServiceName

$binaryPath = '"{0}" serve' -f $installedExecutable
New-Service -Name $ServiceName -BinaryPathName $binaryPath -DisplayName "Halo modem manager" -Description "VoCat/Halo modem and VoWiFi service" -StartupType Automatic | Out-Null
Invoke-Sc -Arguments @("failure", $ServiceName, "reset=", "86400", "actions=", "restart/5000/restart/15000/restart/30000")
Invoke-Sc -Arguments @("failureflag", $ServiceName, "1")

$serviceRegistryPath = "HKLM:\SYSTEM\CurrentControlSet\Services\$ServiceName"
$environmentBlock = @($serviceEnvironment.GetEnumerator() | ForEach-Object { "{0}={1}" -f $_.Key, $_.Value })
New-ItemProperty -Path $serviceRegistryPath -Name Environment -PropertyType MultiString -Value $environmentBlock -Force | Out-Null

$endpoint = Get-ListenEndpoint -Address $serviceEnvironment["VOCAT_ADDR"]
if ($FirewallPort -eq 0) {
    $FirewallPort = $endpoint.Port
}
if ($FirewallPort -lt 1 -or $FirewallPort -gt 65535) {
    throw "Firewall port must be in the range 1-65535."
}
$isLoopback = $endpoint.Host -in @("127.0.0.1", "::1", "localhost")
$firewallRuleName = "Halo Web ($ServiceName)"
Get-NetFirewallRule -DisplayName $firewallRuleName -ErrorAction SilentlyContinue | Remove-NetFirewallRule
if ($OpenFirewall -or -not $isLoopback) {
    New-NetFirewallRule -DisplayName $firewallRuleName -Direction Inbound -Action Allow -Protocol TCP -LocalPort $FirewallPort -Program $installedExecutable -Profile Any | Out-Null
    Write-Host "Firewall rule opened TCP $FirewallPort for $installedExecutable."
} else {
    Write-Host "VOCAT_ADDR is loopback-only; no inbound firewall rule is required."
}

$databasePath = $serviceEnvironment["VOCAT_DATABASE_PATH"]
if (-not $NoStart -and (Test-Path -LiteralPath $databasePath -PathType Leaf)) {
    Start-Service -Name $ServiceName
    (Get-Service -Name $ServiceName).WaitForStatus([ServiceProcess.ServiceControllerStatus]::Running, [TimeSpan]::FromSeconds(45))
    Write-Host "Halo service is installed, set to Automatic, and running."
} elseif (-not $NoStart) {
    Write-Host "Halo service is installed and set to Automatic, but was not started because the administrator database is not initialized."
    Write-Host "Run: `$env:VOCAT_DATABASE_PATH = '$databasePath'; & '$installedExecutable' bootstrap-admin"
    Write-Host "Then run: Start-Service -Name '$ServiceName'"
} else {
    Write-Host "Halo service is installed and set to Automatic. Start was skipped by -NoStart."
}
Write-Host "Data directory: $DataDirectory"
Write-Host "Service environment contains the current VOCAT_* variables; values were not printed."
