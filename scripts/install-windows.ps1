#Requires -Version 5.1
<#
.SYNOPSIS
  Install native Nexus Desktop (tray + embedded capture + CLI) for Windows.

.DESCRIPTION
  Downloads binaries, installs to %LOCALAPPDATA%\Nexus\bin, creates Startup +
  Start Menu shortcuts, and launches a single tray instance.

.EXAMPLE
  irm https://central-memory-releases.s3.ap-south-1.amazonaws.com/desktop/latest/install-windows.ps1 | iex

.EXAMPLE
  irm https://raw.githubusercontent.com/Mpratyush54/nexus/master/scripts/install-windows.ps1 | iex
#>
param(
  [string]$Version = "latest",
  [string]$BucketBase = "https://central-memory-releases.s3.ap-south-1.amazonaws.com"
)

$ErrorActionPreference = "Stop"
$binDir = Join-Path $env:LOCALAPPDATA "Nexus\bin"
New-Item -ItemType Directory -Force -Path $binDir | Out-Null

function Get-Manifest {
  param([string]$Url)
  try {
    return Invoke-RestMethod -Uri $Url -TimeoutSec 30
  } catch {
    return $null
  }
}

$manifest = $null
if ($Version -eq "latest") {
  $manifest = Get-Manifest "$BucketBase/desktop/latest.json"
  if ($manifest -and $manifest.version) {
    $Version = [string]$manifest.version
  } else {
    $Version = "0.1.0"
  }
}

$desktopBase = "$BucketBase/desktop/$Version"
$cliBase = "$BucketBase/cli/$Version"

Write-Host "Installing Nexus Desktop $Version into $binDir"

Get-Process Nexus, nexus-desktop, nexus-daemon -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
Start-Sleep -Seconds 1

function Download-Required([string]$Url, [string]$Dest) {
  Write-Host "  downloading $(Split-Path $Url -Leaf)…"
  Invoke-WebRequest -Uri $Url -OutFile $Dest -UseBasicParsing -TimeoutSec 120
}

$zip = Join-Path $env:TEMP "nexus-desktop-$Version.zip"
Download-Required "$desktopBase/nexus-desktop-windows-amd64.zip" $zip
Expand-Archive -Path $zip -DestinationPath $binDir -Force
Remove-Item $zip -Force -ErrorAction SilentlyContinue
Download-Required "$cliBase/nexus-windows-amd64.exe" (Join-Path $binDir "nexus.exe")

# Retire only the obsolete executables; project data and the new app remain.
Remove-Item (Join-Path $binDir "nexus-desktop.exe"), (Join-Path $binDir "nexus-daemon.exe") -Force -ErrorAction SilentlyContinue

$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if ($userPath -notlike "*$binDir*") {
  [Environment]::SetEnvironmentVariable("Path", "$userPath;$binDir", "User")
  $env:Path = "$binDir;$env:Path"
  Write-Host "Added $binDir to your user PATH."
}

$desktopExe = Join-Path $binDir "Nexus.exe"
$wsh = New-Object -ComObject WScript.Shell

# Autostart (logon)
$startup = [Environment]::GetFolderPath("Startup")
$startupLnk = Join-Path $startup "Nexus.lnk"
$sc = $wsh.CreateShortcut($startupLnk)
$sc.TargetPath = $desktopExe
$sc.WorkingDirectory = $binDir
$sc.Description = "Nexus (system tray)"
$sc.Save()

# Start Menu → Apps (so you can launch anytime)
$programs = Join-Path ([Environment]::GetFolderPath("StartMenu")) "Programs\Nexus"
New-Item -ItemType Directory -Force -Path $programs | Out-Null
$menuLnk = Join-Path $programs "Nexus.lnk"
$sc2 = $wsh.CreateShortcut($menuLnk)
$sc2.TargetPath = $desktopExe
$sc2.WorkingDirectory = $binDir
$sc2.Description = "Nexus — memory and sessions"
$sc2.Save()

# Desktop shortcut (optional convenience)
$deskLnk = Join-Path ([Environment]::GetFolderPath("Desktop")) "Nexus.lnk"
$sc3 = $wsh.CreateShortcut($deskLnk)
$sc3.TargetPath = $desktopExe
$sc3.WorkingDirectory = $binDir
$sc3.Description = "Nexus"
$sc3.Save()

Write-Host ""
Write-Host "Installed."
Write-Host "  Start Menu: Programs → Nexus → Nexus"
Write-Host "  Startup:    launches at logon (system tray)"
Write-Host "  Quit:       right-click the Nexus tray icon → Quit"
Write-Host ""
# Detached GUI process — closing this PowerShell/terminal must not kill the tray.
Start-Process -FilePath $desktopExe -WorkingDirectory $binDir -WindowStyle Hidden
