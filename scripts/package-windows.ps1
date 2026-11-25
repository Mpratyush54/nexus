#Requires -Version 5.1
<#
.SYNOPSIS
  Build the Nexus Windows installer (Velopack): Setup.exe, a portable zip,
  the update feed, and the plain app zip used by install-windows.ps1.

.DESCRIPTION
  Self-contained build (.NET 8 and the Windows App SDK ship inside the app;
  nothing to install first). Per-user install to
  %LOCALAPPDATA%\NexusDesktop, no admin rights.

  Output (in -OutDir):
    publish\                               the app folder that gets packed
    releases\                              Velopack feed: releases.<channel>.json, *.nupkg
    Nexus-<ver>-win-x64-Setup.exe           installer
    Nexus-<ver>-win-x64-Portable.zip        portable copy (self-updating)
    nexus-desktop-windows-amd64.zip         plain app zip (install-windows.ps1)

  Never stops a running Nexus and never installs anything.

.EXAMPLE
  ./scripts/package-windows.ps1 -Version 0.4.0 -OutDir out/installer
.EXAMPLE
  ./scripts/package-windows.ps1 -Version 0.4.0-beta.4 -Channel win-beta -PreviousFeed https://.../desktop/velopack
#>
param(
  [Parameter(Mandatory = $true)][ValidatePattern('^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$')][string]$Version,
  [string]$OutDir = "dist/windows",
  [string]$Channel = "win",
  [string]$ApiUrl = "https://api-nexus.pratyushes.dev",
  # Velopack feed of earlier releases; when set, their packages are
  # downloaded first so this release also gets a delta package.
  [string]$PreviousFeed = "",
  [string]$Vpk = "vpk",
  # Sign with signtool (e.g. '/a /fd sha256 /tr http://timestamp.digicert.com /td sha256').
  # Empty = unsigned; SmartScreen warns on unsigned installers.
  [string]$SignParams = "",
  [switch]$SkipVelopackAppCheck,
  # Reuse the app already in <OutDir>\publish (e.g. to pack a second
  # version for an upgrade test) instead of building it again.
  [switch]$SkipAppBuild
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Set-Location -LiteralPath $root
function Assert-Native([string]$Step) { if ($LASTEXITCODE -ne 0) { throw "$Step failed (exit $LASTEXITCODE)" } }

$out = [IO.Path]::GetFullPath((Join-Path $root $OutDir))
$publish = Join-Path $out "publish"
$compiled = Join-Path $out "compiled"
$releases = Join-Path $out "releases"
if (-not $SkipAppBuild) {
  foreach ($dir in @($publish, $compiled)) {
    if (Test-Path -LiteralPath $dir) { Remove-Item -LiteralPath $dir -Recurse -Force }
  }
}
New-Item -ItemType Directory -Force -Path $out, $publish, $releases | Out-Null

$commit = (git rev-parse HEAD).Trim()
$builtAt = [DateTime]::UtcNow.ToString("yyyy-MM-ddTHH:mm:ssZ")
$ld = "-s -w -X central-memory/internal/buildinfo.Version=$Version -X central-memory/internal/buildinfo.Commit=$commit -X central-memory/internal/buildinfo.BuiltAt=$builtAt -X main.defaultServerURL=$ApiUrl"
$numeric = ($Version -split '-')[0]
$plainZip = Join-Path $out "nexus-desktop-windows-amd64.zip"
$win = Join-Path $root "desktop\windows"

if (-not $SkipAppBuild) {
# 1. Native shell, self-contained (.NET runtime + Windows App SDK inside).
$props = @(
  "-p:AppxGeneratePriEnabled=false",
  "-p:WindowsAppSDKSelfContained=true",
  "-p:Version=$Version",
  "-p:AssemblyVersion=$numeric.0",
  "-p:FileVersion=$numeric.0",
  # Trailing "/" not "\": a "\" before the closing quote breaks native
  # argument quoting when the path has spaces.
  "-p:OutputPath=$compiled/"
)
$appxStubs = Join-Path $root "desktop\build-stubs\AppxPackage"
# The unpackaged WinUI build needs the stub Appx/PRI MSBuild tasks (the real
# ones ship only with Visual Studio). build.ps1 builds them on first use; do
# the same here so a clean checkout (CI) can package without build.ps1.
if (-not (Test-Path -LiteralPath (Join-Path $appxStubs "Microsoft.Build.Packaging.Pri.Tasks.dll"))) {
  Push-Location (Join-Path $root "desktop\build-stubs")
  dotnet build -c Release -v q
  Assert-Native "stub task build"
  Pop-Location
  New-Item -ItemType Directory -Force -Path $appxStubs | Out-Null
  $stubDll = Join-Path $root "desktop\build-stubs\bin\Release\net8.0\Microsoft.Build.AppxPackage.dll"
  Copy-Item $stubDll (Join-Path $appxStubs "Microsoft.Build.AppxPackage.dll") -Force
  Copy-Item $stubDll (Join-Path $appxStubs "Microsoft.Build.Packaging.Pri.Tasks.dll") -Force
}
if (Test-Path -LiteralPath (Join-Path $appxStubs "Microsoft.Build.Packaging.Pri.Tasks.dll")) { $props += "-p:AppxMSBuildToolsPath=$appxStubs/" }
dotnet publish "$win\Nexus.csproj" -c Release -r win-x64 --self-contained true -o $publish -nodeReuse:false @props
Assert-Native "dotnet publish"

# Unpackaged WinUI publish can omit the compiled XAML and framework PRI
# files; without them Nexus.exe exits before it opens a window.
$priCache = Join-Path $win ".runtime-pri"
if (-not (Test-Path -LiteralPath (Join-Path $priCache "Microsoft.UI.Xaml.Controls.pri"))) {
  $sdkVersion = ([xml](Get-Content -LiteralPath "$win\Nexus.csproj")).Project.ItemGroup.PackageReference |
    Where-Object { $_.Include -eq "Microsoft.WindowsAppSDK" } | Select-Object -First 1 -ExpandProperty Version
  $nuget = if ($env:NUGET_PACKAGES) { $env:NUGET_PACKAGES } else { Join-Path $env:USERPROFILE ".nuget\packages" }
  $msix = Get-ChildItem -LiteralPath (Join-Path $nuget "microsoft.windowsappsdk\$sdkVersion\tools\MSIX\win10-x64") -Filter "Microsoft.WindowsAppRuntime.*.msix" |
    Where-Object { $_.Name -notmatch "Singleton|DDLM|Main" } | Select-Object -First 1
  if (-not $msix) { throw "Windows App SDK runtime package not found for $sdkVersion" }
  $extract = Join-Path $out "wasdk-runtime"
  New-Item -ItemType Directory -Force -Path $priCache, $extract | Out-Null
  $zip = Join-Path $out "wasdk-runtime.zip"
  Copy-Item -LiteralPath $msix.FullName -Destination $zip -Force
  Expand-Archive -LiteralPath $zip -DestinationPath $extract -Force
  foreach ($pri in @("resources.pri", "Microsoft.UI.pri", "Microsoft.UI.Xaml.Controls.pri")) {
    Copy-Item -LiteralPath (Join-Path $extract $pri) -Destination $priCache -Force
  }
  Remove-Item -LiteralPath $zip, $extract -Recurse -Force
}
$compiledRid = Get-ChildItem -LiteralPath $compiled -Recurse -Filter "App.xbf" | Select-Object -First 1
foreach ($resource in @("App.xbf", "MainWindow.xbf", "resources.pri", "Microsoft.UI.pri", "Microsoft.UI.Xaml.Controls.pri")) {
  $target = Join-Path $publish $resource
  if (Test-Path -LiteralPath $target) { continue }
  $source = if ($compiledRid) { Join-Path $compiledRid.DirectoryName $resource } else { "" }
  if (-not $source -or -not (Test-Path -LiteralPath $source)) { $source = Join-Path $priCache $resource }
  if (-not (Test-Path -LiteralPath $source)) { throw "Missing required WinUI resource: $resource" }
  Copy-Item -LiteralPath $source -Destination $target
}

# 2. Go core (cgo) next to Nexus.exe, the bundled MCP proxy, and the CLI.
# The CLI lives in cli\: Nexus.exe and nexus.exe cannot share a folder.
$oldCgo = $env:CGO_ENABLED
try {
  $env:CGO_ENABLED = "1"
  go build -tags nexuscorelib -trimpath -ldflags $ld -buildmode=c-shared -o (Join-Path $publish "nexuscore.dll") ./cmd/nexuscore
  Assert-Native "nexuscore.dll"
  Remove-Item -LiteralPath (Join-Path $publish "nexuscore.h") -ErrorAction SilentlyContinue
  $env:CGO_ENABLED = "0"
  go build -trimpath -ldflags "-s -w" -o (Join-Path $publish "nexus-mcp-proxy.exe") ./cmd/nexus-mcp-proxy
  Assert-Native "nexus-mcp-proxy.exe"
  New-Item -ItemType Directory -Force -Path (Join-Path $publish "cli") | Out-Null
  go build -trimpath -ldflags $ld -o (Join-Path $publish "cli\nexus.exe") ./cmd/nexus
  Assert-Native "CLI"
} finally {
  $env:CGO_ENABLED = $oldCgo
}

foreach ($required in @("Nexus.exe", "Nexus.dll", "Velopack.dll", "nexuscore.dll", "nexus-mcp-proxy.exe", "Assets\Nexus.ico", "Assets\Nexus.png", "cli\nexus.exe")) {
  if (-not (Test-Path -LiteralPath (Join-Path $publish $required))) { throw "Incomplete app folder: $required" }
}

# 3. Plain zip: the layout scripts/install-windows.ps1 has always expanded.
if (Test-Path -LiteralPath $plainZip) { Remove-Item -LiteralPath $plainZip -Force }
Compress-Archive -Path (Join-Path $publish "*") -DestinationPath $plainZip -CompressionLevel Optimal
}

# 4. Velopack: Setup.exe, portable zip, full (+ delta) packages, feed.
if ($PreviousFeed) {
  # Without the previous release there is simply no delta package.
  & $Vpk download http --url $PreviousFeed --channel $Channel --outputDir $releases --allowEmptyChannel
  if ($LASTEXITCODE -ne 0) { Write-Warning "Could not download the previous release from $PreviousFeed; building without a delta." }
}
$pack = @(
  "pack",
  "--packId", "NexusDesktop",
  "--packVersion", $Version,
  "--packDir", $publish,
  "--mainExe", "Nexus.exe",
  "--packTitle", "Nexus",
  "--packAuthors", "Nexus",
  "--icon", (Join-Path $win "Assets\Nexus.ico"),
  "--shortcuts", "StartMenuRoot",
  # Branded animated splash (scripts/render-splash.py). Velopack's stock
  # progress bar is hidden; the GIF carries its own shimmer.
  "--splashImage", (Join-Path $win "Assets\InstallerSplash.gif"),
  "--splashProgressColor", "none",
  "--runtime", "win-x64",
  "--channel", $Channel,
  "--outputDir", $releases
)
if ($SignParams) { $pack += @("--signParams", $SignParams) }
if ($SkipVelopackAppCheck) { $pack += "--skipVeloAppCheck" }
& $Vpk @pack
Assert-Native "vpk pack"

$portable = Join-Path $out "Nexus-$Version-win-x64-Portable.zip"
Copy-Item -LiteralPath (Join-Path $releases "NexusDesktop-$Channel-Portable.zip") -Destination $portable -Force

# 5. Branded installer (desktop/installer): carries Velopack's Setup.exe,
# runs it silently and shows progress as the memory graph forming.
$payloadDir = Join-Path $out "installer-payload"
New-Item -ItemType Directory -Force -Path $payloadDir | Out-Null
Copy-Item -LiteralPath (Join-Path $releases "NexusDesktop-$Channel-Setup.exe") -Destination (Join-Path $payloadDir "Setup.exe") -Force
$appFiles = Get-ChildItem -LiteralPath $publish -Recurse -File
$payloadJson = '{{"version":"{0}","bytes":{1},"files":{2}}}' -f $Version, ($appFiles | Measure-Object Length -Sum).Sum, $appFiles.Count
[IO.File]::WriteAllText((Join-Path $payloadDir "payload.json"), $payloadJson)
$installerBin = Join-Path $out "installer-bin"
dotnet build (Join-Path $root "desktop\installer\NexusInstaller.csproj") -c Release -nodeReuse:false -o $installerBin "-p:PayloadDir=$payloadDir" "-p:Version=$numeric" "-p:InformationalVersion=$Version"
Assert-Native "branded installer"
$setup = Join-Path $out "Nexus-$Version-win-x64-Setup.exe"
Copy-Item -LiteralPath (Join-Path $installerBin "NexusInstaller.exe") -Destination $setup -Force
if ($SignParams) {
  $signtool = Get-ChildItem "${env:ProgramFiles(x86)}\Windows Kits\10\bin" -Recurse -Filter signtool.exe -ErrorAction SilentlyContinue | Where-Object { $_.FullName -match "x64" } | Select-Object -Last 1
  if ($signtool) { & $signtool.FullName sign ($SignParams -split ' ') $setup; Assert-Native "signtool" } else { Write-Warning "signtool not found; branded installer left unsigned." }
}

$hashes = foreach ($file in @($setup, $portable, $plainZip)) {
  "{0}  {1}" -f (Get-FileHash -LiteralPath $file -Algorithm SHA256).Hash.ToLowerInvariant(), [IO.Path]::GetFileName($file)
}
$hashes | Set-Content -LiteralPath (Join-Path $out "SHA256SUMS-windows.txt") -Encoding ascii
Get-ChildItem -LiteralPath $out -File | Select-Object Name, Length | Format-Table -AutoSize | Out-String | Write-Host
Get-ChildItem -LiteralPath $releases -File | Select-Object Name, Length | Format-Table -AutoSize | Out-String | Write-Host
Write-Host ("Signed: " + [bool]$SignParams + ". Nothing was installed or launched.")
