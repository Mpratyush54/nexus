#Requires -Version 5.1
<#
.SYNOPSIS
  Install Nexus for Windows from a terminal.

.DESCRIPTION
  Downloads the Nexus installer (Nexus-Setup.exe) from the latest GitHub
  release, checks its size and SHA-256, and runs it. By default the Nexus
  installer window opens; with -Silent Nexus installs without any window.

  It also installs the `nexus` command line into %LOCALAPPDATA%\Nexus\cli
  and adds that folder to your user PATH.

  If you installed Nexus the old way (a zip in %LOCALAPPDATA%\Nexus\bin), the
  old copy is stopped, its Startup and Start menu shortcuts are removed, and
  the old folder is taken off PATH. Your data stays: %APPDATA%\central-memory
  and the preferences and logs in %LOCALAPPDATA%\Nexus are never touched.

  Per-user. No admin rights needed. Windows PowerShell 5.1 or PowerShell 7.

.PARAMETER Silent
  Install without the installer window, then open Nexus.

.PARAMETER DryRun
  Download and verify the installer, then print what would happen. Nothing
  on this PC is changed. -WhatIf does the same.

.PARAMETER Demo
  Download and verify, then open the installer in its demo mode
  (--demo-install). The demo never installs anything.

.PARAMETER Version
  A release tag such as desktop-v0.4.1. Default: the latest release.

.EXAMPLE
  irm https://nexus.pratyushes.dev/install.ps1 | iex

