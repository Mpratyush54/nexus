# MCP Server (internal/mcp) — Design Decisions

**Date:** 2026-09-17
**Scope:** GitHub Mpratyush54/nexus issue #7 / implementation-plan.md Phase 1.4
**Files:** `internal/mcp/server.go`, `internal/mcp/tools.go` (+ tests)

## 1. Why stdio JSON-RPC 2.0, stdlib-only

Claude Code, OpenCode, and Antigravity all spawn MCP servers as child
processes and speak newline-delimited JSON-RPC 2.0 on stdin/stdout. That is
the whole transport: no TCP port to allocate, no TLS to terminate, no SSE
endpoint to host and authenticate. For the Phase 1 single-user skeleton —
one daemon spawning one MCP server on the user's own machine — stdio is the
simplest thing that the hosts already support.

stdlib-only (`encoding/json`, `bufio`, `os`, `path/filepath`) follows for
the same reason: the protocol surface we need is `initialize`,
`tools/list`, and `tools/call` dispatch. Pulling in a third-party MCP SDK
would buy us schema helpers we don't need while adding supply-chain and
version-skew risk to a security-sensitive child process. The server is
~120 lines of framing code; when/if we need SSE for remote agents, it can
be added behind the same `Handle` entrypoint without touching tool logic.

## 2. Why piggyback hint instead of MCP sampling

This is a locked plan decision (Phase 1.4 / §2.2), restated here because it
shapes the tool catalog: **there is deliberately no sampling tool**, and
`memory_reflect` is voluntary-only (empty calls succeed as no-ops).

- Sampling requires user approval in most host apps, injects unexpected
  output into the user's session, burns context tokens on every probe, and
  disrupts workflow — the opposite of the zero-disruption extraction model.
- All real extraction happens daemon-side and silently: Layer 1 tool
  interception, Layer 2 transcript harvesting (the backbone — 90%+ of
  decisions live in conversation), Layer 3 instruction-file watching.
- Layer 4 (this package) is supplementary by design: every `memory_search`
  response carries a `reflection_hint` string in its result metadata. The
  agent is already reading that response, so the hint costs zero extra
  tokens and zero interruptions. The agent may act on it or ignore it —
  Layer 2 catches everything regardless.

Tests assert the negative (`TestHandleToolsList` fails if any `*sampl*`
tool appears) so a future contributor can't reintroduce sampling by
accident.

## 3. Why interface-decoupled store with local DTOs

`internal/mcp` defines its own narrow `Store` interface over local
`MemoryItem` / `Episode` / `Workspace` / `Project` DTOs and imports **no**
other internal package. Three reasons:

1. **Build isolation during parallel construction.** At implementation
   time `internal/store` was mid-rewrite (pgx/pgvector wiring, new module
   deps not yet in `go.mod`) and did not compile; `internal/context`
   existed only as an in-flux draft. Depending on either would have broken
   the `go build ./internal/mcp/` mandate. The MCP package builds and
   tests green on stdlib alone regardless of sibling churn.
2. **Minimal surface, maximal testability.** The interface has exactly the
   six operations the eight tools need. Unit tests inject an in-memory
   fake (`fakeStore` in `server_test.go`) with the same substring-match
   semantics — no database, no network, deterministic.
3. **Cheap future binding.** Production wiring is a thin adapter mapping
   `store.Store` methods to `Store` field-for-field (same shapes), plus
   swapping `buildContextXML` for the real `internal/context` builder —
   both are drop-in because the output shape (`<project_memory>` XML,
   `{context, token_count, budget_remaining, items_included,
   reflection_hint}` envelope) already matches the plan's §1.6 contract.

Deliberate non-goals kept out of this layer: vector ranking (lives in
`internal/store`, behind `SearchMemory`), secret-pattern rejection on
writes (lives in the daemon fileops layer over `internal/scan`), and the
1 MB / workspace-jail sandbox, which *is* enforced here (`resolvePath`)
as defense-in-depth for the daemon proxy seam.
