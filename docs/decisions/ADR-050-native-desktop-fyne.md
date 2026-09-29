# Native desktop UI (ADR)

## Status
Accepted for implementation (2026-09-30). Replaces browser/webview “cockpit”
navigation for Nexus Desktop.

## Context
`nexus-desktop` was a **Windows systray** (`github.com/gogpu/systray`) that
opened **Chrome/Edge to `http://127.0.0.1:7272/`** (daemon-embedded HTML).
Users rejected that fragmented HTML/webview feel and asked for a **real
native app on Windows + Mac (+ Linux)**. Embedded-HTML redesign was aborted.

## Decision
Ship the desktop shell with **[Fyne v2](https://fyne.io/)** (`fyne.io/fyne/v2`):

| Option | Verdict |
|--------|---------|
| **Fyne** | **Chosen** — native widgets in Go, Win/Mac/Linux, no web runtime; can share code with daemon/updater; Fyne also has a later iOS packaging path |
| Wails / Tauri | Rejected for product direction — still HTML/JS webviews |
| Flutter desktop | Strong iOS story later, but splits the stack (Dart) from Go daemon/updater |
| Keep systray + browser | Explicitly rejected by users |

## Architecture
- **Native shell** (`internal/desktopui`) — Fyne window: connection, workspace,
  harvest, updates. System tray via Fyne desktop driver (or interim tray).
- **Local API** (`internal/localclient`) — HTTP to existing `nexus-daemon`
  (`:7272`) + existing cloud API; **no backend rewrite**.
- **Updater** stays in Go (`cmd/nexus-desktop/update.go`): stop locked
  `nexus-daemon.exe`, stage, replace, restart; S3 `latest.json` as SoT.
- Daemon HTML status page remains as a **legacy debug endpoint only** — not
  the product UI. Do not invest in redesigning it.

## iOS (honest scope)
Not shipped in this track. Options later: Fyne `fyne package -os ios`, or a
Flutter/React Native client over the same cloud + optional remote APIs.
For now: shared `localclient`-style API client patterns only; no iOS app
scaffold claiming readiness.

## Consequences
- Release builds need **CGO + per-OS runners** (Fyne is not `CGO_ENABLED=0`
  cross-compile from Linux to macOS). Update `release-desktop.yml` accordingly.
- First Windows compile of Fyne is slow; subsequent builds are fine.
