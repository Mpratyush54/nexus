// Package governance implements issue #26 (Cost & Governance, plan §§1.7,
// 2.7–2.8): local LLM cost tracking, token-budget ceilings, the
// confidence-decay auditor, the archival selector, and dashboard analytics.
//
// Scope and ownership: ONLY this package (+ its test) and docs/ may be
// touched by issue #26. internal/daemon/processor.go and
// internal/context/builder.go are READ-ONLY — the seam points where the
// processor and builder will call into this package are described in
// docs/decisions/ADR-026-cost-governance.md ("processor/builder mapping"),
// and every shared formula is re-implemented here behind a local interface
// so this package stays decoupled (same pattern as processor.ConfirmedMemory
// vs store.MemoryItem, context.Item vs store.MemoryItem).
//
// Everything here is pure stdlib (time, math, strings) with an injectable
// Clock, so every test runs on a fake clock with no DB and no network.
package governance

import (
	"math"
	"strings"
	"time"
)

// Clock is the injectable time source (tests pin it; production passes
// time.Now). Mirrors the daemon package's Clock type without importing it.
type Clock func() time.Time

// ---------------------------------------------------------------------------
// Token / cost model
// ---------------------------------------------------------------------------

// Rates prices LLM usage in USD per 1,000 tokens. Local (Ollama) extraction
// is marginal-cost zero — pass ZeroRates() — but the ledger still records
// token counts so budget ceilings in tokens keep working. Hosted fallbacks
// (user's own OpenAI/Anthropic key over plain HTTP, same one-method seam as
// the processor's LLMClient) use operator-configured rates; DefaultRates is
// a documented placeholder, not a quote.
type Rates struct {
	InputPer1K  float64 // USD per 1K prompt tokens
	OutputPer1K float64 // USD per 1K completion tokens
}

// ZeroRates prices local extraction: tokens counted, USD always 0.
func ZeroRates() Rates { return Rates{} }

// DefaultRates is a placeholder hosted-LLM price point. The operator MUST
// override it with their provider's current price; it exists only so cost
// math has a sane non-zero default for dashboards.
func DefaultRates() Rates { return Rates{InputPer1K: 0.0015, OutputPer1K: 0.002} }

// CostFor estimates one batch's USD: prompt/1000*in + completion/1000*out.
// Negative token counts clamp to 0 (defensive; callers never send them).
func (r Rates) CostFor(promptTokens, completionTokens int) float64 {
	if promptTokens < 0 {
		promptTokens = 0
	}
	if completionTokens < 0 {
		completionTokens = 0
	}
	return float64(promptTokens)/1000*r.InputPer1K + float64(completionTokens)/1000*r.OutputPer1K
}

// EstimateTokensFromChars approximates token counts from character length
// (~4 chars/token, the same rule of thumb as the plan §1.6 budget note:
// 4000 chars ≈ 1000 tokens). The processor mapping (see ADR-026) uses this
// until a real tokenizer is wired: prompt ≈ len(extraction prompt),
// completion ≈ len(LLM response).
func EstimateTokensFromChars(chars int) int {
	if chars <= 0 {
		return 0
	}
	return (chars + 3) / 4
}

// BatchUsage is one recorded extraction batch: the processor's per-flush
// token counts plus the estimated USD at the ledger's rates.
type BatchUsage struct {
	At               time.Time
	PromptTokens     int
	CompletionTokens int
	CostUSD          float64
}

// Tokens returns the batch's total token count.
func (b BatchUsage) Tokens() int { return b.PromptTokens + b.CompletionTokens }

// Ledger accumulates per-batch usage. It is append-only and clock-pinned:
// RecordBatch stamps with the injected clock so tests are deterministic.
type Ledger struct {
	clock   Clock
	rates   Rates
	batches []BatchUsage
}

// NewLedger builds a ledger. A nil clock becomes time.Now.
func NewLedger(rates Rates, clock Clock) *Ledger {
	if clock == nil {
		clock = time.Now
	}
	return &Ledger{clock: clock, rates: rates}
}

// RecordBatch records one extraction batch (processor flush) and returns
// the stamped entry. It never fails and never blocks extraction —
// enforcement is the Budget's job (Halted), consulted before the LLM call.
func (l *Ledger) RecordBatch(promptTokens, completionTokens int) BatchUsage {
	if promptTokens < 0 {
		promptTokens = 0
	}
	if completionTokens < 0 {
		completionTokens = 0
	}
	b := BatchUsage{
		At:               l.clock(),
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		CostUSD:          l.rates.CostFor(promptTokens, completionTokens),
	}
	l.batches = append(l.batches, b)
	return b
}

// Batches returns a copy of all recorded batches (oldest first).
func (l *Ledger) Batches() []BatchUsage {
	return append([]BatchUsage(nil), l.batches...)
}

// Totals sums all recorded tokens and USD.
func (l *Ledger) Totals() (tokens int, usd float64) {
	for _, b := range l.batches {
		tokens += b.Tokens()
		usd += b.CostUSD
	}
	return tokens, usd
}

