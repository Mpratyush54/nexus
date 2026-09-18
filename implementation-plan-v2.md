# Central Memory — Complete Feature Plan (Final)

## Architecture (Decided)

- **React PWA** at `nexus.pratyushes.dev` — one UI for everything
- **API Server** at `api-nexus.pratyushes.dev` — Go backend
- **Local Daemon** — existing + CORS proxy for browser bridge
- **MCP Server** (`cmd/mem mcp`) — 8 tools for AI agents in editors

---

## Feature List

### 1. Auth, Tokens & Connection Revocation

- [ ] Signup (`POST /auth/signup`)
- [ ] Login (`POST /auth/login`) ✅ exists
- [ ] Password change (`PUT /users/me/password`)
- [ ] User profile (`GET/PUT /users/me`) — username, email, avatar, settings
- [ ] Usage stats (`GET /users/me/usage`) — requests, memories created, episodes
- [ ] **Token & API Key Management**:
  - [ ] `GET /auth/tokens` — list active user and agent API tokens
  - [ ] `POST /auth/tokens` — create scoped token (e.g. "Cursor IDE Agent", "Claude Desktop", "Laptop Daemon")
  - [ ] `DELETE /auth/tokens/{tokenId}` — revoke token immediately
  - [ ] **Instant Disconnect**: When a token is revoked, any active WebSocket connection or daemon session bound to that token is forcibly disconnected (`hub.DropByToken(tokenID)`).
  - [ ] Invalidate cached token lookups so requests immediately receive `401 Unauthorized`.

---

### 2. Roles & Permissions (RBAC)

#### Built-in Roles

| Role | Can Do |
|:---|:---|
| **Owner** | Everything. Cannot be removed. |
| **Admin** | Manage members, roles, settings. All read/write. |
| **Editor** | Read/write memories, branches, episodes, sessions. No member management. |
| **Viewer** | Read-only everything. No writes. |

#### Custom Roles

- [ ] Create custom role with granular permissions
- [ ] 15 permission types:

```
memory:read, memory:write, memory:confirm, memory:delete, memory:promote, memory:edit
branch:read, branch:write
episode:read, episode:write
session:read, session:write, session:steer
member:invite, member:manage
```

#### Routes
- [ ] `GET/POST /projects/{id}/roles` — list / create
- [ ] `PUT/DELETE /projects/{id}/roles/{roleId}` — update / delete
- [ ] `PUT /projects/{id}/members/{userId}/role` — assign role

#### Migration
- [ ] `013_roles.up.sql` — `project_roles` table, expand role CHECK

---

### 3. GitHub Collaborator Import

- [ ] GitHub OAuth ("Connect GitHub" in project settings)
- [ ] Import collaborators from linked repo
- [ ] Map GitHub permissions → Central Memory roles (admin→Admin, write→Editor, read→Viewer)
- [ ] Auto-match existing users by GitHub username/email
- [ ] Invite links for unregistered collaborators
- [ ] Sync options: one-time import / auto-sync / manual re-import

#### Routes
- [ ] `POST /projects/{id}/github/connect`
- [ ] `POST /projects/{id}/github/import`
- [ ] `GET /projects/{id}/github/status`
- [ ] `DELETE /projects/{id}/github/disconnect`

#### Migration
- [ ] `014_github_integration.up.sql` — `github_links` + `github_user_map` tables

---

### 4. Memory Sharing & Visibility

#### Visibility Modes (per memory)

| Mode | Who Sees It |
|:---|:---|
| **Private** | Only creator |
| **Shared** | Specific users/roles you choose |
| **Project** | All project members (default) |
| **Public** | Anyone with link |

- [ ] Share with specific user / role
- [ ] Unshare / list shares
- [ ] Bulk share (multiple memories)
- [ ] Copy memory to another project
- [ ] SearchMemory enforces visibility rules

#### Routes
- [ ] `POST /memory/{id}/share` — share with user/role
- [ ] `DELETE /memory/{id}/share/{userId}`
- [ ] `GET /memory/{id}/shares`
- [ ] `POST /memory/{id}/copy` — cross-project copy

