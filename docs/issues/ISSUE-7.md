# ISSUE-7 — [Phase 1] MCP Server for Pull-Model Agents

- **Issue:** #7 — MCP server (`internal/mcp/`, plan §§1.4, 1.6)
- **Status:** Done (implementation + tests + docs; awaiting merge)
- **Assignee:** Wave-2 subagent
- **Scope constraint:** ONLY `internal/mcp/` + `docs/`. Did NOT touch
  `internal/context/`, `internal/store/`, `internal/daemon/`, `main.go`,
  `go.mod`/`go.sum`, or any other dir.

## What was built

| File | Contents |
|---|---|
| `internal/mcp/server.go` | `Server` + `Config`, 8 tool handlers, `MemoryBackend`/`MemoryWriter`/`EpisodeBackend`/`WorkspaceProvider`/`FileBackend` narrow interfaces, `QuerierBackend` (`store.Querier` → `store.Search`), `ValidateMemoryWrite` (rune-counted 20–2000 + level/scope allowlists), `ValidateEpisodeReport`, `ReflectionHint`, store→builder mapping, `Tools()` catalog |
| `internal/mcp/backend.go` | `HashEmbed` (dim 1536, deterministic, L2-normalized); `InMemoryMemoryStore` (PROPOSED writes + `Confirm`, CONFIRMED/>0.3 guards, `store.Rank` rerank); `InMemoryEpisodeStore` (§2.4 filters); `StaticWorkspaceProvider`, `DaemonWorkspaceProvider` (`daemon.Git*`), `DaemonFileProxy` (`daemon.Read/WriteFileSandboxed`) |
| `internal/mcp/protocol.go` | Newline-delimited JSON-RPC 2.0 on stdio: `initialize`/`ping`/`tools/list`/`tools/call` + direct `<tool>` dispatch, silent notifications, -32700/-32601/-32602 mapping |
| `internal/mcp/server_test.go` | 12 tests: XML+hint+budget, query/level validation, write validation + 19/20/2000/2001 boundaries + PROPOSED status, voluntary reflect, episode report/search (error/file/semantic/status), workspace_info, sandbox proxy roundtrip + traversal/missing mapping + sentinel proof, unknown tool, 8-tool catalog, embed properties, level/scope allowlists |
| `internal/mcp/protocol_test.go` | 6 tests: tools/call dispatch, direct dispatch, tools/list count, initialize+ping, parse-error/unknown-method/invalid-params codes + silent notification, end-to-end hint over stdio |
| `docs/decisions/ADR-007-mcp-server-pull-model.md` | Why-mandatory ADR (stdlib transport, narrow-interface reuse, sandbox delegation, HashEmbed interim) |
| `go.mod` / `go.sum` | Untouched — zero new dependencies |

## Decisions (see ADR-007 for rationale)

1. Hand-rolled JSON-RPC line protocol (stdlib only) over an MCP SDK —
   plan asks for stdio line protocol; no allow-list exception needed.
2. Narrow interfaces + delegation instead of reimplementation: sandbox,
   git state, search SQL, and XML rendering stay owned by #3/#6.
3. `HashEmbed` (1536-dim, SQL-compatible) as the interim embedder until
   LLM embeddings arrive via `Config.Embed` (#8/#10).
4. `memory_search` returns char-budget `token_count`/`budget_remaining`
   (matching the §1.4 example: used + remaining == budget) with the
   piggyback `reflection_hint` on every response.

## Verification (2026-09-17, in-repo)

- `go build ./...` → exit 0
- `go vet ./internal/mcp/` → exit 0
- `go test ./internal/mcp/ -count=1 -v` → **18/18 PASS**
- `gofmt -l internal/mcp/` → clean
- Note: mid-task `go build ./...` failed on `internal/store` redeclarations
  (`events.go` vs `episodes.go`) from parallel in-flight agents (#9/#11);
  untouched per ownership, and resolved by those agents before this issue
  closed — final tree builds green. A shadow-module build/vet/test run
  during the breakage confirmed `internal/mcp` itself was never the cause.

Acceptance mapping: XML block + hint + budget
(`TestMemorySearchReturnsXMLReflectionHintAndBudget`,
`TestServeStdioMemorySearchCarriesHint`), 20–2000 validation
(`TestMemoryWriteValidation`, `TestMemoryWriteCharBoundaries`),
voluntary reflect (`TestMemoryReflectVoluntary`), episode error/file/
semantic search + report (`TestEpisodeReportAndSearch`), daemon-sandbox
proxy (`TestFileSandboxProxy`), stdio dispatch
(`TestServeStdioToolsCallDispatch`, `TestServeStdioDirectDispatch`,
`TestServeStdioErrorsAndNotifications`).

## Follow-ups (not this issue)

- #8/#21: production wiring — `QuerierBackend{Q: db}`, PROPOSED INSERT
  writer, and the `mem mcp` stdio entrypoint calling `ServeStdio`.
- #10 processor: LLM `Config.Embed` replacement, auto-confirm timers
  (replacing test-only `Confirm`), SESSION→PROJECT promotion.
- #11: full episode engine (embeddings, event links); MCP `Episode`
  maps 1:1 onto its columns for the swap.
- #3: daemon-side `FILE_READ` event logging for MCP-proxied reads.
- #13: full MCP session/progress negotiation if pull agents ever need it.
