# Build unpackaged WinUI Nexus shell (workaround when VS Appx MSBuild tasks are missing).
$ErrorActionPreference = "Stop"
$root = Resolve-Path (Join-Path $PSScriptRoot "..\..")
$win = Join-Path $root "desktop\windows"
$stubs = Join-Path $root "desktop\build-stubs\AppxPackage"
$out = Join-Path $win "bin\Release\net8.0-windows10.0.19041.0\win-x64"
$launch = ($args -contains "-Launch" -or $args -contains "-Run")

$env:Path = "C:\Program Files\dotnet;" + [Environment]::GetEnvironmentVariable("Path", "Machine") + ";" + [Environment]::GetEnvironmentVariable("Path", "User")

# Always stop a running shell before build so Nexus.exe isn't file-locked.
Get-Process Nexus -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
Start-Sleep -Milliseconds 400

# Ensure stub Appx tasks exist
if (-not (Test-Path (Join-Path $stubs "Microsoft.Build.AppxPackage.dll"))) {
    Push-Location (Join-Path $root "desktop\build-stubs")
    dotnet build -c Release -v q
    New-Item -ItemType Directory -Force -Path $stubs | Out-Null
    $dll = Join-Path $root "desktop\build-stubs\bin\Release\net8.0\Microsoft.Build.AppxPackage.dll"
    Copy-Item $dll (Join-Path $stubs "Microsoft.Build.AppxPackage.dll") -Force
    Copy-Item $dll (Join-Path $stubs "Microsoft.Build.Packaging.Pri.Tasks.dll") -Force
    Pop-Location
}

Push-Location $win
dotnet build -c Release -r win-x64 "-p:AppxMSBuildToolsPath=$stubs\" "-p:AppxGeneratePriEnabled=false" "-p:WindowsAppSDKSelfContained=false"
if ($LASTEXITCODE -ne 0) { Pop-Location; exit $LASTEXITCODE }
Pop-Location

# nexuscore.dll must always be rebuilt. Reusing a pre-existing C-shared
# library silently runs stale core routes after a desktop-only rebuild.
$core = Join-Path $root "bin\nexuscore.dll"
$env:CGO_ENABLED = "1"
$env:Path = "C:\msys64\ucrt64\bin;" + $env:Path
New-Item -ItemType Directory -Force -Path (Split-Path -Parent $core) | Out-Null
Push-Location $root
go build -tags nexuscorelib -buildmode=c-shared -o $core ./cmd/nexuscore
if ($LASTEXITCODE -ne 0) { Pop-Location; exit $LASTEXITCODE }
Pop-Location
Copy-Item $core $out -Force

# Framework PRI files (required for XamlControlsResources without full VS Appx pipeline)
$pkg = Join-Path $env:USERPROFILE ".nuget\packages\microsoft.windowsappsdk\1.6.250108002\tools\MSIX\win10-x64\Microsoft.WindowsAppRuntime.1.6.msix"
$priCache = Join-Path $root "desktop\windows\.runtime-pri"
if (-not (Test-Path (Join-Path $priCache "resources.pri"))) {
    New-Item -ItemType Directory -Force -Path $priCache | Out-Null
    $zip = Join-Path $env:TEMP "wasdk-runtime.zip"
    Copy-Item $pkg $zip -Force
    $extract = Join-Path $env:TEMP "wasdk-runtime-extract"
    Remove-Item $extract -Recurse -Force -ErrorAction SilentlyContinue
    Expand-Archive $zip -DestinationPath $extract -Force
    Copy-Item (Join-Path $extract "resources.pri") $priCache -Force
    Copy-Item (Join-Path $extract "Microsoft.UI.pri") $priCache -Force
    Copy-Item (Join-Path $extract "Microsoft.UI.Xaml.Controls.pri") $priCache -Force
}
Copy-Item (Join-Path $priCache "*.pri") $out -Force

Write-Host "Built: $out\Nexus.exe"
Write-Host "Run:   & '$out\Nexus.exe'"

if ($launch) {
    Start-Process -FilePath (Join-Path $out "Nexus.exe") -WorkingDirectory $out
    Write-Host "Launched Nexus.exe"
}