#### Migration
- [ ] `015_memory_sharing.up.sql` — `memory_shares` table, `visibility` column

---

### 5. ★ Real-Time Memory Editing

**This is the big gap.** Currently memories can only be proposed/confirmed/rejected — never edited after creation.

#### Memory CRUD (Missing)

- [ ] **Edit memory content** — update key, content, tags, context_snippet, level, scope
  - `PUT /memory/{id}` — partial update (only fields you send)
  - Permission: creator can always edit own; `memory:edit` for others
  - Only PROPOSED and CONFIRMED can be edited (terminal states locked)

- [ ] **Partial content edit** — modify just part of the content text
  - API accepts `{content: "new full content"}` (full replacement)
  - Frontend shows rich text area with the current content, user edits inline

- [ ] **Delete memory** — `DELETE /memory/{id}`
  - Sets status to SUPERSEDED (soft delete, preserves history)
  - Hard delete option for owner/admin only

- [ ] **Undo/Revert** — go back to any previous version
  - `POST /memory/{id}/revert` `{version: 3}` — restores content from version history
  - `GET /memory/{id}/history` — returns all versions

#### Version History

Every edit creates a version entry:

- [ ] **Migration `016_memory_versions.up.sql`**:
  ```sql
  CREATE TABLE IF NOT EXISTS memory_versions (
    id          BIGSERIAL PRIMARY KEY,
    memory_id   UUID NOT NULL REFERENCES memory_items(id) ON DELETE CASCADE,
    version     INT NOT NULL,
    key         TEXT NOT NULL,
    content     TEXT NOT NULL,
    tags        TEXT[],
    level       TEXT,
    scope       TEXT,
    edited_by   UUID REFERENCES users(id),
    created_at  TIMESTAMPTZ DEFAULT now(),
    UNIQUE(memory_id, version)
  );
  ```

- [ ] On every `PUT /memory/{id}`: snapshot current state → `memory_versions` → apply update
- [ ] `GET /memory/{id}/history` → list versions with diffs
- [ ] `POST /memory/{id}/revert` → restore from version

#### Real-Time Sync Across Clients

When anyone edits a memory, everyone sees it instantly:

```
User A edits memory in PWA
  → PUT /memory/{id}
  → Server stores new version + appends MEMORY_UPDATED event
  → WebSocket hub fans out memory_update {action: "updated", item: {...}}
  → User B's PWA receives WS frame → updates card in real-time
  → Agent's MCP search returns updated content on next call
```

- [ ] New WebSocket event: `memory_update {action: "updated"}` (alongside existing proposed/confirmed/rejected)
- [ ] New event type: `MEMORY_UPDATED` in wsbridge vocabulary
- [ ] PWA Memory page: live card updates without page refresh
- [ ] Conflict detection: if two users edit simultaneously, last-write-wins + notification "bob also edited this memory just now"

#### PWA Memory Editor UI

```
┌─────────────────────────────────────────────────┐
│  Edit Memory                              [×]   │
├─────────────────────────────────────────────────┤
│  Key:    [redis/caching              ]          │
│  Level:  [Project ▼]  Scope: [decision ▼]      │
│                                                 │
│  Content:                                       │
│  ┌─────────────────────────────────────────┐   │
│  │ Use Redis for session caching with a    │   │
│  │ 30-minute TTL. Connection pooling via   │   │
│  │ redigo with max 50 connections.         │   │
│  │                                         │   │
│  │ Updated: invalidation strategy changed  │   │
│  │ from write-through to write-behind.     │   │
│  └─────────────────────────────────────────┘   │
│                                                 │
│  Tags: [redis] [caching] [performance] [+ Add]  │
│                                                 │
│  ─── Version History ────────────────────       │
│  v3  bob   "Changed invalidation..."   2m ago   │
│  v2  alice "Added connection pooling"  1h ago    │
│  v1  alice "Initial proposal"          3h ago    │
│  [Revert to v2]                                 │
│                                                 │
│  [Save Changes]  [Cancel]                       │
└─────────────────────────────────────────────────┘
```

