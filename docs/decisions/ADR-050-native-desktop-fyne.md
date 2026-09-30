# Native desktop UI (ADR)

## Status
Accepted for implementation (2026-09-30). **Amended 2026-09-30 (Shell v2):** Fyne is the
**long-term** native shell — not an interim bridge to Wails/WebView. See
[native-app-direction.md](../native-app-direction.md).

## Context
`nexus-desktop` was a **Windows systray** (`github.com/gogpu/systray`) that
opened **Chrome/Edge to `http://127.0.0.1:7272/`** (daemon-embedded HTML).
Users rejected that fragmented HTML/webview feel and asked for a **real
native app on Windows + Mac (+ Linux)**. Embedded-HTML redesign was aborted.
A later “embed the React portal (Wails/WebView2)” milestone was also rejected:
it reintroduces browser-shaped UX and a dual UI stack next to Go daemon/updater.

## Decision
Ship and grow the desktop shell with **[Fyne v2](https://fyne.io/)** (`fyne.io/fyne/v2`):

| Option | Verdict |
|--------|---------|
| **Fyne** | **Chosen (long-term)** — native widgets in Go, Win/Mac/Linux, no web runtime; shares code with daemon/updater; later iOS packaging path possible |
| Wails / WebView2 embed | **Rejected** — portal React stays web-only; desktop does not embed it |
| Tauri | Not chosen — separate Rust stack |
| Flutter desktop | Strong iOS story later, but splits the stack (Dart) from Go daemon/updater |
| Keep systray + browser | Explicitly rejected by users |

## Architecture
- **Native shell** (`internal/desktopui`) — Fyne Shell v2: Welcome, Home
  (setup checklist + pulse), Memory (rich preview + file links), Harvest,
  Workspace, Settings + preview; system browser only for labeled team/org/billing.
  Quiet system tray (Open / Scan / Updates / Quit).
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
- Portal feature parity on desktop is earned page-by-page in Fyne, not by
  embedding `frontend/`.
