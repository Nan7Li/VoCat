param(
    [string]$Executable = ""
)

$ErrorActionPreference = "Stop"
$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$projectDir = Split-Path -Parent (Split-Path -Parent $scriptDir)
if ([string]::IsNullOrWhiteSpace($Executable)) {
    $Executable = Join-Path $projectDir "halo-windows-amd64.exe"
}
if (-not (Test-Path -LiteralPath $Executable)) {
    throw "Halo executable not found: $Executable"
}

$dataDir = Join-Path $env:LOCALAPPDATA "Halo\data"
New-Item -ItemType Directory -Force -Path $dataDir | Out-Null
$env:VOCAT_DATABASE_PATH = Join-Path $dataDir "vocat.db"
$env:VOCAT_ADDR = "127.0.0.1:7575"

& $Executable serve
