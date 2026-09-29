# Native app direction

**Status:** In progress (2026-09-30). Authoritative stack decision: [ADR-050](decisions/ADR-050-native-desktop-fyne.md).

## Product goal

One **native desktop** experience on Windows, macOS, and Linux — not a systray that opens Chrome to `127.0.0.1:7272`, and not a redesigned embedded HTML page on the daemon. The daemon keeps a **legacy debug** HTML status route only; the product UI lives in **`nexus-desktop`**.

## Stack (Win + Mac + Linux)

| Choice | Role |
|--------|------|
| **Fyne v2** | Native widgets in Go; same language as daemon, updater, and CLI |
| **`internal/desktopui`** | Main window: connection, workspace, harvest, updates |
| **`internal/localclient`** | HTTP to daemon `:7272` (`/local/status`, `/local/harvest`, …) |
| **Cloud API** | Sign-in via browser callback; portal links; future dashboard parity |

**Why not Wails / Tauri:** still a webview + front-end bundle; user asked for a proper native shell, not another HTML surface.

**Why not Flutter (yet):** stronger long-term mobile story, but splits the stack from Go services we already ship.

## What ships today (repo)

- `cmd/nexus-desktop` — Fyne app + optional system tray; single instance; spawns `nexus-daemon` when signed in and workspace is set.
- `cmd/nexus-desktop/update.go` — Windows auto-update: download → **stop locked processes** → PowerShell replace-after-exit → relaunch desktop → log + version check (`%LOCALAPPDATA%\Nexus\logs\update.log`).
- `.github/workflows/release-desktop.yml` — per-OS Fyne builds (CGO), cross-compiled daemon/CLI, S3 manifest; promote `desktop/latest.json` on **tags** or `workflow_dispatch` with explicit version.

### Release 0.3.1+

Cut a tagged release so clients pick up the fixed updater:

```bash
git tag desktop-v0.3.1
git push origin desktop-v0.3.1
```

Or: Actions → **Release Desktop (all OS)** → `workflow_dispatch` → version `0.3.1`.

## Updater (root cause summary)

**Symptom:** 0.3.0 artifacts downloaded but **`nexus-daemon.exe` stayed old** while the tray/desktop updated.

**Cause:** `nexus-daemon.exe` was **still running** (file lock). Replace/copy steps could fail or skip the daemon binary while other files updated.

**Fix:**

1. Kill the in-process daemon owned by desktop, then `taskkill` loop until `nexus-daemon` exits.
2. Deferred `apply-update.ps1`: wait for tray PID, force-stop `nexus-desktop` / `nexus-daemon` / `nexus`, **Move-Item + Copy-Item** with retries and **size verification**.
3. Run `nexus-daemon version` on the new binary and relaunch `nexus-desktop`.

## iOS (honest scope)

**Not in this track.** Shipping a real Nexus iOS app means App Store constraints, background harvest, Keychain auth, and likely **no local `:7272` daemon** on phone — the phone talks to **cloud APIs** (and maybe a Mac/PC bridge later).

Reasonable later paths (plan only today):

- **Fyne iOS packaging** (`fyne package -os ios`) if the shell stays Go/Fyne — still requires Apple toolchain, entitlements, and a mobile-specific UX pass.
- **Swift/SwiftUI or Flutter** client reusing the same REST/agent APIs and auth flows as the portal — more typical for production iOS.

For now: share API client patterns (`localclient`-style types, auth config); **no iOS scaffold** claiming store readiness.

## Next milestones

1. Fyne folder picker on macOS/Linux (Windows still has PowerShell picker in `main.go`).
2. Parity with portal “cockpit” views where it matters (memory browse, notifications) — still via API, not daemon HTML.
3. macOS/Linux auto-update (today: Windows-only in `applyDesktopUpdate`).
4. iOS discovery doc + API contract review before any Xcode project.
