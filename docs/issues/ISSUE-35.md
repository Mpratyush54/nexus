# ISSUE-35 — Processor end-to-end (Embed, ConfirmAfter sweep, promotion, budget halt)

- **Status:** Done
- **Assignee:** issue-35 (processor-e2e subagent)
- **Scope:** `internal/daemon/processor.go`, `internal/daemon/processor_test.go`,
  NEW `internal/store/memory_transitions.go` (+ test), append-only
  `internal/store/sessions.go` (+ test additions), `docs/` only.
  `governance.go`, `builder.go`, `memory.go` untouched (read-only).
- **Plan refs:** `implementation-plan.md` §§2.7 (promotion), 2.8
  (confirmation flow); §1.1 (CHECK constraints); ADR-026
  (processor/governance mapping, now wired).

## What was built

| Piece | Behavior |
|---|---|
| `Embedder` + `embedFor` | Optional `Embed(ctx, text)` seam; `StubLLMClient` (scripted vector/error) + `OllamaClient` (`POST /api/embeddings`, plain net/http, no SDK); absent/error/empty → nil → token-cosine fallback, never a flush failure |
| `processBatch` threading | Candidate vector replaces the hardcoded nil in `IsNearDuplicate`; vector rides `ProposedMemory.Embedding` (nil under fallback → store NULLs it for backfill) |
| Cost accounting | `Ledger.RecordBatch(EstimateTokensFromChars(len(prompt)), EstimateTokensFromChars(len(resp)))` per successful extraction (ADR-026 mapping) |
| `ConfirmDue` sweep | `SweepConfirms` (token-free, runs even under halt) + `CheckIdle` integration; store confirms PROPOSED rows whose `created_at` + source-tag tier (1h/4h/24h, default 24h) ≤ now; empty sweep issues no UPDATE |
| Promotion post-flush | Distinct proposed keys counted (`CountKeySessions`); keys in ≥3 sessions flipped via `PromoteKey` (NULL-ing `session_id`, level → project); best-effort (errors never fail the flush); skipped when no projectID wired; sorted `res.Promoted` |
| Halt gating | `FlushSession`/`CheckIdle` evaluate `Budget.Halted(Ledger.Snapshot(clock))` first; halt returns `ErrHalted` + reason with buffers fully retained (zero LLM calls); nil budget/ledger = unlimited |
| `MemoryStore` (NEW file) | `SaveProposed` (CHECK-mirroring validation, `'PROPOSED'` literal, tier in `source`), `ConfirmDue`, `PromoteKey`, `SetStatus` (PROPOSED→CONFIRMED/REJECTED, CONFIRMED→SUPERSEDED, zero rows → `ErrNotFound`) over `DBTX` |
| `sessions.go` seam | Append-only `CountKeySessions` alias + `PromoteKey` (shared `BuildPromoteKeySQL`) |

## Decisions

- See `docs/decisions/ADR-035-processor-end-to-end.md` (optional-Embedder
  vs mandatory, source-tag tier encoding vs uniform-24h sweep vs new
  migration, retain-buffer halt, best-effort promotion).
- One schema constraint worked around without a migration: `memory_items`
  has no `confirm_due_at` column, so the per-item tier travels in the
  `source` tag (`processor:confirm_after=<duration>`, parseable back,
  24h fallback) — a dedicated column is filed as the follow-up migration.
- Store structs satisfy the new `ProcessorStore` methods structurally
  (identical stdlib-only signatures); the composing adapter lives in the
  server wiring (follow-up).

## Verification

- `go build ./...` — **OK**
- `go vet ./internal/daemon/ ./internal/store/` — **clean**
- `go test ./internal/daemon/ ./internal/store/` (full packages) — **PASS**,
  no regressions (all 12 pre-existing `TestProcess*` + 4 pre-existing
  promotion/visibility session tests still green)
- New coverage, all PASS: `TestProcessEmbedThreading`,
  `TestProcessConfirmSweep`, `TestProcessPromotionPostFlush`,
  `TestProcessHaltedRetainsBuffer`, `TestProcessLedgerRecordsBatch`;
  `TestTransitionsValidateChecks`, `TestTransitionsStatusEdges`,
  `TestTransitionsConfirmTierRoundTrip`, `TestTransitionsIsConfirmDue`,
  `TestTransitionsSaveProposedSQL`, `TestTransitionsPromoteKeySQL`,
  `TestTransitionsConfirmSQL`, `TestTransitionsSaveProposedValidationBlocksDB`,
  `TestTransitionsConfirmDueSweep`,
  `TestTransitionsConfirmDueEmptyIssuesNoUpdate`,
  `TestTransitionsPromoteKey`, `TestTransitionsSetStatus`;
  `TestSessionCountKeySessionsAlias`, `TestSessionPromoteKeyNullsSessionID`,
  `TestSessionPromoteKeySQLShape`,
  `TestSessionPromoteKeyValidationBlocksDB` — fake LLM + fake store +
  scripted DBTX, no network, no live Postgres.

## Follow-ups

1. Server-wiring adapter composing `MemoryStore` + `SessionStore` behind
   `daemon.ProcessorStore` with the real project UUID.
2. `confirm_due_at TIMESTAMPTZ` migration replacing the source-tag tier
   encoding (read path already isolated in `ParseConfirmAfter`).
3. Real tokenizer for prompt/completion counts; per-project budgets;
   80%-of-cap alerting (per ADR-026).
4. `RootCause` LLM enrichment for episode drafts (carried over from #10).
