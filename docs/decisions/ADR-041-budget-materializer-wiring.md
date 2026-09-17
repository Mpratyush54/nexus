# ADR-041 — Budget + Materializer Production Wiring

- **ADR ID:** ADR-041-budget-materializer-wiring
- **Date:** 2026-09-17
- **Issue:** #41 (per-agent budgets never consumed; materializer never
  constructed in prod; FileStore→sandbox injection interface-only)
- **Status:** Accepted

## Context

Issues #15 (agent registry: seed budgets, `BudgetFor`,
`BudgetForProject`, `PushTargets`) and #16 (materializer core: debounce,
render, merge, `FileStore` seam) each shipped tested but unwired:
`internal/mcp/server.go` capped `memory_search` at a static
`Config.BudgetChars` (default 4000) so a Claude session never saw its 10k
seed budget; `Materializer` was never constructed outside tests; and the
sandbox-backed `FileStore` existed only as an interface the daemon was
"supposed to" fill in later. Three gaps, one wiring issue.

Constraints:

1. **Do NOT touch `internal/store/agents.go`** — budgets are consumed,
   not changed.
2. **No import cycles** — `mcp` already imports `store` (and store
   imports nothing from `mcp`); `materializer` must gain sandbox
   validation without creating a `materializer ↔ daemon` cycle
   (`daemon` imports only `internal/scan` plus `internal/project` in
   `harvester.go` — verified, so `materializer → daemon` is safe).
3. **No signature breakage** — `mcp.New` keeps its shape; `AddTarget`'s
   existing void callers must still compile; the race-prone test
   `fakeClock` must become `-race` clean.

## Decision

- **MCP budget resolution (`internal/mcp/server.go`).** `Config` gains
  `AgentName` + a narrow `BudgetResolver` interface (`BudgetForProject`,
  satisfied by `*store.AgentStore` — compile-time asserted, no new
  import). `budget()` becomes `budget(ctx)` with precedence: project
  resolver → seed default for known agents (`store.BudgetFor`, gated by
  a local `isSeedAgent` scan so an unknown name never shadows an
  explicit `BudgetChars`) → explicit `BudgetChars` → 4000. Resolver
  errors fall through — a budget must never fail a search.
- **Materializer sandbox check (`internal/materializer/materializer.go`).**
  `Materializer` gains a `sandboxRoot` (set via `SetSandboxRoot`, empty
  = legacy) and `AddTarget` validates `OutputPath` with
  `daemon.ResolveInSandbox`, returning `bool` (false = blank, duplicate,
  or sandbox escape). The injected `FileStore` remains the enforcement
  point; this is fail-fast config validation. The package doc records
  the now-single `daemon` import and why it is cycle-safe.
- **Production construction (`deploy/daemon-bootstrap/main.go`).**
  `run()` now builds a `Materializer` with a `sandboxFileStore`
  (`daemon.ReadFileSandboxed`/`WriteFileSandboxed` — the injection made
  real), a `storeMemorySource` (real `store.MemoryItem → Memory`
  mapping with a `CONFIRMED`-only filter; the list closure yields no
  rows yet — the daemon container is local-first with no DB, so the
  live query + `Subscribe→HandleEvent` adapter is an explicit
  follow-up), targets from `pushTargetsForProject` (seed fan-out via
  `store.PushTargets` + `BudgetFor` + seed formats, overridable with
  `PUSH_TARGETS="agent:path,..."`; `PROJECT_ID` scopes them, empty
  means no targets with a log), and `go mat.Run(ctx)` for the debounce
  loop.

## Why (Rationale)

- **Seams, not ownership:** the MCP layer consumes budgets through one
  method; the materializer validates paths through one function; the
  daemon owns both adapters. `store/agents.go` untouched as required.
- **Precedence is the bug fix stated plainly:** before, any static
  `BudgetChars` (or its 4000 default) won unconditionally; now the
  agent's own budget wins whenever the agent is known, and the project
  override wins over that.
- **Fail-fast beats fail-at-regen:** a misconfigured `OutputPath`
  (`../escape`, absolute) is rejected at registration with a bootstrap
  log line, not as a midnight write error.
- **Honest stubbing:** the empty memory source is documented at the
  declaration, in the bootstrap log path, and here — it cannot be
  mistaken for live data, and the follow-up swaps one closure body.

## Consequences

- `memory_search` output caps now vary by agent (10k/8k/6k seeds);
  consumers asserting `used + remaining == 4000` must set no `AgentName`
  (existing tests unchanged and green).
- `AddTarget` returns `bool`; callers ignoring it still compile.
- Follow-ups (not this issue): live store query + `Subscribe →
  HandleEvent` adapter in daemon-bootstrap; MCP production wiring
  passing `*store.AgentStore` as `Budgets` with the serving agent's
  name; watcher treating outside-delimiter edits as new input (per
  ADR-016).

## Alternatives Rejected

- **Per-call `agent` param on `memory_search`:** spreads registry
  knowledge into every caller; the server is already project-anchored,
  so `Config`-level resolution matches the existing shape.
- **`materializer` keeping zero `daemon` imports with a validator
  interface instead:** interface-only injection was exactly the reported
  gap; the direct import is cycle-free by verification, so indirection
  buys nothing.
- **Wiring a live DB connection into daemon-bootstrap now:** violates
  the daemon's local-first design and duplicates the server-bootstrap
  retry/migrate path for no immediate consumer.