// UsageSnapshot is the ledger's day/month window totals around a reference
// instant, the exact input to Budget.Halted.
type UsageSnapshot struct {
	DayTokens   int
	MonthTokens int
	DayUSD      float64
	MonthUSD    float64
}

// Snapshot sums batches in the reference instant's calendar day (UTC) and
// calendar month (UTC). UTC keeps day/month boundaries deterministic across
// daemon timezones; the dashboard renders them in local time.
func (l *Ledger) Snapshot(now time.Time) UsageSnapshot {
	now = now.UTC()
	y, m, d := now.Date()
	var s UsageSnapshot
	for _, b := range l.batches {
		t := b.At.UTC()
		by, bm, bd := t.Date()
		if by == y && bm == m && bd == d {
			s.DayTokens += b.Tokens()
			s.DayUSD += b.CostUSD
		}
		if by == y && bm == m {
			s.MonthTokens += b.Tokens()
			s.MonthUSD += b.CostUSD
		}
	}
	return s
}

// ---------------------------------------------------------------------------
// Budget ceilings — halt extraction at the cap
// ---------------------------------------------------------------------------

// Budget caps extraction spend. Any cap <= 0 means "unlimited" on that
// axis, so the zero Budget halts nothing. Example from the issue: $5/mo
// (MonthlyUSDCap: 5) or 500k tokens (MonthlyTokenCap: 500_000).
type Budget struct {
	DailyTokenCap   int
	MonthlyTokenCap int
	DailyUSDCap     float64
	MonthlyUSDCap   float64
}

// Halted reports whether extraction must stop BEFORE the next LLM call.
// The rule is usage >= cap on ANY capped axis (boundary halts: reaching the
// ceiling exactly stops extraction — acceptance criterion "halt on
// ceiling"). The reason names the first tripped axis for dashboards/logs.
func (b Budget) Halted(s UsageSnapshot) (bool, string) {
	if b.DailyTokenCap > 0 && s.DayTokens >= b.DailyTokenCap {
		return true, "daily token cap"
	}
	if b.MonthlyTokenCap > 0 && s.MonthTokens >= b.MonthlyTokenCap {
		return true, "monthly token cap"
	}
	if b.DailyUSDCap > 0 && s.DayUSD >= b.DailyUSDCap {
		return true, "daily USD cap"
	}
	if b.MonthlyUSDCap > 0 && s.MonthUSD >= b.MonthlyUSDCap {
		return true, "monthly USD cap"
	}
	return false, ""
}

// ---------------------------------------------------------------------------
// Confidence-decay auditor (plan §§1.7, 2.7)
// ---------------------------------------------------------------------------

// Decay constants mirror the Context Builder (plan §1.7:
// effective = base * 0.95^(days/30)) without importing it — same
// decoupling rule as the processor's VecCosine vs the store curve.
const (
	DecayBase       = 0.95
	DecayWindowDays = 30.0
)

// Staleness rule (plan §2.7 row "Any → archived", issue #26 acceptance
// "stale flagged for review/archival"): effective confidence below 0.2 AND
// 180 days unaccessed.
const (
	StaleConfidenceThreshold = 0.2
	StaleAfterDays           = 180.0
)

