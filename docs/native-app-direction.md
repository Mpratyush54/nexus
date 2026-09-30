# Native app direction

**Status:** In progress (2026-09-30). Stack: **Fyne native shell (long-term)** + daemon + tray + updater — see [ADR-050](decisions/ADR-050-native-desktop-fyne.md). No React/HTML/WebView/Wails embed for the desktop product window.

## Product goal

One **native desktop** install for Nexus — not “open Chrome to `127.0.0.1:7272`”, and not a one-page HTML status page as the product. OAuth sign-in may use the system browser once; **normal workflows stay in the desktop window**.

**Parity path:** grow the Fyne shell section by section (Welcome → Home → Memory / Harvest / Workspace → Settings). Team/org/admin surfaces may open in the **system browser** only where explicitly labeled until those pages exist natively.

## What “files” means (user-facing)

| Term | Meaning | Where in desktop |
|------|---------|------------------|
| **Memory entries** | Durable facts/decisions in Nexus cloud (searchable, keyed) | **Memory** — portal `GET /memory/search` (+ agent search fallback) |
| **Agent transcript files** | JSONL/SQLite logs from Cursor, Claude Code, etc. | **Harvest** — paths from daemon `/local/harvest` `files[]` |
| **Workspace files** | Source files under your chosen repo root | **Workspace** — pick folder + preview via daemon `/local/file/read` |
| **Agent artifacts** | Processed proposals / portal queue items | Portal **Sessions** / **Agents** (web; optional native later) |

Clicking a list row in Memory / Harvest / Workspace opens the **preview pane** (path, metadata, content snippet) — not a dead control. Memory selection shows **full metadata** plus **clickable linked-file buttons** (paths parsed from content / `files_affected`); opening a file reads it in-app via the daemon when the path is under the workspace.

## Architecture (2026-09-30)

| Layer | Role |
|-------|------|
| **`cmd/nexus-desktop`** | Single instance, quiet tray, spawns `nexus-daemon`, Windows updater |
| **`internal/desktopui`** | Fyne **Shell v2**: Welcome, Home, Memory (filters), Harvest, **Sessions (teleport)**, Workspace, Settings + wide chat-style preview |
| **`internal/localclient`** | Daemon `:7272` — status, harvest, workspace, file read, snapshots/restore |
| **`internal/cloudclient`** | Cloud API — memory browse/search with level/tags (Bearer token from login) |
| **`internal/authbrowser`** | System-browser OAuth once; tokens land in local config |

**Why Fyne long-term:** one Go stack with daemon/updater/tray; no second UI runtime; Win/Mac/Linux from the same codebase. Portal React remains the **web** product; desktop does not embed it.

**Why not Wails/WebView:** users rejected browser-shaped desktop UX; embedding the portal reintroduces that feel and a dual-stack maintenance cost.

## Shell v2 information architecture

| Section | Job |
|---------|-----|
| **Welcome** | Brand + Sign in (when no token) |
| **Home** | Portal-style dashboard: stats, GitHub activity heatmap, recent pulse; compact Connect setup strip |
| **Memory** | Cloud facts; search + level/status/category/tags filters; rich preview with linked-file open |
| **Harvest** | Agent transcript files + harness status; **chat-style preview** (scrollable turn blocks, ~40/60 split) |
| **Sessions** | Cloud **Teleport** snapshots — list + restore-to-workspace (same as portal `/app/sessions`) |
| **Workspace** | Folder pick (Fyne dialog, all OS) + recent list + workspace-relative preview |
| **Settings** | Account, updates, quit; Team/org/billing → system browser |

No separate **Connect** nav item — setup lives as a **compact strip on Home**. No auto folder dialog on launch. Tray stays quiet: Open / Scan / Updates / Quit.

### Session teleport (where it lives)

| Surface | Route / nav | What you get |
|---------|-------------|--------------|
| **Desktop** | Sidebar **Sessions** | Lists `/local/snapshots`, **Restore to workspace** via `/local/session/restore`, copy CLI |
| **Web portal** | SideNav **Sessions** → `/app/sessions` | Active sessions + **Session snapshots (Teleport)** table (copy restore / download bundle) |
| **CLI** | `nexus session restore <id> --workspace <path>` | Same restore pipeline as Desktop |

Desktop pushes snapshots while you work; restore reconstitutes harness transcript + git branch/diff onto the linked folder.

## Web portal feature map (parity checklist)

| Portal route | Section | Desktop today | Target |
|--------------|---------|---------------|--------|
| `/app/dashboard` | Home | Fyne Home (stats + heatmap + pulse + setup strip) | Native Home |
| `/app/memory` | Memory | Browse/search + rich preview + file links | Native browse/edit over time |
| `/app/connect` | Desktop / harvest | Compact setup strip on **Home** | Native Home setup |
| `/app/sessions` | Sessions | Fyne **Sessions** (snapshots + restore) | Native restore + web parity |
| `/app/agents` | Agents | — | Native or labeled web |
| `/app/team`, `/app/org` | Team, Org | Settings → “Team, org & billing (web)” | Labeled web until native |
| `/app/branches` | Overlays | — | Later |
| `/app/activity`, `/app/notifications` | Activity | — | Later |
| `/app/settings`, `/app/admin` | Settings, Admin | Settings (account, workspace, updates) | Native Settings + labeled web admin |

## What ships today (repo)

- `cmd/nexus-desktop` — Fyne Shell v2 + quiet system tray; spawns `nexus-daemon` when signed in and workspace is set.
- `cmd/nexus-desktop/update.go` — Windows auto-update (stop locked processes, PowerShell replace, relaunch).
- `.github/workflows/release-desktop.yml` — per-OS Fyne builds.

### Run from `bin\` (dev)

```powershell
cd d:\central-memory
go build -o bin\nexus-desktop.exe .\cmd\nexus-desktop
go build -o bin\nexus-daemon.exe .\cmd\daemon
.\bin\nexus-desktop.exe
```

Sign in from **Welcome**, choose a workspace on **Home** / **Workspace**, use **Memory** / **Harvest** / **Workspace**; Memory preview shows full details and clickable file links.

### Release 0.3.1+

Tag `desktop-v0.3.1` or workflow_dispatch **Release Desktop** with version `0.3.1` so clients pick up the fixed updater.

## Updater (root cause summary)

**Symptom:** 0.3.0 artifacts downloaded but **`nexus-daemon.exe` stayed old** while tray/desktop updated.

**Fix:** stop locked `nexus-daemon`, deferred PowerShell replace with retries + size verify, relaunch desktop.

## iOS

Not in this track — phone clients use cloud APIs; no local `:7272` on device.

## Next milestones

1. Deepen Fyne pages (memory edit, richer workspace tree, Home setup polish).
2. macOS/Linux auto-update.
3. iOS discovery doc before any Xcode project.
