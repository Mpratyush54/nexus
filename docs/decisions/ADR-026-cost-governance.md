# ADR-026-cost-governance

- **ADR ID:** ADR-026-cost-governance
- **Date:** 2026-09-17
- **Author:** ParthKhandelwal537 (issue-26 subagent)
- **Issue:** #26 Cost & Governance — local LLM cost tracking, token quotas, health monitoring
- **Status:** Accepted

## Context

The Memory Processor (issue #10) burns LLM calls on the user's device on
every session flush, with no accounting and no brake. The issue demands:
per-batch prompt/completion tokens + est. USD; budget ceilings (e.g. $5/mo
or 500k tokens, daily + monthly) that HALT extraction; a confidence-decay
auditor (flag conf < 0.2 + 180d unaccessed, plan §§1.7/2.7); an archival job
(stale superseded → archive); dashboard analytics (growth rate, daily
events, extraction accuracy). Acceptance: halt on ceiling; stale items
flagged for review/archival.

Ownership constraint: only `internal/governance/` (new package) + `docs/`.
`internal/daemon/processor.go` and `internal/context/builder.go` are
READ-ONLY — integration is specified here as a mapping, not an edit.

## Options Considered

1. **New stdlib-only `internal/governance` package with local interfaces
   (chosen).** `Ledger`/`Budget`/`MemoryRef`/`Analytics` re-implement the
   §1.7 decay formula locally instead of importing `internal/context`,
   mirroring the established decoupling pattern (`processor.ConfirmedMemory`
   vs `store.MemoryItem`, `context.Item` vs `store.MemoryItem`). Zero
   `go.mod` changes, pure functions + injectable `Clock`, every test on a
   fake clock.
2. **Extend processor.go / builder.go directly.** Rejected: violates issue
   ownership, risks merge collisions with parallel wave-4 agents, and mixes
   governance policy into the extraction hot path.
3. **DB-backed accounting from day one.** Rejected: untestable without
   Postgres and unnecessary — the daemon already runs offline-first; the
   ledger is in-memory with the store-persistence seam left as a follow-up.
4. **Boundary semantics `usage > cap` (halt only when exceeded).**
   Rejected: the acceptance criterion is "halt on ceiling" — reaching the
   cap exactly must stop extraction, so the rule is `usage >= cap` on every
   axis (pinned by `TestBudgetHaltBoundaries` + `TestBudgetHaltViaLedger`).
5. **Stale rule on age alone (180d unaccessed ⇒ flag).** Rejected: plan §2.7
   requires BOTH decayed confidence < 0.2 AND disuse. A conf-1.0 item at 2
   years decays only to ~0.28 and must NOT flag (pinned by
   `TestDecayFlagRule`). Strict `<` on confidence, `>=` on the 180-day edge
   (pinned by `TestDecayFlag180DayEdge`).

## Decision

Implemented `internal/governance/governance.go` (`package governance`,
stdlib only: `time`, `math`, `strings`):

- **Cost:** `Rates` (USD/1K in+out; `ZeroRates()` for local Ollama,
  `DefaultRates()` documented placeholder the operator must override) +
  `Rates.CostFor` + `Ledger.RecordBatch` (per-flush prompt/completion
  tokens + stamped USD) + `Ledger.Snapshot` (UTC day/month windows) +
  `EstimateTokensFromChars` (chars/4 until a real tokenizer lands).
- **Budgets:** `Budget{DailyTokenCap, MonthlyTokenCap, DailyUSDCap,
  MonthlyUSDCap}` (≤0 = unlimited per axis) + `Budget.Halted(snapshot)`
  returning `(bool, reason)` with `>=` boundary semantics.
- **Auditor:** `EffectiveConfidence`/`DaysSince` (same §1.7 formula as the
  builder) + `MemoryRef` (boundary DTO: key, base confidence, last-access /
  creation fallback, use count, status) + `ShouldFlagForReview` (eff < 0.2
  AND ≥180d since last touch; zero age ⇒ never flags — staleness must be
  proven) + `FlagForReview` batch filter.
- **Archival:** `SelectForArchive` — SUPERSEDED **and** stale only.
  Freshly superseded rows stay queryable (lineage/diff); CONFIRMED rows
  never archive here (human review instead); ARCHIVED rows never reselected
  (idempotent reruns).
- **Analytics:** `Analytics` (`RecordEvents`/`DailyEvents`,
  `RecordProposed`/`RecordConfirmed`/`ExtractionAccuracy` with 0-proposals
  ⇒ 0 rendered as "—", `GrowthRate` with 0-baseline ⇒ 0 or +Inf,
  `EventGrowth` day-over-day).

## Why (Rationale)

This is the only combination meeting every acceptance criterion within the
ownership box: ceiling halt incl. exact-boundary is proven by
`TestBudgetHaltBoundaries` (all four axes at-cap halt, under-cap clears,
zero-Budget unlimited) and `TestBudgetHaltViaLedger` (495/500 clear →
500/500 halts with reason); cost math by `TestCostMath` (0.004 USD case,
ledger totals, day/month windowing); the decay-flag rule incl. the 180-day
edge by `TestDecayFlagRule` + `TestDecayFlag180DayEdge` (exactly-180d flags,
179d does not, above-threshold-at-200d does not, strict-0.2 pinned);
archival selection by `TestArchivalSelector` (victim picked, fresh/stale-
confirmed/proposed/archived excluded, case-insensitive status); analytics by
`TestAnalyticsCounters` + `TestGrowthRateEdges`. Verification is green:
`go build ./...` OK, `go vet ./internal/governance/` clean,
`go test ./internal/governance/` 10/10 PASS (see `docs/issues/ISSUE-26.md`).

## Processor / Builder Mapping (no edits — future wiring)

- **Cost recording:** at the end of `Processor.processBatch`, after
  `p.llm.Complete` returns `resp` for `prompt`, call
  `govLedger.RecordBatch(EstimateTokensFromChars(len(prompt)),
  EstimateTokensFromChars(len(resp)))`. Prompt and response strings are both
  in scope there today; no signature changes needed.
- **Ceiling enforcement:** at the top of `Processor.FlushSession` (and in
  `CheckIdle` before flushing), evaluate
  `budget.Halted(govLedger.Snapshot(govClock()))`; on halt, skip the flush
  (buffer retained — same retain-on-error semantics as LLM failures) and
  emit a dashboard/health event. The `Ledger` + `Budget` live on the
  `Processor` struct, injected via a `ProcessorOption`.
- **Decay auditor:** the archival background job (store owner) maps each
  `memory_items` row onto `governance.MemoryRef{Key, Confidence,
  LastAccessedAt: last_used_at, CreatedAt: created_at, UseCount: use_count,
  Status}` and calls `FlagForReview` (review queue) + `SelectForArchive`
  (status → ARCHIVED). `use_count` reset on serve (builder already tracks
  serving) keeps decay honest.
- **Analytics feed:** the server/dashboard layer owns an `Analytics`
  instance fed from the events table (`RecordEvents` per day per type) and
  the memory lifecycle (`RecordProposed` on PROPOSED insert,
  `RecordConfirmed` on confirm); `ExtractionAccuracy`/`EventGrowth` render
  directly. No builder changes — `BuildStats` already reports
  items-included/dropped per call if per-request accuracy is ever wanted.

## Consequences

- No `go.mod`/`go.sum` changes (stdlib only).
- `internal/store` gains future work, not edits: persist ledger batches
  (durable spend history), run the auditor/archival job on a cron, host the
  `Analytics` instance behind dashboard endpoints.
- Follow-ups: real tokenizer for prompt/completion counts (replace
  `EstimateTokensFromChars` at the processor call site); `RootCause`-style
  LLM enrichment is out of scope; per-project budgets (v1 is a single
  global `Budget`); alerting/webhook when usage crosses 80% of a cap.

## Alternatives Rejected

See Options 2–5 above: direct processor/builder edits (ownership +
collision risk), DB-backed accounting (untestable offline, premature),
`>` halt boundary (fails "halt on ceiling"), age-only staleness (flags
durable high-confidence memories, contradicts §2.7).
