# ADR-035-processor-end-to-end

- **ADR ID:** ADR-035-processor-end-to-end
- **Date:** 2026-09-17
- **Author:** issue-35 (processor-e2e subagent)
- **Issue:** #35 Processor end-to-end (Embed, ConfirmAfter sweep, promotion, budget halt)
- **Status:** Accepted

## Context

Issue #10 built the Memory Processor as a pure, testable core behind the
`ProcessorStore` seam — but left five wires unconnected: the store seam had
only the test fake (no real implementation); dedup called
`IsNearDuplicate(m.Content, nil, confirmed)` with a hardcoded nil embedding
so the vector path was unreachable; `ConfirmAfter` was computed and handed
to `SaveProposed` but never consumed by any timer or sweep; the plan §2.7
promotion signal (`CountKeySessionsDB`, `PromotionCandidates` in
`internal/store/sessions.go`) was never called post-flush; and the
governance ceiling (`Budget.Halted`, `Ledger.RecordBatch` in
`internal/governance/governance.go`) was specified only as a future mapping
in ADR-026, never wired into `FlushSession`/`CheckIdle`.

Constraints: touch only `internal/daemon/processor.go` (+ test), a NEW
`internal/store/memory_transitions.go` (+ test), append-only additions to
`internal/store/sessions.go` (+ test additions), and docs. `governance.go`,
`builder.go` and `memory.go` are read-only. `memory_items` has no
`confirm_due_at` column and no migration may be added here.

## Options Considered

1. **Optional `Embedder` interface with token fallback (chosen).**
   `Embed(ctx, text) ([]float32, error)` is implemented by `StubLLMClient`
   (scripted vector/error) and `OllamaClient` (`POST /api/embeddings`, plain
   net/http, no SDK). `processBatch` resolves the candidate vector via
   `embedFor` only when the injected LLM implements the interface; any
   absence (unimplemented, error, empty vector) yields nil and the existing
   `TokenCosineSimilarity` path engages. No signature on `LLMClient`
   changes, so existing OpenAI/Anthropic callers keep working.
2. **Require embeddings on `LLMClient`.** Would force every provider caller
   to implement embedding up front and break the one-method seam from #10.
   Rejected.
3. **Confirm sweep with per-row tier parsed from the `source` tag
   (chosen).** With no migration allowed, `SaveProposed` encodes the
   processor's tier (`FormatConfirmSource`: 1h explicit / 4h high-conf /
   24h default) into `source`, and `ConfirmDue` selects PROPOSED
   id/source/created_at rows, parses each tier (`ParseConfirmAfter`,
   default `ConfirmDueAfterDefault`), and confirms only due ids
   (`created_at + tier <= now`). Durable across restarts, honors fast
   tracks, zero schema change.
4. **Uniform 24h sweep (`created_at <= now-24h`).** Simpler but silently
   delays 1h/4h rows to 24h — violates the §2.8 timers the issue demands be
   consumed. Rejected as the steady state (kept only as the fallback for
   rows whose tag is missing/unparseable).
5. **Halt gating with retain-buffer semantics (chosen).** `FlushSession`
   and `CheckIdle` evaluate `Budget.Halted(ledger.Snapshot(clock))` before
   touching any buffer; on halt they return `ErrHalted` (wrapping the
   tripped-axis reason) with buffers fully retained — the same
   retain-on-error contract as LLM failures. The confirm sweep still runs
   under halt (token-free). Missing budget or ledger disables gating (zero
   config = unlimited, matching `Budget{}` semantics).
6. **Best-effort post-flush promotion (chosen).** After a successful flush,
   distinct proposed keys are counted (`CountKeySessions`) and keys at
   `>= PromotionThreshold` (3, mirroring `store.PromotionThreshold`) are
   flipped via `PromoteKey` (NULL-ing `session_id`, level → project).
   Count/promote errors never fail the flush — extraction is already
   durable, and the next flush retries. Skipped entirely when no projectID
   is wired, so pre-#35 callers see zero behavior change.

## Decision