---

### 6. ★ MCP Visibility & Control

**Gap: users have zero visibility into what AI agents are doing via MCP.**

The MCP server (`cmd/mem mcp`) exposes 8 tools to AI agents:
`memory_search`, `memory_write`, `memory_reflect`, `episode_search`, `episode_report`, `workspace_info`, `file_read`, `file_write`

Users need to see and control all of this.

#### Agent Activity Feed

- [ ] **Live MCP call log** in the PWA — every tool call an agent makes:
  ```
  ┌─────────────────────────────────────────────────┐
  │  Agent Activity                        [Pause]  │
  ├─────────────────────────────────────────────────┤
  │  🤖 cursor-agent  via MCP                       │
  │  14:23:05  memory_search {query: "redis"}       │
  │            → 3 results returned                  │
  │  14:23:08  memory_write {key: "redis/ttl"...}   │
  │            → PROPOSED (awaiting review)          │
  │  14:23:12  file_read {path: "config.go"}        │
  │            → 2.3KB read                          │
  │  14:23:15  episode_report {title: "OOM bug"...} │
  │            → episode created (OPEN)              │
  │                                                  │
  │  🤖 claude-agent  via MCP                        │
  │  14:22:50  memory_search {query: "deploy"}      │
  │            → 5 results returned                  │
  └─────────────────────────────────────────────────┘
  ```

- [ ] **Implementation**: MCP tool calls emit events to the server:
  - Daemon intercepts MCP calls (Interceptor already exists: `daemon.Interceptor`)
  - Interceptor forwards to server via `AppendEvent` with type `MCP_TOOL_CALL`
  - Server fans out via WebSocket as `event {event_type: "MCP_TOOL_CALL"}`
  - PWA renders in Agent Activity feed

#### Agent Permissions

- [ ] **Per-agent access control** in project settings:
  ```
  ┌─────────────────────────────────────────────────┐
  │  Agent Permissions                              │
  ├─────────────────────────────────────────────────┤
  │  cursor-agent (alice's workspace)               │
  │    ✅ memory_search     ✅ memory_write          │
  │    ✅ memory_reflect    ✅ episode_search        │
  │    ✅ episode_report    ✅ workspace_info        │
  │    ⬚ file_read         ⬚ file_write            │
  │    Mode: [Propose only ▼]  (agent can't confirm)│
  │    Rate: [10 calls/min ▼]                       │
  │                                                  │
  │  claude-agent (bob's workspace)                 │
  │    ✅ memory_search     ⬚ memory_write          │
  │    Mode: [Read-only ▼]                          │
  └─────────────────────────────────────────────────┘
  ```

- [ ] **Agent modes**:
  - **Read-only**: agent can search memories/episodes, cannot write
  - **Propose only**: agent can write (PROPOSED), cannot confirm (human must review)
  - **Full access**: agent can read + write + confirm (trusted agent)
  - **Blocked**: agent cannot access this project's memories

- [ ] **Rate limiting per agent**: configurable calls/minute cap

- [ ] **Implementation**:
  - `agent_permissions` table: project_id, agent_id, tool_name, allowed, mode, rate_limit
  - MCP Server checks permissions before executing tool
  - Daemon passes project permission config to MCP on startup

#### Approve/Deny Agent Proposals

- [ ] Agent writes via MCP → memory created as PROPOSED
- [ ] Appears in PWA Review Queue with agent badge 🤖
- [ ] Human reviews → confirm or reject
- [ ] If rejected → agent gets feedback on next `memory_search` (hint: "your proposal X was rejected")

#### Active MCP Connections

- [ ] PWA shows active agent connections:
  ```
  Active Agents:
  🤖 cursor-agent  alice's workspace  online  12 calls today
  🤖 claude-agent  bob's workspace    idle    3 calls today
  ```
- [ ] Source: workspace registrations + daemon heartbeats carry agent info

---

### 7. ★ Real-Time Code Sharing via MCP

