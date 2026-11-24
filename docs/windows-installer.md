# Windows installer

Nexus for Windows ships as a [Velopack](https://velopack.io) installer.

| Download | What it is |
| --- | --- |
| `Nexus-<ver>-win-x64-Setup.exe` | The installer. Per-user, no admin rights, no prerequisites. |
| `Nexus-<ver>-win-x64-Portable.zip` | Same app, unzip anywhere. Updates itself like an installed copy. |
| `nexus-desktop-windows-amd64.zip` | Plain app folder, kept for `install-windows.ps1`. |

Build locally (never stops a running Nexus, never installs):

```powershell
dotnet tool install vpk --version 1.2.161 --tool-path .tools
./scripts/package-windows.ps1 -Version 0.4.0 -Vpk .tools/vpk.exe
```

CI (`.github/workflows/release-desktop.yml`, job `build-windows`) runs the same
script for every `desktop-v*` / `v*` tag. Stable tags use update channel `win`,
`-beta` / `-alpha` / `-rc` tags use `win-beta` and a GitHub prerelease.

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
- Retires the old `install-windows.ps1` shortcuts (Startup, Start menu,
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
