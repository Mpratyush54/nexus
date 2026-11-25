#Requires -Version 5.1
param(
  [string]$Version = "0.1.0",
  [switch]$Upload
)

# The installer (Setup.exe, portable zip, update feed) is built by
# scripts/package-windows.ps1 (Velopack). This script only builds the
# framework-dependent app zip for local checks.

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

$api = "https://api-nexus.pratyushes.dev"
$sha = (git rev-parse --short HEAD)
$builtAt = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")
$ld = "-s -w -X central-memory/internal/buildinfo.Version=$Version -X central-memory/internal/buildinfo.Commit=$sha -X central-memory/internal/buildinfo.BuiltAt=$builtAt -X main.defaultServerURL=$api"
$dist = Join-Path $root "dist"
$app = Join-Path $dist "Nexus"
$win = Join-Path $root "desktop\windows"

New-Item -ItemType Directory -Force -Path $dist, $app | Out-Null

# The CLI is intentionally separate. The retired Fyne shell and local daemon
# are not release artifacts: Nexus.exe hosts nexuscore.dll in-process.
go build -trimpath -ldflags $ld -o "$dist\nexus-windows-amd64.exe" ./cmd/nexus

$props = @("-p:AppxGeneratePriEnabled=false", "-p:WindowsAppSDKSelfContained=false")
$appxTools = Join-Path $root "desktop\build-stubs\AppxPackage"
if (Test-Path (Join-Path $appxTools "Microsoft.Build.Packaging.Pri.Tasks.dll")) {
  $props += ('-p:AppxMSBuildToolsPath={0}\' -f $appxTools)
}
dotnet publish "$win\Nexus.csproj" -c Release -r win-x64 --self-contained false -o $app @props

# WinUI's unpackaged publish omits generated XAML and PRI resources on some
# SDK installations. The compiled output has them; without these files the
# published EXE starts and exits before it can create a window.
$compiled = Join-Path $win "bin\Release\net8.0-windows10.0.19041.0\win-x64"
foreach ($resource in @("App.xbf", "MainWindow.xbf", "Microsoft.UI.pri", "Microsoft.UI.Xaml.Controls.pri", "resources.pri")) {
  $source = Join-Path $compiled $resource
  if (-not (Test-Path $source)) { throw "Missing compiled WinUI resource: $source" }
  Copy-Item $source (Join-Path $app $resource) -Force
}

# Publish first, then place the Go library beside Nexus.exe. This makes a
# stale core impossible even if dotnet cleans its destination.
$env:CGO_ENABLED = "1"
go build -tags nexuscorelib -trimpath -ldflags $ld -buildmode=c-shared -o "$app\nexuscore.dll" ./cmd/nexuscore
if ($LASTEXITCODE -ne 0) { throw "nexuscore build failed" }
# Bundled stdio MCP proxy for Antigravity: the app must not need the CLI.
$env:CGO_ENABLED = "0"
go build -trimpath -ldflags "-s -w" -o "$app\nexus-mcp-proxy.exe" ./cmd/nexus-mcp-proxy
if ($LASTEXITCODE -ne 0) { throw "nexus-mcp-proxy build failed" }

Compress-Archive -Path "$app\*" -DestinationPath "$dist\nexus-desktop-windows-amd64.zip" -Force
Copy-Item "$root\scripts\install-windows.ps1" "$dist\install-windows.ps1" -Force

if ($Upload) {
  aws s3 sync $dist "s3://central-memory-releases/desktop/$Version/" `
    --exclude "*" `
    --include "nexus-windows-amd64.exe" `
    --include "nexus-desktop-windows-amd64.zip" `
    --include "install-windows.ps1"
}

Get-ChildItem $dist | Format-Table Name, Length, LastWriteTime
Write-Host "Built native Nexus release. The retired nexus-desktop.exe and nexus-daemon.exe are not included."
