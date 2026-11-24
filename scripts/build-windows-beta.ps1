#Requires -Version 7.0
param([ValidatePattern('^\d+\.\d+\.\d+-beta\.\d+$')][string]$Version = '0.4.0-beta.3')
$ErrorActionPreference = 'Stop'
$betaRepo = Split-Path -Parent $PSScriptRoot
Set-Location -LiteralPath $betaRepo
$betaDist = Join-Path $betaRepo "dist\beta\$Version"
if (Test-Path -LiteralPath $betaDist) { throw "Beta output already exists: $betaDist. Use a new beta version; never overwrite a published build." }
$betaApp = Join-Path $betaDist 'NexusBeta'
$betaCompiled = Join-Path $betaRepo 'desktop\windows\bin-beta-publish\Release\net8.0-windows10.0.19041.0\win-x64'
New-Item -ItemType Directory -Path $betaApp | Out-Null
function Assert-Native([string]$Step) { if ($LASTEXITCODE -ne 0) { throw "$Step failed (exit $LASTEXITCODE)" } }
$betaCommit = (git rev-parse HEAD).Trim()
Assert-Native 'Git revision'
$betaDirty = -not [string]::IsNullOrWhiteSpace((git status --porcelain | Out-String))
$betaBuiltAt = [DateTime]::UtcNow.ToString('yyyy-MM-ddTHH:mm:ssZ')
$betaLd = "-s -w -X central-memory/internal/buildinfo.Version=$Version -X central-memory/internal/buildinfo.Commit=$betaCommit -X central-memory/internal/buildinfo.BuiltAt=$betaBuiltAt -X main.defaultServerURL=https://api-nexus.pratyushes.dev"
$betaFileVersion = $Version.Replace('-beta.','.')
$betaProps = @('-p:AppxGeneratePriEnabled=false','-p:WindowsAppSDKSelfContained=true',"-p:OutputPath=$betaCompiled\", "-p:Version=$Version", "-p:FileVersion=$betaFileVersion")
$betaAppx = Join-Path $betaRepo 'desktop\build-stubs\AppxPackage'
if (Test-Path -LiteralPath (Join-Path $betaAppx 'Microsoft.Build.Packaging.Pri.Tasks.dll')) { $betaProps += "-p:AppxMSBuildToolsPath=$betaAppx\" }
dotnet publish desktop/windows/Nexus.csproj -c Release -r win-x64 --self-contained true -o $betaApp @betaProps
Assert-Native 'Self-contained native publish'
foreach ($betaResource in @('App.xbf','MainWindow.xbf','resources.pri','Microsoft.UI.pri','Microsoft.UI.Xaml.Controls.pri')) {
  $betaSource = Join-Path $betaCompiled $betaResource
  if (-not (Test-Path -LiteralPath $betaSource)) { $betaSource = Join-Path (Join-Path $betaRepo 'desktop\windows\.runtime-pri') $betaResource }
  if (-not (Test-Path -LiteralPath $betaSource)) { throw "Missing required WinUI resource: $betaResource" }
  Copy-Item -LiteralPath $betaSource -Destination (Join-Path $betaApp $betaResource)
}
$betaOldCgo = $env:CGO_ENABLED
try {
  $env:CGO_ENABLED = '1'
  go build -tags nexuscorelib -trimpath -ldflags $betaLd -buildmode=c-shared -o (Join-Path $betaApp 'nexuscore.dll') ./cmd/nexuscore
  Assert-Native 'Embedded Go core'
  # Bundled stdio MCP proxy (Antigravity): the app must not need the CLI.
  $env:CGO_ENABLED = '0'
  go build -trimpath -ldflags '-s -w' -o (Join-Path $betaApp 'nexus-mcp-proxy.exe') ./cmd/nexus-mcp-proxy
  Assert-Native 'Bundled MCP proxy'
} finally { $env:CGO_ENABLED = $betaOldCgo }
# Windows paths are case-insensitive: Nexus.exe and nexus.exe CANNOT share a
# directory. CLI is deliberately isolated so it never overwrites the GUI.
$betaCli = Join-Path $betaApp 'cli'
New-Item -ItemType Directory -Path $betaCli | Out-Null
go build -trimpath -ldflags $betaLd -o (Join-Path $betaCli 'nexus.exe') ./cmd/nexus
Assert-Native 'CLI'
Copy-Item -LiteralPath (Join-Path $betaRepo 'docs\desktop-beta-notes.md') -Destination (Join-Path $betaApp 'README-BETA.md')
foreach ($betaRequired in @('Nexus.exe','Nexus.dll','nexuscore.dll','nexus-mcp-proxy.exe','Assets\Nexus.ico','Assets\Nexus.png','cli\nexus.exe')) {
  if (-not (Test-Path -LiteralPath (Join-Path $betaApp $betaRequired))) { throw "Incomplete beta: $betaRequired" }
}
$betaZip = Join-Path $betaDist "nexus-desktop-windows-x64-$Version.zip"
Compress-Archive -LiteralPath $betaApp -DestinationPath $betaZip -CompressionLevel Optimal
$betaHash = (Get-FileHash -LiteralPath $betaZip -Algorithm SHA256).Hash.ToLowerInvariant()
$betaManifest = @{ app='Nexus Desktop'; version=$Version; channel='beta'; os='windows'; arch='x64'; commit=$betaCommit; source_dirty=$betaDirty; built_at=$betaBuiltAt; filename=[IO.Path]::GetFileName($betaZip); sha256=$betaHash; self_contained=$true; signed=$false }
$betaManifest | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $betaDist 'release.json') -Encoding utf8NoBOM
"$betaHash  $([IO.Path]::GetFileName($betaZip))" | Set-Content -LiteralPath (Join-Path $betaDist 'SHA256SUMS.txt') -Encoding utf8NoBOM
Write-Output "Beta built at $betaDist. No upload, stable feed update, install or process termination was performed."
