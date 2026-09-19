#Requires -Version 5.1
<#
.SYNOPSIS
  Install Nexus Desktop (tray + daemon + CLI) for Windows.

.DESCRIPTION
  Downloads binaries, installs to %LOCALAPPDATA%\Nexus\bin, creates Startup +
  Start Menu shortcuts, and launches a single tray instance.

.EXAMPLE
  irm https://central-memory-releases.s3.ap-south-1.amazonaws.com/desktop/latest/install-windows.ps1 | iex

.EXAMPLE
  .\install-windows.ps1 -Version 0.2.0
#>
param(
  [string]$Version = "latest",
  [string]$BucketBase = "https://central-memory-releases.s3.ap-south-1.amazonaws.com"
)

$ErrorActionPreference = "Stop"
$binDir = Join-Path $env:LOCALAPPDATA "Nexus\bin"
New-Item -ItemType Directory -Force -Path $binDir | Out-Null

# Resolve versioned prefix: desktop/latest.json or desktop/{ver}/ / cli/{ver}/
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

$files = @(
  @{ Name = "nexus-desktop-windows-amd64.exe"; Dest = "nexus-desktop.exe" },
  @{ Name = "nexus-daemon-windows-amd64.exe"; Dest = "nexus-daemon.exe" },
  @{ Name = "nexus-windows-amd64.exe"; Dest = "nexus.exe" }
)

Write-Host "Installing Nexus Desktop $Version into $binDir"

# Stop running tray/daemon so files can be replaced
Get-Process nexus-desktop, nexus-daemon -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
Start-Sleep -Seconds 1

foreach ($f in $files) {
  $urls = @(
    "$desktopBase/$($f.Name)",
    "$cliBase/$($f.Name)"
  )
  $dest = Join-Path $binDir $f.Dest
  $ok = $false
  foreach ($url in $urls) {
    try {
      Write-Host "  downloading $($f.Name)…"
      Invoke-WebRequest -Uri $url -OutFile $dest -UseBasicParsing -TimeoutSec 120
      $ok = $true
      break
    } catch {
      # try next mirror
    }
  }
  if (-not $ok) {
    throw "Could not download $($f.Name) from desktop/ or cli/ prefixes"
  }
}

$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if ($userPath -notlike "*$binDir*") {
  [Environment]::SetEnvironmentVariable("Path", "$userPath;$binDir", "User")
  $env:Path = "$binDir;$env:Path"
  Write-Host "Added $binDir to your user PATH."
}

$desktopExe = Join-Path $binDir "nexus-desktop.exe"
$wsh = New-Object -ComObject WScript.Shell

# Autostart (logon)
$startup = [Environment]::GetFolderPath("Startup")
$startupLnk = Join-Path $startup "Nexus Desktop.lnk"
$sc = $wsh.CreateShortcut($startupLnk)
$sc.TargetPath = $desktopExe
$sc.WorkingDirectory = $binDir
$sc.Description = "Nexus Desktop (system tray)"
$sc.Save()

# Start Menu → Apps (so you can launch anytime)
$programs = Join-Path ([Environment]::GetFolderPath("StartMenu")) "Programs\Nexus"
New-Item -ItemType Directory -Force -Path $programs | Out-Null
$menuLnk = Join-Path $programs "Nexus Desktop.lnk"
$sc2 = $wsh.CreateShortcut($menuLnk)
$sc2.TargetPath = $desktopExe
$sc2.WorkingDirectory = $binDir
$sc2.Description = "Nexus Desktop — memory harvest tray"
$sc2.Save()

# Desktop shortcut (optional convenience)
$deskLnk = Join-Path ([Environment]::GetFolderPath("Desktop")) "Nexus Desktop.lnk"
$sc3 = $wsh.CreateShortcut($deskLnk)
$sc3.TargetPath = $desktopExe
$sc3.WorkingDirectory = $binDir
$sc3.Description = "Nexus Desktop"
$sc3.Save()

Write-Host ""
Write-Host "Installed."
Write-Host "  Start Menu: Programs → Nexus → Nexus Desktop"
Write-Host "  Startup:    launches at logon (system tray)"
Write-Host "  Quit:       right-click tray icon → Quit Nexus Desktop"
Write-Host ""
Start-Process $desktopExe