**Gap: the MCP↔Daemon↔Server↔PWA loop isn't connected for real-time sharing.**

#### The Full Loop

```
Code Editor (Cursor/VS Code)
    ↕ MCP stdio
Local MCP Server (cmd/mem mcp)
    ↕ local store / daemon
Daemon
    ↕ HTTPS + events
Server
    ↕ WebSocket
React PWA (all team members)
```

**Currently broken**: MCP uses an in-memory `MemStore` — writes are lost when the MCP process exits. Searches only see local data, not server data.

#### Fix: Connect MCP to Server

- [ ] **MCP → Server**: When daemon has `ServerURL` set, MCP tool calls go through the server API instead of local MemStore:
  - `memory_search` → `GET api-nexus/memory/search`
  - `memory_write` → `POST api-nexus/memory`
  - `episode_search` → `GET api-nexus/episodes/search`
  - `episode_report` → `POST api-nexus/episodes`
  
- [ ] **Server → MCP**: When a human edits a memory in the PWA, agents see the update:
  - Human edits in PWA → server stores → event emitted
  - Daemon receives event via WebSocket subscription
  - Daemon invalidates MCP's local cache
  - Agent's next `memory_search` returns updated content

- [ ] **Cross-agent sharing**: Agent A (alice's editor) writes memory → Server stores → Agent B (bob's editor) can search and find it immediately

#### Real-Time Memory Sync Between Code and PWA

```
Alice in VS Code:
  Agent calls memory_write("redis/caching", "Use Redis...")
    → Daemon → POST /memory → Server stores as PROPOSED
    → Server WS → Bob's PWA: "🤖 alice's agent proposed redis/caching"
    → Bob reviews in PWA → confirms
    → Server WS → Alice's daemon → MCP cache invalidated
    → Alice's agent searches → finds CONFIRMED memory
```

- [ ] Every MCP write is immediately visible in the PWA review queue
- [ ] Every PWA edit is immediately available to MCP searches
- [ ] Version history shows both human edits and agent writes

#### Agent-to-Agent Memory Sharing

When multiple agents are working on the same project:

- [ ] Agent A writes a memory → immediately searchable by Agent B
- [ ] Agents on different branches see branch-scoped memories (existing branch overlay system)
- [ ] Agent coordination: agents can discover what other agents know via shared memory

---

### 8. Teams / Organizations

- [ ] Create organizations (teams above projects)
- [ ] Org members inherit default role on all org projects
- [ ] Org dashboard — projects, members
- [ ] Cross-project memory sharing within org

#### Routes
- [ ] `POST/GET /orgs` — create / list
- [ ] `GET /orgs/{id}` — details + members
- [ ] `POST/PUT/DELETE /orgs/{id}/members` — manage
- [ ] `POST /orgs/{id}/projects` — create project under org

#### Migration
- [ ] `017_organizations.up.sql` — `organizations` + `organization_members` + `projects.org_id`

---

### 9. Memory Export / Import

- [ ] Export project memories as JSON / YAML / Markdown
- [ ] Import memories from file (bulk create)
- [ ] Cross-project copy (single memory)
- [ ] Fork project (clone all memories + branches)

---

### 10. Notifications & Activity

- [ ] In-app toast notifications (WebSocket driven)
- [ ] PWA push notifications (Web Push API)
- [ ] Notification preferences (per event type toggle)
- [ ] Activity feed with filters (by user, by type, by date)
- [ ] @mentions in memory content → notification to tagged user

---

### 11. Collaboration UX & Frontend Design Standards

#### Design References & Aesthetic
- **Skiper UI (`skiper-ui.com`)**:
  - Floating glass navigation bar (`backdrop-blur-md`, `shadow-glass`, `rounded-2xl`, centered at top of viewport)
  - Global Command Palette (`Cmd+K` / `Ctrl+K`) for fast fuzzy jumping across memories, episodes, branches, and settings
  - Hover-member avatar stacks with smooth expansion and live status tooltips
