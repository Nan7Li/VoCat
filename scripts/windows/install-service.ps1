param(
    [string]$Executable = ""
)

$ErrorActionPreference = "Stop"
if (-not ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw "Run PowerShell as Administrator."
}

$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$projectDir = Split-Path -Parent (Split-Path -Parent $scriptDir)
if ([string]::IsNullOrWhiteSpace($Executable)) {
    $Executable = Join-Path $projectDir "halo-windows-amd64.exe"
}
$Executable = (Resolve-Path -LiteralPath $Executable).Path
$serviceName = "Halo"
$dataDir = Join-Path $env:ProgramData "Halo\data"
$databasePath = Join-Path $dataDir "vocat.db"
New-Item -ItemType Directory -Force -Path $dataDir | Out-Null
[Environment]::SetEnvironmentVariable("VOCAT_DATABASE_PATH", $databasePath, "Machine")
[Environment]::SetEnvironmentVariable("VOCAT_ADDR", "127.0.0.1:7575", "Machine")

$binPath = '"{0}" serve' -f $Executable
sc.exe stop $serviceName | Out-Null
sc.exe delete $serviceName | Out-Null
sc.exe create $serviceName binPath= $binPath start= auto DisplayName= "Halo modem manager" | Out-Null
sc.exe failure $serviceName reset= 86400 actions= restart/5000/restart/15000/restart/30000 | Out-Null
Write-Host "Service created. Run the bootstrap-admin command once before starting Halo."
Start-Service -Name $serviceName
