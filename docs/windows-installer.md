# Windows installer

Nexus for Windows installs through a branded front-end (`desktop/installer`)
on top of [Velopack](https://velopack.io), which owns the install layout,
updates and uninstall.

| Download | What it is |
| --- | --- |
| `Nexus-<ver>-win-x64-Setup.exe` | The branded installer. Per-user, no admin rights, no prerequisites. |
| `Nexus-<ver>-win-x64-Portable.zip` | Same app, unzip anywhere. Updates itself like an installed copy. |
| `Nexus-Setup.exe` | The same installer under a fixed name, for `releases/latest/download` links. |
| `install-windows.ps1` | The terminal install script (below). |
| `nexus-desktop-windows-amd64.zip` | Plain app folder from before the installer. The install script no longer uses it. |

## Install from a terminal

```powershell
irm https://nexus.pratyushes.dev/install.ps1 | iex
```

`nexus.pratyushes.dev/install.ps1` (and `/install`) redirects to
`install-windows.ps1` on the latest GitHub release, which is
`scripts/install-windows.ps1` from that release. The script:

- downloads `Nexus-Setup.exe` and `nexus-windows-amd64.exe` from the release,
  with a progress line, and checks each file's size and SHA-256 (from
  `SHA256SUMS-windows.txt` or `SHA256SUMS.txt`);
- installs the `nexus` command as `%LOCALAPPDATA%\Nexus\cli\nexus.exe` and
  adds that folder to the user PATH (not next to `Nexus.exe`: Windows file
  names ignore case);
- moves people off the old zip install in `%LOCALAPPDATA%\Nexus\bin`: stops
  processes running from it, takes it off PATH, and after the install removes
  its Startup, Start menu and desktop shortcuts (an old autostart or desktop
  shortcut moves to the installed app). The old files stay; user data in
  `%APPDATA%\central-memory` and `%LOCALAPPDATA%\Nexus` is never touched;
- opens the installer window, or with `-Silent` runs the Velopack `Setup.exe`
  embedded in it with `--silent` and then opens Nexus.

Switches need the script-block form, or an environment variable with `iex`:

```powershell
& ([scriptblock]::Create((irm https://nexus.pratyushes.dev/install.ps1))) -Silent
& ([scriptblock]::Create((irm https://nexus.pratyushes.dev/install.ps1))) -DryRun   # or -WhatIf
& ([scriptblock]::Create((irm https://nexus.pratyushes.dev/install.ps1))) -Demo     # installer --demo-install
$env:NEXUS_INSTALL_SILENT = "1"; irm https://nexus.pratyushes.dev/install.ps1 | iex
```

`-DryRun` downloads and verifies everything into a temp folder, prints what
it would do, and changes nothing. `-Version desktop-v0.4.1` pins a release.
No admin rights; Windows PowerShell 5.1 and PowerShell 7.

## Building

Build locally (never stops a running Nexus, never installs):

```powershell
dotnet tool install vpk --version 1.2.161 --tool-path .tools
./scripts/package-windows.ps1 -Version 0.4.0 -Vpk .tools/vpk.exe
```

CI (`.github/workflows/release-desktop.yml`, job `build-windows`) runs the same
script for every `desktop-v*` / `v*` tag. Stable tags use update channel `win`,
`-beta` / `-alpha` / `-rc` tags use `win-beta` and a GitHub prerelease.

## The front-end

`desktop/installer` is a small WPF app on .NET Framework 4.7.2, which every
supported Windows 10/11 already has. It starts instantly, adds about 100 KB
to the download, and WPF's vector rendering stays crisp at 100/150/200%.
Velopack's own `Setup.exe` is embedded as a resource.

- A borderless 720x460 window with rounded corners, a DWM shadow and dark
  acrylic (Windows 11 22H2+; solid ink elsewhere). It has custom minimize and
  close buttons, can be dragged anywhere, and opens with a fade and scale.
- Welcome: the Nexus mark assembles inside an orbiting node ring, followed by
  one Install button. "Options" opens the install location, a desktop
  shortcut and start at login. Fine print links Privacy (an in-window
  summary) and the source.
