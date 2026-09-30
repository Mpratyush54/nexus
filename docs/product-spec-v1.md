# Nexus Product Spec v1 (for approval)

**Status:** Draft for approval · 2026-09-30 · Not implemented · 22 user decisions recorded (section 0.1) · no open questions · architecture: cloud is the source of truth, Go engine in-process, no localhost port, remote MCP only (section 7)
**Supersedes (on approval):** the desktop direction in `docs/native-app-direction.md` and ADR-050 (Fyne)
**Scope:** Product definition, memory model, timeline, session fidelity, continuing sessions, Teleport (moving a session from one developer to another), native desktop architecture, administration and roles, roadmap.

---

## 0. Executive summary

1. Nexus captures every session from every agent the user runs (16 supported harnesses) and turns it into central memory that any other agent can fetch through MCP or the HTTPS API.
2. The desktop Home becomes a **Timeline**: what each agent did, when, and in which project. Sessions, memories, files, and Teleports all open directly from timeline rows.
3. Memory has stages **Raw**, **Extracted**, **Session summaries**, and **Project knowledge**, plus two cross-project scopes: **Personal** (only me) and **Team** (whole org). There is **no confirm step**. Extracted items are live immediately, and facts from every session, including private ones, **auto-promote** into project knowledge after redaction (**D11**). Capture can be turned off **per project** (**D10**).
4. Sessions become first-class objects keyed by the harness's own session ID (text, not UUID). **Every file a session touches, plus the full working tree** (only regenerable dependency and build folders excluded), is uploaded as verified, content-addressed blobs in S3. A snapshot is only marked complete when every blob it references is present, so any version can be restored exactly on any machine (**D8**). Secret files are encrypted on the client, never stored in plaintext.
5. **Continue session:** 11 of the 16 harnesses have documented CLI resume. Each time, the user picks either a **Nexus chat pane** that drives the agent in the background, or **launching the agent's own app already resumed**. The other harnesses get a "seeded new session". IDE-first agents clearly say they don't support resume, but the session is still written into the IDE's own chat history (**D13**).
6. **Teleport** grows from same-user, partial snapshots into a **cross-developer, full-fidelity handoff**. Developer A sends a session; Developer B previews it, gets the exact repo state in an isolated worktree, and continues it in the same agent on B's laptop.
7. Sessions are **private by default**. Sharing is **person-specific**: only the named recipient sees a shared session, and team-wide sharing happens only by explicit choice (**D9**). Outsiders can get session-scoped guest links (**D12**). Any share can be live or point-in-time, and team shares are live (**D15**). Sessions form a fork tree (**D16**). Today's leak, where any project member can download any snapshot, is closed as the **first task of P0**. Teleport adds revoke, audit, path remapping between machines and operating systems, and a redaction preview before anything is sent.
8. **The cloud is the source of truth (D6).** The desktop app is one integrated native app: WinUI 3 (C#) on Windows first, then SwiftUI on macOS, with the **Go engine embedded in-process** (Windows DLL, macOS static library). There is **no separate daemon and no localhost port**; the `:7272` API is removed. The engine captures and uploads, and the UI reads from the cloud API. Local disk holds only an **upload outbox** (capture survives network drops) and a **disposable read cache**, so losing a laptop loses nothing already uploaded. Agents use the **hosted MCP endpoint** (**D7**). Fyne is retired once the new app reaches parity.
9. There is no HTML, WebView, Electron, or Wails in the desktop app. The web portal **also gets the Timeline and Teleport inbox**, matching the desktop, alongside admin, billing, and org. Windows ships as a **Velopack** per-user installer with auto-update.
10. Roadmap: P0 (close the snapshot leak first, then cloud-authoritative, complete capture) → P1 timeline and memory APIs → P2 WinUI app → P3 Continue → P4 cross-developer Teleport → P5 macOS. Org administration lands in P1 and P4, and the separate super admin console is P7. Each phase has acceptance criteria (section 11).
11. **Administration (D20):** org roles are Owner, Admin, Member, and Guest. Org admins manage members, projects, capture, storage, learnings, shares, audit, billing, and offboarding, but **can't read members' private sessions** (only through an audited, two-Owner legal-hold path the member is told about). Super admins run the service from a **separate, MFA-protected console** and **can never read customer content or decrypt secrets**; support sees content only through time-limited, customer-approved break-glass access shown in the customer's audit log.

### 0.1 Decisions (confirmed by the user, 2026-09-30)


| #      | Decision                                                                                                                                                                                                                                            | Affects                     |
| ------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------- |
| **D1** | **Sessions are private by default.** The owner explicitly shares a session, with named people (the default audience; see D9) or, by explicit choice, the whole team. This replaces today's implicit "every project member can read every snapshot". | 2.1, 2.4, 7.7, 9.3, P0, P4  |
| **D2** | **Continue supports both modes, chosen each time:** (a) a Nexus chat pane driving the agent in the background; (b) launching the agent's own app or CLI window already resumed.                                                                     | 5.2, 5.3, 6.3, 6.5, 7.7, P3 |
| **D3** | **Two cross-project memory scopes:** **Personal** (only me, across all my projects) and **Team** (the whole org).                                                                                                                                   | 2.1, 2.2, 6.4               |
| **D4** | **The web portal gets the Timeline and Teleport inbox too**, matching the desktop (not admin-only).                                                                                                                                                 | 3.5, 6.1, 7.9, P1, P4       |
| **D5** | **Windows installer: Velopack**, an unpackaged per-user installer with auto-update. Not MSIX or the Store. Remaining detail, not blocking P2: choose the code-signing route (EV certificate or Azure Trusted Signing).                              | 7.6, P2                     |
| **D6** | **The cloud is the source of truth.** This reverses the earlier local-first design. Everything captured is pushed to the cloud, which is authoritative. The desktop embeds the Go engine in-process (still no separate daemon, no `:7272`): the engine captures and uploads, and the UI reads from the cloud API. Local disk is **only** an upload outbox plus a disposable read cache. Offline, the UI shows a clear banner and cached views are read-only; capture continues into the outbox. There is no SQLite source of truth; a small SQLite file is kept only for the outbox and cache. | 2.1, 3.5, 4.2, 7.1–7.11, P0, P2, P5 |
| **D7** | **Remote MCP only.** Agents connect to the hosted MCP endpoint (`/v1/agent/mcp`, HTTPS, bearer token or OAuth, project header). The local stdio `nexus mcp` over a local database is dropped. New tools (`memory_search`, `memory_write`, `session_summary_get`, `session_fetch`, `project_knowledge`) live on the server. A thin stdio-to-HTTPS proxy with no local data is allowed only for agents that can't use remote MCP (section 7.5 lists which). | 2.3, 7.5, 7.7, 7.8, P0 |
| **D8** | **Restore must work for all files.** Every file a session references or touches, plus the full working tree needed to reproduce state, is uploaded as content-addressed blobs. A manifest may **never** reference a blob that isn't uploaded: upload is hash-verified before a snapshot is marked complete, and restore validates that every blob is present. Only regenerable dependency and build folders are excluded; restore reinstalls them. Secret files are never uploaded in plaintext. Large files are chunked, with explicit limits and warnings instead of silent skips. | 4.1–4.6, 9.4, 9.7, P0, P4 |
| **D9** | **Sharing is person-specific.** Sessions are private by default (D1). When a member shares with another member, **only that recipient** can see the session, its chat, and its files. Team-wide sharing happens only by explicit choice. **Today's leak (any project member can download any snapshot) is closed as the first P0 task**, and existing snapshots become owner-only. | 2.4, 4.5, 6.5, 9.2, 9.3, 9.8, P0 |
| **D10** (Q2) | **Capture is controlled per project.** Raw capture can be turned off per project (not per agent in v1). For captured projects, everything needed for restore is always kept, and learnings keep accruing to the project. **Retention:** every version is kept for 90 days, then one version per day after that. | 2.1, 4.1 (F6), 6.6, P0, P1 |
| **D11** (Q3) | **Learnings auto-promote.** Facts extracted from any session, including private ones, flow automatically into the project's shared knowledge. There's no "share learnings" toggle. Chat and file privacy still follows D1 and D9. Required safeguards: secret and PII redaction before promotion, provenance so the owner sees what was promoted, and the owner can remove a promoted fact. | 2.1, 2.4, 9.3, P1 |
| **D12** (Q5) | **Share targets:** the whole team; a specific person; or someone outside the project through a link invite, which gives guest access scoped to that one session, expires, and has an optional membership request. | 6.1, 9.2, 9.3, P4 |
| **D13** (Q6) | **IDE-first agents** (Cursor IDE, Windsurf, Antigravity IDE) clearly show "this agent doesn't support resume". Nexus still restores the full session into the IDE's own local history, so it appears in the IDE's chat list. This is best effort, with version checks and a backup before every write. | 5.1, 5.2, 5.4, P3 |
| **D14** (Q8) | **Encryption:** KMS at rest is the default. End-to-end encryption is off by default and is a paid premium feature. | 9.7, P6 |
| **D15** (Q10) | **Live sharing:** every share (one-to-one, guest, or team) can be live. For one-to-one and guest shares, the sharer chooses live or a fixed point-in-time copy. Team shares are always live. | 9.2, 9.9, P4 |
| **D16** (Q11) | **Fork tree:** both A and B can fork at any point, and fork again, forming a fork tree. | 9.2, 9.10, P4 |
| **D17** (Q15) | **Fork merge is code only.** Merging forks merges code through git (one branch per fork). Conversations stay separate; a merge creates no new session. | 9.10, P6 |
| **D18** (Q13) | **Secret recovery via sign-in.** Users recover encrypted secrets by signing in alone. The server holds the key material (KMS) and **can** decrypt, only for the owner or explicit value-share recipients, with every decrypt audited. Customers who need the server unable to read secrets use premium end-to-end encryption (D14). | 4.2, 4.4, 7.7, 7.11, 9.7, P0 |
| **D19** (Q14) | **Capture limits accepted, plus per-plan storage caps.** Files over 64 MiB are chunked; the user is asked before files over 1 GiB; files over 10 GiB are refused; a warning appears when a version adds over 5 GiB. Total storage is capped per plan (Free 5 GiB, Pro 100 GiB, Team 250 GiB per seat pooled). At the cap, uploads of new file blobs stop while transcript capture continues. | 4.2, 4.3, 7.11, P0 |
| **D20** | **Administration has two separate levels.** **Org admin** (customer side) has roles Owner, Admin, Member, and Guest. Admins manage members, projects, capture, storage, learnings moderation, shares, audit, billing, and offboarding, but **don't read members' private sessions**; the only path is an audited two-Owner legal hold that the member is notified about. **Super admin** (the Nexus operator) works from a separate MFA-protected console. It manages tenants, users, plans, flags, queues, releases, health, and abuse, but **never reads customer content and never decrypts secrets**. Support sees content only through time-limited, customer-approved break-glass access that appears in the customer's audit log. | 1.2, 9.3, 10, P0, P1, P4, P7 |
| **D21** (Q16) | **Org admins see only aggregates for private sessions:** per-member session counts, storage used, and last-active time. They never see titles, summaries, file names, or per-session timestamps. | 10.2, 10.6, P1 |
| **D22** (Q17) | **Super admins see org names and project IDs.** Project names appear only under customer-approved break-glass access. | 10.3, 10.6, P7 |

Architecture history: the desktop must not depend on a separate daemon or localhost port (first revision). D6 and D7 then moved the source of truth to the cloud and MCP to the hosted endpoint. Section 7 reflects both.

---

## 1. Product definition

### 1.1 One-liner

Nexus is the shared memory and session layer across all of your AI coding agents. Anything one agent learned, any other agent (or any teammate) can pick up and continue.

### 1.2 Users (in priority order)


| User                             | Situation                                     | What they need first                                                    |
| -------------------------------- | --------------------------------------------- | ----------------------------------------------------------------------- |
| **Solo multi-machine developer** | Uses 3–6 agents across a desktop and a laptop | See everything in one place, continue any session on any machine        |
| **Small team (2–20)**            | Shares a repo; hands work between people      | Send a session to a teammate who continues it; shared project knowledge |
| **Org Owner or Admin**           | Runs the team's Nexus org                     | Members and roles, capture per project, storage, learnings moderation, share revocation, audit, billing, offboarding (section 10.2) |
| **Nexus operator** (super admin) | Runs the service for all tenants              | Tenants, plans, flags, queues, releases, health, abuse; no customer content (section 10.3) |


### 1.3 Core jobs to be done

1. **"What happened?"** Show every agent session across projects and machines on one timeline.
2. **"Don't make me re-explain."** Any agent can fetch the relevant decisions and facts automatically (MCP `memory_search`, session summaries).
3. **"Pick up where I left off."** Open any past session, see the full chat, and continue it in the same agent without a terminal.
4. **"Take this over."** Send a live or past session to a teammate, who continues it on their laptop with the same code state.
5. **"What does the project know?"** Browse curated project knowledge and team-wide conventions, with links back to their sources.

### 1.4 Non-goals (v1)

- Not a chat log viewer for its own sake. Raw transcripts exist to power resume, provenance, and extraction.
- No manual review or confirm queue for memory.
- No desktop web views. No mobile client in v1.
- No automated merging of two developers' divergent sessions.

---

## 2. Memory model

### 2.1 Stages


| Stage                        | What it is                                                                                               | Produced by                                                                                                                        | Storage                                                                                                                      | Default scope                                       |
| ---------------------------- | -------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------- |
| **Raw**                      | Full conversation: every turn, tool call and tool result, file ops, commands, timestamps                 | Harvester in the embedded Nexus engine (full fidelity, including `tool_calls`), captured continuously for every project where capture is on (**D10**: on or off per project) | **Cloud (authoritative, D6):** turns in Postgres, content in S3 blobs. Locally only in the upload outbox until acknowledged (section 7.2) | Owner only (**D1**; only named recipients or an explicit team share widen it, **D9**) |
| **Extracted**                | Atomic facts and decisions ("we use pgx not database/sql"), with provenance (session, turn range, files) | Extraction worker (existing server harvest queue) running over new raw turns once they reach the cloud                             | `memory_items` level=`session`, category, `files_affected`, `source_session`, `source_turns` (cloud)                         | Same as the session (owner only by default, **D1**) |
| **Session summary**          | One per session (and refreshed while it runs): goal, outcome, open threads, files touched, next steps    | Extraction worker at idle or session end (existing `SESSION_TRANSCRIPT_COMPLETE` trigger)                                          | `session_summaries` (new; one row per version)                                                                               | Same as the session                                 |
| **Project knowledge**        | Durable, deduplicated project truths: architecture, conventions, decisions, gotchas                      | Automatic promotion from **every** session, private ones included (**D11**), after secret and PII redaction: recurrence, high importance, or an explicit `memory_write` with level=project | `memory_items` level=`project`, `supersedes_key` chain                                                                       | Project members                                     |
| **Personal** (scope, **D3**) | Only me, across all my projects: preferences, style, personal conventions                                | Promotion by the owner, or explicit writes (`memory_write` level=personal)                                                         | `memory_items` level=`personal`                                                                                              | The user only                                       |
| **Team** (scope, **D3**)     | The whole org: house rules, shared conventions                                                           | Promotion from project knowledge by a member with the grant, or explicit writes                                                    | `memory_items` level=`organization`                                                                                          | All org members                                     |


### 2.2 Removing "confirm"

- Today the lifecycle is `PROPOSED → CONFIRMED`, with a 24-hour auto-confirm sweep (`internal/store/memory_transitions.go`), and **vector search returns CONFIRMED items only** (`internal/store/memory.go`). The result is that fresh knowledge is invisible to agents for a day, and the UI shows a status the user doesn't care about.
- **Decision:** items become **active on write**. Quality is controlled by confidence, dedupe, and supersede, not by human review. `status` stays as an internal column (`active | superseded | forgotten`). The UI never shows "Proposed" or "Confirmed".
- The user can act on any item: **Edit**, **Pin** (boosts ranking and prevents decay), **Change scope** (session → project, or → Personal, or → Team), and **Forget** (soft delete with history, via the existing `/memory/{id}/history` and `/revert`).
- Low-confidence extractions (below a threshold) are still stored but ranked down, and hidden from the default Memory view behind "Show low confidence".

### 2.3 How agents fetch memory


| Path                                                     | Use                                  | Notes                                                                                                                                                                                                                                        |
| -------------------------------------------------------- | ------------------------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Remote MCP** `POST /v1/agent/mcp` (**D7**, the only MCP) | Every agent: installed on a laptop, cloud agents, CI | Streamable HTTP over HTTPS. Bearer token (or MCP OAuth where the agent supports it) plus `X-Nexus-Project`. Tools: `memory_search`, `memory_write`, `session_summary_get` (new), `session_fetch` (new, paged raw turns), `project_knowledge` (new, top-N for bootstrap). Requires network; see section 7.5 |
| **stdio-to-HTTPS proxy** (`nexus mcp-proxy`, fallback only) | Agents that can't send remote-MCP auth | Forwards JSON-RPC to `/v1/agent/mcp` unchanged. **No local data, no cache, no port.** Today only needed for Antigravity (header bug) and possibly unverified harnesses (section 7.5) |
| **HTTPS** `/v1/agent/memory/search                       | write`                               | Scripts                                                                                                                                                                                                                                      |
| **Bootstrap injection** (optional)                       | Harnesses that read rule files       | The materializer in the Nexus core writes a short project-knowledge digest into `.cursor/rules`, `CLAUDE.md`, and similar files (existing materializer). Opt-in per project                                                                  |


Ranking: pinned items first, then level (session context is boosted for the active session), then vector similarity, recency, and confidence. Superseded items are excluded.

### 2.4 Scoping rules

- Project identity stays as it is today: git origin plus root commit (`/projects/resolve`). Every teammate who clones the repo resolves to the **same project**, and membership controls access.
- **Sessions are private by default (D1).** The owner shares a session explicitly with **named people**; each recipient gets a personal grant and nobody else sees it (**D9**). Sharing with the whole team is a separate, explicit choice.
- **Learnings auto-promote (D11).** Facts extracted from any session, including private ones, are promoted automatically into the project's shared knowledge. There's no "share learnings" toggle. Before promotion is complete, facts stay at session level (visible to the owner and grantees only).
- **Tension with D1 and D9, stated plainly:** the chat and files of a private session stay private, but **what was learned from it doesn't**. Teammates can see a promoted fact ("we pin pgx to v5.6 because of the pool leak") without ever seeing the session. A fact can reveal what the owner was working on. The safeguards below reduce the risk but don't remove it; users are told at onboarding and in the Memory page.
- **Safeguards (required before this ships):**
  1. **Redaction before promotion:** the secret scanner plus PII detection (emails, phone numbers, personal names not on the team, customer identifiers, file paths under `${HOME}`) run on the fact text. Hits are redacted, or the fact is held at session level.
  2. **Provenance to the owner:** a promoted fact shows teammates "from a private session" (no link). The owner sees the exact session and turn range, and gets a **"Promoted from your sessions"** list in Memory (and an optional daily digest).
  3. **Owner removal:** the owner can **Remove from project** for any fact promoted from their session. It's withdrawn from project knowledge, search, and bootstrap digests immediately (recorded in history and audit as `memory.removed_by_owner`) and is not re-promoted from the same turns.
  4. **Per-project capture off (D10)** remains the hard stop: projects with capture off produce no learnings at all.
- **Capture control (D10):** Settings → Projects has a **Capture** switch per project. Off means no raw capture, no restore data, and no learnings for that project. On means everything needed for restore is always kept. v1 has no per-agent switch.
- **Retention (D10):** every version is kept for 90 days, then one version per day. Plan limits can change this later.

---

## 3. Timeline

### 3.1 Event types (user-visible)


| Kind                                         | Source                                              | Row shows                                                                |
| -------------------------------------------- | --------------------------------------------------- | ------------------------------------------------------------------------ |
| Session started / active / ended             | Harvester and new `agent_sessions`                  | Agent icon, project, machine, title (from summary), duration, turn count |
| Turn burst                                   | Raw turns grouped per 10-minute window              | "12 turns · 4 files edited · 3 commands" (collapsed)                     |
| File changes                                 | Provenance (`session_file_operations`)              | File chips, with +/- lines                                               |
| Commands                                     | `session_tool_executions`                           | Command, exit code                                                       |
| Commit                                       | Git watcher / `GIT_COMMITTED`                       | SHA, message                                                             |
| Memory extracted / promoted                  | Extraction worker                                   | "3 new facts", "1 promoted to project"                                   |
| Session summary ready                        | Worker                                              | Summary first line                                                       |
| Teleport sent / received / opened / restored | Teleport service                                    | From → to, status                                                        |
| Episode (bug or incident)                    | Existing episodes                                   | Title, status                                                            |
| Teammate activity (team mode)                | Same event types from other members, limited by ACL | Avatar and name                                                          |


### 3.2 Grouping and layout

- Day, then **session cards** (the primary unit). Within a card, child events are collapsed ("show 23 events").
- Sessions in the same project within 30 minutes stack into a "work block" when the "Compact" density is on.
- Live sessions are pinned at the top with a pulse indicator.

### 3.3 Filters

Project · Agent (harness) · Machine · Person (team) · Kind · Date range · Text search (titles, summaries, memory text). Filters persist per window. `Ctrl+K` opens a command and search palette.

### 3.4 What clicking opens


| Target       | Opens                                                                          |
| ------------ | ------------------------------------------------------------------------------ |
| Session card | **Session view** (full chat, files, memory, Continue, Teleport)                |
| File chip    | Diff viewer at that point in the session (blob-to-blob), with "Open in editor" |
| Memory chip  | Memory detail (text, provenance, history, scope actions)                       |
| Commit       | Commit detail (message, files), "Open in GitHub" (system browser)              |
| Teleport row | Teleport inbox item (preview, then Prepare and Continue)                       |
| Summary line | Session view scrolled to the Summary tab                                       |


### 3.5 Existing APIs and gaps


| Existing API                                                                     | Usable for                       | Gap                                                                                                                                          |
| -------------------------------------------------------------------------------- | -------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------- |
| `GET /projects/{id}/events`                                                      | Per-project activity             | One project per call; scans at most 1,000 events (`store.MaxEventsLimit`), then filters in memory; no cursor; no harness or session grouping |
| `GET /projects/{id}/dashboard`                                                   | Stats and heatmap                | Counts `PROPOSED`/`CONFIRMED`, which go away; heatmap only                                                                                   |
| `GET /sessions`, `/sessions/{id}/join                                            | leave`                           | Live collaborative sessions (UUID)                                                                                                           |
| `GET /projects/{id}/snapshots`                                                   | Teleport list                    | Capped at 100; latest only; no uploader identity                                                                                             |
| `GET /sessions/{id}/operations`, `/files`                                        | Provenance                       | Almost always empty today (see section 4.1); UUID `session_id`                                                                               |
| `GET /memory/search`, `/memory/{id}/history`                                     | Memory                           | No `source_session` or turn provenance                                                                                                       |
| `GET /memory/harvest`                                                            | Extraction job list              | Job-centric, not user-meaningful                                                                                                             |
| `GET /projects/{id}/presence`, `/notifications`, `/projects/{id}/mcp/tool-calls` | Live indicators, MCP activity    | Separate feeds                                                                                                                               |
| Daemon `/local/harvest`, `/local/harvest/read`, `/local/snapshots`               | Local transcript files, previews | File-centric; preview capped at 2 MiB; **removed in the target architecture** (section 7.8)                                                  |


**Where the timeline comes from:**

- **Desktop (D6):** read from the **cloud** (`/v1/timeline`) through the embedded engine, which keeps a disposable read cache. Items still waiting in this machine's upload outbox are shown with an "uploading" marker. **Offline:** a banner ("Offline: showing cached data, read-only. Capture continues."); cached views stay browsable but actions that change data (share, Teleport, memory edits, Continue on uncached sessions) are disabled.
- **Web portal (D4):** the same Timeline and Teleport inbox, from the same cloud API.

**New cloud APIs needed** (for the desktop, the web portal, and teammates' events): `GET /v1/timeline` (cross-project, cursor-paginated, server-side filters, ACL-aware); `GET /v1/agent-sessions` and `/v1/agent-sessions/{sid}` (plus `/turns`, `/files`, `/operations`, `/memories`, `/summary`); a WebSocket or SSE `timeline` channel scoped to the user. Details in section 7.7.

---

## 4. Session model (full fidelity)

### 4.1 Audit findings and required fixes


| #   | Finding (today)                                                                                                                                                               | Where                                                                      | Required fix                                                                                                                                                                                                                                                                                                                                            |
| --- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| F1  | Only `git diff HEAD` plus untracked files ≤100 KiB, capped at 512 KiB, are synced. Commits made during the session are lost, and large or binary changes are silently dropped | `internal/daemon/snapshot.go` (`collectUncommittedDiff`)                   | Capture **workspace state** per **D8** (section 4.3): `base_commit` (HEAD) + `remote_ref`, plus a verified blob for **every file in the working tree** and every file the session touched (inside or outside the repo), plus deletions and renames. Only regenerable dependency and build folders are excluded. If commits aren't on any remote, include a `git bundle`. No silent caps |
| F2  | Restore checks out the **branch**, not the recorded `git_commit`                                                                                                              | `internal/daemon/restore.go`                                               | Restore to the **exact commit** into a dedicated worktree (section 9.4). Verify blob hashes after applying                                                                                                                                                                                                                                              |
| F3  | Provenance is effectively empty: the harvester strips `tool_calls` (`Turn.Content` = "tool-call blocks stripped"); interceptor FILE_* events never reach `pushParsedToolOps`  | `harvester.go`, `runtime.go`, `tool_parser.go`, `interceptor.go`           | Raw capture keeps structured `tool_calls` and `tool_results` for every turn. One provenance pipeline takes both parsed tool calls **and** interceptor FILE_*/COMMAND_* events, dedupes them by (turn, path, op), and pushes                                                                                                                             |
| F4  | Artifact extension allowlists, 2 MiB caps (`maxSnapshotSQLiteArtifactBytes`), silent skips                                                                                    | `snapshot.go`, `snapshot_sqlite.go`                                        | Remove allowlists and the 2 MiB cap. Exclude only regenerable dependency and build folders (section 4.3). Large files are chunked with explicit limits; anything over a limit produces a visible warning and a choice, never a silent skip                                                                                                        |
| F5  | `session_id UUID` rejects native harness IDs (`ses_…`, `20250305_091523_a1b2`, `rollout-…`)                                                                                   | `migrations/028_session_provenance.up.sql`, `internal/store/provenance.go` | New `agent_sessions` table: `id UUID` (internal) plus `(harness, native_id TEXT, origin_machine)` unique key. Provenance and snapshots reference the internal UUID                                                                                                                                                                                      |
| F6  | Only the latest 5 snapshot versions are retained                                                                                                                              | 028 retention note, `PruneOldSnapshots`                                    | Content addressing makes versions cheap. Keep **every version for 90 days, then one per day** (**D10**)                                                                                                                                                                                                                                         |
| F7  | Snapshot blobs live inside Postgres `BYTEA`; not encrypted at the application level (code TODO)                                                                                | 028                                                                        | Move to S3 (the snapshots bucket already exists, ADR-020) with per-project envelope encryption (KMS)                                                                                                                                                                                                                                                    |
| F8  | OpenCode's harvest session ID is the global `"opencode"`, not per-session                                                                                                     | `snapshot_harness.go` (`findSQLiteFile`)                                   | Enumerate real `ses_*` IDs from `opencode.db`                                                                                                                                                                                                                                                                                                           |


### 4.2 Storage design

The model lives **in the cloud only** (Postgres plus S3), which is the source of truth (**D6**). The laptop holds captured data only until the cloud acknowledges it (upload outbox) plus a disposable read cache (section 7.2). The cloud schema is below.

```
agent_sessions(id uuid, project_id, owner_user_id, harness, native_id text,
               origin_machine_id, workspace_root_hint, title, started_at,
               last_active_at, ended_at, visibility, parent_session_id,  -- fork/teleport lineage
               lineage_kind)                                           -- fork | teleport | seeded
session_versions(id, session_id, version, created_at, turn_count,
                 manifest_blob sha256, uploaded_by_user_id,
                 state)                                                -- uploading | complete | transcript_only | failed; immutable once complete
blobs(sha256, project_id, size, chunk_count, kind, verified_at)        -- kind: plain | chunked | secret_envelope
blob bytes: s3://nexus-sessions/{project}/{sha256}                     -- content-addressed, SSE-KMS
session_turns(session_id, idx, role, ts, text_preview, tool_calls jsonb_small, blob_ref)
session_grants(session_id, version_id|null, grantee_user_id|team, granted_by, created_at, revoked_at)
secret_blobs(blob sha256, owner_user_id, kms_key_ref, wrapped_dek, names[])  -- secrets, section 4.4 (D18)
secret_grants(blob, grantee_user_id, granted_by, created_at, revoked_at)     -- explicit value shares
storage_usage(plan_scope, bytes_used, bytes_cap, state)                       -- D19: ok | warn | full
```

**Manifest** (JSON, itself a blob):

```json
{
  "harness": "claude", "native_id": "…", "harness_version": "2.1.230",
  "source": { "os": "windows", "home": "C:\\Users\\alice", "workspace_root": "D:\\central-memory",
              "user": "alice", "machine_id": "…" },
  "transcript": { "format": "claude-jsonl", "blob": "sha256:…" },
  "harness_state": [ { "rel": "projects/<enc>/<id>.jsonl", "blob": "sha256:…" },
                     { "rel": "todos/<id>.json", "blob": "sha256:…" } ],
  "git": { "remote": "git@github.com:org/repo.git", "root_commit": "…",
           "base_commit": "…", "branch": "feat/x", "bundle": "sha256:…|null" },
  "files": [ { "path": "internal/x.go", "state": "modified", "blob": "sha256:…", "mode": "0644" },
             { "path": "README.md", "state": "unchanged", "blob": "sha256:…", "mode": "0644" },
             { "path": "assets/video.mp4", "state": "added", "blob": "sha256:…", "chunked": true },
             { "path": "${HOME}/.config/tool/settings.json", "state": "read", "blob": "sha256:…" },
             { "path": "old.go", "state": "deleted" } ],
  "secrets": [ { "path": ".env", "mode": "envelope", "blob": "sha256:…", "names": ["DATABASE_URL", "STRIPE_KEY"] } ],
  "excluded": [ { "path": "node_modules/", "reason": "regenerable", "rule": "default:node" } ],
  "rebuild": [ { "dir": ".", "lockfile": "pnpm-lock.yaml", "command": "pnpm install --frozen-lockfile",
                 "tool": "pnpm 9.12.0", "runtime": "node 22.11.0" } ],
  "warnings": [ { "path": "data/dump.sql", "reason": "over 1 GiB soft limit; included after user choice" } ],
  "redaction": { "rules_version": 3, "hits": 2 }
}
```

The manifest contains no `skipped` list. An entry either has a blob that is uploaded, or it's in `excluded` (regenerable, with a `rebuild` recipe) or `secrets` (section 4.4). Nothing a manifest points to can be missing.

**Upload (two-phase, D8):**
1. The engine creates the version in state `uploading` and asks which hashes are missing (`POST /v1/blobs/missing`).
2. It uploads only those through presigned S3 PUTs with `x-amz-checksum-sha256`, so S3 rejects corrupt bytes. Files over 64 MiB are split into 16 MiB content-addressed chunks, uploaded resumably (S3 multipart).
3. `POST /v1/agent-sessions/{sid}/versions/{v}/complete` makes the server check that **every** blob and chunk the manifest references exists with a matching hash. Only then does the version become `complete`. Versions still `uploading` are never listed, shared, or restorable. Missing blobs return a list for the engine to re-upload.

### 4.3 Complete file capture (D8)
**What is captured for each session version:**
- **The full working tree:** every tracked file (modified or not), every untracked file, and ignored files that are *not* regenerable (for example `config.local.json`, fixtures, local data). Unchanged files cost nothing after the first capture of a repo because blobs are deduplicated per project.
- **Every file the session references or touches** (read, written, created, deleted, according to provenance), including files outside the repo, such as `${HOME}/.config/...`, stored with tokenised paths. Files outside the user's home or the workspace (system files) are recorded as a path plus hash only, marked `external`, and never restored.
- **Git state:** `base_commit`, branch, remote, and a `git bundle` of every commit not reachable from the remote (a full bundle if there's no remote). The stash and other branches are out of scope.
- **Harness state:** transcript and the harness's own session files (section 9.5 re-keys them on restore).
- **Git LFS:** the pointer plus the LFS object if it exists locally; otherwise a warning.

**Excluded (regenerable only):** a folder is excluded when it matches the default list **and** a lockfile or manifest that can regenerate it exists, or when it matches a dependency pattern in `.gitignore`. Default list: `node_modules`, `bower_components`, `.pnpm-store`, `vendor` (only with `go.mod` plus `vendor/modules.txt`, `composer.lock`, or `Gemfile.lock`), `.venv`/`venv`/`__pycache__`/`*.pyc`, `target` (with `Cargo.toml` or `pom.xml`), `dist`, `build`, `out`, `.next`, `.nuxt`, `.turbo`, `.gradle`, `bin`/`obj` (with `*.csproj`), `Pods` (with `Podfile.lock`), `DerivedData`, `.cache`, `coverage`, `.pytest_cache`. **User override** per project in Settings, or in a committed `.nexus/capture.toml` with `include` and `exclude` globs. If a dependency folder has no lockfile, it is uploaded, not excluded.

**Rebuild on restore:** the manifest records lockfiles (already uploaded as tracked files), the detected install command (`npm ci`, `pnpm install --frozen-lockfile`, `yarn install --immutable`, `go mod download`, `uv sync`, `pip install -r`, `poetry install`, `cargo fetch`, `bundle install`, `dotnet restore`, `pod install`), and tool and runtime versions. Prepare shows these commands, runs them after the user confirms, and reports failures. A missing runtime becomes a warning ("Node 22 is needed; you have 20").

**Size limits (D19):**
| Limit | Default | Behaviour |
|---|---|---|
| Chunking threshold | 64 MiB per file | Chunked, resumable upload |
| Warn per file | 1 GiB | Upload paused for that file; the user picks *Include* or *Exclude this file* (remembered per path). The session version waits in `uploading` and the Agents page shows why |
| Hard limit per file | 10 GiB | Not uploaded. The version can't complete until the user excludes the file, which records it as a user exclusion with a warning shown at restore |
| Warn per version (new bytes) | 5 GiB | Warning and confirmation, same as above |

Nothing is ever skipped silently: every exclusion is either a regenerable rule or a user decision, both of which appear in the manifest and the restore plan.

**Storage caps per plan (D19, proposed numbers):**
| Plan | Total storage | Notes |
|---|---|---|
| Free | 5 GiB per user | Enough for a few mid-size repos |
| Pro | 100 GiB per user | |
| Team | 250 GiB per seat, pooled across the org | Admins can see usage per project and per person |

- **What counts:** unique bytes (after dedup and compression) of every blob referenced by a retained version, plus turns. A file shared by 50 versions is counted once. Thinning to one version per day after 90 days (D10) frees space automatically.
- **At 80% and 95%:** a warning in the app, a banner on the web portal, and an email to the owner (to admins for Team).
- **At 100%:** Nexus **stops uploading new file blobs**, but **keeps capturing and uploading transcripts**, summaries, and memory, so the timeline, `session_fetch`, and learnings stay complete.
  - New versions become `transcript_only`: they have no file manifest, so they never reference a missing blob. They're shown with a "Files not saved: storage full" badge and **can't be restored** as files, though Continue still works if the files are on disk.
  - Pending blobs wait in the outbox, within its disk budget (section 7.2), and upload automatically once space is freed or the plan is upgraded. Versions whose blobs all arrive are then upgraded to `complete`.
- **Freeing space:** Settings → Storage lists the largest projects, sessions, and files, with "Delete old versions" and "Turn off capture for this project" (D10).

### 4.4 Secrets (D8)
**Detection:** filename rules (`.env*` except `.env.example`, `*.pem`, `*.key`, `id_rsa*`/`id_ed25519*`, `*.p12`/`*.pfx`, `.npmrc`/`.pypirc` with tokens, `.aws/credentials`, kubeconfigs, `*credentials*.json`, service-account JSON) plus the content scanner (the existing `RedactSecrets` patterns) over every text file being captured. Any file with a hit is treated as a secret file.

**Two capture modes:**
- **Encrypted (envelope):** a random per-file data key encrypts the file on the client (XChaCha20-Poly1305) before upload, so plaintext never goes over the wire or into S3. The data key is wrapped by a **per-user key held in the cloud KMS** (**D18**). The server holds the key material and **can** decrypt when an authorized request needs it.
- **Names only:** a template with the variable names and blank values (`STRIPE_KEY=`), for the recipient to fill in.

**Recommended default:** each detected secret file is uploaded **encrypted, with decrypt rights for the owner only**, and **Teleport recipients get names only**. Values reach a recipient only if the sender ticks *Include values* for that file at send time, which adds that recipient to the file's decrypt grant. Team-wide access to values requires an explicit, separate grant.

**Why this default:**
- It satisfies D6 and D18. The owner can lose a laptop, **sign in on a new one, and restore a working `.env` with no recovery code**. With names only, every restore would break until the owner re-entered every secret.
- Secrets are never stored in plaintext. A bucket or database dump alone doesn't leak them, because the data keys can only be unwrapped through KMS.
- Recipients usually should use their own credentials. Names only is least privilege, and it still tells them exactly what to fill in.
- Sharing values is deliberate, per file, per recipient, and audited.

**Key management and server access (D18):**
- **Keys:** each user gets a per-user key-encryption key in KMS (per-project CMK hierarchy), created at first sign-in. The client asks the secrets service for a fresh data key (KMS `GenerateDataKey`), encrypts locally, and uploads ciphertext plus the wrapped data key.
- **Recovery is sign-in alone:** on a new machine, the signed-in owner requests decryption and the secrets service unwraps the data key through KMS and returns it over TLS to the engine, which decrypts locally. There is no recovery code and no device-held master key.
- **Who can decrypt:** only a dedicated **secrets service** calls KMS unwrap, and only for (1) the **owner** or (2) a **recipient with an explicit value grant** for that file. Project admins, teammates without a grant, guests without a grant, the extraction pipeline, and support staff can't. KMS key policy enforces this: only the secrets service role has `Decrypt`, and every call carries an encryption context `{user, project, blob}` that must match the grant.
- **Every decrypt is audited** (`secret.decrypted` with requester, machine, file, reason) and shown to the owner in Settings → Security. Break-glass access by operators would need a separate, dual-approved role that is also audited and visible to the owner. In v1 no such role exists.
- **The server *can* read secrets.** Customers who need the server to be *unable* to read secrets (or chats and files) use **premium end-to-end encryption (D14)**. There, data keys are wrapped by device-held user keys instead of KMS, and recovery needs a user-held recovery key.

**Never uploaded in any form:** agents' own auth files (`~/.codex/auth.json`, `~/.claude/.credentials.json`, Copilot and Gemini tokens), SSH private keys under `~/.ssh`, OS keychains. Recipients sign in to their own agents.

**Transcripts and tool output:** secret values found in chat text, tool output, and command lines are **redacted irreversibly** (`«redacted:NAME»`) before upload, as today. Only files get the encrypted mode.

**Audit:** `secret.uploaded`, `secret.decrypted` (owner restore, with the machine), `secret.values_shared` (sender, recipient, file), `secret.values_accessed` (recipient restore), and `secret.grant_revoked`. Owners and project admins can see them; admins see metadata, never values.

### 4.5 Access fix (D9, first P0 task)
Today `GET /projects/{id}/snapshots`, `/sessions/{id}/snapshot`, and `/snapshot/download` check project membership only (`authorizeProject` in `internal/server/snapshot_routes.go`). **Fix:** add an owner column and enforce owner-or-grantee on every snapshot, blob, provenance, and handoff read. Existing snapshots become **owner-only**. The owner is backfilled from the uploading machine's registered user; rows with no resolvable owner are hidden from everyone except project admins, who can assign or delete them. This ships before any other P0 work.

### 4.6 Restore guarantees

1. **Exact tree:** after a restore, `git rev-parse HEAD` equals `base_commit`, and every file in `files[]` matches its blob hash (verified). Anything that doesn't match fails loudly.
   - **Preflight:** before writing anything, restore checks that every blob and chunk the manifest references is present on the server (the version must be `complete`) and aborts with a list if not.
   - **Rebuild:** excluded dependency folders are regenerated from `rebuild[]` after the user confirms.
   - **Secrets:** secret files are decrypted for the owner, or written as name-only templates for a recipient without values (section 4.4).
2. **Non-destructive:** a restore never overwrites uncommitted work in the user's checkout. The default target is a new worktree (section 9.4).
3. **Harness-loadable:** after a restore, the harness's own resume command finds the session (verified by a per-harness probe such as `claude -p --resume <id> "noop"` in dry-run form, or checks against the harness's list command).
4. **Idempotent:** restoring the same version twice leaves the same state.

---

## 5. Continue a session

### 5.1 Per-harness resume feasibility

Research date: 2026-09-30, from current vendor docs. "Native" means the vendor documents resuming a session by ID from the command line. "Seeded" means we start a new session in the chosen agent, pre-loaded with a generated context pack plus MCP access to the full raw session. "View-only" means we can show the session but cannot drive the agent.


| Harness                      | Native resume command (interactive / headless)                                                                                | Same user                                          | Cross-developer (Teleport)    | Notes and risks                                                                                                                                                                    |
| ---------------------------- | ----------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------- | ----------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Claude Code**              | `claude --resume <id>` / `claude -p --resume <id> "…" --output-format stream-json`; `--fork-session`                          | **Native**                                         | **Native (fork)**             | JSONL under `~/.claude/projects/<encoded-cwd>/`. Since v2.1.223 it finds an ID in any project. Restore must write under B's encoded path                                           |
| **Codex CLI**                | `codex resume <id>` / `codex exec resume <id> "…" --json`; `codex fork`                                                       | **Native**                                         | **Native (fork)**             | Rollouts under `~/.codex/sessions/YYYY/MM/DD/`. Today's restore target `sessions/nexus-restored/` must be checked against Codex's indexer. `--ephemeral` sessions can't be resumed |
| **Cursor (CLI agent)**       | `agent --resume <chatId>` / `agent -p --resume <chatId> --output-format stream-json`; `agent acp`                             | **Native**                                         | **Native (fork)**             | Applies only to CLI chats (`~/.cursor/chats`)                                                                                                                                      |
| **Cursor (IDE chats)**       | None documented for `agent-transcripts` / `state.vscdb` composers                                                             | **No resume** (D13): restore into IDE history, plus seeded | **No resume**; history restore plus seeded | Most of our harvested Cursor data is IDE chats. History restore: section 5.4 |
| **Gemini CLI**               | `gemini --resume <uuid>` / `gemini -r <uuid> "…"`; `--list-sessions`                                                          | **Native**                                         | **Native (fork)**             | Sessions are keyed per project hash under `~/.gemini/tmp/<hash>/`. B's hash must be recomputed from B's path                                                                       |
| **Antigravity**              | CLI `agy --conversation <id>` / `agy -p … --conversation <id> --output-format stream-json`; IDE has no conversation deep link | **Native** (CLI)                                   | **Seeded**                    | The CLI verifies the conversation against Google's backend, which is tied to A's account, so cross-account resume is not expected to work. **IDE chats: no resume** (D13); same-user history restore (section 5.4) |
| **GitHub Copilot CLI**       | `copilot --resume=<id>`; `--session-id=<uuid>` (≥1.0.51, create-or-resume)                                                    | **Native**                                         | **Native (fork)**             | `~/.copilot/session-state/`. Requires ≥1.0.51 for deterministic IDs. Copilot inside VS Code chat is view-only or seeded                                                            |
| **OpenCode**                 | `opencode -s <ses_id>` / `opencode run -s <ses_id> --format json`; `--fork`                                                   | **Native**                                         | **Native (fork), riskier**    | Global SQLite `opencode.db`. Cross-machine means inserting rows, which is schema-version sensitive. Fall back to seeded if the schema doesn't match                                |
| **Windsurf / Devin Desktop** | No resume by ID. `windsurf://cascade/newChat?folder=&prompt=` only pre-fills a new chat (no auto-run)                         | **No resume** (D13): seeded deep link; history restore is experimental | **No resume**; seeded | Cascade history is in encrypted `.pb` files (section 5.4). Devin Local CLI resume is unverified |
| **Kimi Code**                | `kimi --session <id>` (creates the session if missing) / `kimi -p`                                                            | **Native**                                         | **Native (fork)**             | `~/.kimi/sessions/<id>` plus the cwd map in `~/.kimi/kimi.json`. Our layout searches `~/.kimi-code` too; verify                                                                    |
| **Hermes Agent**             | `hermes --resume <id>` / `hermes -q … --format stream-json`                                                                   | **Native**                                         | **Native (fork), riskier**    | SQLite storage, same risk as OpenCode                                                                                                                                              |
| **Grok CLI**                 | Unverified                                                                                                                    | **Seeded**                                         | **Seeded**                    | Verify in P6                                                                                                                                                                       |
| **Codeium (legacy)**         | None                                                                                                                          | **View-only**                                      | **Seeded** into another agent | Superseded by Windsurf                                                                                                                                                             |
| **Command Code**             | Unverified                                                                                                                    | **Seeded**                                         | **Seeded**                    | Verify in P6                                                                                                                                                                       |
| **cagent (Docker)**          | Unverified                                                                                                                    | **Seeded**                                         | **Seeded**                    | Verify in P6                                                                                                                                                                       |
| **Z Code**                   | Unverified                                                                                                                    | **Seeded**                                         | **Seeded**                    | Verify in P6                                                                                                                                                                       |
| **DeepSeek CLI**             | Unverified                                                                                                                    | **Seeded**                                         | **Seeded**                    | Verify in P6                                                                                                                                                                       |


Sources: code.claude.com/docs (sessions, headless, cli-reference); developers.openai.com/codex/cli/reference; cursor.com/docs/cli; google-gemini/gemini-cli `docs/cli/session-management.md`; antigravity.google/docs/cli (resume, headless); docs.github.com Copilot CLI and copilot-cli issues #3377/#3406; opencode.ai/docs/cli; moonshotai.github.io/kimi-code; hermes-agent.nousresearch.com/docs; Exafunction/codeium #283.

### 5.2 How "Continue" works: two modes, the user picks each time (D2)

**Mode A: Continue here (Nexus chat pane).** Nexus drives the agent in the background and renders a native chat pane in the Session view. There are two ways to drive the agent, chosen per harness:

1. **ACP driver (preferred where available).** The Nexus core spawns the agent in Agent Client Protocol mode (`agent acp` for Cursor; other agents natively or through adapters). ACP streams turns and supports permission requests.
2. **Headless-resume driver (baseline).** Each user message becomes one invocation of the harness's headless resume (`claude -p --resume`, `codex exec resume`, `agent -p --resume`, `gemini -r`, `agy -p --conversation`, `opencode run -s`, `kimi -p`, `hermes -q`) with streaming JSON output. The session on disk is the same one, so opening the agent natively later shows the continuation.

Tool-approval prompts appear as native dialogs, and the approval policy comes from the agent's own config (never "skip permissions" by default). A run can be cancelled (the existing `steer/interrupt` plumbing).

**Mode B: Open in the agent (already resumed).** Nexus launches the agent's own UI on the resumed session:

- **CLI agents:** a new terminal window (Windows Terminal / Terminal.app) running the harness's interactive resume command in the right directory (`claude --resume <id>`, `codex resume <id>`, `agent --resume <id>`, `gemini --resume <id>`, `copilot --resume=<id>`, `opencode -s <id>`, `kimi --session <id>`, `hermes --resume <id>`, `agy --conversation <id>`).
- **IDE-first agents** (Cursor IDE, Windsurf, Antigravity IDE), per **D13**: the Continue menu says plainly **"<Agent> doesn't support resuming a session."** Nexus still (1) restores the full session into the IDE's own chat history where feasible (section 5.4), so it shows up in the IDE's chat list for reading, and (2) offers **"Start a new chat with this context"**: a seeded session through a deep link or pre-filled prompt.

Both modes write to the same native session, so the user can switch between them. The Session view makes the choice explicit every time (no silent default), with "remember for this agent" offered as a preference.

**Seeded session contents:** the session summary, the last N turns verbatim, open threads, files touched with their current diffs, and the instruction "call `session_fetch` for the full history" (so the full raw session is reachable through MCP). Lineage is recorded as `parent_session_id` with `lineage_kind=seeded`.

### 5.3 UX

- The Session view header shows **Continue** (primary). Clicking it asks **where to continue (D2)**: *Here in Nexus* (chat pane) or *Open in Claude Code* (the agent's own app, already resumed). Secondary options: *Fork and continue* · *Continue in another agent…*.
- If the session was captured on another machine, Continue first runs **Prepare** (restore repo state and harness state, section 9), shows a one-screen plan, then starts.
- If the agent isn't installed or its version is too old, the menu explains why and offers alternatives (section 9.6).

### 5.4 Restoring into IDE chat history (D13)
Research date 2026-09-30. None of this is vendor-documented. It comes from community reverse-engineering (cursaves "how Cursor stores chats", Cursor forum recovery threads, dayearleo/windsurf-local-user-data-decryption, the Antigravity Database Manager and Legacy Migrator projects) and from our own harvester code.

| IDE | Where history lives | Write feasibility | Main risks |
|---|---|---|---|
| **Cursor IDE** | `%APPDATA%\Cursor\User\globalStorage\state.vscdb` (macOS `~/Library/Application Support/Cursor/User/...`), table `cursorDiskKV`: `composerData:<id>` (metadata plus ordered `fullConversationHeadersOnly`), `bubbleId:<composerId>:<bubbleId>` (messages), `checkpointId:…`, `composer.content.<hash>`. The sidebar index is `composer.composerHeaders` in the global `ItemTable` (Cursor 3.0+), or `composer.composerData.allComposers` in `workspaceStorage/<hash>/state.vscdb` (2.x) | **Medium.** Plain SQLite with JSON values. Insert the composer and bubbles under a new ID, then add an index entry tagged with B's workspace identifier. The chat is visible and readable; the IDE may or may not let the agent continue it | Format changed at 3.0 (a one-way migration). The payload `_v` versions change. Cursor must be **fully closed** while writing (it caches state in memory). The DB can be tens of GB |
| **Windsurf / Devin Desktop** | `~/.codeium/windsurf/cascade/<id>.pb` (Cascade trajectories, AES-GCM-encrypted protobuf using a key embedded in the language server), plus index state in `User/globalStorage/state.vscdb` | **Low.** Writing needs the vendor's embedded key and private protobuf schemas, which likely conflicts with the vendor's terms | Encryption key and schema can change any release; legal and ToS risk. **v1: seeded only; history restore stays behind an experimental flag, off by default** |
| **Antigravity IDE** | 2.0+: `~/.gemini/antigravity-ide/conversations/<id>.db` (SQLite: `trajectory_meta`, `steps`) plus `brain/<id>/`. Legacy: `~/.gemini/antigravity/conversations/<id>.pb`. The sidebar index is `trajectorySummaries` (protobuf) and `ChatSessionStore.index` in `User/globalStorage/state.vscdb` | **Medium for the same user, low across users.** Write the `.db` and rebuild the index entry. Conversations are also checked against the owner's Google account, so a recipient's copy may not open | Directory and format changed at 2.0. The IDE and language server must be fully stopped. The protobuf index is undocumented |

**Safety rules for every IDE write:**
1. **Version gate:** detect the IDE version and the storage schema version (payload `_v`, table layout). Write only for combinations on a tested allowlist; otherwise fall back to seeded with "History restore not supported for Cursor 3.4 yet".
2. **IDE must be closed:** detect running processes, ask the user to quit, and never write while the IDE runs.
3. **Backup before write:** copy the target DB (or `.pb`/`.db` file) plus its `-wal`/`-shm` to `…\Nexus\ide-backups\<ide>\<timestamp>\`, keeping the last 5. "Undo history restore" puts the backup back.
4. **Additive only:** insert new IDs; never modify or delete existing chats.
5. **Verify:** reopen the DB read-only and check integrity (`PRAGMA integrity_check`) and that the new entry is readable. On failure, roll back from the backup automatically.
6. A CI fixture per supported IDE version, re-run on every IDE release. Breakage turns the feature off for that version remotely.

---

## 6. Desktop information architecture and key screens

### 6.1 Navigation

Left rail (compact, icon plus label): **Timeline** (Home) · **Memory** · **Teleport** (inbox, with badge) · **Agents** · **Settings**. Global `Ctrl+K` search opens sessions, memories, files, and projects.
Sessions have no separate nav entry; they're reached from the Timeline, search, Teleport, and Memory provenance. "Harvest" and "Workspace" are removed as pages (section 8).
**Web portal parity (D4):** the portal gets the same **Timeline** and **Teleport inbox** (preview, Sent, Shared with team), built from the cloud copy. From the web, Continue and Prepare hand off to the desktop through `nexus://session/<id>` or `nexus://teleport/<id>`, since restoring and resuming need the local machine.
**Offline (D6):** a persistent banner across the top of every page: "Offline: showing cached data, read-only. Capture continues (N items waiting to upload)." Actions that change cloud data are disabled with a tooltip, not hidden.
**Sharing (D9, D12, D15):** the Share and Teleport dialogs default to picking **named people**. The other targets are "Everyone in this project", which needs an extra confirmation ("All 14 members will see this chat and its files") and is always live, and "Invite someone outside the project", which creates a guest link for this session only. For people and guests there's a **Live / Point-in-time** choice.

### 6.2 Timeline (Home)

```
┌──────┬──────────────────────────────────────────────────────────────────────────┐
│ ◉ TL │  Timeline                     [Project ▾][Agent ▾][Machine ▾][Person ▾] 🔍│
│ ◇ Mem│ ─────────────────────────────────────────────────────────────────────────│
│ ⇄ Tel│  LIVE                                                                     │
│   (2)│  ● Claude Code · central-memory · desktop-01 · 14m     [Open] [Continue]  │
│ ⚙ Agt│    "Fix snapshot restore to exact commit"  · 18 turns · 6 files · 2 cmds  │
│ ☰ Set│ ─────────────────────────────────────────────────────────────────────────│
│      │  TODAY                                                                    │
│      │  ▣ Codex · nexus-web · laptop · 10:02–10:47                    [Open]     │
│      │    Summary: Migrated dashboard to /v1/timeline; 2 open threads            │
│      │    📄 web/src/api.ts +40 −12   📄 web/src/Home.tsx +8   ⎇ a1b2c3 "feat…"   │
│      │    ◇ 3 facts extracted · 1 promoted to project                            │
│      │  ⇄ Teleport from Priya · Cursor · "Auth refactor"  [Preview] [Continue]   │
│      │  ▣ Gemini CLI · infra · desktop-01 · 09:10–09:31   (show 14 events ▾)     │
│      │  YESTERDAY …                                                              │
└──────┴──────────────────────────────────────────────────────────────────────────┘
```

### 6.3 Session view

```
┌──────────────────────────────────────────────────────────────────────────────────┐
│ ← Timeline   Claude Code · central-memory · desktop-01 · started 10:02 · 42 turns│
│ "Fix snapshot restore"  Continue: [Here in Nexus] [Open in Claude Code] [Teleport…] [⋯] │
├───────────────────────────────────────────────┬──────────────────────────────────┤
│ [Chat] [Files] [Commands] [Memory] [Summary]  │  Context                         │
│                                               │  Repo  central-memory @ 3f9e2a1  │
│  You   Restore should use git_commit…         │  Branch feat/restore-exact       │
│  Claude  I'll read restore.go…                │  Files 6 changed (view diffs)    │
│   ▸ tool: Read internal/daemon/restore.go     │  Memory 4 extracted              │
│   ▸ tool: Edit restore.go  (+22 −5)  [diff]   │  Versions v1…v9  [compare]       │
│   ▸ cmd:  go test ./internal/daemon  ✓        │  Visibility  Private  [change]   │
│  …                                            │  Lineage  forked from #a81 (Priya)│
│ ───────────────────────────────────────────── │                                  │
│  ✎ Message Claude Code (resumes this session) │                                  │
│  [ Send ]   approval: ask   model: default    │                                  │
└───────────────────────────────────────────────┴──────────────────────────────────┘
```

### 6.4 Memory

```
┌──────────────────────────────────────────────────────────────────────────────────┐
│ Memory   [Project knowledge] [Session facts] [Summaries] [Personal] [Team]    🔍  │
│ Project: central-memory ▾     Sort: Relevance ▾   ☐ Show low confidence           │
├──────────────────────────────────────────────┬───────────────────────────────────┤
│ 📌 Daemon never imports internal/store        │ "Daemon never imports             │
│    decision · from 3 sessions · 2d            │  internal/store"                  │
│ ▢ Restore must target git_commit              │ Scope: Project   [Change scope ▾] │
│    decision · Claude · today                  │ Sources:                          │
│ ▢ Use pgx pool, not database/sql              │  ▣ Claude · Sep 28 · turns 12–15  │
│    convention · Codex · 1w                    │  ▣ Codex · Sep 20 · turn 4        │
│                                               │ Files: internal/daemon/*.go       │
│                                               │ [Edit] [Pin] [Forget] [History]   │
└──────────────────────────────────────────────┴───────────────────────────────────┘
```

### 6.5 Teleport inbox and Prepare

```
┌──────────────────────────────────────────────────────────────────────────────────┐
│ Teleport   [Inbox (2)] [Sent] [Shared with team]                                  │
├──────────────────────────────────────────────┬───────────────────────────────────┤
│ ⇄ Priya → you · Cursor CLI · "Auth refactor"  │ Preview (read-only chat, files)   │
│   2h ago · repo nexus-web @ 9c1d… · 5 files   │ Note: "JWT refresh half-done,     │
│ ⇄ Team · Claude · "Flaky test hunt"           │  see TODO in auth.ts"             │
│                                               │ ───────────── Prepare ─────────── │
│                                               │ Repo   ✓ D:\src\nexus-web (clone) │
│                                               │ Commit ✓ fetch 9c1d… from origin  │
│                                               │ Target ○ New worktree (default)   │
│                                               │        ○ This checkout (clean? ✗) │
│                                               │ Agent  ✓ Cursor CLI 2026.09 found │
│                                               │ Paths  /Users/priya/… → D:\src\…  │
│                                               │ Deps   ▸ pnpm install (confirm)   │
│                                               │ Secrets .env: names only, fill 2  │
│                                               │ [Continue here] [Open in Cursor]  │
│                                               │ [Seeded in another agent ▾]       │
└──────────────────────────────────────────────┴───────────────────────────────────┘
```

### 6.6 Agents (replaces Harvest)

A card for each of the 16 harnesses: installed or not, version, capture status (last capture, sessions today, errors), resume capability (from section 5.1), and a link to the per-project capture setting (**D10**; no per-agent switch in v1). IDE-first agents show a "Doesn't support resume; restores into chat history" badge (**D13**).

---

## 7. Native app architecture

### 7.1 Principles

- **The cloud is the source of truth (D6).** Postgres plus S3 behind `/v1` hold every session, version, blob, memory item, summary, grant, and audit event. The desktop, the web portal, the CLI, and agents (through MCP) all read the same cloud data. Nothing is authoritative on a laptop.
- **One integrated app, no daemon, no localhost port.** The Go engine is compiled as a native library and loaded **into the app process**. The app opens **no HTTP server and no TCP listener**. Its only network traffic is outbound HTTPS to `/v1`.
- **The engine captures and uploads; the UI reads from the cloud.** Capture, harvest, provenance, redaction, secret encryption, the upload pipeline, restore, path remap, and agent drivers live in the engine. UI reads go through the engine's cloud client (`nx_call` → HTTPS), which fills a disposable read cache.
- **Local disk is an outbox and a cache, nothing else.** The outbox keeps captured data until the cloud acknowledges it, so capture survives network drops. The cache makes views fast and browsable offline, read-only. Deleting either loses nothing that has already been uploaded.
- **Agents use remote MCP (D7).** There is no local MCP server and no local database for agents to open. An optional stdio-to-HTTPS proxy exists only for agents that can't send auth to a remote server (section 7.5).
- **Local IPC is only for app control**, such as single instance and CLI-to-app commands like "restore this here". It uses a per-user named pipe or Unix socket or XPC, **never TCP**.

**Process model (Windows; macOS is equivalent):**

```
┌──────────────── Nexus.exe (WinUI 3, per user, tray-resident) ──────────────┐
│  UI thread (XAML) ◄── DispatcherQueue ◄── callback shim (C#)               │
│        │ P/Invoke (C ABI: nx_call / nx_subscribe / nx_free)                │
│  nexuscore.dll (Go c-shared, one Go runtime)                               │
│    harvester · interceptor · watcher · provenance · redaction · secrets ·  │
│    uploader · cloud client · restore/teleport · agent drivers · materializer│
│        │ outbox + cache (disposable)            │ HTTPS (all reads/writes) │
└────────┼────────────────────────────────────────┼──────────────────────────┘
         ▼                                        ▼
  %LOCALAPPDATA%\Nexus\outbox\ (spool)     api-nexus… /v1  ◄── SOURCE OF TRUTH
  %LOCALAPPDATA%\Nexus\cache.db (SQLite)   Postgres + S3 · /v1/agent/mcp
                                                  ▲
┌──────────────────────┐   HTTPS (streamable MCP) │
│ Agents (Claude,      │ ─────────────────────────┘
│ Codex, Cursor, …)    │   (Antigravity only: stdio → nexus mcp-proxy → HTTPS)
└──────────────────────┘
┌──────────────────────┐   named pipe \\.\pipe\nexus-<SID> (app control only)
│ nexus.exe CLI        │ ─────────────────────────────────────────────► Nexus.exe
└──────────────────────┘   (reads data from /v1 over HTTPS, like the app)
```

### 7.2 Local disk: upload outbox and read cache only (D6)
Nothing on the laptop is authoritative. The outbox holds data **until the cloud acknowledges it**; the cache holds **copies** of cloud data and can be deleted at any time.


| Concern             | Decision                                                                                                                                                                                                                                                                                                                                                                                                                                                                           |
| ------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Location            | Windows `%LOCALAPPDATA%\Nexus\outbox\` and `cache.db` (user-only ACL). macOS `~/Library/Application Support/Nexus/` (mode 0700) |
| Outbox              | An append-only spool of captured items (turn batches, manifests, blob bytes or chunks, encrypted secret envelopes), each with its sha256. It's indexed by a small SQLite file (`outbox.db`: item id, kind, hash, size, state `pending`, `uploading`, or `acked`, attempts, next_retry). Items are fsynced before being marked captured. They are deleted **only after** the server acknowledges them (and, for versions, after `complete`) |
| Upload              | Runs whenever online: exponential backoff with jitter, resumable chunk uploads, idempotent by hash and by `(harness, native_id, version)`. Order: turns first (so the timeline and memory stay fresh), then manifests and blobs. Progress shows in Agents ("412 MB waiting to upload") |
| Outbox limits       | Default budget 20 GiB or 10% of free disk, whichever is smaller. At 80% the app warns. When full, capture **keeps text (turns) and pauses large-blob capture** with a visible "upload backlog full" error; it never drops data silently |
| Read cache          | `cache.db` (SQLite) plus cached blob files: recently viewed sessions and turns, the last 30 days of timeline, memory search results, and summaries. Least-recently-used eviction with a 2 GiB default. **Disposable:** "Clear cache" in Settings; it's rebuilt from the cloud on demand. Never read by other processes |
| Offline UI          | Banner (section 6.1); cached views are **read-only**; uncached items show "Available when online". Capture continues into the outbox. Memory edits, sharing, and Teleport are disabled. Continue works only for sessions whose files are already restored locally (the agent runs locally, and its MCP calls fail until reconnect) |
| Crash and loss      | Losing the laptop loses only items not yet acknowledged, which are shown in Agents. On a new laptop, sign-in shows the full history from the cloud. Re-harvesting agent files there is deduplicated by hash and native ID |
| Harvest offsets     | Kept in `outbox.db` (replacing `.central-memory/daemon.*.json`). An offset advances only after the corresponding items are in the outbox |
| Driver              | `modernc.org/sqlite` (pure Go) for `outbox.db` and `cache.db`, used only by the engine inside the app process (no cross-process access) |


### 7.3 Go core embedded in-process


| Concern                 | Decision                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| ----------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Build                   | Windows: `go build -buildmode=c-shared -o nexuscore.dll ./cmd/nexuscore` (cgo toolchain: mingw-w64 or `zig cc` in CI). macOS: `-buildmode=c-archive` per architecture, combined with `lipo` into a universal `libnexuscore.a` and linked into the SwiftUI target through a module map                                                                                                                                                                                                                                                                    |
| ABI                     | A small C surface: `nx_init(config_json)`, `nx_call(method, request_json) → response_json`, `nx_subscribe(callback)`, `nx_free(ptr)`, `nx_shutdown()`. Methods and payloads are defined once in an IDL (JSON Schema). The Go dispatcher, the C# wrappers (source-generated `LibraryImport`), and the Swift wrappers are all generated from it                                                                                                                                                                                                            |
| Async                   | `nx_call` is synchronous, and the UI always calls it off the UI thread (`Task.Run` / Swift `Task.detached`). Long operations (restore, Teleport apply, upload backlog, Continue runs) return an `op_id` and report progress as events through the subscription                                                                                                                                                                                                                                                                                                     |
| Callbacks               | Go calls the registered C function pointer **from a Go-owned OS thread**. C#: an `[UnmanagedCallersOnly]` shim copies the payload and posts it to `DispatcherQueue.TryEnqueue`. Swift: copy, then hop to `@MainActor`. Callbacks must never block and never re-enter `nx_call` synchronously (deadlock risk)                                                                                                                                                                                                                                             |
| Memory rules            | Go returns `C.CString` buffers that the caller frees with `nx_free`. C never retains Go pointers (cgo pointer-passing rules). Every export wraps its body in `recover()` and turns panics into error responses                                                                                                                                                                                                                                                                                                                                           |
| One runtime per process | All Go code the app needs goes into **one** library. We never load two Go c-shared libraries into one process. The Go runtime can't be unloaded, so the DLL is loaded once at startup and lives until exit. The CLI and the optional MCP proxy are separate processes with their own runtimes, which is fine                                                                                                                                                                                                                                                               |
| Crash isolation         | Default: in-process. Capture loops run under a supervisor goroutine that restarts a failed module with backoff and reports it in Agents. Unrecoverable Go faults (for example `fatal error: concurrent map writes`, out of memory) would take down the process, so **optional out-of-process mode** exists: the app launches the same core as `nexus-core.exe --pipe` (hidden, child of the app, no port) and speaks the same IDL over the per-user named pipe. It's switchable in Settings and turned on automatically after 2 core crashes in 24 hours |


### 7.4 Background capture without a visible daemon

- **The app itself is the background process.** Closing the window hides it to the tray, and capture keeps running inside the app's embedded core. **Quit** from the tray stops capture, with a clear "Capture paused until Nexus starts" note. There is no separate daemon for the user to install, start, or manage.
- **Start at login:** Windows uses an HKCU `Run` entry (`Nexus.exe --background`, set on first run, toggle in Settings). macOS uses `SMAppService.mainApp` as a login item. `--background` starts without a window.
- **Capture always writes to the outbox first**, then uploads (section 7.2). Network drops, sleep, and captive portals pause uploads, not capture.
- **Catch-up:** agents' transcript files persist on disk. On start, the harvester resumes from its offsets, so sessions that happened while Nexus was closed are captured late but not lost. The exception is agents that rotate or delete logs quickly, which the Agents page flags.
- **Single instance:** a second launch activates the existing instance through the named pipe or XPC (replacing today's `singleinstance_windows.go` approach).

### 7.5 Agent access: remote MCP and CLI (D7, no port)
**Remote MCP is the only MCP.** Agents connect to `https://api-nexus.pratyushes.dev/v1/agent/mcp` (streamable HTTP) with auth and `X-Nexus-Project`. The server hosts all tools: `memory_search`, `memory_write`, `session_summary_get`, `session_fetch` (paged, ACL-checked per D9), and `project_knowledge`. The local stdio `nexus mcp` over a local database is removed. There's nothing local for it to read.

**Auth for agents:**
- The server exposes MCP OAuth (authorization-server metadata plus dynamic client registration). Agents that support it (Claude Code, Cursor, Codex, Gemini CLI, Grok, and others) sign in once in the browser, and no static token lands in a config file.
- Otherwise, the app mints a **per-agent, per-machine, revocable token** scoped to the user's projects (existing agent tokens). It's written as an env-var reference where the agent supports one (Codex `bearer_token_env_var`, Kimi, Grok, Hermes, and cagent `${…}` expansion). As a last resort it goes into the config file as a literal header, with a warning in Agents.
- The Agents page configures each agent's MCP entry in one click and shows the last successful call.

**Which agents need the stdio-to-HTTPS proxy** (research 2026-09-30, from current docs):
| Agent | Remote MCP with auth headers or OAuth | Proxy needed? |
|---|---|---|
| Claude Code, Codex CLI, Cursor, Gemini CLI, GitHub Copilot CLI, OpenCode, Windsurf | Documented (HTTP/SSE `url` plus headers, or OAuth) | No |
| Kimi Code (`kimi mcp add --transport http … --header`), Hermes (`url` plus `headers`), Grok CLI (`--transport http … --header`), cagent (`remote.url` plus `headers`) | Documented | No |
| **Antigravity** (CLI and IDE) | Documented (`serverUrl` plus `headers` in `mcp_config.json`), but google-antigravity/antigravity-cli **issue #25**: headers and OAuth bearer are not sent on `initialize` (CLI 1.0.0, IDE 2.0) | **Yes, until fixed.** Re-test every release |
| Command Code, Z Code, DeepSeek CLI, Codeium (legacy) | Unverified | Verify in P6; proxy if they are stdio-only |

**The proxy (`nexus mcp-proxy`)** is a small Go binary spawned by the agent over stdio. It forwards each JSON-RPC message unchanged to `/v1/agent/mcp` over HTTPS, reading the token from Credential Manager or the Keychain at request time. It has **no database, no cache, no tools of its own, and no listener**. It exists only for agents in the "Yes" rows above.

**Offline:** agents' MCP calls fail with a clear tool error ("Nexus is offline; memory unavailable"). This is an accepted consequence of D7. Agents keep working; they just lack Nexus context until reconnect.

**CLI (`nexus`):** reads and writes cloud data over HTTPS with the user's token, the same way the app does. Capture-related commands (`nexus capture --foreground` for headless machines) use their own outbox. **App-control commands** ("restore this session here", "show window") go to the running app over **native IPC**: on Windows the named pipe `\\.\pipe\nexus-<user SID>`, with a DACL for the current user's SID only and `PIPE_REJECT_REMOTE_CLIENTS`; on macOS a Unix domain socket (0600) or XPC. If the app isn't running, the CLI runs the restore itself.

**Never TCP locally.** This includes sign-in. Today's CLI browser login opens a loopback listener on a random port (`internal/authbrowser/browser_login.go`). It's replaced by a `nexus://auth/callback` redirect handled by the app, or a device-code flow when no app is installed.

### 7.6 Windows (first): WinUI 3


| Concern                  | Decision                                                                                                                                                                                                                                                                                                                                                                                                                         |
| ------------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Stack                    | C# / .NET 8+, Windows App SDK 1.6+, WinUI 3, CommunityToolkit.Mvvm, Mica, Fluent icons                                                                                                                                                                                                                                                                                                                                           |
| Core                     | `nexuscore.dll` loaded once at startup through source-generated P/Invoke (`LibraryImport`). No HTTP server, no port; outbound HTTPS to `/v1` only (sections 7.1–7.5)                                                                                                                                                                                                                                                                                            |
| Cloud auth               | The core runs system-browser OAuth with a `nexus://auth/callback` redirect (no loopback port). The refresh token is stored in **Windows Credential Manager** (DPAPI) by the core. UI code never handles tokens                                                                                                                                                                                                                   |
| Tray                     | App-owned tray icon (WinUIEx `TrayIcon` or H.NotifyIcon): Open · Pause capture · Teleport inbox · Quit. Closing the window hides it to the tray; the window is created on demand                                                                                                                                                                                                                                                 |
| Background and autostart | Section 7.4. HKCU `Run` entry, `--background` flag. **No Windows Service and no helper process by default** (optional out-of-process core: section 7.3)                                                                                                                                                                                                                                                                          |
| Packaging (**D5**)       | **Velopack**, an unpackaged, self-contained WinUI 3 app with a per-user installer (no admin, delta updates). It ships `Nexus.exe`, `nexuscore.dll`, the `nexus.exe` CLI, and `nexus-mcp-proxy.exe` (used only by agents listed in section 7.5) behind a stable shim path. Not MSIX or the Store, because MSIX virtualises `%APPDATA%` writes, which breaks restoring harness state for Windsurf, Copilot, and other agents                                                                  |
| Updater                  | Velopack downloads in the background and applies on quit or relaunch, because **a loaded DLL can't be replaced while the app runs**. Velopack installs into a fresh version directory and the app restarts into it. The CLI shim moves to the new version. Agents' remote MCP configs point to the cloud, so updates never touch them; running proxy processes finish on the old binary. Release channel from the existing `/platform/releases`. Replaces the PowerShell swap in `cmd/nexus-desktop/update.go` |
| Code signing             | Needed for SmartScreen: EV certificate or Azure Trusted Signing (follow-up under D5)                                                                                                                                                                                                                                                                                                                                             |
| Notifications            | App toasts for Teleport received and session summaries (App SDK `AppNotificationManager`, registered for unpackaged use)                                                                                                                                                                                                                                                                                                         |
| Deep links               | `nexus://session/<id>`, `nexus://teleport/<id>`, `nexus://auth/callback`, registered by the installer. Used by web-portal links, CLI output, and sign-in                                                                                                                                                                                                                                                                         |


### 7.7 Contracts

**Core API (in-process, via `nx_call`; also over the named pipe in out-of-process mode).** It replaces the previously planned `/local/v2` HTTP API. **Data methods are thin wrappers over cloud `/v1` calls** that fill the read cache (D6). Only capture, upload, restore, agent drivers, and local git and file access do local work:


| Method                                                                                                                                                    | Purpose                                                                 |
| --------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------- |
| `timeline.list {cursor, project, agent, machine, person, kind, since, q}`                                                                                 | Timeline from `/v1/timeline` (cached; read-only offline)           |
| `events.subscribe` (callback stream)                                                                                                                      | Live timeline (from the cloud WebSocket/SSE channel), run output, Teleport inbox, capture and upload health, online or offline |
| `sessions.get` · `sessions.turns {cursor}` · `sessions.files` · `sessions.operations` · `sessions.memories` · `sessions.summary` · `sessions.versions`    | Session view                                                            |
| `files.diff {session, version, path}` · `files.read`                                                                                                      | Blob-to-blob diff; workspace-scoped read                                |
| `agents.list`                                                                                                                                             | Installed harnesses, versions, capture status, resume capability        |
| `continue.start {session, mode: here or open_in_agent, resume: native, fork, or seeded, agent, prompt}` → `op_id` | Start Continue (**D2**) |
| `runs.message` · `runs.approve` · `runs.cancel`                                                                                                           | Drive a "Here in Nexus" run                                             |
| `memory.search` · `memory.update` · `memory.forget` · `memory.pin` · `memory.scope`                                                                       | Memory actions (no confirm)                                             |
| `sessions.share {people[], team: bool}` · `sessions.unshare` · `sessions.grants` | Person-specific sharing (**D1**, **D9**). `team: true` needs an explicit confirmation |
| `secrets.list {session, version}` · `secrets.include_values {teleport, file, recipient}` · `secrets.restore` | Secret files per section 4.4; every call is audited |
| `uploads.status` · `uploads.retry` · `uploads.resolve_large_file {path, include or exclude}` | Outbox backlog and large-file decisions (sections 4.3, 7.2) |
| `cache.clear` · `net.status` | Disposable cache; online or offline state for the banner |
| `teleport.redaction_preview` · `teleport.send` · `teleport.inbox` · `teleport.sent` · `teleport.prepare` (dry run) · `teleport.apply` · `teleport.revoke` | Teleport                                                                |
| `auth.*` · `projects.*` · `git.*` · `status.get` · `diagnostics.get` · `settings.*` · `agents.configure_mcp` | Account, projects, git, health, one-click remote MCP setup per agent (section 7.5) |


**Cloud `/v1`** (the source of truth for the desktop, CLI, web portal, and agents):
- **Reads:** `GET /v1/timeline` (cursor, filters, ACL-aware) plus a WebSocket or SSE channel; `/v1/agent-sessions[/{sid}[/turns|/files|/operations|/memories|/summary|/versions]]`; `/v1/memory/*`.
- **Capture upload:** `POST /v1/agent-sessions` (upsert by `(harness, native_id)`); `POST /v1/agent-sessions/{sid}/turns` (idempotent batches); `POST /v1/agent-sessions/{sid}/versions` (state `uploading`); `POST /v1/blobs/missing`; presigned PUTs with `x-amz-checksum-sha256` and multipart for chunks; `POST /v1/agent-sessions/{sid}/versions/{v}/complete` (server-side verification of every referenced blob, section 4.2); `GET …/versions/{v}/preflight` for restore.
- **Sharing and Teleport:** `PUT/DELETE /v1/agent-sessions/{sid}/grants/{user}` and `PUT …/grants/team` (explicit); `/v1/teleports` (create, inbox, accept, revoke); presigned downloads only after a grant check.
- **Secrets (D18):** `POST /v1/secrets/data-key` (KMS-generated data key for client-side encryption); `POST /v1/secrets/{blob}/decrypt` (owner or value-grant recipient only, returns the unwrapped data key over TLS, audited); `PUT/DELETE /v1/secrets/{blob}/grants/{user}`; `GET /v1/secrets/audit`.
- **Storage (D19):** `GET /v1/storage/usage` (per user, project, and org); upload endpoints return `507 storage_full` for new file blobs at the cap while turn uploads continue.
- **Audit:** `/v1/audit?resource=`.
- **MCP:** `/v1/agent/mcp` hosts `memory_search`, `memory_write`, `session_summary_get`, `session_fetch`, `project_knowledge` (D7). `/v1/sync/pull` is dropped because there is no local replica to sync.

### 7.8 Migration from today's daemon (removing `:7272`)

The `:7272` localhost API (`internal/daemon/proxy.go`, `cmd/daemon`) is **removed from the target architecture**. Where each piece goes:


| Today                                                                                                                        | Target                                                                                                                                         |
| ---------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------- |
| `/local/healthz`, `/local/status`, `/local/diagnostics`                                                                      | Core `status.get`, `diagnostics.get` (Agents and Settings)                                                                                     |
| `/local/browser-login`, `/local/login` (password form), `/local/logout`                                                      | Core `auth.*` with the `nexus://auth/callback` redirect. The password form is dropped                                                          |
| `/local/git/status`, `/local/git/diff`, `/local/git/log` | Core `git.*` (Session view, Prepare) |
| `/local/file/read`                                                                                                           | Core `files.read` (workspace-scoped)                                                                                                           |
| `/local/workspace`, `/local/workspace/switch`, `/local/workspace/recent`                                                     | Core `projects.*` (Settings → Projects). Capture covers all registered projects, not one "current" workspace                                   |
| `/local/harvest`, `/local/harvest/read`                                                                                      | Core `sessions.*` and `timeline.list` over cloud `/v1` (cached)                                                                                    |
| `/local/snapshots`, `/local/session/restore`                                                                                 | Core `sessions.versions`, `teleport.prepare` and `apply`                                                                                       |
| `/local/file/write`, `/local/command/run` (already blocked)                                                                  | Removed                                                                                                                                        |
| Daemon cloud proxy (`proxy.go`) and `HTTPMemoryStore`                                                                        | Core cloud client (reads) and uploader (outbox)                                                                                                          |
| Background loops: harvester, interceptor, watcher, processor, materializer, snapshot push, workspace heartbeat, steer bridge | Core modules supervised inside the app process                                                                                                 |
| Portal localhost probing (`frontend/src/api/daemon.ts`, `ConnectPage`, `LocalWorkspace`, `AccountMismatchBanner`)            | Removed. The portal reads cloud data (the `/workspaces/{projectId}/local` relay already exists) and opens the desktop through `nexus://` links |


**Steps:**

1. Extract daemon logic from HTTP handlers into an `internal/core` service layer with no `net/http` server dependency (handlers become thin adapters during the transition).
2. Add `internal/outbox` (spool plus `outbox.db`, harvest offsets) and `internal/cache` (disposable `cache.db`), with a one-time importer for existing daemon state (`.central-memory/daemon.*.json`, config, token). Anything the old daemon captured but never pushed is uploaded through the outbox.
3. Add `cmd/nexuscore` (c-shared and c-archive exports), the IDL, and generated C# and Swift bindings.
4. **MCP goes remote (D7):** ship the new tools on `/v1/agent/mcp`, and have the app rewrite each agent's MCP config from `mem mcp` (stdio) to the remote endpoint (or to `nexus mcp-proxy` for the agents in section 7.5). `mem mcp` then becomes an alias for `nexus mcp-proxy` for one release, so old configs keep working, and is then removed. The `nexus` and `mem` CLIs read from the cloud.
5. Ship the WinUI app with the embedded core. On first run it stops any running `nexus-daemon.exe`, removes its autostart entry, imports its state, and drains its unsent data.
6. Freeze `cmd/daemon` and the `:7272` listener. Delete them one release after WinUI general availability. `nexus daemon` (`cmd/nexus/daemon_cmd.go`) becomes `nexus capture --foreground` for headless Linux and CI machines (explicit, no port).

**Existing Fyne app and CLI:**

- The **Fyne app** is frozen at its current release and keeps using the old daemon until the user installs the WinUI app, which replaces it (same install root, migration step 5). It's removed from releases once P2 passes, and `internal/desktopui` plus `internal/localclient` are deleted one release later.
- The **CLI** stays and gets simpler: same commands, backed by cloud `/v1` and the shared core packages. `nexus session restore` becomes `nexus teleport apply` (the old name is kept as an alias).

### 7.9 Web portal parity (D4)

The portal gets the **Timeline** and the **Teleport inbox** (Inbox, Sent, Shared with team, preview of chat and files, send and revoke for uploaded sessions). Both are built on `/v1/timeline` and `/v1/teleports` with the same ACLs as the desktop. Actions that need the local machine (Continue, Prepare, Apply) open the desktop through `nexus://` links. Admin, billing, and org stay on the portal as today.

### 7.10 macOS (second): SwiftUI mirror

- SwiftUI app with `MenuBarExtra` for the tray. `libnexuscore.a` (c-archive, universal) is linked in-process. Login item via `SMAppService.mainApp`. Tokens in the Keychain. Updates via Sparkle. Notarised DMG with a Developer ID. Screens mirror section 6.
- **Not sandboxed:** capture must read `~/.claude`, `~/.codex`, `~/.gemini`, and other agent folders, and restore must write into them and into arbitrary repo folders, which the App Sandbox would block. It uses the hardened runtime. Repos under `~/Documents` or `~/Desktop` trigger macOS privacy (TCC) prompts, which are explained at onboarding.
- IPC for CLI app-control commands uses a Unix socket (0600) or XPC (section 7.5). Data comes from the cloud, as on Windows.
- Go's signal handlers coexist with Swift through the c-archive defaults; this is verified by a crash-reporting test in P5.
- Path remapping (section 9.5) must be proven on Windows↔Mac before this ships.

### 7.11 Risks and mitigations


| Risk                                                                          | Mitigation                                                                                                                                                                                                                                                                                                                  |
| ----------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Cloud outage or no network: the UI and agents' MCP lose live data (D6, D7) | Offline banner; read-only cache; capture continues into the outbox; MCP returns a clear tool error; server availability SLO, multi-AZ Postgres, and S3; status page linked from the banner |
| Data loss before upload (laptop dies with a backlog) | Upload turns first, continuously; Agents shows the unacknowledged backlog; warn when the backlog is more than 24 hours or 1 GiB old; fsync before offsets advance |
| Outbox fills the disk during long offline periods | Budget and 80% warning (section 7.2); turns are kept, large blobs paused with a visible error; never a silent drop |
| A manifest points at a missing or corrupt blob | Two-phase upload: checksummed PUTs plus server-side `complete` verification; restore preflight; periodic server audit that re-hashes a sample of blobs |
| Upload volume and storage cost of full working trees (D8) | Per-project deduplication, so only the first capture of a repo is large; regenerable folders excluded; per-plan storage caps with transcript-only fallback (D19, section 4.3); zstd before upload |
| Remote-MCP auth friction and static tokens in agent config files | MCP OAuth where supported; env-var references otherwise; per-agent, per-machine revocable tokens; Agents page shows token status |
| Antigravity header bug (issue #25) | `nexus mcp-proxy` fallback; re-test every Antigravity release and remove the proxy once it's fixed |
| The server can decrypt secrets (D18) | Client-side encryption before upload; KMS policy lets only the secrets service decrypt, bound to the owner and value grants through the encryption context; every decrypt is audited and visible to the owner; no operator break-glass role in v1; premium E2E (D14) for customers who need zero server access |
| A Go fault in the DLL or library takes down the UI                            | `recover()` at every export; supervised capture goroutines; automatic switch to **out-of-process core** (`nexus-core.exe`, child of the app, named pipe) after repeated crashes; crash reports include a Go stack dump                                                                                                      |
| The updater can't replace a loaded DLL                                        | Velopack applies on quit or relaunch into a new version directory; agent configs point to the cloud, so they're unaffected; old proxy processes drain naturally                                                                                                                                                                              |
| One Go runtime per process; can't unload                                      | A single `nexuscore` library holds all Go code; loaded once; never `FreeLibrary`                                                                                                                                                                                                                                            |
| cgo callback threading                                                        | Callbacks arrive on Go threads and are marshalled to `DispatcherQueue` or `@MainActor`; they never block or re-enter; covered by stress tests                                                                                                                                                                               |
| cgo build toolchain on Windows CI                                             | Pinned mingw-w64 or `zig cc`; reproducible builds; the CLI is built `CGO_ENABLED=0`                                                                                                                                                                                                                                         |
| macOS sandbox and entitlements                                                | Ship outside the sandbox (Developer ID, hardened runtime); document TCC prompts; no Mac App Store in v1                                                                                                                                                                                                                     |
| Local data exposure (the outbox and cache hold chats and files) | User-only ACLs; the outbox is emptied once uploads are acknowledged; the cache is size-capped and clearable; secret files sit in the outbox only as encrypted envelopes (section 4.4); optional cache encryption as a follow-up |
| Capture gaps while the app is quit                                            | Login autostart on by default; catch-up from offsets; Agents page shows "Nexus was not running from X to Y"                                                                                                                                                                                                                 |


### 7.12 Fyne retirement

Section 7.8 covers it: Fyne is frozen now, removed from releases when P2 passes, and its code is deleted one release later.

---

## 8. Harvest and Memory presentation redesign

### 8.1 Why the current version fails

- **Harvest shows the plumbing.** It lists transcript *files and paths* ("agent transcript files under your workspace"). Users think in sessions and agents, not JSONL or `state.vscdb`.
- **Memory is a flat list of keys with lifecycle jargon.** It shows `PROPOSED`/`CONFIRMED`, raw levels (`ephemeral`, `session`, `organization`), and no clear link back to the conversation that produced an item.
- **Two unconnected places.** Harvest (inputs) and Memory (outputs) have no path between them. You can't go from a fact to the chat it came from, or from a chat to what was learned.
- **Confirm adds work without value.** The user has said it makes no sense, and it hides fresh knowledge from agents (search returns CONFIRMED only).
- **Sessions and Teleport are a separate, file-shaped island** ("restore harness transcript + git diff").

### 8.2 What replaces it


| Old                                              | New                                                                                                                             |
| ------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------- |
| Harvest page (file list plus transcript preview) | **Timeline** (sessions as cards) plus the **Session view** (formatted chat with tool calls). Capture health moves to **Agents** |
| Harness status strip                             | **Agents** cards: capture on or off, last capture, errors in plain language ("Can't read Windsurf DB: file locked, retrying")   |
| Memory list with statuses                        | **Memory by stage** tabs; every item shows provenance chips (agent, date, turn range) that open the Session view at that turn   |
| Confirm / Reject                                 | Edit · Pin · Change scope · Forget                                                                                              |
| Sessions page (Teleport snapshots)               | Teleport inbox plus Continue and Teleport buttons on every session                                                              |
| Workspace page                                   | Folded into Settings (project folders and clones) plus "Open in editor" actions                                                 |


Harvesting still happens underneath, as a background pipeline the user sees through its results.

---

## 9. Teleport: cross-developer session handoff

### 9.1 Teleport today vs. Teleport must become

**Today** (code as of `b3365e1`):

- **Capture:** transcript (redacted), uncommitted `git diff HEAD` plus small untracked files (512 KiB cap), and an allowlisted artifacts bundle, pushed to `POST /sessions/{id}/snapshot`. Only the latest 5 versions are kept.
- **Access:** `authorizeProject` only. **Any member of the project can list and download every snapshot** (`GET /projects/{id}/snapshots`, `/snapshot/download`). There is no uploader identity (no user column in `session_snapshots`), no per-session visibility, no audience, no revoke, and no audit.
- **Restore** (`nexus session restore`, desktop Sessions page, web `/app/sessions` "Session snapshots (Teleport)"): checks out the **branch**, runs `git apply` on the diff into the **current checkout**, and writes the transcript to the harness path **computed on the restoring machine**. There's no path remapping inside the transcript and no harness re-keying.
- **Separate "Handoff" feature** (`internal/handoff`, `POST /sessions/{id}/handoff[/accept]`, web Sessions page): builds a package of task, memory snippets, file *names*, and branch state, addressed `to_user`. Packages live in an **in-memory map with a 24-hour TTL** (lost on server restart). They carry **no transcript or code**, require a UUID row in the collaborative `sessions` table (harvested sessions have none), and the recipient must **paste a handoff ID**. There is no inbox.

**In practice, Teleport is a same-user, same-project, partial-fidelity feature.**

**Target:** Teleport = **full-fidelity, cross-developer, ACL-controlled handoff.** A sends a session (live or past, any version) to a specific person, the whole team, or someone outside the project through a link invite (**D12**). B gets it in their inbox, previews the full chat and files, runs Prepare (repo plus paths plus agent), and continues it in the same agent on B's laptop. The existing Handoff package is folded into Teleport as the optional **note, task, and memory snippets** attached to a Teleport.

### 9.2 User experience

1. **A sends.** From the Session view or the timeline, A picks **Teleport…**, then an audience (**D12**): specific people (the default), the whole team (explicit, with confirmation), or a link invite for someone outside the project. A adds an optional note or task and picks **live or point-in-time** (**D15**, section 9.9). A **redaction preview** (section 9.7) lists the files included, regenerable folders excluded, secret files and whether their values are included (names only by default, section 4.4), and personal paths tokenised. A clicks **Send**.
2. **B is notified.** A toast plus a Teleport inbox badge; the session also appears on B's timeline ("⇄ from Priya"). Email or push is optional (existing notifications service).
3. **B previews** without changing anything on disk: the read-only full chat (tool calls included), the file list with diffs against `base_commit`, the summary, and A's note.
4. **B prepares.** B's Nexus core produces a plan: repo match, commit availability, target (new worktree by default), path map, agent availability, and any conflicts. B can adjust it.
5. **B continues.** The native (fork) resume opens in the same agent on B's machine through the desktop chat pane, or through the agent app for IDE agents. Lineage is recorded: B's session is `parent=A's session, lineage_kind=teleport`.
6. **A sees status** in Sent: delivered, previewed, restored, continued. If A's session was "live", A can see that B forked it.

**Cross-developer Teleport always forks.** B gets a new native session ID, so A's and B's histories never interleave. Same-user Teleport (same person, another machine) keeps the ID by default. Forks can be forked again by anyone with access, forming a **fork tree** (**D16**, section 9.10).

### 9.3 Permissions, ACL, and audit

- **Share targets (D12):**
  - **A specific person** in the project. This is the default.
  - **The whole team**, by explicit choice with confirmation.
  - **Someone outside the project, through a link invite.** Proposal: the link grants **guest access scoped to that single session** (chat, files, summary; no other project data, no project memory, no timeline). The guest must sign in; there are no anonymous links. Guest access **expires** (default 7 days, single-use by default) and can be revoked. The guest can optionally click **"Request project membership"**, which a project admin approves. Guest seats and billing are an implementation detail for P4.
- **Visibility model (D1, D9):** each session is private (owner only), plus a set of **grants**. A grant goes to one user (`people`), a guest (a `guest`, scoped to a single session), or `team` (all project members, only when explicitly chosen). **Only grantees see the session, its chat, and its files.** Being a project member gives no access to other members' sessions. Sending a Teleport creates grants for its audience on that version, or on all versions when live (section 9.9).
- **Migration (first P0 task, section 4.5):** every existing snapshot becomes owner-only. There is no "share with team?" nudge, since D9 makes team-wide sharing a deliberate act.
- **Server enforcement:** every session, version, blob, turn, summary, provenance, and `session_fetch` (MCP) read checks owner or grant, not project membership. Blob downloads use short-lived presigned URLs issued only after the check. Secret-file values need a separate per-recipient key share (section 4.4).
- **Revoke:** A (or a project admin) revokes a grant. Future fetches fail, pending inbox items disappear, and B's Nexus engine purges its cached (un-restored) copy the next time it reaches the cloud. **Content B has already restored into a worktree cannot be clawed back**; the UI says so at send and at revoke time.
- **Audit log** (`audit_events`, append-only): `teleport.created`, `teleport.previewed`, `teleport.prepared`, `teleport.restored`, `teleport.continued`, `teleport.revoked`, `session.grant_added`, `session.grant_revoked`, `session.shared_with_team`, `guest.invited`, `guest.accessed`, `blob.downloaded`, the `secret.*` events (section 4.4), and `memory.promoted` / `memory.removed_by_owner` (D11). Each records actor, machine ID, IP, and timestamp. Visible to A (their sessions) and project admins; exportable from the web portal.
- **Memory derived from any session** (Teleported or not) is promoted into project knowledge automatically, after redaction (**D11**, section 2.4). The chat and files stay restricted to grantees.

### 9.4 Repo alignment

1. **Match the repo.** The project identity (origin URL plus root commit) is in the manifest. B's Nexus core looks for a matching clone among registered workspaces and common roots. If there's none: "Clone nexus-web to… [choose folder]" (git clone through B's own credentials).
2. **Get the exact commit.** `git fetch origin <base_commit>`. If the commit isn't on the remote (A never pushed), use the `**git bundle`** from the manifest (`git fetch <bundle>`). If neither works, Prepare fails with "Ask Priya to push feat/x or re-send with a bundle".
3. **Choose the target:**
  - **Default: a new worktree** at `<clone>/../<repo>.teleport/<short-id>` on branch `teleport/<sender>/<slug>` at `base_commit`. B's current checkout and uncommitted work are never touched.
  - **Optional: this checkout**, allowed only if clean. If it's dirty, Prepare offers "Stash my changes (named stash) and apply" or "Use a worktree".
4. **Apply file state.** First run the preflight (every referenced blob present, section 4.6). Then write each `files[]` blob (full content, not a patch; the full working tree per D8) and apply deletions and renames. **In a fresh worktree at the exact commit this cannot conflict.** Hashes are verified.
5. **Rebuild and secrets.** Run the recorded install commands after B confirms (section 4.3). Write secret files as name-only templates, or decrypt them if A included values for B (section 4.4). Prepare lists the variables B must fill in.
6. **Later reconciliation** is plain git. B commits in the worktree and merges or rebases normally. Nexus shows "3 files differ from your main checkout" but never auto-merges.

### 9.5 Path remapping (users, home folders, clone paths, operating systems)

**Recorded at capture:** `source.os`, `home`, `workspace_root`, `user`, path separator, and the harness's own encoded project key.

**Computed at Prepare:** a remap table, shown to B and editable:


| Token          | A (source)                    | B (target)                       |
| -------------- | ----------------------------- | -------------------------------- |
| `${WORKSPACE}` | `/Users/priya/code/nexus-web` | `D:\src\nexus-web.teleport\a81f` |
| `${HOME}`      | `/Users/priya`                | `C:\Users\pratyush`              |
| `${USER}`      | `priya`                       | `pratyush`                       |


**Rewrite rules:**

1. **Structured fields first.** Per-harness parsers rewrite known path fields (Claude `cwd`, tool `file_path`, Codex `session_meta.cwd`, Gemini `projectHash`/`cwd`, OpenCode `session.directory`, Copilot `workspace`). Rewritten paths are converted to the target OS format (separators, drive letter) and normalised to repo-relative form wherever the field allows.
2. **Free text second.** Inside message text and tool output, boundary-aware replacement of the source prefixes (longest first). Separators are **not** rewritten in free text; the display keeps the original meaning.
3. **Harness re-keying.** Re-derive the storage key on B's machine: Claude `~/.claude/projects/<encode(B.workspace)>/<newId>.jsonl`; Gemini `~/.gemini/tmp/<sha256(B.workspace)>/chats/`; Cursor `~/.cursor/projects/<slug(B.workspace)>` or CLI `~/.cursor/chats/<hash>/<newId>`; Kimi `kimi.json` cwd map; Codex a new dated rollout file with a new ID; Copilot `session-state/<newId>`; OpenCode and Hermes new rows in the SQLite store (schema checked first).
4. **Sanitisation at capture** already replaces A's `home` and `user` with tokens in the uploaded copy (section 9.7), so A's personal paths never reach the server in plain form. B's Nexus core expands the tokens to B's values.
5. **Verification:** after writing, run the harness's list or resume probe. If the harness can't load the session, fall back automatically to a seeded session and explain why.

### 9.6 Agent availability on B's machine

- B's Nexus core reports installed harnesses and versions (`agents.list`). Prepare checks a **minimum version per harness** (for example Copilot ≥ 1.0.51 for `--session-id`, Claude ≥ 2.1.223 for cross-directory resume) and a **transcript-format compatibility** check.
- **Missing or too old:** "Install Claude Code" (opens the vendor page), **or** "Continue in another agent B has" as a **seeded** session (summary, last N turns, file state, and MCP `session_fetch` for the rest).
- **Account-bound harnesses** (Antigravity, whose conversations are checked against A's account; IDE-bound Cursor and Windsurf chats) are always seeded for cross-developer Teleport.
- Model or provider differences (A used a model B doesn't have access to) are shown as a warning; B's default model is used.

### 9.7 Secrets, redaction, and privacy

- **At capture (A's machine, before upload):** the existing `RedactSecrets` pass on the transcript and diff is extended to tool outputs, command lines, env dumps, and harness state files. Values of A's environment variables whose names match `*_KEY`, `*_TOKEN`, `*_SECRET`, `PASSWORD`, `AWS_*`, and similar patterns are replaced with `«redacted:NAME»`.
- **Secret files (D8):** `.env*`, keys, and credential files in the workspace are handled as in section 4.4. They're encrypted client-side for the owner, and recipients get names only unless A includes values per file. Agents' own auth files (for example `~/.codex/auth.json`, `~/.claude/.credentials.json`) and `~/.ssh` keys are **never captured**.
- **Path tokenisation:** A's home, username, and machine name become tokens (section 9.5).
- **Send-time preview (mandatory for cross-developer Teleport):** a count of redactions by type, a list of files included and skipped, and the option to **exclude turns** (drop selected turns from the shared version, recorded in the manifest) or **exclude files**.
- **In transit and at rest (D14):** TLS, and S3 SSE-KMS with per-project keys, by default. Blobs are never public. **End-to-end encryption** of chats and files (the server can't read them) is **off by default and a paid premium feature**. When on, data keys are wrapped by device-held user keys instead of KMS, recovery needs a user-held recovery key, and server-side extraction for those sessions is disabled or moved to the client. **This is also the only mode in which the server can't read secret files.** On every plan, secret files are encrypted on the client before upload. By default the key sits in KMS, so the owner can recover by signing in (D18).
- **On B's machine:** Teleport previews live only in the disposable read cache (section 7.2), which is purged on revoke. Restored files live in the worktree B chose.
- **Server-side scan:** a second secret scan runs on upload. A hit blocks sharing (but still allows private storage) until A reviews it.

### 9.8 Gaps in the current code


| Requirement                       | Current state                                                    | Gap                                                                                                                                            |
| --------------------------------- | ---------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------- |
| Sender identity                   | `session_snapshots` has no user column                           | Add `owner_user_id` on `agent_sessions`; record the uploader per version                                                                       |
| Per-session visibility            | Project membership only (`authorizeProject`)                     | **First P0 task (D9, section 4.5):** owner-only on all existing snapshot routes. Then a grants table (people, guest, team) checked on every session, blob, turn, summary, and MCP read |
| Guest access (D12)                | None; non-members can't see anything                             | Session-scoped guest grants, expiring invite links, optional membership request |
| Blob completeness (D8)            | Postgres `BYTEA`, silent skips, 2 MiB and 512 KiB caps           | S3 blobs, two-phase `complete`, restore preflight, chunked large files |
| Secrets (D8)                      | Transcript and diff redaction only; `.env` never captured        | Client-side envelope encryption, per-user keys, name-only templates, per-recipient key shares, `secret.*` audit |
| Fork tree (D16)                   | No lineage                                                       | `parent_session_id`, `fork_point_turn`, tree view |
| Addressing and inbox              | Handoff is in-memory, 24-hour TTL, paste-an-ID, no inbox         | Persistent `teleports` table, inbox API, notifications, WebSocket events                                                                       |
| Link between handoff and snapshot | Handoff carries file *names* only                                | A Teleport references `session_version` (the manifest)                                                                                         |
| Harvested sessions as targets     | Handoff and provenance require a UUID `sessions` row             | `agent_sessions` with text `native_id` (F5)                                                                                                    |
| Exact code state                  | Branch checkout plus `git diff HEAD`, 512 KiB cap                | Commit plus file blobs plus bundle (F1, F2)                                                                                                    |
| Non-destructive apply             | Applies into the current checkout                                | Worktree default plus clean check                                                                                                              |
| Path remap                        | None                                                             | Section 9.5 engine plus per-harness re-keying                                                                                                  |
| Agent checks                      | `DetectHarnessForConversation` only checks for existing files    | Installed and version detection plus capability matrix                                                                                         |
| Redaction preview                 | Automatic redaction only, no preview                             | Preview API plus UI, turn and file exclusion                                                                                                   |
| Revoke and audit                  | None                                                             | Grants revoke, `audit_events`                                                                                                                  |
| Membership onboarding             | First resolver claims the project; others need an explicit grant | Invite and approve flow from a Teleport link                                                                                                   |
| Listing scale                     | `ListSnapshotsForProject(…, 100)`                                | Cursor pagination in `/v1/timeline` and `/v1/teleports`                                                                                        |


### 9.9 Live and point-in-time shares (D15)
- **Every share supports both modes.** For any share, one-to-one or guest, the sharer picks **Live** (the recipient sees new turns and versions as the owner keeps working) or **Point-in-time** (a fixed copy of one chosen version). **Team shares are always live.**
- **Live** grants cover all current and future `complete` versions until revoked. The recipient's timeline shows "updated 3 min ago". Preview follows the latest version, while Prepare pins the version it restored from and offers "Update to latest" later.
- **Point-in-time** grants cover exactly one version. Later work by the owner stays private.
- Switching a share from live to point-in-time pins it to the latest version the recipient has seen. Revoke works the same for both modes (section 9.3).

### 9.10 Fork tree and merge (D16, D17)
- **Fork anywhere, repeatedly.** A and B (and anyone with access) can fork any session at any turn, and fork the forks. Each fork is its own session with `parent_session_id`, `fork_point_turn`, `fork_point_version`, and `lineage_kind` (`fork`, `teleport`, or `seeded`). The Session view shows the **fork tree** (who, when, from which turn) and lets you jump between branches.
- **Code:** each fork gets its own git branch (`teleport/<person>/<slug>` or `fork/<person>/<slug>`) in its own worktree, so code state never collides.
- **Merge is code only (D17):**
  - **Code** merges through git: a normal merge or PR between the fork branches. The fork tree offers **"Merge code from…"**, which opens the merge in the user's git tool, or creates a PR on the remote when there is one. It shows the diff and conflicts; Nexus never auto-merges.
  - **Conversations stay separate.** A merge creates **no new session** and never combines or interleaves turns. Each fork keeps its own history and can still be continued.
  - The fork tree marks merged branches ("code merged into `fork/priya/auth` at 3f9e2a1") by watching git, so users can see which conversation's code landed where.

---

## 10. Administration and roles (D20)

Two separate levels, which never overlap: **org administration** (customers managing their own organization) and **platform administration** (the Nexus operator running the service). Neither level can read a member's private sessions, chats, files, or secrets in normal operation.

### 10.1 What exists today (code as of this spec)
| Area | Exists | Where | Problem against this spec |
|---|---|---|---|
| Project roles | `OWNER`, `ADMIN`, `EDITOR`, `VIEWER` plus custom roles with granular permissions (`memory:*`, `branch:*`, `episode:*`, `session:read/write/steer`, `member:invite/manage`) | `migrations/012`, `013_roles`, `internal/store/roles.go`, `authorizePermission` in `internal/server/rbac_routes.go` | Permissions are enforced on only some routes (members, memory writes, harvest, GitHub, export). Snapshot and session routes use `authorizeProject`, which only checks membership. `session:read` means "every session in the project", which contradicts D9 |
| Org roles | `organization_members.role` is `ADMIN` or `MEMBER` only; `organizations.created_by` | `migrations/017_organizations`, `internal/server/org_routes.go` (`authorizeOrgMember`, `authorizeOrgAdmin`) | No `OWNER` or `GUEST` role. **Org ADMINs automatically get full access to every org project** (`IsProjectMember`), so today an org admin can read every member's snapshots |
| Org management API | Create and list orgs; add, remove, and set the role of members; create org projects; get and set org billing | `/orgs…`, `/orgs/{id}/billing` | No invites by email, no offboarding or ownership transfer, no capture or retention controls, no org audit, no SSO or SCIM |
| Platform super admin | `platform_admins` table plus env bootstrap `PLATFORM_ADMIN_USERNAMES` / `PLATFORM_ADMIN_IDS`; `requirePlatformAdmin`; "cannot revoke the last super admin" guard | `migrations/020_platform`, `internal/server/admin_routes.go` | Uses the **same sign-in as customers, with no MFA or step-up**. Env bootstrap matches by **username**, so a user who registers that username becomes super admin (**being hot-fixed in production now**: bootstrap by user ID only; section 10.3). No audit of admin actions |
| Platform console API | `/admin/overview` (stats, AWS info), `/admin/users` (list, grant or revoke super admin), `/admin/releases` (publish, yank; backs `/platform/releases/latest`, the desktop and CLI update feed), `/admin/subscriptions` (billing overrides) | `admin_routes.go`, `billing_routes.go` | No tenant list, feature flags, usage per tenant, queue health, incidents, suspension, or support-access workflow |
| Portal pages | **AdminPage** ("Platform": Overview, Super Admins, App releases, Subscriptions, Desktop CLI), shown in the side nav when `is_platform_admin`; **OrgPage** (orgs, members, roles, billing); **TeamPage** (project members, roles, GitHub) | `frontend/src/pages/AdminPage.tsx`, `OrgPage.tsx`, `TeamPage.tsx`, `SideNav.tsx` | The super admin console is inside the customer portal. There's no org audit, storage, capture, learnings moderation, or offboarding screen |
| Audit log | None (only ad-hoc `edited_by` on memory history) | n/a | Needed by both levels |
| Queue health | Harvest queue list API, capped at 40 items. It showed "40" whenever the backlog was 40 or more, with a single worker | `internal/server/harvest_queue.go` | Operators need true counts, age, throughput, and per-tenant backlog (section 10.3) |

### 10.2 Org administration (customer side)
**Roles per organization:**
| Role | Who | Summary |
|---|---|---|
| **Owner** | 1 or more (at least one is always required) | Everything an Admin can do, plus billing and plan, deleting the org, SSO and SCIM, legal-hold requests, and managing other Owners |
| **Admin** | Any number | Manages members, projects, settings, moderation, and audit. **No access to private session content** |
| **Member** | Default | Uses Nexus; owns their sessions; shares per D9 and D12 |
| **Guest** | Session-scoped (D12) | Sees only the sessions explicitly shared with them. Not counted as a seat; can't see the member list, projects, or project knowledge |

Project roles (`OWNER`/`ADMIN`/`EDITOR`/`VIEWER`) stay for project settings and memory permissions. **`session:read` is redefined as "sessions you own or have a grant for"**, never all sessions in the project.

**Capabilities:**
| Capability | Owner | Admin | Member |
|---|---|---|---|
| Invite members (email or link), remove members, assign roles (Owner only for Owner role) | ✓ | ✓ | — |
| Create and archive projects; manage project membership | ✓ | ✓ | Project OWNER only |
| **Capture on or off per project (D10)** | ✓ | ✓ | Project OWNER only |
| View storage usage per project and person; delete old versions; see retention (D10, D19) | ✓ | ✓ | Own usage |
| **Team memory moderation (D11):** edit, merge, or remove promoted learnings and Team-scope items; see provenance as "from a private session by Priya", never the session | ✓ | ✓ | Remove learnings promoted from own sessions |
| **Revoke any share in the org** (grants, guest links, team shares), without seeing content | ✓ | ✓ | Own shares |
| View the org audit log (section 10.4) and export it | ✓ | ✓ | Own events |
| Billing, plan, invoices, seat count | ✓ | — | — |
| SSO (SAML/OIDC) and SCIM provisioning (later, Team plan) | ✓ | — | — |
| **Offboarding** (below) | ✓ | ✓ | — |
| Delete the org (with export and a 30-day grace period) | ✓ | — | — |

**Offboarding a departing member:**
1. An Admin clicks **Offboard** and picks a **receiving member**. The departing member's sign-in, agent tokens, and MCP tokens are revoked immediately, and their devices are signed out.
2. **Ownership of their sessions in org projects transfers** to the receiver, with each session's visibility unchanged (private stays private to the new owner). Their shares stay valid unless the Admin revokes them. Encrypted secret files transfer their decrypt grant to the receiver (the transfer is audited, and values are never shown to the Admin).
3. Personal-scope memory (D3) and sessions in personal (non-org) projects are **not** transferred. They remain the departing user's.
4. The departing member gets an email summary of what was transferred.

**Private sessions and admins:**
- By default, org Owners and Admins **cannot open, search, or export** a member's private session content (chat, files, secrets). Server enforcement is the same as for any other non-grantee (section 10.5).
- **The only path is a legal hold or export.** An Owner files a request with a reason and named custodians. It requires **two Owners** (or an Owner plus Nexus compliance review on single-Owner orgs). It exports the named custodians' sessions to an encrypted archive for the stated purpose. **The custodian is notified** (unless a court order in the request says otherwise, which Nexus compliance must verify). Every step is in the org audit log and the custodian's own audit view. Secret values are excluded from exports unless the custodian's decrypt grant is transferred through offboarding.
- **What admins see about private sessions (D21):** only per-member aggregates: session count, storage used, and last-active time (per member, optionally per project). **No titles, summaries, file names, or per-session timestamps**, and no list of individual private sessions. The same aggregates drive the Storage view and the offboarding preview. Shared sessions are visible to an admin only if the admin is a grantee.

### 10.3 Platform administration (super admin, the Nexus operator)
**Where it lives:** a **separate admin console** at `admin.nexus…`, a distinct web app with its own deployment. It is **not** in the customer portal nav and **not** in the desktop app. Access requires:
- a super admin grant **by user ID** (env bootstrap by username is removed). **Hot-fix in progress, ahead of P0 and P7:** the username-bootstrap hole is being closed in production now, so `PLATFORM_ADMIN_*` bootstrap matches user IDs only;
- **phishing-resistant MFA** (passkey or hardware key) at every sign-in, and step-up for sensitive actions;
- short sessions (8 hours), and optionally an IP allowlist or VPN.

Super admin API routes move from `/admin/*` on the customer API to a separate `/ops/v1/*` surface that customer tokens can't call.

**Capabilities:**
| Area | What super admins can do | What they can't do |
|---|---|---|
| **Tenants** | List and search orgs and personal accounts; plan, seats, created date, last activity, storage used vs cap (D19), usage trends | See **project names** (project IDs only, **D22**), session titles, or content. Project names are shown only inside a customer-approved break-glass grant |
| **Users** | Search by email or ID; account state; sign-in methods; devices; force sign-out; reset MFA with verification; grant or revoke super admin (the "last super admin" guard is kept, and every grant needs two super admins) | Sign in as the user (see support access) |
| **Plans and billing** | Plan catalogue; per-tenant overrides (trial extension, seat or storage bump, discount); refunds via the billing provider | — |
| **Feature flags** | Global, per-plan, and per-tenant flags with gradual rollout (for example IDE history restore per IDE version, D13; the Antigravity proxy, D7) | — |
| **Queue and pipeline health** | Harvest and extraction queue: **true queued, running, and failed counts** (not capped lists), oldest job age, throughput per worker, per-tenant backlog, worker count, and retry or drain actions. Also upload backlog errors and blob verification failures (D8) | Read job payloads (the transcript content) |
| **Releases and update channels** | Publish, yank, and promote builds per app (`api`, `pwa`, `cli`, `desktop`) and channel (stable, beta); see the desktop `latest.json` / Velopack feed and `/platform/releases/latest` as served; staged rollout percentage | — |
| **System health and incidents** | API and DB health, S3 and KMS errors, MCP endpoint latency, error rates; create an incident that shows on the status page and as an in-app banner | — |
| **Abuse and suspension** | Suspend or unsuspend a tenant or user (reason required, customer notified); rate-limit; takedown workflow for reported content (review happens only through break-glass, below) | Delete customer data outside the tenant deletion flow |
| **Support access** | Request break-glass access to a specific resource (below) | Get access without customer approval |

**Hard limits for D18 (the server can decrypt secrets):**
1. **No super admin role can call the secrets service's decrypt, ever.** The KMS key policy grants `Decrypt` only to the secrets service's workload role, and that service accepts only end-user tokens of the owner or a value-grant recipient. There is no break-glass for secrets.
2. **No super admin API returns session content** (turns, blobs, summaries, memory text, file names). The ops API is a separate service with **no read permission on the content tables or the sessions S3 bucket** (enforced with separate database roles and bucket policies).
3. **Break-glass support access** is the only way an operator sees customer content:
   - A support engineer opens a request naming the **org, the specific resource** (a session or project), the **scope** (metadata only, or content), the reason, and a duration (at most 72 hours).
   - An **org Owner** (for a personal account, the user) **approves it in the portal**. Approval can be revoked at any time.
   - Access is **read-only**, via a watermarked viewer in the admin console. There is no download, and secrets are always masked. It expires automatically.
   - Every view is logged (who, what, when) in the **customer's audit log**, and the customer gets an email when it starts and ends.
   - "Impersonation" (acting as the user) isn't offered. Support sees what the approved scope allows, as themselves.
4. The ops console's own actions are audited in a platform audit log that customers can't see, **except** break-glass and suspension events, which also appear in the customer's audit log.

### 10.4 Audit log (shared model)
One append-only `audit_events` table, partitioned by month, with tamper evidence (a hash chain per tenant). Rows contain no content, only identifiers and metadata.

`audit_events(id, at, tenant_id, actor_user_id, actor_kind [user|admin|super_admin|support|system|agent], action, resource_kind, resource_id, project_id, ip, device_id, user_agent, reason, request_id, outcome, prev_hash)`

**Logged actions:**
| Group | Actions |
|---|---|
| Sessions and sharing | `session.grant_added/revoked`, `session.shared_with_team`, `session.owner_transferred`, `guest.invited/accessed/expired`, `teleport.*` (section 9.3), `blob.downloaded` |
| Secrets | `secret.uploaded`, `secret.decrypted`, `secret.values_shared`, `secret.grant_revoked`, `secret.grant_transferred` |
| Memory | `memory.promoted`, `memory.removed_by_owner`, `memory.moderated` (admin edit or remove) |
| Org admin | `member.invited/joined/removed/role_changed`, `member.offboarded`, `project.created/archived`, `project.capture_toggled`, `retention.versions_deleted`, `share.revoked_by_admin`, `billing.plan_changed`, `sso.configured`, `legal_hold.requested/approved/exported` |
| Auth | `auth.signed_in/failed`, `token.created/revoked` (agent and MCP tokens), `device.signed_out` |
| Platform (super admin) | `ops.signed_in` (with MFA method), `ops.super_admin_granted/revoked`, `ops.plan_override`, `ops.flag_changed`, `ops.release_published/yanked/promoted`, `ops.tenant_suspended/unsuspended`, `ops.queue_action`, `ops.breakglass_requested/approved/denied/viewed/expired` |

**Who sees what:**
- Members see events about their own sessions and account.
- Org Admins and Owners see all events for their org.
- Super admins see platform events and tenant metadata events, but not org-internal events beyond counts, unless they have a break-glass grant.
- Customers always see break-glass and suspension events on their org.
- Retention: 1 year for Free and Pro, 7 years exportable for Team.

### 10.5 Permission model and enforcement
**Every request is checked in this order, server-side, in one middleware plus a resource check:**
1. **Authentication:** a user token, agent or MCP token (scoped to user and projects), guest token (scoped to sessions), or ops token (separate issuer, MFA claim required; only accepted on `/ops/v1`).
2. **Tenant and org role:** is the caller in the resource's org, and with which role (Owner, Admin, Member, or Guest)? Suspended tenants or users get 403 with a reason.
3. **Project role and permission:** project role → permission set (existing `authorizePermission`), used for project settings, memory writes, and admin actions.
4. **Resource ACL:** for **session content** (sessions, versions, turns, blobs, summaries, provenance, `session_fetch`, secrets), the caller must be the **owner or a grantee**. Org or project role never satisfies this check. The only exceptions are an approved legal-hold export job and a break-glass grant, both time-limited and audited.

**Implementation rules:**
- Handlers declare their policy (`policy: session_content`, `org_admin`, `project_perm:memory:write`, `ops:queue`), and a route table test fails if any route lacks one.
- Deny by default.
- The ops service uses separate DB credentials with no grants on content tables.

**Migration from today's model:**
1. **With P0 task 1 (D9):** stop org ADMINs from inheriting session access through `IsProjectMember`. They keep project-settings access. Also narrow project `session:read` to owner-or-grant, and put `authorizeProject`-only session and snapshot routes behind the resource ACL.
2. Add `OWNER` and `GUEST` to `organization_members.role`. Backfill `organizations.created_by` → `OWNER`. Existing `ADMIN` rows stay `ADMIN`. Personal (non-org) projects get an implicit single-user org, so every resource has a tenant.
3. Create `audit_events` and emit events from existing member, role, billing, release, and super admin handlers first.
4. Split `/admin/*` into the ops service at `/ops/v1/*`. Require passkey MFA for super admins. Username-based bootstrap is already replaced by ID-only bootstrap through the production hot-fix; here it becomes a one-time ID-based bootstrap with no env fallback. Keep the old routes for one release, returning 410 afterwards.
5. Move AdminPage out of the customer portal into the admin console. The customer SideNav no longer shows "Platform".

### 10.6 Screens (wireframe level)
**Org admin (web portal, Settings → Organization; desktop Settings links to the same pages in the browser):**
```
┌ Organization: Acme ─────────────────────────────────────────────────────────┐
│ [Members] [Projects] [Learnings] [Shares] [Storage] [Audit] [Billing] [SSO] │
├─────────────────────────────────────────────────────────────────────────────┤
│ Members (14 · 2 guests)                         [Invite] [Offboard…]        │
│  Priya   priya@acme.dev   Owner    last active 2h    [Role ▾] [⋯]           │
│  Arjun   arjun@acme.dev   Admin    last active 1d    [Role ▾] [⋯]           │
│  Sam     (guest) · 1 session · expires Oct 7          [Revoke]              │
├─────────────────────────────────────────────────────────────────────────────┤
│ Projects   nexus-web  Capture ● On   312 GiB   retention 90d+daily  [⋯]     │
│            infra      Capture ○ Off  —                              [⋯]     │
│ Learnings  "pin pgx to v5.6"  from a private session · Priya  [Edit][Remove]│
│ Shares     Priya → Sam (guest) · live · "Auth refactor"   [Revoke]          │
│ Audit      10:02 Arjun revoked share · 09:40 capture off: infra · [Export]  │
└─────────────────────────────────────────────────────────────────────────────┘
```
- Offboard wizard: pick member → pick receiver → preview (counts only per D21: N sessions, M shares, K secret grants, storage) → confirm.
- The Members and Storage tabs show per-member private-session counts, storage, and last-active time only (D21).
- Legal hold (Owners only): request → second Owner approves → export status.

**Super admin console (`admin.nexus…`, separate app, passkey required):**
```
┌ Nexus Ops ── [Tenants] [Users] [Plans] [Flags] [Queues] [Releases] [Health] [Abuse] [Support] ┐
│ Queues                                                                                       │
│  harvest     queued 1,284  running 1  failed 12  oldest 3h 12m  throughput 40/min  [+worker] │
│  extraction  queued 96     running 4  failed 0   oldest 4m                                   │
│  Top backlog: acme (812) · solo-dev-42 (210)                    [Retry failed] [Drain…]      │
│ Releases   desktop 0.19.2 stable 100% · 0.20.0 beta 10%   latest.json ✓   [Publish] [Yank]   │
│ Support    #481 acme · session a81f · content · 24h · ⏳ awaiting Owner approval             │
│ Health     API p95 180ms · MCP p95 240ms · KMS errors 0 · S3 errors 0 · Incident: none       │
└──────────────────────────────────────────────────────────────────────────────────────────────┘
```
- Tenant detail: plan, seats, storage vs cap, usage graph, flags, suspend (reason required), overrides. It has **no** session list or content.
- Break-glass viewer: watermarked, read-only, with a timer and a banner ("Customer-approved until 14:00; every view is logged to Acme's audit log").

---

## 11. Phased roadmap


| Phase                                                       | Scope                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         | Acceptance criteria                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                            |
| ----------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **P0: Close the leak, then cloud-authoritative, complete capture** (Go and server) | **Task 1 (ships first, D9):** owner-only access on every existing snapshot, download, provenance, and handoff route; owner backfill; existing snapshots become owner-only (section 4.5); **org ADMINs and project `session:read` no longer grant access to others' sessions** (section 10.5, step 1); `audit_events` table created and emitting from member, role, billing, release, and super admin handlers. (Already done ahead of P0 as a production hot-fix: super admin bootstrap matches user IDs only, not usernames.) **Then:** cloud `agent_sessions` (text IDs), `session_versions` with two-phase `complete`, S3 blobs with chunking and checksums, `session_grants`; **`internal/core`** service layer extracted from the daemon's HTTP handlers; **`internal/outbox`** and **`internal/cache`** (D6); **`cmd/nexuscore`** exports plus the IDL and generated C# and Swift bindings; raw turns with `tool_calls`; one provenance pipeline; full working-tree plus touched-file capture with regenerable excludes and `rebuild[]` (D8); secret detection, client-side envelope encryption with KMS-held per-user keys, a secrets service with grant-bound, audited decrypt (D18); per-plan storage metering and caps (D19); **remote MCP tools** on `/v1/agent/mcp` plus MCP OAuth, and `nexus mcp-proxy` (D7); per-project capture switch and retention of every version for 90 days, then one per day (D10); OpenCode per-session IDs; `nexus://auth/callback` sign-in | (a) **Leak closed:** a project member who isn't the owner gets 403 on every existing snapshot list, get, and download route (regression test on today's endpoints). **An org Admin or Owner also gets 403** on a member's private session content. (b) A session with 2 mid-session commits, one 5 MiB untracked file, one 3 GiB file (chunked), an ignored `config.local.json`, and `node_modules` restores to an identical tree on a clean machine. Every file is hash-verified, and `node_modules` is rebuilt from the lockfile. (c) **No manifest ever references a missing blob:** fault injection (killed uploads, dropped chunks, corrupt bytes) never produces a `complete` version, and restore preflight rejects incomplete ones. (d) **Capture continues offline and uploads on reconnect with zero loss:** 2 hours offline with active sessions, then reconnect, and the server's turn and blob counts match the harness files exactly. (e) **Reinstalling on a new laptop and signing in shows the full history,** and the owner can restore a session including its decrypted `.env`. (f) No secret value from a seeded-secret corpus reaches the server in plaintext (checked in the DB, S3, and logs). Every secret decryption shows up in the audit log. (g) Operations are non-empty for Claude, Codex, Cursor, and Copilot sessions that edited files. Non-UUID IDs (`ses_…`) round-trip. (h) Re-uploading an unchanged session uploads 0 blobs. (i) Claude Code, Codex, and Cursor call `memory_search` and `session_fetch` on the remote endpoint. Antigravity works through the proxy. No local MCP server exists. (j) No Nexus process listens on any TCP port (`netstat` check in CI). (k) A project with capture off produces no uploads. (l) **Secrets:** on a new laptop, sign-in alone restores the owner's `.env` without a recovery code; a teammate without a value grant gets 403 from decrypt; every decrypt appears in the owner's audit view. (m) **Storage caps:** a Free account at 5 GiB keeps uploading turns (timeline and `session_fetch` stay current), stops new file blobs with a visible "storage full" state, creates only `transcript_only` versions (never a manifest with missing blobs), and uploads the queued blobs automatically after an upgrade |
| **P1: Timeline and memory (cloud), web Timeline** | Cloud `/v1/timeline` plus a live channel, `/v1/agent-sessions/*`; core `timeline.list` over the cloud with the read cache; session summaries; confirm removed (active on write, search includes new items); pin, scope (Personal / Team, **D3**), and forget; provenance on memory items; **auto-promotion from all sessions with redaction, owner provenance, and owner removal (D11)**; **web portal Timeline (D4)**; **org administration, part 1 (D20):** Owner, Admin, Member roles (Owner backfilled), email invites, role changes, project membership, per-project capture switch, storage usage view, learnings moderation, org audit log viewer and export, billing for Owners                                                                                                                                                                                                                                                                                                                                                                                 | (a) The cloud timeline returns 30 days across 5 projects in <300 ms p95 with cursor paging; cached re-open in <100 ms. (b) A new fact is searchable through remote MCP within 60 s of the turn reaching the cloud. (c) No API or UI surface exposes PROPOSED or CONFIRMED. (d) Every extracted item links to a session and a turn range. (e) The web Timeline shows the same sessions as the desktop, for the same grants. (f) A fact from a private session appears in project knowledge for a teammate, with no link to the session; the owner sees it under "Promoted from your sessions" and can remove it, which takes effect in search within 60 s. (g) Zero secrets or PII from a test corpus survive into promoted facts. (h) An org Admin can invite, change roles, toggle capture, and remove a promoted learning, and each action appears in the org audit log. An Admin opening a member's private session gets 403, and the Admin UI offers no path to it. A Member can't reach any admin endpoint (403). Only an Owner can change billing or add Owners. (i) **D21:** admin APIs and screens return only per-member counts, storage, and last-active time for private sessions. A contract test asserts that no admin response contains a session title, summary, file name, or per-session timestamp                                                                                                                                                                                                                                                                                                                                |
| **P2: WinUI 3 app with embedded core** (Windows)            | Timeline, Session view (read-only), Memory, Agents, Settings; `nexuscore.dll` in-process; tray residency and login autostart (section 7.4); single instance over the named pipe; optional out-of-process core; **Velopack** installer and updater (**D5**); CLI shim; deep links; first-run migration from `nexus-daemon.exe`; Fyne frozen                                                                                                                                                                                                                                                                                                                                                    | (a) **Capture continues offline and uploads on reconnect with zero loss; reinstalling on a new laptop and signing in shows the full history** (D6). Offline, a clear banner appears, cached views are read-only, and data-changing actions are disabled. (b) A fresh Windows 11 per-user install with no admin reaches the timeline in <2 min including sign-in. (c) No console windows and no separate daemon process appear in Task Manager (only `Nexus.exe`, plus `nexus-mcp-proxy.exe` for Antigravity if configured). (d) Closing the window keeps capturing; after reboot, capture resumes at login without opening the window. (e) An update applies on relaunch without a reboot and without breaking agents' remote MCP configs. (e2) Deleting the cache folder loses nothing; the app rebuilds it from the cloud. (f) Every timeline row opens its target. (g) A forced Go panic in a capture module is recovered and shown in Agents; the UI stays up. (h) No TCP listener is opened by the app. (i) Fyne is removed from releases |
| **P3: Continue (both modes, D2)**                           | **Here in Nexus:** headless-resume driver (Claude, Codex, Cursor CLI, Gemini, Copilot, OpenCode, Kimi, Hermes, Antigravity CLI), ACP driver for Cursor, approvals UI. **Open in agent:** resumed launch in a terminal window or the agent's app, deep links for IDE agents. Seeded sessions for the rest; mode picker on every Continue; same-user cross-machine restore; **IDE chat-history restore for Cursor IDE and Antigravity IDE behind version gates and backups (D13); Windsurf experimental and off by default**                                                                                                                                                                                                                                                                                                                      | (a) Continuing a Claude session in the Nexus pane and then running `claude --resume <id>` in a terminal shows the new turns. (b) "Open in agent" launches each native harness already resumed in the right directory. (c) (a) and (b) hold for each native harness in section 5.1 (a matrix test in CI with fixtures). (d) Cursor IDE, Windsurf, and Antigravity IDE clearly show "doesn't support resume" (D13). A restored Cursor IDE session (and Antigravity for the same user) appears in the IDE's chat list on each allowlisted IDE version, with a backup taken first and zero changes to existing chats. Unsupported versions fall back to seeded without writing. Seeded sessions open with the context pack. (e) Tool approvals appear natively; nothing runs with skip-permissions by default. (f) The user is asked which mode on every Continue unless they chose "remember for this agent"                                                                                                                                                                                                                           |
| **P4: Cross-developer Teleport**                            | Person-specific grants UI (**D1**, **D9**; server enforcement shipped in P0), explicit team share, **guest links scoped to a single session (D12)**, **live and point-in-time shares (D15)**, **fork tree (D16)**, secret-value inclusion per recipient (D8), and audit; Teleport create, inbox, prepare, apply, revoke; path remap engine; worktree target; bundle fallback; agent version checks; redaction preview; notifications; Handoff folded into Teleport; **web portal Teleport inbox (D4)**; **org administration, part 2 (D20):** Guest role, the org Shares view with admin revoke, the offboarding wizard (session ownership and secret-grant transfer, token revocation), and the two-Owner legal-hold export with custodian notification                                                                                                                                                                                                                                                                                                                                  | (a) A (Windows, `C:\Users\a\x`) → B (Windows, `D:\src\x`): B continues A's Claude session in Claude with the correct file paths, and HEAD equals A's commit. (b) Same for Mac → Windows (on P5 hardware). (c) B's dirty main checkout is untouched. (d) Revoke before Prepare removes the inbox item and blob access (403). (e) The audit log shows the whole chain. (f) A seeded fallback works when B lacks the agent. (g) Zero secret values are uploaded or stored in plaintext; the server decrypts only for the owner or a value-grant recipient. B gets name-only `.env` templates unless A ticked *Include values*, and every value access is audited. (h) **Person-specific:** when A shares with B, a third member C gets 403 on the session, its turns, its blobs, and `session_fetch`. A team share makes it visible to all members only after confirmation. (i) The web inbox shows the same items as the desktop inbox. (j) A guest link opens only that session for a signed-in outsider, expires on schedule, and a membership request reaches the admin. (k) In a live share, B sees A's new turns within 60 s; in a point-in-time share, B never sees later versions. Team shares are live. (l) A → B fork → A forks again → B forks again renders as a correct tree, each fork on its own branch and worktree. (m) An Admin revokes any share in the org without seeing its content, and the recipient loses access immediately. (n) Offboarding moves every org session to the receiver with visibility unchanged, revokes the departing member's tokens, and never shows secret values to the Admin. (o) A legal-hold export needs two Owners, notifies the custodian, and appears in both audit views                                                                                                                                                    |
| **P5: macOS SwiftUI**                                       | SwiftUI mirror of P2 to P4 with `libnexuscore.a` in-process, login item (`SMAppService`), Unix socket or XPC, Keychain, Sparkle, notarised DMG (not sandboxed)                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                | Parity checklist for screens and actions; capture continues offline and uploads on reconnect with zero loss; reinstalling on a new Mac and signing in shows the full history; offline banner and read-only cache; Mac↔Windows Teleport tests pass                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| **P6: Long tail and polish**                                | Verify or implement resume for Grok, Command Code, cagent, Z Code, DeepSeek; Windsurf and Cursor IDE deep links if vendors expose them; **end-to-end encryption as a paid premium feature (D14)**; **code-only fork merge from the fork tree (D17)**: "Merge code from…" opens the git merge or PR, and the tree shows merged markers; verify remote MCP for Command Code, Z Code, DeepSeek CLI, and Codeium (proxy if needed)                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                | Every harness in section 5.1 has a verified classification and a fixture test. Merging two forks' code creates no new session and leaves both conversations unchanged and continuable. With E2E on, the server can't decrypt a session's chats or files (verified by inspecting storage), and the owner and grantees can still restore it                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                 |

| **P7: Super admin console** (separate app; can start in parallel after P0) | Separate `admin.nexus…` app and `/ops/v1` service with its own DB role (no content grants); passkey MFA and step-up; ID-based bootstrap (the username hole is already hot-fixed in production; P7 removes the env fallback entirely); tenant views show org names and project IDs only, with project names only under break-glass (D22); two-person super admin grants; Tenants, Users, Plans and overrides, Feature flags, **Queues** (true counts, oldest age, throughput, per-tenant backlog, retry and drain), **Releases** (channels, staged rollout, `latest.json` / Velopack feed check), Health and incidents, Abuse and suspension; **break-glass support access** with Owner approval; platform audit log; AdminPage removed from the customer portal | (a) **A super admin can't decrypt any secret:** every ops-role call to the secrets service and KMS `Decrypt` is denied, in an automated test and by KMS policy review. (b) No `/ops/v1` endpoint returns turns, blobs, summaries, memory text, or file names; the ops DB role has no SELECT on content tables (checked in CI). (c) **Break-glass needs customer approval:** without an Owner approval the viewer returns 403. With approval, access is read-only, secrets are masked, it expires on time, and every view appears in the **customer's** audit log and email. (d) Signing in to the console without a passkey fails; customer tokens get 401 on `/ops/v1`. Registering a username that matches a bootstrap entry never grants super admin (regression test for the hot-fix). (d2) **D22:** tenant and project views show org names and project IDs, never project names. Project names appear only inside an approved break-glass grant, and that view is logged in the customer's audit log. (e) The queue view shows the true backlog (for example 1,284 queued, not a capped 40) and the oldest job age. (f) A release promoted to 10% beta reaches only that cohort through the desktop update feed; a yank removes it from `latest.json` within 5 minutes. (g) Suspending a tenant blocks sign-in and API with a reason, and the event appears in the tenant's audit log |

---

## 12. Open questions for the user

**None.** Every question is resolved and recorded in section 0.1: Q1 (→ D3), Q2 (→ D10), Q3 (→ D11), Q4 (→ D1), Q5 (→ D12), Q6 (→ D13), Q7 (→ D2), Q8 (→ D14), Q9 (→ D5), Q10 (→ D15), Q11 (→ D16), Q12 (→ D4), Q13 (→ D18), Q14 (→ D19), Q15 (→ D17), Q16 (→ D21), Q17 (→ D22).

Non-blocking detail to finalise during implementation: the exact per-plan storage numbers in section 4.3 are proposals under D19 and can be tuned with pricing without changing the design.