// EffectiveConfidence applies plan §1.7 decay. Anchor points (shared with
// the builder): 0d ≈ base, 90d ≈ 86% of base, 180d ≈ 74% of base.
// Negative days clamp to 0; output clamps to [0,1].
func EffectiveConfidence(baseConfidence, daysSince float64) float64 {
	if daysSince < 0 {
		daysSince = 0
	}
	v := baseConfidence * math.Pow(DecayBase, daysSince/DecayWindowDays)
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// DaysSince returns fractional days from t to now; zero/future t clamps to
// 0 (unknown age → no decay, never a boost).
func DaysSince(t, now time.Time) float64 {
	if t.IsZero() {
		return 0
	}
	d := now.Sub(t).Hours() / 24
	if d < 0 {
		return 0
	}
	return d
}

// MemoryRef is the auditor's view of a memory item: the fields the stale /
// archive rules need. Owners (store/processor callers) map their row onto
// this at the boundary; the auditor never touches SQL.
type MemoryRef struct {
	Key            string
	Confidence     float64 // stored base confidence
	LastAccessedAt time.Time
	CreatedAt      time.Time // fallback age anchor when never accessed
	UseCount       int
	Status         string // PROPOSED, CONFIRMED, SUPERSEDED, ...
}

// statusName normalizes lifecycle status for comparison (store CHECK set is
// uppercase; callers may pass anything).
func statusName(s string) string { return strings.ToUpper(strings.TrimSpace(s)) }

// Status constants mirror the memory_items CHECK set (plan §1.1).
const (
	StatusProposed   = "PROPOSED"
	StatusConfirmed  = "CONFIRMED"
	StatusRejected   = "REJECTED"
	StatusSuperseded = "SUPERSEDED"
	StatusArchived   = "ARCHIVED"
)

// lastTouch returns the age anchor: last access, else creation, else zero
// (unknown age → not flaggable; the auditor must prove staleness, never
// assume it).
func (m MemoryRef) lastTouch() time.Time {
	if !m.LastAccessedAt.IsZero() {
		return m.LastAccessedAt
	}
	return m.CreatedAt
}

// ShouldFlagForReview implements the issue #26 stale rule: effective
// confidence STRICTLY below 0.2 AND days-since-last-touch at least 180.
// Both conditions are required — a low-confidence item used yesterday is
// noise, not rot; a 2-year-old item with confidence 1.0 decays only to
// ~0.28 and stays unflagged. Boundary behavior (pinned by tests): exactly
// 180 days flags; effective confidence exactly 0.2 does NOT (strict <).
func ShouldFlagForReview(m MemoryRef, now time.Time) bool {
	anchor := m.lastTouch()
	if anchor.IsZero() {
		return false
	}
	days := DaysSince(anchor, now)
	if days < StaleAfterDays {
		return false
	}
	return EffectiveConfidence(m.Confidence, days) < StaleConfidenceThreshold
}

// FlagForReview filters items to those needing human review/archival
// triage. Order is stable (input order preserved).
func FlagForReview(items []MemoryRef, now time.Time) []MemoryRef {
	var out []MemoryRef
	for _, m := range items {
		if ShouldFlagForReview(m, now) {
			out = append(out, m)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Archival job selector (plan §2.7)
// ---------------------------------------------------------------------------

// SelectForArchive picks the archival job's victims: SUPERSEDED items that
// are also stale per ShouldFlagForReview. Freshly superseded items stay
// queryable (lineage/diff needs them); only rot gets archived. CONFIRMED
// items are NEVER archived here even when stale — they go to human review
// via FlagForReview instead. Already-archived rows are skipped
// (idempotent reruns select nothing new).
func SelectForArchive(items []MemoryRef, now time.Time) []MemoryRef {
	var out []MemoryRef
	for _, m := range items {
		if statusName(m.Status) != StatusSuperseded {
			continue
		}
		if !ShouldFlagForReview(m, now) {
			continue
		}
		out = append(out, m)
	}
	return out
}

// ---------------------------------------------------------------------------
// Dashboard analytics
// ---------------------------------------------------------------------------

// Analytics holds dashboard counters: per-day event volume, extraction
// outcomes, and memory inventory over time. All mutations are additive;
// rates are derived on read so any window can be recomputed. Day keys are
// UTC calendar dates (YYYY-MM-DD), matching Ledger.Snapshot.
func NewAnalytics() *Analytics { return &Analytics{eventsByDay: map[string]int64{}} }

// Analytics is constructed with NewAnalytics.
type Analytics struct {
	eventsByDay map[string]int64
	proposed    int64
	confirmed   int64
}

// dayKey formats the UTC calendar date.
func dayKey(t time.Time) string { return t.UTC().Format("2006-01-02") }

// RecordEvents adds n events to a day's volume (n <= 0 is a no-op).
func (a *Analytics) RecordEvents(day time.Time, n int64) {
	if a.eventsByDay == nil {
		a.eventsByDay = map[string]int64{}
	}
	if n <= 0 {
		return
	}
	a.eventsByDay[dayKey(day)] += n
}

// DailyEvents returns one day's event volume (0 when unrecorded).
func (a *Analytics) DailyEvents(day time.Time) int64 {
	if a == nil {
		return 0
	}
	return a.eventsByDay[dayKey(day)]
}

// RecordProposed records n LLM-extracted candidates (n <= 0 no-op).
func (a *Analytics) RecordProposed(n int64) {
	if n > 0 {
		a.proposed += n
	}
}

// RecordConfirmed records n candidates that reached CONFIRMED (n <= 0
// no-op). Accuracy = confirmed / proposed.
func (a *Analytics) RecordConfirmed(n int64) {
	if n > 0 {
		a.confirmed += n
	}
}

// Proposed returns total extracted candidates; Confirmed returns total
// confirmed.
func (a *Analytics) Proposed() int64  { return a.proposed }
func (a *Analytics) Confirmed() int64 { return a.confirmed }

// ExtractionAccuracy is confirmed/proposed in [0,1]. With no proposals it
// returns 0 ("no data" — dashboards must render it as "—", never as 0%,
// see ADR-026). It can exceed 1 only if callers confirm items proposed
// before tracking began; dashboards clamp for display.
func (a *Analytics) ExtractionAccuracy() float64 {
	if a.proposed <= 0 {
		return 0
	}
	return float64(a.confirmed) / float64(a.proposed)
}

// GrowthRate is the fractional inventory change between two samples:
// (later-earlier)/earlier. Zero earlier sample: 0 when later is also 0
// (flat), +Inf when later > 0 (new inventory from nothing — dashboards
// render "new"). Negative values are contraction (archival working).
func GrowthRate(earlier, later int64) float64 {
	if earlier == 0 {
		if later == 0 {
			return 0
		}
		return math.Inf(1)
	}
	return float64(later-earlier) / float64(earlier)
}

// EventGrowth is GrowthRate over two days' volumes — the dashboard's
// "events/day trend" sparkline input.
func (a *Analytics) EventGrowth(earlierDay, laterDay time.Time) float64 {
	return GrowthRate(a.DailyEvents(earlierDay), a.DailyEvents(laterDay))
}
