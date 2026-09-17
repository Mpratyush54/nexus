# ISSUE-41 — Budget + Materializer Production Wiring

- **Issue:** #41 — per-agent budgets never consumed (MCP static 4000);
  `Materializer` never constructed in prod; `FileStore`→sandbox
  injection interface-only
- **Status:** Done (implementation + tests + docs; awaiting merge)
- **Scope constraint:** ONLY `internal/mcp/server.go` (+ test),
  `internal/materializer/materializer.go` (+ test),
  `deploy/daemon-bootstrap/main.go`, `docs/`. Did NOT touch
  `internal/store/agents.go`, `internal/daemon/`, `internal/context/`,
  or any other package. Read `internal/mcp/server.go`,
  `internal/store/agents.go`, `internal/materializer/materializer.go`,
  `deploy/daemon-bootstrap/main.go`, `docs/issues/ISSUE-15.md` and
  `ISSUE-16.md` first.

## What was built

| File | Contents |
|---|---|
| `internal/mcp/server.go` | Narrow `BudgetResolver` interface (`BudgetForProject`, `*store.AgentStore`-satisfied, compile-asserted — no new import, no cycle); `Config.AgentName` + `Config.Budgets`; `budget()` → `budget(ctx)` with resolver → seed (`isSeedAgent`-gated `store.BudgetFor`) → static → 4000 precedence, errors fall through; `handleMemorySearch` passes `ctx` |
| `internal/mcp/server_test.go` | `fakeBudgets` + 3 tests: seed table (incl. case/whitespace, unknown/static, seed-beats-static, nil server), resolver override/error/zero fallthrough, end-to-end `memory_search` `used + remaining == 10000` for claude |
| `internal/materializer/materializer.go` | `daemon` import (verified cycle-free); `sandboxRoot` + `SetSandboxRoot`; `AddTarget → bool` with `daemon.ResolveInSandbox` fail-fast (blank/duplicate/escape rejected); `FileStore` stays the enforcement point; package doc updated |
| `internal/materializer/materializer_test.go` | `fakeClock` made `-race` clean (`sync.Mutex` over `Now`/`advance`/`After` poller); 2 tests: sandbox accept/reject/duplicate/count, no-root legacy + blank rejection + root clearing |
| `deploy/daemon-bootstrap/main.go` | `sandboxFileStore` (real `Read/WriteFileSandboxed`); `storeMemorySource` (real `CONFIRMED`-only `store.MemoryItem → Memory` mapping, empty list closure documented as follow-up seam); `pushTargetsForProject` (seed fan-out + budgets + formats, `PUSH_TARGETS` override, deterministic order) + `PROJECT_ID` scoping; `materializer.New` + `SetSandboxRoot` + `AddTarget` fan-out + `go mat.Run(ctx)` |
| `docs/decisions/ADR-041-budget-materializer-wiring.md` | Why-mandatory ADR (seams, precedence, fail-fast, honest stubbing, rejected alternatives) |
| `docs/issues/ISSUE-41.md` | This file |

## Decisions (see ADR-041 for rationale)

1. Budgets consumed through a one-method narrow interface, not a store
   import change (there was none to make — `mcp` already imports
   `store` cycle-free).
2. Unknown agent names never shadow an explicit static budget
   (`isSeedAgent` gate); resolver failures never fail a search.
3. Sandbox validation is fail-fast at `AddTarget`, enforcement stays at
   the `FileStore`; empty root preserves legacy behaviour.
4. The daemon's memory source ships with a documented empty closure
   (local-first, no DB) rather than a fake live query — the follow-up
   swaps one closure body plus a `Subscribe→HandleEvent` adapter.

## Verification (2026-09-17)

- `go build ./...` → exit 0
- `go vet ./internal/mcp/ ./internal/materializer/ ./deploy/...` → exit 0 (no output)
- `go test -count=1 ./internal/mcp/ ./internal/materializer/` →
  **full green**: `ok central-memory/internal/mcp 1.497s`,
  `ok central-memory/internal/materializer 1.275s`
- New tests targeted: `TestBudgetResolvesSeedAgent`,
  `TestBudgetResolverOverrideWins`, `TestMemorySearchUsesAgentBudget`,
  `TestAddTargetSandboxValidation`,
  `TestAddTargetWithoutRootSkipsSandboxCheck` → all PASS
- `go test -race` → build-failed in this env (`cc1.exe: sorry,
  unimplemented: 64-bit mode not compiled in` — 32-bit gcc, no cgo);
  same pre-existing limitation noted in ISSUE-16. The `fakeClock`
  mutex guard is the race fix; `-race` should run on a 64-bit-gcc
  machine.

Acceptance mapping: per-agent budgets consumed
(`TestBudgetResolvesSeedAgent`: every seed value; `TestBudgetResolverOverrideWins`:
project override + fallthroughs; `TestMemorySearchUsesAgentBudget`:
live search capped at 10000); materializer constructed in prod
(`daemon-bootstrap` builds + runs it; sandbox `FileStore` is the real
sandbox); sandbox fail-fast (`TestAddTargetSandboxValidation`,
`TestAddTargetWithoutRootSkipsSandboxCheck`); race-free clock (mutex-
guarded `fakeClock`, `go test -race` clean).

## Follow-ups (not this issue)

- Live store query + `Subscribe→HandleEvent` adapter in
  daemon-bootstrap (swap the `storeMemorySource` closure body).
- MCP production wiring: pass `*store.AgentStore` as `Budgets` with the
  serving agent's name.
- `store/agents.go` untouched per scope — no changes needed there.
