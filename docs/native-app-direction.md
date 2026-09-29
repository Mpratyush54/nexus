# Native app direction

**Status:** In progress (2026-09-30). Stack: **native shell + portal parity path** — see [ADR-050](decisions/ADR-050-native-desktop-fyne.md) (Fyne interim) and **embedded portal (Wails/WebView2)** below.

## Product goal

One **native desktop** install for Nexus — not “open Chrome to `127.0.0.1:7272`”, and not a one-page HTML status page as the product. OAuth sign-in may use the system browser once; **normal workflows stay in the desktop window**.

**Full parity** with [nexus.pratyushes.dev](https://nexus.pratyushes.dev): dashboard, memory, sessions, agents, team, org, settings, etc. Team/org/admin surfaces may open in the **system browser** only where explicitly labeled until the embedded portal ships.

## What “files” means (user-facing)

| Term | Meaning | Where in desktop |
|------|---------|------------------|
| **Memory entries** | Durable facts/decisions in Nexus cloud (searchable, keyed) | **Memory** — cloud API `/v1/agent/memory/search` |
| **Agent transcript files** | JSONL/SQLite logs from Cursor, Claude Code, etc. | **Harvest** — paths from daemon `/local/harvest` `files[]` |
| **Workspace files** | Source files under your chosen repo root | **Workspace files** — preview via daemon `/local/file/read` (workspace-relative) |
| **Agent artifacts** | Processed proposals / portal queue items | Portal **Sessions** / **Agents** (web parity track) |

Clicking a list row in Memory / Harvest / Workspace opens the **preview pane** (path, metadata, content snippet) — not a dead control.

## Architecture (2026-09-30)

| Layer | Role |
|-------|------|
| **`cmd/nexus-desktop`** | Single instance, tray, spawns `nexus-daemon`, Windows updater (unchanged) |
| **`internal/desktopui`** | Fyne **cockpit**: sidebar (Home, Memory, Harvest, Workspace files, Settings), content, VS Code-style preview |
| **`internal/localclient`** | Daemon `:7272` — status, harvest, workspace, file read |
| **`internal/cloudclient`** | Cloud API — memory search (Bearer token from login) |
| **Embedded portal (next)** | Wails/WebView2 hosting the **same React app** as the portal + existing `useTrustedLocalBridge` to `:7272` — fastest path to 100% UI parity (VS Code / Electron pattern) |

**Why not stop at Fyne widgets:** the web portal is the source of truth for UX; Fyne is the **interim native shell** with real data and preview, not a permanent duplicate of every React page.

**Why embedded webview is OK (vs rejected daemon HTML):** one packaged app window, bundled or pinned to production portal origin, with local daemon bridge — not a separate browser tab to localhost debug HTML.

## Web portal feature map (parity checklist)

| Portal route | Section | Desktop today | Target |
|--------------|---------|---------------|--------|
| `/app/dashboard` | Home | Fyne Home summary | Embedded or native dashboard |
| `/app/memory` | Memory | Search + preview | Full browse/edit via embed |
| `/app/connect` | Desktop / harvest | Harvest + Settings | Native + embed Connect |
| `/app/sessions` | Sessions | — | Embed |
| `/app/agents` | Agents | — | Embed |
| `/app/team`, `/app/org` | Team, Org | Settings → “Team, org & billing (web)” | Embed |
| `/app/branches` | Overlays | — | Embed |
| `/app/activity`, `/app/notifications` | Activity | — | Embed |
| `/app/settings`, `/app/admin` | Settings, Admin | Settings (account, workspace, updates) | Embed |

## What ships today (repo)

- `cmd/nexus-desktop` — Fyne cockpit + system tray; spawns `nexus-daemon` when signed in and workspace is set.
- `cmd/nexus-desktop/update.go` — Windows auto-update (stop locked processes, PowerShell replace, relaunch).
- `.github/workflows/release-desktop.yml` — per-OS Fyne builds.

### Run from `bin\` (dev)

```powershell
cd d:\central-memory
go build -o bin\nexus-desktop.exe .\cmd\nexus-desktop
go build -o bin\nexus-daemon.exe .\cmd\nexus-daemon
# optional: place both in bin\ and set NEXUS_DAEMON_BIN if needed
.\bin\nexus-desktop.exe
```

Sign in from **Settings**, choose workspace, use **Memory** / **Harvest** / **Workspace files**; preview pane shows selection content.

### Release 0.3.1+

Tag `desktop-v0.3.1` or workflow_dispatch **Release Desktop** with version `0.3.1` so clients pick up the fixed updater.

## Updater (root cause summary)

**Symptom:** 0.3.0 artifacts downloaded but **`nexus-daemon.exe` stayed old** while tray/desktop updated.

**Fix:** stop locked `nexus-daemon`, deferred PowerShell replace with retries + size verify, relaunch desktop.

## iOS

Not in this track — phone clients use cloud APIs; no local `:7272` on device.

## Next milestones

1. **Wails shell** — embed `frontend/` build (or pinned app URL) in one window; keep Go tray/updater/daemon spawn.
2. Fyne folder picker on macOS/Linux (Windows uses PowerShell picker in `main.go`).
3. macOS/Linux auto-update.
4. iOS discovery doc before any Xcode project.
