#Requires -Version 5.1
param(
  [string]$Version = "0.1.0",
  [switch]$Upload,
  [switch]$Inno
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

$api = "https://api-nexus.pratyushes.dev"
$sha = (git rev-parse --short HEAD)
$builtAt = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")
$ld = "-s -w -X central-memory/internal/buildinfo.Version=$Version -X central-memory/internal/buildinfo.Commit=$sha -X central-memory/internal/buildinfo.BuiltAt=$builtAt -X main.defaultServerURL=$api"

$env:CGO_ENABLED = "0"
$env:GOOS = "windows"
$env:GOARCH = "amd64"

$dist = Join-Path $root "dist"
New-Item -ItemType Directory -Force -Path $dist | Out-Null

Write-Host "Building Windows amd64 binaries…"
go build -trimpath -ldflags $ld -o "$dist\nexus-windows-amd64.exe" ./cmd/nexus
go build -trimpath -ldflags $ld -o "$dist\nexus-daemon-windows-amd64.exe" ./cmd/daemon
# windowsgui: no console window — tray-only UX
go build -trimpath -ldflags "$ld -H=windowsgui" -o "$dist\nexus-desktop-windows-amd64.exe" ./cmd/nexus-desktop
Copy-Item "$root\scripts\install-windows.ps1" "$dist\install-windows.ps1" -Force

if ($Inno) {
  $iscc = @(
    "${env:ProgramFiles(x86)}\Inno Setup 6\ISCC.exe",
    "$env:ProgramFiles\Inno Setup 6\ISCC.exe"
  ) | Where-Object { Test-Path $_ } | Select-Object -First 1
  if (-not $iscc) {
    Write-Warning "Inno Setup 6 not found — skipping NexusSetup.exe (install from https://jrsoftware.org/isinfo.php)"
  } else {
    & $iscc "$root\scripts\nexus.iss"
  }
}

if ($Upload) {
  aws s3 sync $dist "s3://central-memory-releases/cli/$Version/" `
    --exclude "*" `
    --include "nexus-*-windows-*" `
    --include "install-windows.ps1" `
    --include "NexusSetup-*"
}

Get-ChildItem $dist | Format-Table Name, Length, LastWriteTime
Write-Host "Done."
