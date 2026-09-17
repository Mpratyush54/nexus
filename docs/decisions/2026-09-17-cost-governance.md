# Decision: Local Cost Governance, Quotas & Health Monitoring — stdlib-only pure package

Date: 2026-09-17
Scope: `internal/governance/cost.go` + `health.go` (+ `cost_test.go`, `health_test.go`)
(GitHub Mpratyush54/nexus issue #26)

## Context

The Memory Processor (`internal/daemon/processor.go`) runs on the designated
device under the user's own LLM key — zero server-side LLM cost by design. But
"local" does not mean "free": a runaway agent loop emitting high event velocity
translates directly into the user's API bill, and ungoverned memory growth
(decaying confidence, superseded items piling up) degrades retrieval quality.
Issue #26 asks for cost tracking, budget ceilings that halt extraction, a
confidence-decay auditor, an archival job, and dashboard analytics.

Read first per the brief: `processor.go` (per-batch char Budget 4000 ≈ 1000
tokens, §1.6 scale; BatchSize 20) and `internal/context/builder.go`
(TokenCount/BudgetRemaining as char lengths). Thresholds below are aligned to
both, plus `internal/context/decay.go` (geometric decay + stale rule).

## Decisions

### 1. Local tracking, no billing API — estimate from a per-model rate table

- `CostTracker` accumulates prompt/completion tokens per batch plus a USD
  estimate via `DefaultModelRates` (per-1K prompt/completion prices).
  Unknown models fall back to the zero rate rather than guessing.
- Why local: there is no server-side spend to meter (processor doc decision
  §3 — the server never sees prompts or keys), and calling a provider billing
  API from the daemon would leak usage patterns off-device and add a network
  dependency to the offline-first path. A list-price table is approximate but
  private, deterministic, and testable in CI with no key.
- Why split prompt/completion: hosted chat pricing is asymmetric (completion
  ~3–5× prompt); a single blended rate would mislead once episode summaries
  and verification re-runs skew completion-heavy.

### 2. Shared chars/4 token proxy — same units as processor and builder

- `CharsToTokens`/`TokensToChars` use 1 token ≈ 4 chars, the exact proxy behind
  `DefaultBudget = 4000` ("~1000 tokens") in both `processor.go` and
  `builder.go`.
- Why: quota accounting must speak the same units as the caps it guards. If
  governance counted real BPE tokens while the processor budgeted chars, a
  batch the processor considers "within budget" could read as over-quota (or
  vice versa). One proxy everywhere keeps `applyCaps`, `AssembleXML`, and
  `QuotaEnforcer` mutually intelligible.

### 3. Caps: $5 / 500k monthly (+ $1 / 100k daily) that halt extraction

- `QuotaEnforcer` holds daily/monthly token and USD ceilings; `Allows` names
  the first ceiling hit, `ShouldHalt` is the processor-facing predicate
  (`if enforcer.ShouldHalt(usage) { return }`), and `AllowsBatch` rejects a
  prospective batch that would cross a cap even when current usage is still ok.
  Zero on any cap means "no limit" for that dimension.
- Why caps: the failure mode is a runaway loop — an agent generating high
  event velocity at 3am. Without a ceiling, the designated processor happily
  burns the user's key until morning. $5/month and 500k tokens/month are the
  issue's example guardrails: high enough that normal use never trips them,
  low enough that a loop stops after dollars, not hundreds.
- Why halt (not just warn): a warning nobody reads at 3am is not a control.
  Extraction is batch-oriented and events are append-only, so halting loses
  nothing — unprocessed events catch up after the window resets, exactly like
  the processor's offline-stall trade-off.

### 4. Auditor thresholds 0.2 / 180d — mirrored from decay.go, both required

- `IsStale` reuses `decay.go`'s exact rule: effective confidence
  (`base * 0.95^(days/30)`) below 0.2 AND idle 180+ days. Either condition
  alone stays live. The formula and constants are duplicated (not imported) so
  `governance` stays stdlib-only and importable from the daemon without
  pulling the pgx-backed store.
- Why mirror instead of inventing: the Context Builder already serves
  decayed confidence and `FlagStale` with these numbers — a second,
  disagreeing definition of "stale" would flag items the builder still serves
  confidently (or ignore ones it has already discounted). One definition of
  stale across serving and governance.
- Why the conjunction: a weak-but-used memory (low confidence, fresh) is still
  load-bearing for an active task; a strong-but-dormant one (high confidence,
  old) is exactly what long-term memory is for. Only weak AND abandoned needs
  human review.

### 5. Archive superseded, never delete

- `SelectForArchive` picks status `SUPERSEDED` (case-insensitive) into an
  archive list; `ARCHIVED` items are never re-selected; nothing deletes.
- Why archive vs delete: a superseded memory is provenance ("we used X before
  Y, because…") — deleting it destroys the reasoning chain future audits and
  episode summaries may need, and makes "un-supersede" (revert of a bad
  replacement) impossible. Archive removes it from the serving set (so
  retrieval quality improves) while keeping the row recoverable. Storage cost
  of cold rows is negligible next to the LLM spend this package guards.

### 6. Pure funcs over DTOs — analytics included

- `MemoryDTO` decouples auditors/analytics from `store.MemoryItem` (pgx
  types); `DailyEventCounts`, `GrowthPerDay`/`RelativeGrowth`, and
  `ExtractionAccuracy` are total funcs (empty input → 0, never NaN) over plain
  values. `CostTracker` is the only stateful type, mutex-guarded for the
  background goroutine.
- Why: everything here must run deterministically in CI with no DB and no key
  (same rationale as the processor's `HeuristicProvider`), and stay importable
  from the stdlib-only daemon. Mapping `store ↔ DTO` is a plain struct copy at
  the wiring layer, deferred to the follow-up that connects the processor loop.

## Consequences

- `go build ./...` stays green; `governance` adds no `go.mod` dependencies.
- Tests cover quota halt (monthly/daily, prospective-batch, zero=unlimited),
  cost math (1K+1K at 0.005/0.015 = $0.02, unknown-model zero fallback),
  stale flag (stale / fresh-weak / dormant-strong), archival filter
  (case-insensitive SUPERSEDED only), and analytics edge cases.
- Follow-ups (not this issue): wire `ShouldHalt` into `ProcessEvents`, persist
  tracker snapshots + window resets, back `MemoryDTO` mapping with
  `internal/store`, render analytics in the dashboard.
