# ISSUE-26 — Cost & Governance (local LLM cost tracking, token quotas, health monitoring)

- **Status:** Done
- **Assignee:** ParthKhandelwal537
- **Scope:** `internal/governance/` (new package: `governance.go` + test) +
  `docs/` only. `internal/daemon/processor.go` and
  `internal/context/builder.go` untouched (interfaces local, mapping in ADR).
- **Plan refs:** `implementation-plan.md` §1.7 (confidence decay), §§2.7–2.8
  (promotion/demotion + confirmation flow).

## What was built

`internal/governance/governance.go` (`package governance`, stdlib only:
`time`, `math`, `strings`) — cost tracking, budget ceilings, the
confidence-decay auditor, the archival selector, and dashboard analytics:

| Piece | Behavior |
|---|---|
| `Rates` / `CostFor` | USD per 1K prompt/completion tokens; `ZeroRates()` (local Ollama: tokens counted, $0), `DefaultRates()` placeholder the operator overrides |
| `EstimateTokensFromChars` | chars/4 estimate until a real tokenizer lands (plan §1.6 rule of thumb) |
| `Ledger` (`RecordBatch` / `Totals` / `Snapshot` / `Batches`) | Append-only per-flush usage with fake-clock stamping; UTC day/month windows for enforcement |
| `Budget.Halted` | Daily/monthly token + USD ceilings; `usage >= cap` on ANY set axis halts (boundary halts); `≤0` = unlimited; returns `(bool, reason)` |
| `EffectiveConfidence` / `DaysSince` | Plan §1.7 formula (`base·0.95^(days/30)`), re-implemented locally per the decoupling rule |
| `MemoryRef` | Auditor boundary DTO (key, base conf, last-access + creation fallback, use count, status) |
| `ShouldFlagForReview` / `FlagForReview` | eff-conf **< 0.2 AND ≥180d** since last touch; zero age never flags |
| `SelectForArchive` | SUPERSEDED **and** stale only; CONFIRMED never archived here; ARCHIVED never reselected |
| `Analytics` (`RecordEvents`/`DailyEvents`, `RecordProposed`/`RecordConfirmed`/`ExtractionAccuracy`, `GrowthRate`/`EventGrowth`) | Daily volumes, accuracy (0 proposals ⇒ 0/"—"), growth with 0-baseline ⇒ 0 or +Inf |

## Decisions

- See `docs/decisions/ADR-026-cost-governance.md` (new-package-over-edit,
  `>=` halt boundary, both-conditions staleness with strict-`<` confidence
  and `>=`-180d age, in-memory ledger with store persistence deferred, full
  processor/builder wiring map with zero edits).
- One compile issue found and fixed in-test: composite literal `Budget{}`
  directly in an `if` header trips the parser — hoisted to a variable.

## Verification

- `go build ./...` — **OK** (whole repo; the `internal/store` constant
  collision noted in ISSUE-10 is resolved)
- `go vet ./internal/governance/` — **clean**
- `go test ./internal/governance/ -v` — **10/10 PASS**, fake clock, no
  DB/network:
  `TestCostMath`, `TestEstimateTokensFromChars`,
  `TestBudgetHaltBoundaries` (all four axes at-cap halt, under-cap clears,
  zero-Budget unlimited, partial budget), `TestBudgetHaltViaLedger`
  (495/500 clear → 500/500 halts), `TestDecayFlagRule` (stale/low flags;
  fresh/low, old/high, unknown-age do not; CreatedAt fallback),
  `TestDecayFlag180DayEdge` (exactly-180d flags, 179d does not,
  above-threshold-at-200d does not, strict-0.2 pinned),
  `TestFlagForReviewBatch`, `TestArchivalSelector` (victim + lowercase
  status picked; fresh-superseded, stale-confirmed/proposed, archived
  excluded), `TestAnalyticsCounters`, `TestGrowthRateEdges`

## Follow-ups

1. Processor wiring per ADR-026 mapping: `RecordBatch` after `LLM.Complete`
   in `processBatch`; `Budget.Halted` gate in `FlushSession`/`CheckIdle`
   (retain-buffer-on-halt, health event).
2. Store owners: persist ledger batches, run auditor/archival job on cron
   via the `MemoryRef` mapping, host `Analytics` behind dashboard endpoints.
3. Real tokenizer replacing `EstimateTokensFromChars`; per-project budgets;
   80%-of-cap alerting webhook.
