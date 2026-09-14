param(
    [string]$Executable = "",
    [string]$DesktopExecutable = "",
    [string]$WintunDll = "",
    [string]$ServiceName = "Halo",
    [string]$InstallDirectory = "",
    [string]$DataDirectory = "",
    [int]$FirewallPort = 0,
    [switch]$OpenFirewall,
    [switch]$NoDesktopShortcut,
    [switch]$NoStart
)

$ErrorActionPreference = "Stop"
$installer = Join-Path $PSScriptRoot "install.ps1"
$parameters = @{
    Executable = $Executable
    DesktopExecutable = $DesktopExecutable
    WintunDll = $WintunDll
    ServiceName = $ServiceName
    InstallDirectory = $InstallDirectory
    DataDirectory = $DataDirectory
    FirewallPort = $FirewallPort
    OpenFirewall = $OpenFirewall
    NoDesktopShortcut = $NoDesktopShortcut
    NoStart = $NoStart
}
& $installer @parameters
