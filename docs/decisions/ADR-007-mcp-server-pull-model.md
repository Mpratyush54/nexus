# ADR-007 — MCP Server for Pull-Model Agents

- **ADR ID:** ADR-007-mcp-server-pull-model
- **Date:** 2026-09-17
- **Author:** issue-#7 agent (Wave 2)
- **Issue:** #7 MCP server (`internal/mcp/`, plan §§1.4, 1.6)
- **Status:** Accepted

## Context

Issue #7 owns `internal/mcp/` — the pull-model agent surface (Claude Code,
OpenCode) per `implementation-plan.md` §1.4 — while three constraints
collide:

1. **Parallel ownership** — `internal/context/builder.go` (#6),
   `internal/store/memory.go` (#6), and `internal/daemon/*.go` (#3/#4/#5)
   belong to other agents and MUST NOT be edited, yet the MCP tools need
   Context Builder XML output, store types/search, and the daemon sandbox.
2. **Zero new dependencies** — orchestration ADR-000-go-deps allows only
   plan-§1.9 deps (`pgx`, `pgvector-go`, `fsnotify`, `jwt`, `websocket`);
   the MCP layer should add none.
3. **Plan-exit-criteria fidelity** — `memory_search` must return the §1.6
   XML block *plus* the piggyback `reflection_hint`, `token_count` and
   `budget_remaining`; `memory_write` must enforce the §1.1 CHECK
   (20–2000 chars) and land as PROPOSED; `file_read`/`file_write` must go
   through the daemon sandbox, not around it.

## Options Considered

1. **Import a full MCP SDK (e.g. `github.com/mark3labs/mcp-go`).**
   Pros: spec-complete protocol. Cons: a new dependency outside the
   §1.9 allow-list (needs its own ADR + version-maintenance owner);
   session/streamable-HTTP machinery is unneeded in Phase 1, where the
   daemon spawns the server over stdio.
2. **(Chosen) Hand-rolled newline-delimited JSON-RPC 2.0 on stdio, stdlib
   `encoding/json` only, with MCP tool semantics.**
   `tools/list` + `tools/call` + `initialize`/`ping` plus direct
   `<tool-name>` dispatch; tool names, inputs and outputs match the §1.4
   table exactly. No `go.mod`/`go.sum` change.
3. **Reimplement sandbox/validation inside `internal/mcp`.**
   Rejected: a second sandbox drifts from the daemon's (the exact failure
   mode ADR-003 exists to prevent). Instead the MCP layer holds narrow
   interfaces (`FileBackend`, `WorkspaceProvider`, `MemoryBackend`,
   `EpisodeBackend`) and the default implementations delegate to
   `daemon.ReadFileSandboxed` / `daemon.WriteFileSandboxed` /
   `daemon.Git*` — reuse, never reimplementation.

## Decision

- `internal/mcp/server.go` — `Server` + `Config`, the 8 plan tools,
  `ValidateMemoryWrite` (rune-counted 20–2000 + level/scope allowlists),
  `ValidateEpisodeReport`, `ReflectionHint` constant, store→builder
  mapping (`store.MemoryItem` → `builder.Item` grouped by level into
  `builder.ContextInput`, rendered by `builder.BuildXML`), and the
  `Tools()` catalog.
- `internal/mcp/backend.go` — `HashEmbed` (deterministic FNV bag-of-words,
  dim 1536 = the `vector(1536)` column, L2-normalized, so it flows through
  `store.FormatEmbedding`/SQL unchanged); `InMemoryMemoryStore`
  (PROPOSED writes, CONFIRMED+confidence>0.3 search guard, rerank via
  `store.Rank`); `InMemoryEpisodeStore` (§2.4 filter semantics);
  `StaticWorkspaceProvider` / `DaemonWorkspaceProvider`;
  `DaemonFileProxy` (daemon sentinels propagate for `errors.Is` mapping).
- `internal/mcp/protocol.go` — stdio line protocol: one JSON-RPC 2.0
  message per line, notifications answered with silence, parse errors as
  -32700/null-id, unknown methods as -32601, bad arguments as -32602.
- `QuerierBackend` adapts any `store.Querier` (e.g. `*store.DB`) via
  `store.Search` — the production path imports no driver here.

## Why (Rationale)

- **Narrow interfaces compose untouched:** `Server` depends on
  `MemoryBackend`/`MemoryWriter`/`EpisodeBackend`/`WorkspaceProvider`/
  `FileBackend` — all satisfied by fakes in tests and by
  `store.Querier`/`daemon.*` in production — so #7 shipped without a
  single edit outside `internal/mcp/` + `docs/`, verified by
  `git status` showing only those paths.
- **Stdlib transport is sufficient:** the plan requires "MCP over stdio
  (JSON-RPC line protocol)" — `tools/list` + `tools/call` give agents the
  full §1.4 surface; session/progress negotiation is Phase-3+ scope
  (issue #13). Zero `go.mod` delta proves the no-new-deps claim.
- **Sandbox reuse is proven, not asserted:**
  `TestFileSandboxProxy` round-trips through `DaemonFileProxy` and
  asserts `errors.Is(err, daemon.ErrTraversal)` on the real sentinel,
  plus -32602 mapping for traversal/secret/missing through `Server.Call`.
- **Phase-1 exit criteria covered:** `workspace_info`,
  daemon-proxied `file_read`, PROPOSED `memory_write`, XML
  `memory_search` with hint + budget, `episode_report`/`episode_search`
  by error/file/semantic — each with a dispatch test.
- **Evidence:** `go build ./...` exit 0, `go vet ./internal/mcp/` exit 0,
  `go test ./internal/mcp/ -count=1 -v` **18/18 PASS** (dispatch,
  19/20/2000/2001-char boundaries, hint presence, sandbox proxy,
  episode filters, stdio envelope incl. silent notifications).

## Consequences

- Real Postgres wiring: `QuerierBackend{Q: db}` + a `MemoryWriter`
  INSERT (status PROPOSED) land with the server/CLI wiring (issues
  #8/#21); `Config.Embed` accepts LLM embeddings when the processor
  lands (#10) — `HashEmbed` is the documented interim.
- Full episode rows (embeddings, event links) belong to issue #11; the
  local `Episode` type is the searchable/renderable subset and maps
  1:1 onto the §1.1 `episodes` columns.
- `mem mcp` stdio entrypoint (spawning `ServeStdio`) is issue #21's;
  this issue ships the server + tests, no `main.go` changes per
  ownership.
- Daemon-side event logging of MCP-proxied reads (interceptor `FILE_READ`)
  activates when the daemon core routes these calls (#3 follow-up).

## Alternatives Rejected

- MCP SDK dependency (option 1): spec-completeness we don't need yet,
  at the cost of an allow-list exception and a maintenance owner.
- Duplicated sandbox/validation (option 3): two sources of truth for
  security-critical checks; rejected in favor of interface + delegation.
- Shared cross-package item/episode types: same rejection as ADR-006 —
  mapping at the boundary keeps each package's owner authoritative.
