#Requires -Version 5.1
<#
.SYNOPSIS
  Install Nexus Desktop (CLI + daemon + tray) from S3.

.EXAMPLE
  irm https://central-memory-releases.s3.ap-south-1.amazonaws.com/cli/0.1.0/install-windows.ps1 | iex
#>
param(
  [string]$Version = "0.1.0",
  [string]$BucketBase = "https://central-memory-releases.s3.ap-south-1.amazonaws.com/cli"
)

$ErrorActionPreference = "Stop"
$binDir = Join-Path $env:LOCALAPPDATA "Nexus\bin"
New-Item -ItemType Directory -Force -Path $binDir | Out-Null

$base = "$BucketBase/$Version"
$files = @(
  @{ Name = "nexus-windows-amd64.exe"; Dest = "nexus.exe" },
  @{ Name = "nexus-daemon-windows-amd64.exe"; Dest = "nexus-daemon.exe" },
  @{ Name = "nexus-desktop-windows-amd64.exe"; Dest = "nexus-desktop.exe" }
)

# Prefer full installer when present
$setupUrl = "$base/NexusSetup-$Version.exe"
try {
  $head = Invoke-WebRequest -Uri $setupUrl -Method Head -UseBasicParsing
  if ($head.StatusCode -ge 200 -and $head.StatusCode -lt 300) {
    $setup = Join-Path $env:TEMP "NexusSetup-$Version.exe"
    Write-Host "Downloading installer…"
    Invoke-WebRequest -Uri $setupUrl -OutFile $setup -UseBasicParsing
    Start-Process $setup -Wait
    exit 0
  }
} catch {
  Write-Host "No .exe installer yet — installing portable binaries…"
}

Write-Host "Installing Nexus $Version into $binDir"
foreach ($f in $files) {
  $url = "$base/$($f.Name)"
  $dest = Join-Path $binDir $f.Dest
  Write-Host "  downloading $($f.Name)…"
  Invoke-WebRequest -Uri $url -OutFile $dest -UseBasicParsing
}

$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if ($userPath -notlike "*$binDir*") {
  [Environment]::SetEnvironmentVariable("Path", "$userPath;$binDir", "User")
  $env:Path = "$binDir;$env:Path"
  Write-Host "Added $binDir to your user PATH."
}

# Autostart tray app
$startup = [Environment]::GetFolderPath("Startup")
$shortcutPath = Join-Path $startup "Nexus Desktop.lnk"
$wsh = New-Object -ComObject WScript.Shell
$sc = $wsh.CreateShortcut($shortcutPath)
$sc.TargetPath = Join-Path $binDir "nexus-desktop.exe"
$sc.WorkingDirectory = $binDir
$sc.Description = "Nexus Desktop"
$sc.Save()

Write-Host ""
Write-Host "Installed. Launching Nexus Desktop (system tray)…"
Write-Host "  Right-click the tray icon → Sign in with browser"
Write-Host ""
Start-Process (Join-Path $binDir "nexus-desktop.exe")