- `internal/daemon/processor.go`: `Embedder` + `StubLLMClient.Embed` +
  `OllamaClient.Embed`; `ProposedMemory.Embedding`; extended
  `ProcessorStore` (`ConfirmDue`/`CountKeySessions`/`PromoteKey` with
  stdlib-only signatures identical to the store side);
  `PromotionThreshold = 3`; `ErrHalted`; `projectID`/`budget`/`ledger`
  fields with `WithProcessorProjectID/Budget/Ledger` options;
  `halted()` + `embedFor()` helpers; `processBatch` threads embeddings and
  records `Ledger.RecordBatch(EstimateTokensFromChars(...))` per ADR-026;
  `promoteKeys` post-flush (sorted `res.Promoted`); `SweepConfirms`;
  `FlushSession`/`CheckIdle` halt gates with retain-buffer semantics.
- `internal/store/memory_transitions.go` (NEW): CHECK-mirroring pure
  validators (content/level/scope/confidence/status-DAG, statuses
  normalized case-insensitively); `FormatConfirmSource`/`ParseConfirmAfter`
  tier-tag round-trip; `IsConfirmDue`; `BuildSaveProposedSQL`
  (status always the `'PROPOSED'` literal, tier in `source`, NULL
  embedding when absent); `BuildListProposedSQL`/`BuildConfirmIDsSQL`
  (idempotent `status = 'PROPOSED'` guard, TEXT comparison for `ANY($1)`);
  `BuildPromoteKeySQL` (`session_id = NULL`, `level = 'project'`,
  idempotent `IS NOT NULL` guard); `BuildSetStatusSQL` (expected-status
  predicate, zero rows → `ErrNotFound`); `MemoryStore` over `DBTX` with
  `SaveProposed`/`ConfirmDue`/`PromoteKey`/`SetStatus`.
- `internal/store/sessions.go` (append-only): `CountKeySessions` seam
  alias of `CountKeySessionsDB` and `SessionStore.PromoteKey` reusing
  `BuildPromoteKeySQL` — `SessionStore` now structurally satisfies the
  promotion half of `ProcessorStore`.

## Why (Rationale)

Every acceptance maps to a passing test, fake-LLM + fake-store + scripted
DBTX, no network, no live Postgres: `TestProcessEmbedThreading` (vector
dedup, token fallback, embed-error tolerance, vector riding the proposal);
`TestProcessConfirmSweep` (direct + via `CheckIdle`, non-designated no-op);
`TestProcessPromotionPostFlush` (threshold met/below, unwired projectID,
promote-error tolerance); `TestProcessHaltedRetainsBuffer` (flush + idle
retain buffers, zero LLM calls, sweep still runs, unlimited control);
`TestProcessLedgerRecordsBatch` (ADR-026 accounting);
`TestTransitions*` (10 tests: CHECK mirrors incl. 20/19-char boundary,
status DAG incl. case-insensitivity, tier round-trip, due predicate,
all four SQL builders, validation-blocks-DB, tiered sweep selecting
exactly the due ids, empty-sweep issues no UPDATE, promote + zero-row
`ErrNotFound`); `TestSessionCountKeySessionsAlias` +
`TestSessionPromoteKey*` (alias parity, NULL-ing SQL shape, validation
blocks DB). Verification is green: `go build ./...` OK,
`go vet ./internal/daemon/ ./internal/store/` clean,
`go test ./internal/daemon/ ./internal/store/` PASS (see
`docs/issues/ISSUE-35.md`).

## Consequences

- No `go.mod`/`go.sum` changes (new import is the local stdlib-only
  `internal/governance`).
- `memory_items` follow-up migration (out of scope): a dedicated
  `confirm_due_at TIMESTAMPTZ` column to replace the `source`-tag tier
  encoding; `ParseConfirmAfter` already isolates the migration to one
  read path.
- Follow-ups: thin server-wiring adapter composing `MemoryStore` +
  `SessionStore` behind `daemon.ProcessorStore` with the real project
  UUID; real tokenizer replacing `EstimateTokensFromChars` at the
  processor call site; per-project budgets; 80%-of-cap alerting.

## Alternatives Rejected

See Options 2 and 4 above: mandatory embeddings (breaks the #10 seam),
uniform-24h sweep (violates fast-track timers). Direct `governance` struct
coupling beyond the option-injected pointers was avoided — nil budget or
nil ledger cleanly disables gating/accounting.