- **VengeanceUI (`github.com/Ashutoshx7/VengeanceUI`)**:
  - Deep dark surface layering (`#080808` base, `#121212` cards, `#1c1c1c` inputs)
  - Animated glowing border highlights on proposed/active cards
  - Tactile interactive buttons and keyboard shortcut badges with active scale-down (`active:scale-95`)
- **Anim Master (`animmasterlib.dev`)**:
  - Fluid spring physics (Framer Motion) for slide-over drawers, version history diffs, and modal dialogs
  - Mesh gradient background glow with smooth scroll-driven reveals

| Page | Key Features & Design |
|:---|:---|
| **Team** | Members + roles + presence dots + Skiper hover avatar stack + GitHub import |
| **Memory** | Review queue with animated amber borders + search + inline editor + version history drawer |
| **Branches** | Tree hierarchy + side-by-side diff viewer with word-level highlight + merge preview |
| **Sessions** | Handoff panel (context transfer) + agent steering HUD (Interrupt / Prompt / Resume) |
| **Workspace** | Local git/file view via daemon proxy with sandboxed file reader |
| **Agents** | Live MCP monospace call log + active HUD connection cards + permission toggle matrix |
| **Settings** | API tokens & revocation list + role manager + GitHub repo link + notifications |
| **Org** | Organization dashboard + multi-project management |

---

### 12. Local Daemon Bridge

- [ ] CORS proxy at `:7272` in daemon (read-only, browser-safe)
- [ ] Auto-detect in PWA (`localhost:7272/local/healthz`)
- [ ] Workspace panel: git status, file browser, commit log
- [ ] Graceful absence: "Install daemon" banner

---

### 13. Server Cleanup

- [ ] Remove `registerWebRoutes()` from routes.go
- [ ] Remove `COPY web/ /web` from Dockerfile
- [ ] Add CORS middleware for PWA origin
- [ ] Rate limiting on signup/login

---

### 14. ★ Vector Embedding Configuration & Providers

**The Missing Config Explained:**
The database schema carries `vector(1536)` columns with IVFFlat indexes, and `internal/context/embed.go` has `HashEmbed` (deterministic 1536-dim bag-of-words) as a test interim. But external LLM provider configuration (`OPENAI_API_KEY`, etc.) was a placeholder seam (`Config.Embed`).

This feature adds real LLM vector embedding support for agents and searches:

- [x] **Provider Interface**:
  - `openai`: `text-embedding-3-small` (1536 dimensions, default for production)
  - `ollama`: local model (e.g. `nomic-embed-text`, `bge-m3` with endpoint)
  - `hash`: stdlib `HashEmbed` (fallback, zero external dependencies)
- [x] **Server & Daemon Environment Configuration**:
  ```env
  CENTRAL_EMBEDDING_PROVIDER=openai           # openai | ollama | hash
  CENTRAL_EMBEDDING_API_KEY=sk-...           # OpenAI or custom API key
  CENTRAL_EMBEDDING_MODEL=text-embedding-3-small
  CENTRAL_EMBEDDING_ENDPOINT=                # Optional: custom endpoint or Ollama URL
  ```
- [x] **Agent & Search Integration**:
  - When `memory_write` runs from MCP or PWA, text is embedded via configured provider.
  - When `memory_search` runs, the query is converted to a vector and matched via pgvector cosine similarity `SearchMemoryVector`!
  - Safe fallback: if provider fails or key is missing, automatically falls back to `HashEmbed` or keyword matching.

---

### 15. ★ Compile-Time & Runtime ServerURL Configuration

To ensure binaries work seamlessly out-of-the-box on client machines without manual configuration:

- [ ] **Compile-Time `-ldflags` Injection**:
  - In `cmd/nexus/client.go`, `cmd/daemon/main.go`, `cmd/mem/main.go`:
    ```go
    var defaultServerURL = "https://api-nexus.pratyushes.dev"
    ```
  - Settable during compilation:
    ```bash
    go build -ldflags "-X main.defaultServerURL=https://api-nexus.pratyushes.dev" ./cmd/daemon
    ```
