param(
    [string]$Executable = ""
)

$ErrorActionPreference = "Stop"
$installer = Join-Path $PSScriptRoot "install.ps1"
& $installer -Executable $Executable