.EXAMPLE
  & ([scriptblock]::Create((irm https://nexus.pratyushes.dev/install.ps1))) -Silent

.EXAMPLE
  & ([scriptblock]::Create((irm https://nexus.pratyushes.dev/install.ps1))) -DryRun

.NOTES
  With `| iex` you can't pass switches; set $env:NEXUS_INSTALL_SILENT = "1"
  or $env:NEXUS_INSTALL_DRYRUN = "1" first instead.
#>
[CmdletBinding(SupportsShouldProcess = $true)]
param(
  [switch]$Silent,
  [switch]$DryRun,
  [switch]$Demo,
  [string]$Version = "latest"
)

# Everything runs inside a function so that `irm | iex` leaves no settings
# behind in the caller's session, and errors never close the window.
function Install-NexusApp {
  param([bool]$Silent, [bool]$DryRun, [bool]$Demo, [string]$Version)

  $ErrorActionPreference = "Stop"
  $ProgressPreference = "SilentlyContinue"
  # -WhatIf was turned into $DryRun; cmdlets below (Get-FileHash too) must still run.
  $WhatIfPreference = $false

  # Plain ASCII source: `irm` may not decode the script as UTF-8. The
  # console draws these; redirected output gets plain ASCII.
  $fancy = -not [Console]::IsOutputRedirected
  $ell = if ($fancy) { [string][char]0x2026 } else { "..." }
  $dash = if ($fancy) { [string][char]0x2014 } else { "-" }
  $ok = if ($fancy) { [string][char]0x2713 } else { "+" }

  $repo = "Mpratyush54/nexus"
  $setupAsset = "Nexus-Setup.exe"
  $cliAsset = "nexus-windows-amd64.exe"
  $sumFiles = @("SHA256SUMS-windows.txt", "SHA256SUMS.txt")

  $local = [Environment]::GetFolderPath("LocalApplicationData")
  $legacyBin = Join-Path $local "Nexus\bin"
  $cliDir = Join-Path $local "Nexus\cli"
  $appRoot = Join-Path $local "NexusDesktop"           # Velopack install root
  $appExe = Join-Path $appRoot "current\Nexus.exe"

  function Say([string]$Text, [string]$Color = "Gray") { Write-Host "  $Text" -ForegroundColor $Color }
  function Step([string]$Text) { Write-Host "  $ok " -ForegroundColor Green -NoNewline; Write-Host $Text }
  function Plan([string]$Text) { Write-Host "    - would $Text" -ForegroundColor Cyan }
  function Fail([string]$Text) { throw [System.InvalidOperationException]::new($Text) }

  function Format-MB([long]$Bytes) { "{0:N1} MB" -f ($Bytes / 1MB) }

  function Test-Under([string]$Path, [string]$Root) {
    if ([string]::IsNullOrWhiteSpace($Path) -or [string]::IsNullOrWhiteSpace($Root)) { return $false }
    try {
      $p = [IO.Path]::GetFullPath($Path)
      $r = [IO.Path]::GetFullPath($Root).TrimEnd('\') + '\'
      return $p.StartsWith($r, [StringComparison]::OrdinalIgnoreCase)
    } catch { return $false }
  }

  # ---- network

  # Windows PowerShell 5.1 can default to TLS 1.0; GitHub needs 1.2+.
  try {
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
  } catch { }
  Add-Type -AssemblyName System.Net.Http
  $http = [System.Net.Http.HttpClient]::new()
  $http.Timeout = [TimeSpan]::FromMinutes(15)
  $http.DefaultRequestHeaders.UserAgent.ParseAdd("nexus-install/1.0")

  function Get-Text([string]$Url) {
    try {
      $r = $http.GetAsync($Url).GetAwaiter().GetResult()
      if (-not $r.IsSuccessStatusCode) { return $null }
      return $r.Content.ReadAsStringAsync().GetAwaiter().GetResult()
    } catch { return $null }
  }

  function Save-Url([string]$Url, [string]$Dest, [string]$Label) {
    $resp = $http.GetAsync($Url, [System.Net.Http.HttpCompletionOption]::ResponseHeadersRead).GetAwaiter().GetResult()
    if (-not $resp.IsSuccessStatusCode) {
      Fail "Couldn't download $Label (HTTP $([int]$resp.StatusCode)). Check your connection and try again."
    }
    $total = $resp.Content.Headers.ContentLength
    $in = $resp.Content.ReadAsStreamAsync().GetAwaiter().GetResult()
    $out = [IO.File]::Create($Dest)
    $done = [long]0
    $buf = New-Object byte[] 262144
    $watch = [Diagnostics.Stopwatch]::StartNew()
    $tick = -1000
    $interactive = $fancy
    try {
      while (($n = $in.Read($buf, 0, $buf.Length)) -gt 0) {
        $out.Write($buf, 0, $n)
        $done += $n
        if ($interactive -and ($watch.ElapsedMilliseconds - $tick) -ge 120) {
          $tick = $watch.ElapsedMilliseconds
          if ($total) {
            $pct = [int][Math]::Floor(100 * $done / $total)
            $bar = ("#" * [int][Math]::Floor($pct / 4)).PadRight(25, ".")
            [Console]::Write("`r  Downloading $Label  [$bar] $pct%  $(Format-MB $done) / $(Format-MB $total)   ")
          } else {
            [Console]::Write("`r  Downloading $Label  $(Format-MB $done)   ")
          }
        }
      }
    } finally {
      $out.Dispose(); $in.Dispose(); $resp.Dispose()
    }
    if ($interactive) { [Console]::Write("`r" + (" " * 79) + "`r") }
    if ($total -and $done -ne $total) {
      Fail "The download of $Label was cut short ($(Format-MB $done) of $(Format-MB $total)). Try again."
    }
    return $done
  }

  # Release metadata: the API gives the version, exact sizes and digests. If
  # it is unavailable (rate limit, proxy) the /latest/download links still work.
  function Find-Release {
    $api = "https://api.github.com/repos/$repo/releases"
    $candidates = @()
    if ($Version -eq "latest") {
      $one = Get-Text "$api/latest"
      if ($one) { $candidates += ($one | ConvertFrom-Json) }
      $hasSetup = $candidates | Where-Object { $_.assets | Where-Object { $_.name -eq $setupAsset } }
      if (-not $hasSetup) {
        # The newest release may be CLI-only; take the newest one with the installer.
        $list = Get-Text "$api`?per_page=15"
        if ($list) { $candidates = @($list | ConvertFrom-Json | ForEach-Object { $_ } | Where-Object { -not $_.draft -and -not $_.prerelease }) }
      }
    } else {
      $one = Get-Text "$api/tags/$Version"
      if (-not $one) { Fail "Release '$Version' wasn't found." }
      $candidates += ($one | ConvertFrom-Json)
    }
    foreach ($rel in $candidates) {
      if ($rel.assets | Where-Object { $_.name -eq $setupAsset }) { return $rel }
    }
    return $null
  }

  function Get-ExpectedHash($Release, [string]$Base, [string]$Asset) {
    foreach ($f in $sumFiles) {
      $text = Get-Text "$Base/$f"
      if (-not $text) { continue }
      foreach ($line in ($text -split "`r?`n")) {
        if ($line -match '^\s*([0-9a-fA-F]{64})\s+\*?(.+?)\s*$' -and $Matches[2] -eq $Asset) {
          return @{ Hash = $Matches[1].ToLowerInvariant(); From = $f }
        }
      }
    }
    if ($Release) {
      $a = $Release.assets | Where-Object { $_.name -eq $Asset } | Select-Object -First 1
      if ($a -and $a.digest -and "$($a.digest)" -match '^sha256:([0-9a-f]{64})$') {
        return @{ Hash = $Matches[1]; From = "GitHub release" }
      }
    }
    return $null
  }

  function Confirm-Download([string]$Path, [string]$Asset, $Release, [string]$Base, [long]$MinBytes) {
    $size = (Get-Item -LiteralPath $Path).Length
    $a = if ($Release) { $Release.assets | Where-Object { $_.name -eq $Asset } | Select-Object -First 1 } else { $null }
    if ($a -and [long]$a.size -ne $size) {
      Fail "$Asset is $size bytes but the release says $($a.size). The download is damaged; try again."
    }
    if ($size -lt $MinBytes) { Fail "$Asset is too small ($(Format-MB $size)). The download is damaged; try again." }
    $fs = [IO.File]::OpenRead($Path)
    try { $mz = ($fs.ReadByte() -eq 0x4D) -and ($fs.ReadByte() -eq 0x5A) } finally { $fs.Dispose() }
    if (-not $mz) { Fail "$Asset isn't a Windows program. The download is damaged; try again." }
    $want = Get-ExpectedHash $Release $Base $Asset
    if ($want) {
      $sha = [Security.Cryptography.SHA256]::Create()
      $fs = [IO.File]::OpenRead($Path)
      try { $got = ([BitConverter]::ToString($sha.ComputeHash($fs)) -replace '-', '').ToLowerInvariant() } finally { $fs.Dispose(); $sha.Dispose() }
      if ($got -ne $want.Hash) {
        Fail "$Asset failed its SHA-256 check (expected $($want.Hash), got $got). Nothing was installed."
      }
      return "size and SHA-256 verified ($($want.From))"
    }
    return "size verified (no published SHA-256)"
  }

  # ---- the old zip install (%LOCALAPPDATA%\Nexus\bin)

  function Get-LegacyProcesses {
    Get-Process -ErrorAction SilentlyContinue | Where-Object {
      $p = $null; try { $p = $_.Path } catch { }
      $p -and (Test-Under $p $legacyBin)
    }
  }

  function Get-UserPath {
    $key = Get-Item -LiteralPath "HKCU:\Environment"
    return [string]$key.GetValue("Path", "", [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
  }

  function Set-UserPath([string]$Value) {
    Set-ItemProperty -LiteralPath "HKCU:\Environment" -Name Path -Value $Value -Type ExpandString
    # Tell Explorer so new terminals pick it up (setting a user variable broadcasts WM_SETTINGCHANGE).
    [Environment]::SetEnvironmentVariable("NEXUS_INSTALL_REFRESH", "1", "User")
    [Environment]::SetEnvironmentVariable("NEXUS_INSTALL_REFRESH", $null, "User")
  }

  function Test-SamePath([string]$Entry, [string]$Dir) {
    $e = [Environment]::ExpandEnvironmentVariables($Entry).Trim().TrimEnd('\')
    return $e -ieq $Dir.TrimEnd('\')
  }

  function Get-LegacyShortcuts {
    $wsh = New-Object -ComObject WScript.Shell
    $found = @()
    $programs = [Environment]::GetFolderPath("Programs")
    $sets = @(
      @{ Kind = "Startup"; Dir = [Environment]::GetFolderPath("Startup"); Pattern = "*Nexus*.lnk" },
      @{ Kind = "Start menu"; Dir = (Join-Path $programs "Nexus"); Pattern = "*.lnk" },
      @{ Kind = "Start menu"; Dir = $programs; Pattern = "Nexus Desktop.lnk" },
      @{ Kind = "desktop"; Dir = [Environment]::GetFolderPath("DesktopDirectory"); Pattern = "Nexus*.lnk" }
    )
    foreach ($s in $sets) {
      if (-not (Test-Path -LiteralPath $s.Dir)) { continue }
      foreach ($lnk in Get-ChildItem -LiteralPath $s.Dir -Filter $s.Pattern -File -ErrorAction SilentlyContinue) {
        $target = ""
        try { $target = $wsh.CreateShortcut($lnk.FullName).TargetPath } catch { }
        if ([string]::IsNullOrWhiteSpace($target) -or (Test-Under $target $appRoot)) { continue }
        $name = [IO.Path]::GetFileName($target)
        $legacy = (Test-Under $target $legacyBin) -or
          $name -like "nexus-desktop*" -or $name -like "nexus-daemon*" -or
          (($name -ieq "Nexus.exe") -and -not (Test-Path -LiteralPath $target))
        if ($legacy) { $found += [pscustomobject]@{ Kind = $s.Kind; Path = $lnk.FullName; Target = $target } }
      }
    }
    return $found
  }

  # Same rules as the installer and the app (LegacyInstall): an old autostart
  # or desktop shortcut moves over to the installed app, so the choice survives.
  function Move-LegacyShortcuts {
    $links = @(Get-LegacyShortcuts)
    if ($links.Count -eq 0) { return }
    foreach ($l in $links) { Remove-Item -LiteralPath $l.Path -Force -ErrorAction SilentlyContinue }
    $folder = Join-Path ([Environment]::GetFolderPath("Programs")) "Nexus"
    if ((Test-Path -LiteralPath $folder) -and -not (Get-ChildItem -LiteralPath $folder -Force)) {
      Remove-Item -LiteralPath $folder -Force -ErrorAction SilentlyContinue
    }
    if ($links | Where-Object Kind -eq "Startup") {
      $run = "HKCU:\Software\Microsoft\Windows\CurrentVersion\Run"
      if (-not (Test-Path $run)) { New-Item -Path $run -Force | Out-Null }
      Set-ItemProperty -LiteralPath $run -Name "Nexus" -Value "`"$appExe`" --background"
    }
    if ($links | Where-Object Kind -eq "desktop") {
      $wsh = New-Object -ComObject WScript.Shell
      $sc = $wsh.CreateShortcut((Join-Path ([Environment]::GetFolderPath("DesktopDirectory")) "Nexus.lnk"))
      $sc.TargetPath = $appExe
      $sc.WorkingDirectory = Split-Path $appExe
      $sc.Description = "Nexus: your coding agents' memory"
      $sc.Save()
    }
    Step "Removed $($links.Count) old Nexus shortcut$(if ($links.Count -ne 1) { 's' })"
  }

  # ---- the installed app

  function Get-AppProcesses {
    Get-Process -Name Nexus -ErrorAction SilentlyContinue | Where-Object {
      $p = $null; try { $p = $_.Path } catch { }
      $p -and (Test-Under $p $appRoot)
    }
  }

  function Get-InstalledVersion {
    $f = Join-Path $appRoot "current\sq.version"
    if (Test-Path -LiteralPath $f) {
      $m = [regex]::Match((Get-Content -LiteralPath $f -Raw), "<version>([^<]+)</version>")
      if ($m.Success) { return $m.Groups[1].Value.Trim() }
    }
    return ""
  }

  # Nexus-Setup.exe is the branded front-end; Velopack's own Setup.exe is
  # embedded in it as the resource "payload.Setup.exe". -Silent runs that
  # directly with Velopack's --silent flag (the front-end does the same).
  function Export-VelopackSetup([string]$FrontEnd, [string]$Dest) {
    $asm = [Reflection.Assembly]::Load([IO.File]::ReadAllBytes($FrontEnd))
    $s = $asm.GetManifestResourceStream("payload.Setup.exe")
    if (-not $s) { Fail "This installer has no silent mode. Run the command without -Silent." }
    $out = [IO.File]::Create($Dest)
    try { $s.CopyTo($out) } finally { $out.Dispose(); $s.Dispose() }
    $fs = [IO.File]::OpenRead($Dest)
    try { $mz = ($fs.ReadByte() -eq 0x4D) -and ($fs.ReadByte() -eq 0x5A) } finally { $fs.Dispose() }
    if (-not $mz) { Fail "The installer's setup payload is damaged. Run the command without -Silent." }
  }

  # ================================================================== start

  $mode = if ($DryRun) { "dry run: nothing on this PC will change" } elseif ($Demo) { "installer demo: nothing on this PC will change" } elseif ($Silent) { "silent" } else { "" }
  Write-Host ""
  Write-Host "  Installing Nexus$ell" -ForegroundColor White -NoNewline
  if ($mode) { Write-Host "  ($mode)" -ForegroundColor DarkGray } else { Write-Host "" }
  Write-Host ""

  if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) { Fail "This installer is for Windows." }
  if (-not [Environment]::Is64BitOperatingSystem) { Fail "Nexus needs 64-bit Windows." }
  $admin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
  if ($admin) { Say "Note: this is an admin terminal. Nexus installs for the signed-in user and doesn't need admin." DarkYellow }

  $release = Find-Release
  if ($release) {
    $tag = [string]$release.tag_name
    $base = "https://github.com/$repo/releases/download/$tag"
    $label = ($tag -replace '^desktop-', '') -replace '^v', ''
    Step "Found Nexus $label"
  } elseif ($Version -eq "latest") {
    $base = "https://github.com/$repo/releases/latest/download"
    $label = "(latest)"
  } else {
    Fail "Release '$Version' has no Windows installer."
  }

  $work = Join-Path ([IO.Path]::GetTempPath()) ("nexus-install-" + [Guid]::NewGuid().ToString("N").Substring(0, 8))
  New-Item -ItemType Directory -Path $work -Force -WhatIf:$false | Out-Null
  try {
    # 1. Download and verify the installer.
    $setup = Join-Path $work $setupAsset
    $bytes = Save-Url "$base/$setupAsset" $setup "Nexus"
    $check = Confirm-Download $setup $setupAsset $release $base 5MB
    Step "Downloaded the installer ($(Format-MB $bytes)), $check"

    # 2. The command line, into its own folder (nexus.exe beside Nexus.exe
    #    would collide: Windows file names ignore case).
    $cliTmp = Join-Path $work "nexus.exe"
    $cliBytes = Save-Url "$base/$cliAsset" $cliTmp "the nexus command"
    $cliCheck = Confirm-Download $cliTmp $cliAsset $release $base 1MB
    Step "Downloaded the nexus command ($(Format-MB $cliBytes)), $cliCheck"

    $legacyProcs = @(Get-LegacyProcesses)
    $userPath = Get-UserPath
    $entries = @($userPath -split ';' | Where-Object { $_.Trim() })
    $oldEntries = @($entries | Where-Object { Test-SamePath $_ $legacyBin })
    $hasCli = [bool]($entries | Where-Object { Test-SamePath $_ $cliDir })
    $oldLinks = @(Get-LegacyShortcuts)
    $installed = Get-InstalledVersion

    if ($DryRun) {
      Write-Host ""
      Say "What would happen:" White
      foreach ($p in $legacyProcs) { Plan "stop the old $($p.ProcessName) (pid $($p.Id)) running from $legacyBin" }
      if ($oldEntries.Count) { Plan "take $legacyBin off your user PATH" }
      Plan "install the nexus command to $cliDir\nexus.exe"
      if (-not $hasCli) { Plan "add $cliDir to your user PATH" }
      if ($Silent) {
        # Unpacked (never run) to prove the silent path works with this build.
        $vpk = Join-Path $work "Setup.exe"
        Export-VelopackSetup $setup $vpk
        Say "  (found Velopack's Setup.exe inside the installer, $(Format-MB (Get-Item -LiteralPath $vpk).Length))" DarkGray
        if (@(Get-AppProcesses).Count) { Plan "ask the running Nexus to quit, then reopen it after the update" }
        Plan "run Velopack's Setup.exe --silent (from inside $setupAsset), installing Nexus $label into $appRoot"
      } else {
        Plan "open the Nexus installer window ($setupAsset $label)"
      }
      foreach ($l in $oldLinks) { Plan "remove the old $($l.Kind) shortcut $($l.Path) (points at $($l.Target))" }
      if ($oldLinks | Where-Object Kind -eq "Startup") { Plan "keep start at login, pointed at the installed Nexus" }
      Write-Host ""
      if ($installed) { Say "Installed now: Nexus $installed in $appRoot" DarkGray }
      if (Test-Path -LiteralPath $legacyBin) { Say "Old files in $legacyBin stay; delete the folder when you no longer need them." DarkGray }
      Say "Kept as is: $env:APPDATA\central-memory and the preferences and logs in $local\Nexus" DarkGray
      Write-Host ""
      Write-Host "  Dry run finished. Nothing on this PC was changed." -ForegroundColor Green
      Write-Host ""
      return
    }

    if ($Demo) {
      Say "Opening the installer in demo mode$ell"
      $p = Start-Process -FilePath $setup -ArgumentList "--demo-install" -PassThru
      $p.WaitForExit()
      Write-Host ""
      Write-Host "  Demo finished. Nothing on this PC was changed." -ForegroundColor Green
      Write-Host ""
      return
    }

    # 3. Retire the old zip install's processes.
    if ($legacyProcs.Count) {
      $legacyProcs | Stop-Process -Force -ErrorAction SilentlyContinue
      Start-Sleep -Milliseconds 800
      Step "Closed the old Nexus"
    }

    # 4. Install the command line. A running nexus.exe (an agent's MCP
    #    server, say) can't be overwritten but can be renamed.
    New-Item -ItemType Directory -Path $cliDir -Force | Out-Null
    $cliExe = Join-Path $cliDir "nexus.exe"
    Get-ChildItem -LiteralPath $cliDir -Filter "nexus.exe.old*" -ErrorAction SilentlyContinue | Remove-Item -Force -ErrorAction SilentlyContinue
    try {
      Copy-Item -LiteralPath $cliTmp -Destination $cliExe -Force
    } catch {
      Rename-Item -LiteralPath $cliExe -NewName ("nexus.exe.old-" + [Guid]::NewGuid().ToString("N").Substring(0, 6)) -Force
      Copy-Item -LiteralPath $cliTmp -Destination $cliExe -Force
    }
    $keep = @($entries | Where-Object { -not (Test-SamePath $_ $legacyBin) })
    if (-not $hasCli) { $keep += $cliDir }
    $newPath = $keep -join ';'
    if ($newPath -ne $userPath) { Set-UserPath $newPath }
    $env:Path = (@(@($env:Path -split ';') | Where-Object { $_ -and -not (Test-SamePath $_ $legacyBin) -and -not (Test-SamePath $_ $cliDir) }) + $cliDir) -join ';'
    Step "Installed the nexus command"
    if ($oldEntries.Count) { Step "Took the old $legacyBin off PATH" }

    # 5. The app.
    if ($Silent) {
      if (@(Get-AppProcesses).Count) {
        Say "Nexus is running. It will restart to update."
        try { Start-Process -FilePath $appExe -ArgumentList "--quit" -WindowStyle Hidden } catch { }
        for ($i = 0; $i -lt 40 -and @(Get-AppProcesses).Count; $i++) { Start-Sleep -Milliseconds 250 }
      }
      $vpk = Join-Path $work "Setup.exe"
      Export-VelopackSetup $setup $vpk
      $log = Join-Path ([IO.Path]::GetTempPath()) "NexusSetup.log"
      Say "Installing Nexus $label$ell"
      $p = Start-Process -FilePath $vpk -ArgumentList @("--silent", "--log", "`"$log`"") -WindowStyle Hidden -PassThru
      $p.WaitForExit()
      if ($p.ExitCode -ne 0 -or -not (Test-Path -LiteralPath $appExe)) {
        Fail "Nexus Setup stopped with an error (code $($p.ExitCode)). The log is at $log."
      }
      Move-LegacyShortcuts
      for ($i = 0; $i -lt 40 -and @(Get-AppProcesses).Count; $i++) { Start-Sleep -Milliseconds 150 }
      Start-Process -FilePath $appExe -WorkingDirectory (Split-Path $appExe)
      Write-Host ""
      Write-Host "  Done $dash Nexus is open." -ForegroundColor Green
    } else {
      Say "Opening the Nexus installer$ell finish there."
      $p = Start-Process -FilePath $setup -PassThru
      $p.WaitForExit()
      if (-not (Test-Path -LiteralPath $appExe)) {
        Write-Host ""
        Say "The installer closed before Nexus was installed. Run the command again any time." Yellow
        Write-Host ""
        return
      }
      Move-LegacyShortcuts
      Write-Host ""
      if (@(Get-AppProcesses).Count) {
        Write-Host "  Done $dash Nexus is open." -ForegroundColor Green
      } else {
        Write-Host "  Done $dash Nexus is installed. Open it from Start." -ForegroundColor Green
      }
    }
    Say "Open a new terminal to use the nexus command. Your memories, sign-in and settings were kept."
    Write-Host ""
  } finally {
    Remove-Item -LiteralPath $work -Recurse -Force -ErrorAction SilentlyContinue -WhatIf:$false
    $http.Dispose()
  }
}

$nexusDry = $DryRun.IsPresent -or [bool]$WhatIfPreference -or ($env:NEXUS_INSTALL_DRYRUN -eq "1")
$nexusSilent = $Silent.IsPresent -or ($env:NEXUS_INSTALL_SILENT -eq "1")
try {
  Install-NexusApp -Silent $nexusSilent -DryRun $nexusDry -Demo $Demo.IsPresent -Version $Version
} catch {
  Write-Host ""
  Write-Host "  Nexus couldn't be installed." -ForegroundColor Red
  Write-Host "  $($_.Exception.Message)" -ForegroundColor Red
  Write-Host "  You can also download the installer: https://nexus.pratyushes.dev/download" -ForegroundColor DarkGray
  Write-Host ""
}
