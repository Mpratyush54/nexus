# Central Memory — Multiplayer Implementation Plan

## Locked Decisions

| Decision | Answer | Rationale |
|---|---|---|
| **Cloud provider** | AWS (RDS Aurora Serverless v2 + S3 + Secrets Manager) | User preference; Aurora scales to zero for early stage |
| **Deployment topology** | Hybrid — local daemon → cloud Postgres | Preserves local-first philosophy, server is sync/merge point |
| **Platform** | Cross-platform (Windows, macOS, Linux) | Daemon is Go, naturally portable; platform-specific bits behind interface |
| **Memory Processor location** | User's device, background goroutine in daemon | Zero server-side LLM cost; user supplies own API keys; data stays local |
| **Memory Processor consistency** | Designated processor (project owner's daemon) for v1 | Eliminates multi-device divergence; other daemons consume only |
| **Agent model for v1** | Pull + push (MCP live + materializer files) | Materializer shipped early (Phase 4 code live; see #138) |
| **Branching strategy** | Copy-on-write | Cheaper, matches git mental model, supports blame-style lineage |
| **Event store engine** | Postgres append-only table + LISTEN/NOTIFY | No Kafka until real throughput pressure |
| **Project identity priority** | Remote repo URL → git root commit → folder name fallback | Already implemented in [project.go](file:///d:/central-memory/internal/project/project.go) |
| **Backup/vault system** | Dropped (vault shim retained for Phase 1b migration import only) | Product is git + Postgres based; robocopy/restic/vault/azure all removed |
| **Memory search** | Vector search (pgvector) from day 1 + keyword fallback (ILIKE + token overlap, not Postgres FTS) | Memory system exists to feed LLMs; keyword matching is too weak |
| **Memory extraction** | 4-layer passive — transcript harvesting (backbone) + tool interception + instruction file watching + piggyback hints | Agents won't call `memory_write`; conversations on disk are the richest source; no MCP sampling (disrupts user). SQLite/vscdb sources are liveness-only without the sqlite3 CLI (see ADR-033); file watching polls (no fsnotify) |
| **Memory levels** | 5 tiers: Organization → Project → Personal → Session → Ephemeral | Different knowledge has different lifetimes and scopes |
| **Server auth** | stdlib HMAC-SHA256 JWT + PBKDF2 password login (migration 011) | No golang-jwt dependency; passwords verify against users.password_hash (see #133) |

> [!NOTE]
> **Drift log (issue #138)**: this plan is the v1 blueprint; where code
> deliberately differs, the decision rows above (not the prose below) are
> authoritative. Known deltas: §1.1 level CHECK now 5 tiers (migration 009);
> §1.9 dependency list (fsnotify, nhooyr/websocket, golang-jwt) intentionally
> unadopted — stdlib polling/WS/HMAC instead (ADRs 013/033); S3 cold-writer
> deferred (no AWS SDK in go.mod); migrations 006–011 extend the original
> 001–005 list (session FKs, branch upkeep, processor election, ephemeral,
> session expiry, password hash).

---

## User Review Required

> [!IMPORTANT]
> **Complete Removal of Vault & Backup Subsystems**:
> The single-user file-based vault (`~/.central-memory`), robocopy drive mirror, Azure restic push, and Windows-specific installer scripts have been removed. Central Memory is now a multiplayer event-sourced platform backed by AWS Aurora PostgreSQL (`pgvector`) and local Git repositories.

> [!IMPORTANT]
> **Zero-Disruption Passive Memory Extraction**:
> Research discussions, architecture debates, and prompt interactions are captured by the daemon silently tailing local agent conversation logs (Claude JSONL, Cursor/VSCode SQLite, etc.). We do **not** use intrusive MCP sampling or interactive agent queries, ensuring zero workflow interruption for the user.

> [!NOTE]
> **Episodic Bug & Incident History**:
> Bug investigations and resolutions are formally indexed in an `episodes` table with semantic embeddings, linking failure triggers, root causes, fixes, and verification runs so future agents can retrieve exact bug histories.

---

## Current Codebase (Post-Cleanup — HISTORICAL, issue #151)

> Historical snapshot: commit `524342e` captured the v1 single-user CLI at
> cleanup time. The tree has since grown far beyond this (daemon, server,
> store, MCP, phases 1–5+); see Target Directory Structure as built and
> the drift log under Locked Decisions for what landed.

Commit `524342e` snapshots the v1 single-user CLI. The following survived:

```
central-memory/
├── adapters/
│   ├── base.go          # Adapter interface, Artifact struct, Classification enum
│   ├── registry.go      # 14 agent adapter definitions (paths, dirs)
│   └── walk.go          # ClassifyPath(), ProjectOf(), path resolution
├── internal/
│   ├── project/         # Fingerprint(), Leaves(), ForPath(), ResolveLeaf()
│   └── scan/            # NeverPatterns (secret regexes), ShouldSkip()
├── main.go              # Stripped CLI: projects, status (skeleton)
└── go.mod               # central-memory, go 1.26.1, zero deps
```

**Dropped** (preserved in git history): `internal/azure`, `internal/backup`, `internal/deps`, `internal/install`, `internal/vault`, `internal/recall`, `internal/ui`, and all vault/sync/harvest/restore/backup commands from `main.go`.

---

## Target Directory Structure

```
central-memory/
├── cmd/
│   ├── daemon/main.go           # Workspace daemon entrypoint
│   ├── server/main.go           # Central API server entrypoint
│   └── mem/main.go              # CLI client entrypoint
├── adapters/                    # ✅ KEPT — agent registry for materializer
│   ├── base.go
│   ├── registry.go
│   └── walk.go
├── internal/
│   ├── project/                 # ✅ KEPT — identity resolution
│   ├── scan/                    # ✅ KEPT — secret patterns
│   ├── daemon/                  # NEW — local workspace daemon
│   │   ├── daemon.go            #   WebSocket server, heartbeat, registration
│   │   ├── fileops.go           #   READ_FILE, WRITE_FILE (sandboxed)
│   │   ├── gitops.go            #   GIT_STATUS, GIT_DIFF, GIT_LOG
│   │   ├── commands.go          #   RUN_COMMAND (allowlisted)
│   │   ├── interceptor.go       #   Layer 1 — captures tool calls, git ops, file changes
│   │   ├── harvester.go         #   Layer 2 — tails agent conversation files (backbone)
│   │   ├── watcher.go           #   Layer 3 — fsnotify watcher for instruction files
│   │   └── processor.go         #   Memory Processor (background, local LLM, reads all layers)
│   ├── server/                  # NEW — central API server
│   │   ├── server.go            #   HTTP + WebSocket server
│   │   ├── routes.go            #   REST endpoints
│   │   └── ws.go                #   WebSocket hub, fan-out, presence
│   ├── store/                   # NEW — Postgres data access
│   │   ├── db.go                #   Connection pool, migrations
│   │   ├── projects.go          #   Project CRUD + resolver
│   │   ├── workspaces.go        #   Workspace CRUD + heartbeat
│   │   ├── events.go            #   Event append + LISTEN/NOTIFY
│   │   ├── memory.go            #   MemoryItem CRUD + vector search
│   │   ├── episodes.go          #   Episode CRUD + event linking
│   │   ├── sessions.go          #   Session + participants
│   │   ├── branches.go          #   MemoryBranch + CoW resolution (Phase 5)
│   │   ├── users.go             #   User CRUD
│   │   └── agents.go            #   Agent registry
│   ├── mcp/                     # NEW — MCP server for pull-model agents
│   │   └── server.go            #   memory_search, memory_write, episode_search, workspace_info, file_read, file_write
│   ├── context/                 # NEW — Context Builder
│   │   └── builder.go           #   Agent-aware token budgets, level-layered assembly, inference-ready XML output
│   ├── materializer/            # NEW (Phase 4) — push-model agent file generation
│   │   └── materializer.go      #   Watch memory changes → regenerate instruction files
│   └── platform/                # NEW — OS abstraction
│       ├── platform.go          #   Interface: ServiceInstall, ConfigDir, ProjectRoots
│       ├── windows.go           #   schtasks, %APPDATA%, configurable roots
│       ├── darwin.go            #   launchd, ~/Library, configurable roots
│       └── linux.go             #   systemd, ~/.config, configurable roots
├── migrations/                  # NEW — Postgres schema migrations
│   ├── 001_initial.up.sql
│   ├── 001_initial.down.sql
│   ├── 002_events.up.sql
│   ├── 003_sessions.up.sql
│   ├── 004_agents.up.sql
│   └── 005_branches.up.sql
├── web/                         # NEW (Phase 3) — dashboard SPA
├── go.mod
└── go.sum
```

---

## Phase 1 — Single-User Skeleton (Prove the Identity Model)

> **Goal**: One user, one project, one agent. Validate Project → Workspace → Daemon → MCP pipeline end-to-end. Vector search and passive extraction wired from day 1.

### 1.1 Postgres Schema (`migrations/001_initial.up.sql`)

```sql
CREATE EXTENSION IF NOT EXISTS "pgcrypto";
CREATE EXTENSION IF NOT EXISTS "vector";

-- ============================================================
-- USERS
-- ============================================================
CREATE TABLE users (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    username    TEXT UNIQUE NOT NULL,
    email       TEXT UNIQUE,
    settings    JSONB DEFAULT '{}',          -- LLM provider, API key ref, preferences
    created_at  TIMESTAMPTZ DEFAULT now()
);

-- ============================================================
-- PROJECTS
-- ============================================================
CREATE TABLE projects (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    canonical_url   TEXT,                    -- normalized git remote URL
    root_commit     TEXT,                    -- git rev-list --max-parents=0 HEAD
    folder_name     TEXT NOT NULL,           -- fallback: leaf dir name
    display_name    TEXT,
    created_by      UUID REFERENCES users(id),
    created_at      TIMESTAMPTZ DEFAULT now(),
    UNIQUE(canonical_url),
    UNIQUE(root_commit)
);

-- ============================================================
-- WORKSPACES
-- ============================================================
CREATE TABLE workspaces (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id  UUID NOT NULL REFERENCES projects(id),
    user_id     UUID NOT NULL REFERENCES users(id),
    machine_id  TEXT NOT NULL,               -- hostname or hardware UUID
    path        TEXT NOT NULL,               -- absolute local path
    branch      TEXT,                        -- current git branch
    commit_sha  TEXT,                        -- current HEAD
    is_dirty    BOOLEAN DEFAULT false,
    is_online   BOOLEAN DEFAULT false,
    is_designated_processor BOOLEAN DEFAULT false,  -- runs Memory Processor
    last_seen   TIMESTAMPTZ,
    daemon_url  TEXT,                        -- ws://localhost:PORT
    created_at  TIMESTAMPTZ DEFAULT now(),
    UNIQUE(machine_id, path)
);

-- ============================================================
-- MEMORY ITEMS — inference-optimized, vector-searchable
-- ============================================================
CREATE TABLE memory_items (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),

    -- Scope & ownership
    project_id      UUID REFERENCES projects(id),       -- NULL for org-level
    user_id         UUID REFERENCES users(id),           -- set for personal memories
    session_id      UUID,                                -- set for session-scoped
    org_id          UUID,                                -- future: organization table

    -- Identity
    key             TEXT NOT NULL,                        -- machine key: "testing/framework"

    -- LLM-optimized content
    content         TEXT NOT NULL                         -- natural language, 20-500 chars enforced
        CHECK (length(content) >= 20 AND length(content) <= 2000),
    context_snippet TEXT,                                -- 1-2 line provenance: "Decided by Alice during auth refactor"

    -- Classification
    level           TEXT NOT NULL DEFAULT 'project'
        CHECK (level IN ('organization', 'project', 'personal', 'session', 'ephemeral')),
        -- 5th tier added by migration 009 (was 4 tiers at v1 blueprint time)
    scope           TEXT NOT NULL DEFAULT 'fact'
        CHECK (scope IN ('fact', 'preference', 'decision', 'constraint', 'pattern', 'episode_summary')),

    -- Search
    embedding       vector(1536),                        -- for semantic search
    tags            TEXT[],

    -- Inference metadata
    confidence      REAL DEFAULT 1.0                     -- decays over time
        CHECK (confidence >= 0.0 AND confidence <= 1.0),

    -- Lifecycle
    status          TEXT DEFAULT 'PROPOSED'
        CHECK (status IN ('PROPOSED', 'CONFIRMED', 'REJECTED', 'SUPERSEDED')),
    source          TEXT,                                 -- "user:alice", "agent:claude", "processor", "extractor:git_diff"
    source_event_id BIGINT,                              -- links back to originating event
    proposed_by     UUID REFERENCES users(id),
    confirmed_by    UUID REFERENCES users(id),
    superseded_by   UUID REFERENCES memory_items(id),

    -- Usage tracking (for confidence decay + relevance)
    use_count       INTEGER DEFAULT 0,
    last_used_at    TIMESTAMPTZ,

    created_at      TIMESTAMPTZ DEFAULT now(),
    updated_at      TIMESTAMPTZ DEFAULT now()
);

-- Vector similarity index (IVFFlat for <100K items; switch to HNSW at scale)
CREATE INDEX idx_memory_embedding ON memory_items
    USING ivfflat (embedding vector_cosine_ops) WITH (lists = 100);
CREATE INDEX idx_memory_project ON memory_items(project_id);
CREATE INDEX idx_memory_level ON memory_items(level);
CREATE INDEX idx_memory_status ON memory_items(status);
CREATE INDEX idx_memory_tags ON memory_items USING GIN(tags);
CREATE INDEX idx_memory_user ON memory_items(user_id);

-- ============================================================
-- EPISODES — retrievable bug/incident/feature arcs
-- ============================================================
CREATE TABLE episodes (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      UUID NOT NULL REFERENCES projects(id),
    session_id      UUID,                                -- may span sessions

    -- Identity
    title           TEXT NOT NULL,                        -- "Fixed auth timeout on WebSocket upgrade"
    episode_type    TEXT NOT NULL
        CHECK (episode_type IN ('bug_fix', 'feature', 'refactor', 'incident', 'investigation', 'onboarding')),

    -- The story arc
    trigger         TEXT,                                 -- what started it: "ConnectionTimeout in ws.go:142"
    investigation   TEXT,                                 -- what was tried: "Checked pool settings, traced pgx lifecycle"
    root_cause      TEXT,                                 -- why it happened: "pool_max_conn_lifetime too short for long-lived WS"
    resolution      TEXT,                                 -- what fixed it: "Increased to 30m, added health check ping"
    verification    TEXT,                                 -- how we know it's fixed: "Load test passed, 0 timeouts over 2h"

    -- Searchability
    tags            TEXT[],
    embedding       vector(1536),                        -- embed the full narrative for similarity search
    files_involved  TEXT[],                               -- ["internal/server/ws.go", "internal/store/db.go"]
    error_patterns  TEXT[],                               -- ["ConnectionTimeout", "pgx pool exhausted"]

    -- Status
    status          TEXT DEFAULT 'OPEN'
        CHECK (status IN ('OPEN', 'INVESTIGATING', 'RESOLVED', 'WONT_FIX')),

    -- Lifecycle
    opened_at       TIMESTAMPTZ DEFAULT now(),
    resolved_at     TIMESTAMPTZ,
    created_by      UUID REFERENCES users(id),
    resolved_by     UUID REFERENCES users(id)
);

CREATE INDEX idx_episodes_project ON episodes(project_id);
CREATE INDEX idx_episodes_type ON episodes(episode_type);
CREATE INDEX idx_episodes_status ON episodes(status);
CREATE INDEX idx_episodes_embedding ON episodes
    USING ivfflat (embedding vector_cosine_ops) WITH (lists = 50);
CREATE INDEX idx_episodes_errors ON episodes USING GIN(error_patterns);
CREATE INDEX idx_episodes_files ON episodes USING GIN(files_involved);
CREATE INDEX idx_episodes_tags ON episodes USING GIN(tags);

-- ============================================================
-- EPISODE EVENTS — links events to their episode
-- ============================================================
CREATE TABLE episode_events (
    episode_id  UUID NOT NULL REFERENCES episodes(id) ON DELETE CASCADE,
    event_id    BIGINT NOT NULL,                         -- REFERENCES events(id), added in migration 002
    role        TEXT NOT NULL
        CHECK (role IN ('trigger', 'investigation', 'attempt', 'fix', 'verification', 'context')),
    note        TEXT,                                    -- optional annotation
    created_at  TIMESTAMPTZ DEFAULT now(),
    PRIMARY KEY (episode_id, event_id)
);

-- ============================================================
-- WATCHED FILES — for passive extraction from instruction files
-- ============================================================
CREATE TABLE watched_files (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    path            TEXT NOT NULL,                        -- relative to workspace root
    last_hash       TEXT,                                -- SHA256 of last known content
    file_type       TEXT NOT NULL
        CHECK (file_type IN ('claude_md', 'cursorrules', 'copilot_instructions',
                             'windsurfrules', 'custom')),
    created_at      TIMESTAMPTZ DEFAULT now(),
    UNIQUE(workspace_id, path)
);

-- ============================================================
-- TASKS
-- ============================================================
CREATE TABLE tasks (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id  UUID NOT NULL REFERENCES projects(id),
    episode_id  UUID REFERENCES episodes(id),            -- bug fix task links to its episode
    session_id  UUID,
    title       TEXT NOT NULL,
    description TEXT,
    status      TEXT DEFAULT 'OPEN'
        CHECK (status IN ('OPEN', 'IN_PROGRESS', 'DONE', 'BLOCKED')),
    assigned_to UUID,
    created_by  UUID NOT NULL,
    created_at  TIMESTAMPTZ DEFAULT now(),
    updated_at  TIMESTAMPTZ DEFAULT now()
);
```

### 1.2 Project Resolver (`internal/store/projects.go`)

Reuses existing [`project.Fingerprint()`](file:///d:/central-memory/internal/project/project.go) logic:

- Daemon calls `Fingerprint(workspacePath)` → gets `(origin, rootCommit)`
- Normalizes remote URL: `git@github.com:x/y.git` → `github.com/x/y`
- Upserts into `projects` table: match on `canonical_url` first, then `root_commit`, then `folder_name`
- Returns canonical `project_id`

### 1.3 Workspace Daemon (`internal/daemon/`)

| Endpoint | Method | Behavior |
|---|---|---|
| `/register` | POST | Daemon registers with server on startup (project_id, machine_id, path, branch, commit, daemon_url) |
| `/heartbeat` | POST | Every 30s. Updates `last_seen`, `branch`, `commit_sha`, `is_dirty`. Server marks offline after 90s silence |
| `/file/read` | POST `{path}` | Returns file contents. **Sandboxed**: `filepath.Clean()` + `strings.HasPrefix(absPath, workspaceRoot)`. Max 1MB. Rejects paths matching [`scan.NeverPatterns`](file:///d:/central-memory/internal/scan/scan.go) |
| `/file/write` | POST `{path, content}` | Writes file. Same sandbox + secret pattern checks |
| `/git/status` | GET | Returns `git status --porcelain` |
| `/git/diff` | GET `{ref?}` | Returns `git diff` or `git diff <ref>` |
| `/command/run` | POST `{cmd, args}` | **Allowlisted commands only** for v1: `git`, `go test`, `npm test`, `pytest`, `cargo test`. 60s timeout |

**Auth**: On startup, daemon generates a random 32-byte token, writes it to `<workspaceRoot>/.central-memory/daemon.token` (file mode 0600). All requests require `Authorization: Bearer <token>`.

**Interceptor** (`internal/daemon/interceptor.go`): Every tool call through the daemon is silently logged as an event to the server. The agent has no idea this is happening.

| Intercepted action | Event type generated | What's captured |
|---|---|---|
| `file_read` | `FILE_READ` | Path, file size, first 200 chars |
| `file_write` | `FILE_MODIFIED` | Path, diff (before/after), size |
| `command_run` | `COMMAND_EXECUTED` | Command, args, exit code, stdout/stderr (capped 4KB) |
| `git diff` | `GIT_DIFF_VIEWED` | Ref, diff stats |
| `git commit` (detected via heartbeat) | `GIT_COMMITTED` | Commit SHA, message, diff stat |

**File Watcher** (`internal/daemon/watcher.go`): On startup, watches known instruction files:

```go
watchPaths := []struct{ path, fileType string }{
    {"CLAUDE.md", "claude_md"},
    {".cursorrules", "cursorrules"},
    {".github/copilot-instructions.md", "copilot_instructions"},
    {".windsurfrules", "windsurfrules"},
}
```

On change: hash content → compare with `watched_files.last_hash` → if different, generate `INSTRUCTION_FILE_CHANGED` event with the diff. Memory Processor extracts facts from the diff.

**Transcript Harvester** (`internal/daemon/harvester.go`): The backbone of passive extraction. Tails agent conversation files on disk — completely invisible to the user and agent. Uses adapter paths from [registry.go](file:///d:/central-memory/adapters/registry.go) to locate conversations:

```go
// Agent conversation storage locations (from adapters/registry.go)
transcriptSources := []struct {
    agent   string
    dirs    []string          // resolved from adapter definitions
    format  string            // "jsonl", "sqlite", "json"
}{
    {"claude",      {"~/.claude/projects/"},                          "jsonl"},
    {"cursor",      {"~/.cursor/", appData+"/Code/User/workspaceStorage/"}, "sqlite"},
    {"opencode",    {"~/.config/opencode/", "~/.local/share/opencode/"},    "jsonl"},
    {"antigravity", {appData+"/Antigravity/User/workspaceStorage/"},        "sqlite"},
    {"copilot",     {appData+"/Code/User/workspaceStorage/"},               "sqlite"},
}
```

Harvester behavior:
1. On daemon startup, scans adapter directories for conversation files matching the current workspace
2. Watches for new/modified conversation files via `fsnotify`
3. Tails new content (tracks last-read byte offset per file to avoid re-reading)
4. Parses conversation turns: extracts `{speaker, content, timestamp}`
5. Emits `CONVERSATION_TURN` events to the server — one per meaningful turn (skips tool-call noise that Layer 1 already captures)
6. For SQLite/vscdb formats: polls every 30s for new rows (fsnotify doesn't work on SQLite WAL writes)

```
User ↔ Agent conversation (uninterrupted, zero awareness)
         │
         │ Agent writes conversation to disk (automatic)
         ▼
┌─────────────────────────────────┐
│ Transcript Harvester (Layer 2)  │  ← daemon background goroutine
│ Tails conversation files        │     user sees nothing
│ Emits CONVERSATION_TURN events  │     agent sees nothing
└────────────────┬────────────────┘
                 ▼
         Event Stream → Memory Processor
         "They decided on Redis because of pub/sub support"
```

**End-of-Session Deep Analysis**: When the harvester detects no new conversation content for 5+ minutes (or the agent process exits), it triggers a deep analysis pass:
1. Collects the full session transcript (all `CONVERSATION_TURN` events from this session)
2. Sends to Memory Processor as a single batch with a specialized prompt: "This session has ended. Extract all decisions, facts, preferences, and learnings from the full conversation."
3. This is the highest-quality extraction pass — it has full session context, not just incremental turns
4. Runs on user's device, user's LLM key, completely invisible

### 1.4 MCP Server (`internal/mcp/`)

Exposed to pull-model agents (Claude Code, OpenCode) via MCP protocol:

| Tool | Input | Output |
|---|---|---|
| `memory_search` | `{query, tags?, level?, limit?}` | Inference-ready XML context block (see §1.6) + **piggyback reflection hint** (see below) |
| `memory_write` | `{key, content, scope?, level?, tags?, context_snippet?}` | Created memory item ID |
| `memory_reflect` | `{}` | Voluntary — agent can call when finishing a task. Not forced, not required. Returns confirmation. |
| `episode_search` | `{query?, error_pattern?, file?, status?}` | Matching episodes with full arc |
| `episode_report` | `{title, episode_type, trigger, tags?}` | Opens a new episode for an active bug/investigation |
| `workspace_info` | `{}` | `{project, branch, commit, is_dirty, path}` |
| `file_read` | `{path}` | File contents (proxied through daemon) |
| `file_write` | `{path, content}` | Success/failure (proxied through daemon) |

**Piggyback Reflection Hint** (Layer 4 — zero-disruption): Every `memory_search` response includes a small hint in the result metadata. This costs nothing — the agent is already reading the response:

```json
{
  "tool": "memory_search",
  "result": {
    "context": "<project_memory>...</project_memory>",
    "token_count": 892,
    "budget_remaining": 3108,
    "items_included": 6,
    "reflection_hint": "If you've made decisions or learned facts in this session not shown above, call memory_write to record them."
  }
}
```

The agent may or may not act on it — that's fine. The Transcript Harvester (Layer 2) is the safety net that catches everything regardless.

**`memory_reflect` (voluntary)**: An MCP tool the agent CAN call if it wants to, but is never forced. The tool description says: *"Summarize decisions, facts, and preferences from this session. Call when finishing a task or before ending a session."* The agent reports structured memories; the daemon writes them directly as PROPOSED items. Some agents will call it, most won't — Layer 2 handles the rest.

> [!IMPORTANT]
> **No MCP sampling is used.** Sampling requires user approval in most host apps, injects unexpected output, burns context tokens, and disrupts the user's workflow. All extraction happens silently via transcript harvesting on the daemon — the user and agent are never interrupted.

### 1.5 Memory Search — Hybrid Vector + Text

```sql
-- Primary: vector similarity search
SELECT id, key, content, level, scope, confidence, tags, context_snippet,
       1 - (embedding <=> $2) AS similarity
FROM memory_items
WHERE project_id = $1
  AND status = 'CONFIRMED'
  AND confidence > 0.3
ORDER BY embedding <=> $2
LIMIT 20;

-- Secondary: boost exact tag matches and key matches
-- Final: re-rank by (similarity * 0.7) + (tag_match * 0.2) + (recency * 0.1)
-- Apply level-based override resolution
-- Cap to agent's token budget
```

### 1.6 Context Builder — Inference-Ready Output

The Context Builder assembles memories into structured XML that LLMs parse efficiently. Memories are grouped by level, with lower levels overriding higher:

```
Resolution order (lower overrides higher):
  SESSION > PERSONAL > PROJECT > ORGANIZATION
```

Output format:

```xml
<project_memory project="central-memory" branch="main" updated="2026-09-17">

<organization>
  <item key="security/auth" confidence="1.0" scope="constraint">
    All APIs must use JWT authentication. No API key auth.
  </item>
</organization>

<project>
  <item key="testing/framework" confidence="0.95" scope="decision" decided_by="Alice" date="2026-09-10">
    The team uses pytest with fixture-based setup. Integration tests use testcontainers
    for Postgres. Coverage target is 80%.
  </item>
</project>

<personal user="alice">
  <item key="style/error_messages" confidence="0.9" scope="preference">
    Alice prefers detailed error messages with full stack context.
  </item>
</personal>

<session title="Auth refactor">
  <item key="scope/do_not_touch" confidence="1.0" scope="constraint">
    Do not modify payments/ directory during this session.
  </item>
</session>

<recent_episodes>
  <episode type="bug_fix" title="Auth timeout on WebSocket" status="RESOLVED" date="2026-09-15">
    Trigger: ConnectionTimeout in ws.go:142. Root cause: pool_max_conn_lifetime too short.
    Fix: Increased to 30m, added health check ping.
  </episode>
</recent_episodes>

<active_task title="Add WebSocket auth" status="IN_PROGRESS" assigned="claude">
  Implement JWT verification on WebSocket upgrade. Must reject expired tokens.
</active_task>

</project_memory>
```

**Token budget**: Default 4000 chars (~1000 tokens). Sections are filled in priority order — if budget runs out, lower-priority sections are truncated:

```
1. Active task (always included)
2. Session memories
3. Relevant episodes (if current errors match known patterns)
4. Personal memories
5. Project memories (by similarity score)
6. Organization memories
```

### 1.7 Confidence Decay

Memories lose confidence over time if not used or reconfirmed:

```
effective_confidence = base_confidence × 0.95^(days_since_last_used / 30)
```

- Used yesterday: confidence ≈ base
- Last used 90 days ago: confidence drops to ~86% of base
- Last used 180 days ago: drops to ~74%
- Never used in 180 days: flagged for review/archival
- `use_count` tracks how often the Context Builder serves it — high use = important, resets decay

### 1.8 Central Server (`internal/server/`)

Minimal for Phase 1:
- REST API over HTTPS
- Endpoints: `POST /projects/resolve`, `POST /workspaces/register`, `POST /workspaces/heartbeat`, `GET /workspaces/:projectId/active`, `POST /memory`, `GET /memory/search`, `POST /episodes`, `GET /episodes/search`
- Connects to RDS Aurora Serverless v2
- Auth: JWT tokens (simple username/password login for v1)

### 1.9 New Go Dependencies

```
github.com/jackc/pgx/v5          # Postgres driver (pgvector support built-in)
github.com/pgvector/pgvector-go   # vector type for Go
```

> [!NOTE]
> The blueprint also listed `nhooyr.io/websocket`, `golang-jwt/jwt/v5`,
> and `fsnotify` — all intentionally unadopted (stdlib `net/http` WS,
> stdlib HMAC-SHA256 JWT + PBKDF2 login, stdlib polling watcher instead;
> see drift log above and ADRs 013/033). `go.mod` stays at pgx +
> pgvector-go.

### Exit Criterion

An agent (Claude via MCP) can:
1. Call `workspace_info` → gets project name, branch, commit
2. Call `file_read` → reads a file through the daemon (not direct FS) — **and the read is silently logged as an event**
3. Call `memory_write` → stores a fact in Postgres with embedding
4. Call `memory_search` → gets inference-ready XML context block via vector similarity
5. Call `episode_report` → opens a bug episode
6. Call `episode_search` → finds a past bug by error pattern or description

---

## Phase 1b — Migration (Import Existing Vault Data)

> **Goal**: Don't lose the data from the old system.

- Parse existing `memory/global/learnings.md` and `memory/projects/*/MEMORY.md` → insert as CONFIRMED memory items with embeddings
- Parse existing `agents/*/normalized/sessions.jsonl` → insert as historical events
- Run `project.Fingerprint()` on all detected projects → seed `projects` table
- `mem recall` and `mem remember` become thin clients to the server API (backward compatible)

---

## Phase 2 — Event Sourcing + Shared Memory + Passive Extraction

> **Goal**: Alice and Bob in the same project converge on the same confirmed facts. Memory is extracted passively, not just written explicitly.

### 2.1 Event Store (`migrations/002_events.up.sql`)

```sql
CREATE TABLE events (
    id              BIGSERIAL PRIMARY KEY,
    project_id      UUID NOT NULL REFERENCES projects(id),
    session_id      UUID,
    user_id         UUID REFERENCES users(id),
    agent_id        UUID,
    workspace_id    UUID REFERENCES workspaces(id),
    episode_id      UUID REFERENCES episodes(id),        -- links event to episode if part of one
    event_type      TEXT NOT NULL,
    payload         JSONB NOT NULL,
    created_at      TIMESTAMPTZ DEFAULT now()
);

CREATE INDEX idx_events_project_time ON events(project_id, created_at);
CREATE INDEX idx_events_project_type ON events(project_id, event_type);
CREATE INDEX idx_events_episode ON events(episode_id);

-- Add FK to episode_events now that events table exists
ALTER TABLE episode_events
    ADD CONSTRAINT fk_episode_events_event
    FOREIGN KEY (event_id) REFERENCES events(id);

-- Real-time notification
CREATE OR REPLACE FUNCTION notify_event() RETURNS trigger AS $$
BEGIN
    PERFORM pg_notify('events', json_build_object(
        'id', NEW.id,
        'project_id', NEW.project_id,
        'event_type', NEW.event_type
    )::text);
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER events_notify AFTER INSERT ON events
    FOR EACH ROW EXECUTE FUNCTION notify_event();
```

**Event types**:
- Agent activity (Layer 1 — tool interception): `FILE_READ`, `FILE_MODIFIED`, `COMMAND_EXECUTED`, `GIT_COMMITTED`, `GIT_DIFF_VIEWED`
- Conversation (Layer 2 — transcript harvesting): `CONVERSATION_TURN`, `SESSION_TRANSCRIPT_COMPLETE`
- Instruction files (Layer 3 — file watcher): `INSTRUCTION_FILE_CHANGED`
- Explicit actions (Layer 4 — piggyback + voluntary): `MEMORY_PROPOSED`, `MEMORY_CONFIRMED`, `MEMORY_REJECTED`, `MEMORY_SUPERSEDED`
- Tasks: `TASK_CREATED`, `TASK_UPDATED`, `TASK_COMPLETED`
- Episodes: `EPISODE_OPENED`, `EPISODE_UPDATED`, `EPISODE_RESOLVED`
- Lifecycle: `MESSAGE_SENT`, `SESSION_STARTED`, `SESSION_ENDED`, `WORKSPACE_REGISTERED`, `WORKSPACE_OFFLINE`

### 2.2 Four-Layer Passive Extraction Pipeline

The daemon captures knowledge through four independent layers, none of which require agent cooperation or user disruption:

```
┌─────────────────────────────────────────────────────────────────┐
│                    EXTRACTION LAYERS                             │
│                                                                 │
│  Layer 1: TOOL INTERCEPTION (interceptor.go)                    │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │ file_read, file_write, command_run, git ops              │   │
│  │ → captures: code changes, test results, file access      │   │
│  │ → mechanism: daemon logs every tool call as event        │   │
│  └──────────────────────────────────────────────────────────┘   │
│                                                                 │
│  Layer 2: TRANSCRIPT HARVESTING — BACKBONE (harvester.go)       │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │ Tails agent conversation files on disk                   │   │
│  │ Claude: ~/.claude/projects/  Cursor: workspaceStorage/   │   │
│  │ → captures: discussions, decisions, explanations,        │   │
│  │   research, reasoning, corrections, preferences          │   │
│  │ → mechanism: fsnotify + byte-offset tailing + SQLite     │   │
│  │   polling. Zero agent/user awareness.                    │   │
│  │                                                          │   │
│  │ End-of-session deep analysis:                            │   │
│  │   No new content for 5min → full transcript sent to      │   │
│  │   Memory Processor for comprehensive extraction.         │   │
│  │   Highest quality pass — has full conversation context.   │   │
│  └──────────────────────────────────────────────────────────┘   │
│                                                                 │
│  Layer 3: INSTRUCTION FILE WATCHER (watcher.go)                 │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │ CLAUDE.md, .cursorrules, copilot-instructions.md         │   │
│  │ → captures: manually written rules and preferences       │   │
│  │ → mechanism: fsnotify + hash comparison                  │   │
│  └──────────────────────────────────────────────────────────┘   │
│                                                                 │
│  Layer 4: PIGGYBACK HINTS (zero-cost supplementary)             │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │ memory_search responses include reflection hint          │   │
│  │ memory_reflect tool available but never forced            │   │
│  │ → captures: whatever the agent voluntarily reports       │   │
│  │ → mechanism: hint text in MCP response metadata          │   │
│  └──────────────────────────────────────────────────────────┘   │
│                                                                 │
│         ALL FOUR ──────► Event Stream ──────► Memory Processor  │
└─────────────────────────────────────────────────────────────────┘
```

### What Each Layer Catches (Coverage Matrix)

| Scenario | L1: Tool intercept | L2: Transcript harvest | L3: File watcher | L4: Piggyback hint |
|---|---|---|---|---|
| "Let's use Redis" (pure discussion) | ❌ | ✅ backbone | ❌ | ✅ maybe |
| Agent explains trade-offs | ❌ | ✅ backbone | ❌ | ❌ |
| User says "I prefer tabs" | ❌ | ✅ backbone | ❌ | ✅ maybe |
| Agent writes code | ✅ | ✅ | ❌ | ❌ |
| Agent researches a library | ❌ | ✅ backbone | ❌ | ❌ |
| User debugs a test failure | ✅ | ✅ | ❌ | ❌ |
| User manually edits .cursorrules | ❌ | ❌ | ✅ | ❌ |
| User corrects agent's misunderstanding | ❌ | ✅ backbone | ❌ | ❌ |
| Pure chat, no tools called at all | ❌ | ✅ backbone | ❌ | ❌ |
| End of session summary | ❌ | ✅ deep analysis | ❌ | ✅ if agent calls reflect |

> [!TIP]
> **Layer 2 (transcript harvesting) is the backbone** — it catches every scenario that involves conversation, which is where 90%+ of decisions and knowledge live. The other layers are supplements for specific gaps. No layer disrupts the user or agent.

### 2.3 Episode Auto-Detection

The Memory Processor recognizes bug/incident arcs automatically:

| Signal | Inference |
|---|---|
| `COMMAND_EXECUTED` with exit code ≠ 0 | Potential bug trigger |
| Series of `FILE_READ` after a failure | Investigation phase |
| `FILE_MODIFIED` after investigation reads | Attempted fix |
| `COMMAND_EXECUTED` same command, exit code = 0 | Verification passed |
| `GIT_COMMITTED` after passing verification | Resolution |

When this pattern is detected, the Memory Processor:
1. Creates an `episode` row with `episode_type = 'bug_fix'`
2. Links the relevant events via `episode_events` with roles (`trigger`, `investigation`, `fix`, `verification`)
3. Fills in `trigger`, `investigation`, `root_cause`, `resolution`, `verification` from the event payloads
4. Generates an embedding of the full narrative for future similarity search
5. Extracts `error_patterns` from the trigger event's stderr/stdout
6. Records `files_involved` from the modified files

### 2.4 Episode Retrieval — "How Did We Fix This Before?"

When an agent encounters an error, it (or the Context Builder automatically) can search for similar past episodes:

```sql
-- Search by error pattern (exact match)
SELECT * FROM episodes
WHERE project_id = $1
  AND $2 = ANY(error_patterns)
  AND status = 'RESOLVED'
ORDER BY resolved_at DESC;

-- Search by similarity (semantic)
SELECT *, 1 - (embedding <=> $2) AS similarity
FROM episodes
WHERE project_id = $1
  AND status = 'RESOLVED'
ORDER BY embedding <=> $2
LIMIT 5;

-- Search by file involvement
SELECT * FROM episodes
WHERE project_id = $1
  AND $2 = ANY(files_involved)
ORDER BY resolved_at DESC;
```

The full episode is returned with all linked events:

```xml
<episode type="bug_fix" title="Auth timeout on WebSocket upgrade" status="RESOLVED">
  <trigger date="2026-09-15T10:23:00Z">
    ConnectionTimeout in ws.go:142 during load test. Error: "pgx pool exhausted,
    all connections in use"
  </trigger>
  <investigation>
    Read: internal/store/db.go (pool config), internal/server/ws.go (connection lifecycle).
    Hypothesis 1: Pool too small → increased to 20, still failed.
    Hypothesis 2: Connections not released on WS close.
  </investigation>
  <root_cause>
    pool_max_conn_lifetime was 5m, but WebSocket connections hold a DB conn for their
    entire lifetime (hours). Connections expired mid-use.
  </root_cause>
  <resolution files="internal/store/db.go, internal/server/ws.go">
    1. Increased pool_max_conn_lifetime to 30m
    2. Changed WS handler to acquire/release DB conn per-message instead of per-connection
    3. Added health check ping every 60s
  </resolution>
  <verification>
    Load test: 500 concurrent WS connections, 2h duration, 0 timeouts.
    Command: go test -race -run TestWSLoadConcurrent -timeout 3h
  </verification>
  <events count="14">
    <!-- Full event timeline available via episode_search -->
  </events>
</episode>
```

### 2.5 Context Builder — Auto-Inject Relevant Episodes

When the agent is working and encounters an error, the Context Builder automatically searches for matching episodes and injects them into the context:

```
Agent calls command_run("go test ./...") → fails with "connection refused"
  ↓
Interceptor logs COMMAND_EXECUTED event with stderr
  ↓
Context Builder detects error pattern in recent events
  ↓
Searches episodes WHERE error_patterns @> '{"connection refused"}'
  ↓
Finds: "Fixed Postgres connection refused by adding retry with backoff"
  ↓
Next memory_search call includes the episode in <recent_episodes>
```

### 2.6 Memory Level Classification — Processor Prompt

```
Given these events from project "central-memory", extract memories.

Classify each memory's level:
- ORGANIZATION: universal policy across all projects (e.g., "all APIs use JWT")
- PROJECT: team decision or codebase fact (e.g., "we use pytest")
- PERSONAL: individual preference (e.g., "Alice prefers verbose errors")
  → Look for "I prefer", "I like", "I always"
- SESSION: temporary, task-specific (e.g., "don't touch payments/ right now")
  → Look for "for now", "right now", "during this", "in this task"

Classify each memory's scope:
- fact: objective truth about the codebase
- preference: subjective choice
- decision: deliberate team choice with reasoning
- constraint: hard rule that must not be violated
- pattern: recurring code/architecture pattern
- episode_summary: condensed bug/incident takeaway

Default to SESSION level if unsure (safer — can be promoted later).

Existing confirmed memories (do not duplicate):
{existing_memories}

New events to process:
{event_batch}
```

### 2.7 Memory Promotion & Demotion

| Transition | Trigger | Mechanism |
|---|---|---|
| SESSION → PROJECT | Same fact appears in 3+ sessions | Memory Processor auto-proposes promotion |
| SESSION → PROJECT | User manually promotes | CLI or dashboard action |
| PERSONAL → PROJECT | Team discussion confirms preference as standard | User promotes |
| PROJECT → ORGANIZATION | Pattern adopted across multiple projects | Admin promotes |
| Any → archived | Confidence decayed below 0.2 AND use_count = 0 | Background job flags for review |
| SESSION → expired | Session ends + 7 day grace period | Background job cleans up |

### 2.8 Confirmation Flow

- PROPOSED items auto-confirm after 24h unless a user rejects
- Users can manually confirm/reject immediately via CLI or dashboard
- High-confidence items (>0.9) from the Memory Processor auto-confirm after 4h
- Items extracted from explicit user statements (detected by Memory Processor) auto-confirm after 1h

### Exit Criterion

Alice and Bob in the same project, on different machines. Without Alice or Bob ever calling `memory_write`:
1. **Pure Discussion / Research Extraction**: Alice and her agent discuss database caching options for 15 minutes and decide on Redis over Memcached. No files are edited and no code is written. The daemon's Transcript Harvester tails the conversation file, emits `CONVERSATION_TURN` events, and the Memory Processor extracts "Team decided on Redis over Memcached due to pub/sub needs" as a PROJECT decision.
2. **Episode Auto-Detection**: Alice's agent debugs a test failure. The daemon passively captures the command failure → file investigation reads → code fix → test pass sequence and auto-creates a `bug_fix` episode with the full arc.
3. **Cross-User Convergence**: Bob's agent queries `memory_search` and immediately receives Alice's Redis decision and testing guidelines in its bounded XML context block.
4. **Episode Retrieval**: Bob's agent hits a similar connection timeout in another branch; `episode_search` returns Alice's resolved episode, including root cause and files involved.

---

## Phase 3 — Sessions + Real-Time

> **Goal**: Live multiplayer — Alice's actions appear for Bob and the agent instantly.

### 3.1 Session Schema (`migrations/003_sessions.up.sql`)

```sql
CREATE TABLE sessions (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id  UUID NOT NULL REFERENCES projects(id),
    title       TEXT,
    created_by  UUID NOT NULL REFERENCES users(id),
    is_active   BOOLEAN DEFAULT true,
    created_at  TIMESTAMPTZ DEFAULT now(),
    ended_at    TIMESTAMPTZ
);

CREATE TABLE session_participants (
    session_id  UUID NOT NULL REFERENCES sessions(id),
    user_id     UUID REFERENCES users(id),
    agent_id    UUID,
    role        TEXT DEFAULT 'MEMBER'
        CHECK (role IN ('OWNER', 'MEMBER', 'OBSERVER')),
    joined_at   TIMESTAMPTZ DEFAULT now(),
    left_at     TIMESTAMPTZ,
    PRIMARY KEY (session_id, COALESCE(user_id, gen_random_uuid()))
);

-- Add FK from memory_items and events to sessions
ALTER TABLE memory_items ADD CONSTRAINT fk_memory_session
    FOREIGN KEY (session_id) REFERENCES sessions(id);
```

### 3.2 WebSocket Protocol

```jsonc
// Client → Server
{"type": "subscribe", "project_id": "...", "session_id": "..."}
{"type": "action", "event_type": "MESSAGE_SENT", "payload": {...}}
{"type": "presence", "status": "typing"}

// Server → Client
{"type": "event", "event": {/* full event row */}}
{"type": "presence", "user_id": "...", "status": "online|typing|idle|offline"}
{"type": "memory_update", "item": {/* memory item */}, "action": "proposed|confirmed|rejected"}
{"type": "episode_update", "episode": {/* episode */}, "action": "opened|updated|resolved"}
```

### 3.3 Session vs Project Memory Scoping

- **Default**: Memories start session-scoped (`session_id` is set, `level = 'session'`)
- **Promotion**: Memory Processor or user promotes to project-scoped (`session_id` → NULL, `level` → `'project'`)
- **New sessions** inherit all project-scoped + org-scoped CONFIRMED memories, not other sessions' memories
- **Override**: Session memory with same `key` as project memory takes precedence within that session

### 3.4 Presence System

- User presence: heartbeat-derived (online if heartbeat < 90s, idle if no action for 5min)
- Agent presence: derived from session_participants + daemon status
- Activity feed: derived from events table, filtered to human-readable types

### Exit Criterion

Alice sends a message in a session. Bob sees it in <1s via WebSocket. A bug episode opened by Alice's agent shows up in Bob's dashboard in real time.

---

## Phase 4 — Multi-Agent + Materializer

> **Goal**: Same memory serves both live MCP agents and static instruction files.

### 4.1 Agent Table (`migrations/004_agents.up.sql`)

```sql
CREATE TABLE agents (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            TEXT UNIQUE NOT NULL,
    adapter_type    TEXT NOT NULL CHECK (adapter_type IN ('pull', 'push')),
    capabilities    JSONB,
    context_budget  INTEGER NOT NULL DEFAULT 4000,
    output_file     TEXT,
    output_format   TEXT,
    created_at      TIMESTAMPTZ DEFAULT now()
);

INSERT INTO agents (name, adapter_type, context_budget, output_file, output_format) VALUES
    ('claude',    'pull', 10000, NULL, NULL),
    ('opencode',  'pull', 10000, NULL, NULL),
    ('codex',     'pull', 10000, NULL, NULL),
    ('antigravity','pull', 10000, NULL, NULL),
    ('copilot',   'push', 8000,  '.github/copilot-instructions.md', 'markdown'),
    ('cursor',    'push', 6000,  '.cursorrules', 'text'),
    ('windsurf',  'push', 6000,  '.windsurfrules', 'text');

CREATE TABLE project_agents (
    project_id  UUID NOT NULL REFERENCES projects(id),
    agent_id    UUID NOT NULL REFERENCES agents(id),
    enabled     BOOLEAN DEFAULT true,
    config      JSONB,
    PRIMARY KEY (project_id, agent_id)
);
```

### 4.2 Materializer (`internal/materializer/`)

- **Trigger**: Subscribes to `MEMORY_CONFIRMED` and `MEMORY_SUPERSEDED` events
- **Debounce**: 5-second quiet period after last change before regenerating
- **Template**: Renders memories into agent-specific format within token budget
- **Managed section**: Uses delimiters to protect user content:
  ```markdown
  <!-- BEGIN CENTRAL MEMORY — DO NOT EDIT -->
  ...generated content...
  <!-- END CENTRAL MEMORY -->
  ```
- **File watcher integration**: If user edits content OUTSIDE the managed section, the watcher treats it as new memory input (passive extraction)

### Exit Criterion

A CONFIRMED memory appears in Claude's live `memory_search` AND in `.github/copilot-instructions.md` on disk. Editing `.cursorrules` manually causes the new content to be extracted as a PROPOSED memory.

---

## Phase 5 — Memory Branching (Copy-on-Write)

> **Goal**: Bob forks from Alice, diverges privately, diffs, merges back.

### 5.1 Schema (`migrations/005_branches.up.sql`)

```sql
CREATE TABLE memory_branches (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id          UUID NOT NULL REFERENCES projects(id),
    name                TEXT NOT NULL,
    owner_id            UUID REFERENCES users(id),
    parent_branch_id    UUID REFERENCES memory_branches(id),
    forked_at_event_id  BIGINT REFERENCES events(id),
    visibility          TEXT DEFAULT 'private'
        CHECK (visibility IN ('private', 'shared')),
    created_at          TIMESTAMPTZ DEFAULT now(),
    UNIQUE(project_id, name)
);

-- Every project gets a "main" branch automatically
ALTER TABLE memory_items ADD COLUMN branch_id UUID REFERENCES memory_branches(id);
```

### 5.2 Copy-on-Write Resolution

- **Read**: Walk branch → parent → parent's parent → main. Return first match.
- **Write**: Always insert into current branch. Never modify parents.
- **Fork**: New branch row with `parent_branch_id` = source. Zero data copied.
- **Diff**: Collect local-only items on each branch, compare resolutions for shared keys.
- **Merge**: Source branch items → PROPOSED on target. Same-value = skip. Different-value = conflict.

### 5.3 Episode Branching

Episodes are project-scoped and not branched — a bug fix is a fact regardless of which memory branch you're on. However, the *lessons learned* from an episode (extracted memories) can be branch-specific.

### 5.4 Staleness Detection + Branch Limits

- Parent update on a key that exists on child → flag child's item as `potentially_stale`
- Max branch depth: 5 levels
- Auto-archive branches untouched for 30 days

### Exit Criterion

Bob forks, diverges, diffs against main, merges back with conflicts surfaced. Resolved episodes are visible on all branches.

---

## Phase 6 — Hardening

### 6.1 Security

- Daemon sandbox audit: adversarial path traversal tests
- Branch visibility enforcement at store layer
- Secret pattern scanning on all file reads

### 6.2 Event Retention

| Age | Policy |
|---|---|
| 0–30 days | Hot: full events in primary table |
| 30–180 days | Warm: partitioned archive, queryable |
| 180+ days | Cold: payload dropped, episodes preserved, raw events → S3 Glacier |

### 6.3 Offline Handling

- Memory unaffected (Postgres)
- File ops return `workspace_offline` error
- Designated processor failover: if owner offline >1h, next team member takes over

### 6.4 Rate Limiting

- Events: 100/s per project
- WebSocket: per-client backpressure with send buffer
- Memory Processor: max 1 LLM call per 5 min per project
- Daemon file ops: 50 req/s

### 6.5 Observability

- Structured JSON logging with project/session/user IDs
- Metrics: events/sec, memory items, connections, LLM cost
- Health endpoints on daemon and server

---

## AWS Infrastructure

```
┌──────────────────────────────────────────────────────────┐
│                        AWS                                │
│                                                          │
│  ┌─────────────────────┐    ┌─────────────────────────┐  │
│  │ RDS Aurora           │    │ S3                       │  │
│  │ Serverless v2        │    │ - cold event archive     │  │
│  │ (Postgres 16 +       │    │ - episode attachments    │  │
│  │  pgvector + pgcrypto)│    │ - exported memory        │  │
│  │                      │    │   snapshots               │  │
│  │ Tables:              │    └─────────────────────────┘  │
│  │  users, projects,    │                                 │
│  │  workspaces, events, │    ┌─────────────────────────┐  │
│  │  memory_items,       │    │ Secrets Manager          │  │
│  │  episodes,           │    │ - DB connection string   │  │
│  │  episode_events,     │    │ - JWT signing key        │  │
│  │  watched_files,      │    └─────────────────────────┘  │
│  │  tasks, sessions,    │                                 │
│  │  session_participants,│   ┌─────────────────────────┐  │
│  │  agents,             │    │ EC2 / ECS                │  │
│  │  project_agents,     │    │ - Central API server      │  │
│  │  memory_branches     │    │ - WebSocket hub           │  │
│  └─────────────────────┘    └─────────────────────────┘  │
└──────────────────────────────────────────────────────────┘
         ▲                              ▲
         │ pgx + pgvector              │ WebSocket + REST
         │                              │
    ┌────┴──────────────────────────────┴────┐
    │          User's Machine                 │
    │                                         │
    │  ┌──────────────────────────────────┐   │
    │  │  Workspace Daemon                 │   │
    │  │  - File/git ops (sandboxed)       │   │
    │  │  - Interceptor (tool capture)     │   │
    │  │  - Harvester (conversation tails) │   │
    │  │  - File watcher (instruction files)│  │
    │  │  - Heartbeat → server             │   │
    │  │  - Memory Processor (if designated)│  │
    │  │  - MCP server → agent             │   │
    │  └──────────────────────────────────┘   │
    │              ▲                           │
    │              │ MCP protocol              │
    │  ┌───────────┴──────────────────────┐   │
    │  │  AI Agent (Claude / OpenCode)     │   │
    │  └──────────────────────────────────┘   │
    └─────────────────────────────────────────┘
```

---

## Verification Plan

### Per-Phase Automated Tests

| Phase | Test | What it validates |
|---|---|---|
| 1 | `TestProjectResolver` | URL normalization, dedup, fallback chain |
| 1 | `TestDaemonSandbox` | Adversarial path traversal rejected, NeverPatterns blocked |
| 1 | `TestMemoryRoundTrip` | Write → embed → vector search → retrieve |
| 1 | `TestEpisodeRoundTrip` | Create episode → search by error pattern → retrieve full arc |
| 1 | `TestContextBuilder` | Level resolution order, token budget enforcement, XML output validity |
| 2 | `TestTranscriptHarvester` | Tails conversation files, emits `CONVERSATION_TURN`, extracts research/discussion decisions without file ops |
| 2 | `TestPassiveExtraction` | file_write intercepted → event created → Memory Processor proposes item |
| 2 | `TestEpisodeAutoDetect` | Error → reads → fix → pass sequence creates episode automatically |
| 2 | `TestMemoryLevelClassification` | "I prefer X" → PERSONAL, "we decided X" → PROJECT |
| 2 | `TestConfidenceDecay` | Old unused memories decay, frequently used memories stay high |
| 2 | `TestDeduplication` | Duplicate facts with >0.9 embedding similarity are skipped |
| 3 | `TestWebSocketFanout` | Event inserted → received by all subscribers <1s |
| 3 | `TestSessionScoping` | Session memory overrides project memory for same key |
| 4 | `TestMaterializer` | CONFIRMED memory → appears in `.github/copilot-instructions.md` |
| 4 | `TestInstructionFileExtraction` | Edit `.cursorrules` → new PROPOSED memory |
| 5 | `TestBranchCoWResolution` | Read walks parent chain, write only touches current branch |
| 5 | `TestMergeConflict` | Conflicting keys flagged, non-conflicting auto-merged |

### Integration Tests

- Docker Compose: Postgres 16 (pgvector) + server + daemon + test agent
- `go test -tags=integration ./...`

### Manual Verification

- Phase 1: `mem daemon` → Claude MCP → `workspace_info` returns correct project/branch
- Phase 2: Alice and agent discuss architecture without writing files → decision extracted via transcript harvester → Bob's agent sees it; Alice debugs test failure → episode auto-created → searchable by error pattern
- Phase 3: Two terminals, same session → events appear in both within 1s
- Phase 5: Fork → diverge → diff → merge with conflict UI