- Installing: no progress bar. A memory graph forms as real progress rises.
  Velopack's `Setup.exe --silent` runs underneath, and progress is read from
  what it does on disk and in the registry, in Velopack's own order:
  1. `Update.exe` is written;
  2. the package is copied;
  3. `current\` fills up, measured by bytes against `payload.json`;
  4. the Start shortcut is added;
  5. `nexus://` is registered by the app's install hook;
  6. the uninstall entry is written;
  7. Setup exits with code 0.

  Status lines change when these happen.
- Done: the graph blooms. "Open Nexus" opens the app's setup guide
  automatically after 2 s, in the same ink, cream and gold style.
- Errors: a friendly screen with Try again and the setup log. If Nexus is
  running: "Nexus is running. It will restart to update." The front-end asks
  the app to quit (`--quit`) and reopens it afterwards.
- Motion follows the Windows animation setting
  (`SystemParameters.ClientAreaAnimation`).
- `--demo-install` / `--demo-error` exercise the screens without installing.

Uninstall (Apps & features) asks through a native task dialog with the Nexus
icon and two command links: "Keep my data" (default, after a 25 s countdown)
or "Remove my data from this PC".

After a background update, the app shows "Updated to Nexus X" with a
"What's new" link once.

## What the installer does

- Installs to `%LOCALAPPDATA%\NexusDesktop` (`current\Nexus.exe`). The id is not
  "Nexus" because `%LOCALAPPDATA%\Nexus` already holds the app's preferences and
  caches, and Velopack deletes its install folder on uninstall.
- Self-contained: the .NET 8 runtime and the Windows App SDK are inside the app.
- Start menu shortcut "Nexus". The desktop shortcut and start-at-login are
  choices in the app's setup guide (and Settings), not installer questions.
- Registers `nexus://` links (install hook, and again on every launch).
- Adds "Nexus" with its version and icon to Apps & features.
- Upgrades in place. Settings stay in `%APPDATA%\central-memory` (sign-in,
  server, capture choices), `%APPDATA%\Nexus` and `%LOCALAPPDATA%\Nexus`.
- Retires the shortcuts of the old zip install (Startup, Start menu,
  desktop) that pointed at `%LOCALAPPDATA%\Nexus\bin`. The old CLI stays.
- Updates: installed and portable copies check
  `https://<bucket>.s3.ap-south-1.amazonaws.com/desktop/velopack` in the
  background, download deltas, and apply on the next start (or from
  Settings > General > Updates). `NEXUS_UPDATE_URL` overrides the feed.

Uninstall (Apps & features) removes the start-at-login entry, the desktop
shortcut and the `nexus://` registration when they point at this install, then
asks whether to also remove Nexus data from this computer. The question times
out after 25 seconds and keeps the data. Cloud history is never touched.

## Code signing (not set up yet)

Builds are currently unsigned. On first run Windows SmartScreen shows
"Windows protected your PC" for `Setup.exe`; users have to choose
**More info > Run anyway**. Verify downloads against `SHA256SUMS-windows.txt`.

To sign, add a code-signing certificate as repository secrets
`WINDOWS_CERT_PFX_BASE64` (base64 of the .pfx) and `WINDOWS_CERT_PASSWORD`.
The workflow then imports it and passes `--signParams` to `vpk pack`, which
signs `Nexus.exe`, `Update.exe` and `Setup.exe`. An OV certificate removes the
"unknown publisher" text but SmartScreen still warns until the certificate
builds reputation; an EV certificate (or Azure Trusted Signing, supported by
`vpk pack --azureTrustedSignFile`) is trusted immediately.

## macOS later

Velopack also packs macOS apps (`vpk pack` on a Mac produces a `.pkg` and an
update feed with the same `UpdateManager` API), so `UpdateService` carries over
to an Uno port. Windows-only integration (registry, `.lnk` shortcuts) sits
behind `Platform/IDesktopIntegration`; a Mac port provides its own
implementation (login item, URL scheme in Info.plist).
