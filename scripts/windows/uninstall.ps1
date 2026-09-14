[CmdletBinding(SupportsShouldProcess)]
param(
    [string]$ServiceName = "Halo",
    [string]$InstallDirectory = "",
    [string]$DataDirectory = "",
    [switch]$RemoveData
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = [Security.Principal.WindowsPrincipal]::new($identity)
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw "Run PowerShell as Administrator to uninstall the Halo service."
}
if ($ServiceName -notmatch '^[A-Za-z0-9_.-]{1,256}$') {
    throw "Invalid Windows service name."
}
if ([string]::IsNullOrWhiteSpace($InstallDirectory)) {
    $InstallDirectory = Join-Path $env:ProgramFiles "Halo"
}
if ([string]::IsNullOrWhiteSpace($DataDirectory)) {
    $DataDirectory = Join-Path $env:ProgramData "Halo\data"
}
$InstallDirectory = [IO.Path]::GetFullPath($InstallDirectory)
$DataDirectory = [IO.Path]::GetFullPath($DataDirectory)

$startMenuDirectory = Join-Path $env:ProgramData "Microsoft\Windows\Start Menu\Programs\Halo"
$shortcutPath = Join-Path $startMenuDirectory "VoCat Windows.lnk"
if (Test-Path -LiteralPath $shortcutPath -PathType Leaf) {
    Remove-Item -LiteralPath $shortcutPath -Force
}
if (Test-Path -LiteralPath $startMenuDirectory -PathType Container) {
    $remaining = Get-ChildItem -LiteralPath $startMenuDirectory -Force
    if ($null -eq $remaining) {
        Remove-Item -LiteralPath $startMenuDirectory -Force
    }
}

$service = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
if ($null -ne $service -and $PSCmdlet.ShouldProcess($ServiceName, "Stop and delete Windows service")) {
    if ($service.Status -ne [ServiceProcess.ServiceControllerStatus]::Stopped) {
        Stop-Service -Name $ServiceName -Force
        $service.WaitForStatus([ServiceProcess.ServiceControllerStatus]::Stopped, [TimeSpan]::FromSeconds(45))
    }
    & "$env:SystemRoot\System32\sc.exe" delete $ServiceName | Out-Null
    if ($LASTEXITCODE -ne 0) {
        throw "Failed to delete Windows service $ServiceName (exit $LASTEXITCODE)."
    }
}

$firewallRuleName = "Halo Web ($ServiceName)"
Get-NetFirewallRule -DisplayName $firewallRuleName -ErrorAction SilentlyContinue | Remove-NetFirewallRule

if ((Test-Path -LiteralPath $InstallDirectory) -and $PSCmdlet.ShouldProcess($InstallDirectory, "Remove installed program files")) {
    Remove-Item -LiteralPath $InstallDirectory -Recurse -Force
}

if ($RemoveData) {
    $programDataHalo = [IO.Path]::GetFullPath((Join-Path $env:ProgramData "Halo"))
    if (-not $DataDirectory.StartsWith($programDataHalo + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase) -and
        -not [string]::Equals($DataDirectory, $programDataHalo, [StringComparison]::OrdinalIgnoreCase)) {
        throw "Refusing to remove data outside $programDataHalo."
    }
    if ((Test-Path -LiteralPath $DataDirectory) -and $PSCmdlet.ShouldProcess($DataDirectory, "Permanently remove Halo data")) {
        Remove-Item -LiteralPath $DataDirectory -Recurse -Force
        Write-Host "Halo data was removed and is not recoverable from this script."
    }
} else {
    Write-Host "Halo data was preserved at $DataDirectory. Use -RemoveData for explicit removal."
}
Write-Host "Halo Windows service and firewall rule were removed."
