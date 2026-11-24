#Requires -Version 7.0
$ErrorActionPreference='Stop'
$betaRoot=Split-Path -Parent $PSScriptRoot
$betaWorkflow=Get-Content -LiteralPath (Join-Path $betaRoot '.github\workflows\release-desktop.yml') -Raw
if (([regex]::Matches($betaWorkflow,"if: needs\.meta\.outputs\.prerelease != 'true'")).Count -ne 3) {throw 'Stable publisher is not gated for all three artifact jobs'}
if ($betaWorkflow -notmatch '\*-beta\*\|\*-alpha\*\|\*-rc\*\)') {throw 'Prerelease version classification is missing'}
$betaBuild=Get-Content -LiteralPath (Join-Path $betaRoot 'scripts\build-windows-beta.ps1') -Raw
if ($betaBuild -match 'aws s3|gh release|Stop-Process|taskkill') {throw 'Local beta build must not publish or kill processes'}
if ($betaBuild -notmatch "Join-Path \`$betaApp 'cli'") {throw 'CLI must not overwrite Nexus.exe on a case-insensitive filesystem'}
Write-Output 'Beta publication boundaries passed: no stable publisher for prereleases; build has no upload/process-kill side effects; CLI isolated.'