- [ ] **4-Tier Priority Cascade**:
  1. CLI Flag: `-server <url>` (highest priority)
  2. Environment Variable: `CENTRAL_SERVER_URL` or `NEXUS_SERVER`
  3. Local Config File: `~/.config/central-memory/config.json`
  4. Compile-time Default: `defaultServerURL` (fallback)
- [ ] Pre-built releases for Windows, macOS, and Linux will have `https://api-nexus.pratyushes.dev` hard-coded as the default fallback.

---

## New Migrations Summary

```
013_roles.up.sql              — custom roles, permission sets
014_github_integration.up.sql — GitHub links, user mapping
015_memory_sharing.up.sql     — sharing table, visibility column
016_memory_versions.up.sql    — version history for edits
017_organizations.up.sql      — orgs, org members, project.org_id
018_api_tokens.up.sql         — api_tokens table for agent/user token revocation
```

## New Server Routes Summary

```
Auth & Users:
  POST   /auth/signup
  GET    /auth/tokens              — list active tokens/keys
  POST   /auth/tokens              — mint new scoped API token
  DELETE /auth/tokens/{tokenId}    — revoke token & drop live connections
  GET    /users/me
  PUT    /users/me
  PUT    /users/me/password
  GET    /users/me/usage

Memory Editing:
  PUT    /memory/{id}              — edit content/key/tags
  DELETE /memory/{id}              — soft delete (SUPERSEDED)
  GET    /memory/{id}/history      — version history
  POST   /memory/{id}/revert      — restore version

Memory Sharing:
  POST   /memory/{id}/share
  DELETE /memory/{id}/share/{userId}
  GET    /memory/{id}/shares
  POST   /memory/{id}/copy

Roles:
  GET/POST   /projects/{id}/roles
  PUT/DELETE /projects/{id}/roles/{roleId}
  PUT        /projects/{id}/members/{userId}/role

GitHub:
  POST   /projects/{id}/github/connect
  POST   /projects/{id}/github/import
  GET    /projects/{id}/github/status
  DELETE /projects/{id}/github/disconnect

Agent Permissions:
  GET    /projects/{id}/agents              — list agent configs
  PUT    /projects/{id}/agents/{agentId}    — set permissions/mode/rate
  DELETE /projects/{id}/agents/{agentId}    — remove agent config

Organizations:
  POST/GET /orgs
  GET      /orgs/{id}
  POST/PUT/DELETE /orgs/{id}/members
  POST     /orgs/{id}/projects

Notifications:
  GET    /notifications?since=

Export/Import:
  GET    /projects/{id}/export?format=json
  POST   /projects/{id}/import
```

---

## Build Phases

| # | What | Scope |
|:---|:---|:---|
| 1 | Server: signup, profile, tokens & revocation, CORS, cleanup | 5 files + migration |
| 2 | Server: memory editing + version history | 4 files + migration |
| 3 | Server: RBAC (roles, permissions) | 4 files + migration |
| 4 | Server: memory sharing (visibility) | 3 files + migration |
| 5 | Server & MCP: vector embedding provider (OpenAI/Ollama) + MCP server sync | 4 files |
| 6 | Server: agent permissions + MCP event logging | 3 files |
| 7 | Server: GitHub import | 3 files + migration |
| 8 | Server: organizations | 3 files + migration |
| 9 | Daemon: browser CORS proxy & compile-time ServerURL default | 2 files |
| 10 | React: scaffolding (Vite + Router + theme + API + WS) | foundation |
| 11 | React: auth (landing, login, signup) | 3 pages |
| 12 | React: memory (review queue + editor + version history + sharing) | 1 page |
| 13 | React: team (roles + presence + GitHub import + invite) | 1 page |
| 14 | React: agents (MCP activity feed + permission controls) | 1 page |
| 15 | React: branches + sessions (handoff + steering) | 2 pages |
| 16 | React: workspace + settings + org | 3 pages |
| 17 | React: PWA (manifest, service worker, push) | config |
| 18 | Deploy: Cloudflare Pages + update CI/CD | infra |
