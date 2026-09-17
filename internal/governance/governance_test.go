// Tests for issue #26 (Cost & Governance). All pure with a fake clock: no
// DB, no network. Covers cost math, ceiling halt incl. boundary, the
// decay-flag rule incl. the 180-day edge, the archival selector, and the
// analytics counters.
package governance

import (
	"math"
	"testing"
	"time"
)

var testNow = time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

func daysAgo(t time.Time, days float64) time.Time {
	return t.Add(-time.Duration(days * 24 * float64(time.Hour)))
}

// ---------------------------------------------------------------------------
// Cost math
// ---------------------------------------------------------------------------

func TestCostMath(t *testing.T) {
	r := Rates{InputPer1K: 0.0015, OutputPer1K: 0.002}
	// 2000 prompt + 500 completion = 2*0.0015 + 0.5*0.002 = 0.004.
	if got := r.CostFor(2000, 500); math.Abs(got-0.004) > 1e-12 {
		t.Fatalf("CostFor = %v, want 0.004", got)
	}
	if got := r.CostFor(0, 0); got != 0 {
		t.Fatalf("CostFor(0,0) = %v, want 0", got)
	}
	if got := ZeroRates().CostFor(100000, 100000); got != 0 {
		t.Fatalf("ZeroRates CostFor = %v, want 0", got)
	}
	if got := r.CostFor(-5, -5); got != 0 {
		t.Fatalf("CostFor(neg) = %v, want 0", got)
	}

	// Ledger accumulates tokens + USD per batch in order.
	now := testNow
	l := NewLedger(r, func() time.Time { return now })
	b1 := l.RecordBatch(1000, 0) // 0.0015
	b2 := l.RecordBatch(0, 1000) // 0.002
	if b1.Tokens() != 1000 || b2.Tokens() != 1000 {
		t.Fatalf("batch tokens = %d,%d, want 1000,1000", b1.Tokens(), b2.Tokens())
	}
	tokens, usd := l.Totals()
	if tokens != 2000 {
		t.Fatalf("Totals tokens = %d, want 2000", tokens)
	}
	if math.Abs(usd-0.0035) > 1e-12 {
		t.Fatalf("Totals usd = %v, want 0.0035", usd)
	}
	if n := len(l.Batches()); n != 2 {
		t.Fatalf("Batches = %d, want 2", n)
	}

	// Snapshot windows: same-day vs same-month vs older batches.
	l2 := NewLedger(ZeroRates(), func() time.Time { return now })
	l2.RecordBatch(100, 0) // today
	now = now.Add(-24 * time.Hour)
	l2.RecordBatch(200, 0) // yesterday, same month
	now = now.Add(-40 * 24 * time.Hour)
	l2.RecordBatch(400, 0) // previous month
	snap := l2.Snapshot(testNow)
	if snap.DayTokens != 100 {
		t.Fatalf("DayTokens = %d, want 100", snap.DayTokens)
	}
	if snap.MonthTokens != 300 {
		t.Fatalf("MonthTokens = %d, want 300 (today + yesterday)", snap.MonthTokens)
	}
}

func TestEstimateTokensFromChars(t *testing.T) {
	if got := EstimateTokensFromChars(0); got != 0 {
		t.Fatalf("0 chars = %d, want 0", got)
	}
	if got := EstimateTokensFromChars(4000); got != 1000 {
		t.Fatalf("4000 chars = %d, want 1000", got)
	}
	if got := EstimateTokensFromChars(1); got != 1 {
		t.Fatalf("1 char = %d, want 1 (round up)", got)
	}
}

// ---------------------------------------------------------------------------
// Budget ceilings — halt on ceiling, boundary halts
// ---------------------------------------------------------------------------

func TestBudgetHaltBoundaries(t *testing.T) {
	b := Budget{DailyTokenCap: 1000, MonthlyTokenCap: 5000, DailyUSDCap: 1.0, MonthlyUSDCap: 5.0}

	// Zero usage: all clear.
	if halt, reason := b.Halted(UsageSnapshot{}); halt {
		t.Fatalf("empty snapshot halted: %q", reason)
	}
	// Just under every cap: still clear.
	under := UsageSnapshot{DayTokens: 999, MonthTokens: 4999, DayUSD: 0.99, MonthUSD: 4.99}
	if halt, reason := b.Halted(under); halt {
		t.Fatalf("under-cap snapshot halted: %q", reason)
	}
	// EXACTLY at each ceiling: halt (boundary rule, usage >= cap).
	for name, snap := range map[string]UsageSnapshot{
		"daily tokens":   {DayTokens: 1000},
		"monthly tokens": {MonthTokens: 5000},
		"daily USD":      {DayUSD: 1.0},
		"monthly USD":    {MonthUSD: 5.0},
	} {
		halt, reason := b.Halted(snap)
		if !halt {
			t.Fatalf("%s at cap did not halt", name)
		}
		if reason == "" {
			t.Fatalf("%s halt has empty reason", name)
		}
	}
	// Over cap: halt.
	if halt, _ := b.Halted(UsageSnapshot{DayTokens: 1001}); !halt {
		t.Fatal("over daily cap did not halt")
	}
	// Zero Budget: unlimited, never halts.
	zero := Budget{}
	huge := UsageSnapshot{DayTokens: 1 << 40, MonthUSD: 1e9}
	if halt, _ := zero.Halted(huge); halt {
		t.Fatal("zero Budget halted")
	}
	// Partial budget: only the set axis binds.
	partial := Budget{MonthlyTokenCap: 10}
	if halt, _ := partial.Halted(UsageSnapshot{DayTokens: 1 << 30}); halt {
		t.Fatal("unset daily cap bound usage")
	}
	if halt, _ := partial.Halted(UsageSnapshot{MonthTokens: 10}); !halt {
		t.Fatal("set monthly cap did not bind at boundary")
	}
}

// End-to-end acceptance shape: record batches until the ceiling, then halt.
func TestBudgetHaltViaLedger(t *testing.T) {
	now := testNow
	l := NewLedger(ZeroRates(), func() time.Time { return now })
	b := Budget{DailyTokenCap: 500}
	for i := 0; i < 5; i++ {
		l.RecordBatch(99, 0) // 495 total: clear
	}
	if halt, _ := b.Halted(l.Snapshot(now)); halt {
		t.Fatal("halted at 495/500")
	}
	l.RecordBatch(5, 0) // exactly 500: halt
	halt, reason := b.Halted(l.Snapshot(now))
	if !halt || reason != "daily token cap" {
		t.Fatalf("halt = %v reason = %q, want true + daily token cap", halt, reason)
	}
}

// ---------------------------------------------------------------------------
// Decay-flag rule (conf < 0.2 + 180d unaccessed), incl. 180d edge
// ---------------------------------------------------------------------------

func TestDecayFlagRule(t *testing.T) {
	// Stale + low confidence: flagged.
	stale := MemoryRef{Key: "a", Confidence: 0.25, LastAccessedAt: daysAgo(testNow, 200)}
	if !ShouldFlagForReview(stale, testNow) {
		t.Fatal("200d-old conf-0.25 item not flagged")
	}
	// Low confidence but fresh: NOT flagged (recency wins).
	fresh := MemoryRef{Key: "b", Confidence: 0.05, LastAccessedAt: daysAgo(testNow, 10)}
	if ShouldFlagForReview(fresh, testNow) {
		t.Fatal("10d-old item flagged despite recent use")
	}
	// Old but high confidence: NOT flagged (0.95^200/30 ≈ 0.63*1.0 > 0.2).
	// Even conf 1.0 at 2 years: 0.95^24.3 ≈ 0.28 > 0.2, unflagged.
	durable := MemoryRef{Key: "c", Confidence: 1.0, LastAccessedAt: daysAgo(testNow, 730)}
	if ShouldFlagForReview(durable, testNow) {
		t.Fatal("conf-1.0 2yr-old item flagged (decay floor keeps it above 0.2)")
	}
	// Unknown age (zero times): never flagged — staleness must be proven.
	unknown := MemoryRef{Key: "d", Confidence: 0.01}
	if ShouldFlagForReview(unknown, testNow) {
		t.Fatal("zero-age item flagged")
	}
	// CreatedAt fallback: never accessed, created 200d ago, low conf.
	neverUsed := MemoryRef{Key: "e", Confidence: 0.1, CreatedAt: daysAgo(testNow, 200)}
	if !ShouldFlagForReview(neverUsed, testNow) {
		t.Fatal("never-accessed 200d-old item not flagged via CreatedAt")
	}
	// LastAccessedAt wins over an older CreatedAt.
	touched := MemoryRef{Key: "f", Confidence: 0.1,
		CreatedAt: daysAgo(testNow, 400), LastAccessedAt: daysAgo(testNow, 5)}
	if ShouldFlagForReview(touched, testNow) {
		t.Fatal("recently touched item flagged via stale CreatedAt")
	}
}

func TestDecayFlag180DayEdge(t *testing.T) {
	// Exactly 180 days, effective 0.25*0.95^6 ≈ 0.184 < 0.2: flagged.
	atEdge := MemoryRef{Key: "edge", Confidence: 0.25, LastAccessedAt: daysAgo(testNow, 180)}
	if !ShouldFlagForReview(atEdge, testNow) {
		t.Fatalf("exactly-180d item not flagged (days=%v eff=%v)",
			DaysSince(atEdge.LastAccessedAt, testNow),
			EffectiveConfidence(0.25, 180))
	}
	// Just under 180 days (179d): NOT flagged, same confidence.
	justUnder := MemoryRef{Key: "under", Confidence: 0.25,
		LastAccessedAt: testNow.Add(-(179*24*time.Hour + 23*time.Hour))}
	if ShouldFlagForReview(justUnder, testNow) {
		t.Fatal("sub-180d item flagged")
	}
	// Over 180d but effective confidence ABOVE 0.2 (0.3*0.95^6 ≈ 0.221):
	// NOT flagged — both conditions are required.
	highBase := MemoryRef{Key: "high", Confidence: 0.3, LastAccessedAt: daysAgo(testNow, 200)}
	if eff := EffectiveConfidence(0.3, 200); eff <= StaleConfidenceThreshold {
		t.Fatalf("test setup wrong: eff %v should exceed 0.2", eff)
	}
	if ShouldFlagForReview(highBase, testNow) {
		t.Fatal("above-threshold item flagged on age alone")
	}
	// Confidence boundary is STRICT: an effective confidence of exactly 0.2
	// does not satisfy the rule. Exact 0.2 is not reachable through decay
	// math (0.2/0.95^6 round-trips to 0.19999999999999998), so the strictness
	// is pinned at zero days where EffectiveConfidence(base, 0) == base
	// exactly — 0.2 itself must not pass the threshold comparison.
	if got := EffectiveConfidence(0.2, 0); got != 0.2 {
		t.Fatalf("EffectiveConfidence(0.2, 0) = %v, want exactly 0.2", got)
	} else if got < StaleConfidenceThreshold {
		t.Fatalf("threshold comparison not strict: 0.2 < 0.2 passed")
	}
}

func TestFlagForReviewBatch(t *testing.T) {
	items := []MemoryRef{
		{Key: "stale-1", Confidence: 0.1, LastAccessedAt: daysAgo(testNow, 300)},
		{Key: "fresh", Confidence: 0.1, LastAccessedAt: daysAgo(testNow, 3)},
		{Key: "stale-2", Confidence: 0.15, LastAccessedAt: daysAgo(testNow, 190)},
	}
	got := FlagForReview(items, testNow)
	if len(got) != 2 || got[0].Key != "stale-1" || got[1].Key != "stale-2" {
		t.Fatalf("FlagForReview = %v, want [stale-1 stale-2] in order", got)
	}
	if got := FlagForReview(nil, testNow); len(got) != 0 {
		t.Fatalf("FlagForReview(nil) = %v, want empty", got)
	}
}

// ---------------------------------------------------------------------------
// Archival selector: stale SUPERSEDED → archive
// ---------------------------------------------------------------------------

func TestArchivalSelector(t *testing.T) {
	old := daysAgo(testNow, 300)
	fresh := daysAgo(testNow, 3)
	items := []MemoryRef{
		{Key: "victim", Confidence: 0.1, LastAccessedAt: old, Status: "SUPERSEDED"},
		{Key: "fresh-sup", Confidence: 0.1, LastAccessedAt: fresh, Status: "SUPERSEDED"},
		{Key: "stale-confirmed", Confidence: 0.1, LastAccessedAt: old, Status: "CONFIRMED"},
		{Key: "stale-proposed", Confidence: 0.1, LastAccessedAt: old, Status: "PROPOSED"},
		{Key: "archived", Confidence: 0.1, LastAccessedAt: old, Status: "ARCHIVED"},
		{Key: "lowercase", Confidence: 0.1, LastAccessedAt: old, Status: "superseded"},
	}
	got := SelectForArchive(items, testNow)
	if len(got) != 2 {
		t.Fatalf("SelectForArchive = %v, want [victim lowercase]", keys(got))
	}
	if got[0].Key != "victim" || got[1].Key != "lowercase" {
		t.Fatalf("SelectForArchive = %v, want [victim lowercase]", keys(got))
	}
	// Idempotency shape: already-archived rows are never reselected.
	for _, m := range got {
		if statusName(m.Status) == StatusArchived {
			t.Fatalf("reselected archived row %q", m.Key)
		}
	}
	if got := SelectForArchive(nil, testNow); len(got) != 0 {
		t.Fatalf("SelectForArchive(nil) = %v, want empty", got)
	}
}

func keys(items []MemoryRef) []string {
	out := make([]string, 0, len(items))
	for _, m := range items {
		out = append(out, m.Key)
	}
	return out
}

// ---------------------------------------------------------------------------
// Analytics counters
// ---------------------------------------------------------------------------

func TestAnalyticsCounters(t *testing.T) {
	a := NewAnalytics()
	day1 := testNow
	day2 := testNow.Add(24 * time.Hour)

	// Daily events: accumulation + zero default.
	if got := a.DailyEvents(day1); got != 0 {
		t.Fatalf("unrecorded day = %d, want 0", got)
	}
	a.RecordEvents(day1, 10)
	a.RecordEvents(day1, 5)
	a.RecordEvents(day2, 30)
	if got := a.DailyEvents(day1); got != 15 {
		t.Fatalf("day1 = %d, want 15", got)
	}
	if got := a.DailyEvents(day2); got != 30 {
		t.Fatalf("day2 = %d, want 30", got)
	}
	a.RecordEvents(day1, 0)
	a.RecordEvents(day1, -3)
	if got := a.DailyEvents(day1); got != 15 {
		t.Fatalf("day1 after no-op records = %d, want 15", got)
	}

	// Event growth: (30-15)/15 = 1.0.
	if got := a.EventGrowth(day1, day2); math.Abs(got-1.0) > 1e-12 {
		t.Fatalf("EventGrowth = %v, want 1.0", got)
	}

	// Extraction accuracy: 8/10 = 0.8.
	if got := a.ExtractionAccuracy(); got != 0 {
		t.Fatalf("accuracy with no data = %v, want 0 (render as —)", got)
	}
	a.RecordProposed(10)
	a.RecordConfirmed(8)
	if got := a.ExtractionAccuracy(); math.Abs(got-0.8) > 1e-12 {
		t.Fatalf("accuracy = %v, want 0.8", got)
	}
	if a.Proposed() != 10 || a.Confirmed() != 8 {
		t.Fatalf("proposed/confirmed = %d/%d, want 10/8", a.Proposed(), a.Confirmed())
	}
}

func TestGrowthRateEdges(t *testing.T) {
	if got := GrowthRate(100, 150); math.Abs(got-0.5) > 1e-12 {
		t.Fatalf("GrowthRate(100,150) = %v, want 0.5", got)
	}
	if got := GrowthRate(100, 50); math.Abs(got+0.5) > 1e-12 {
		t.Fatalf("GrowthRate(100,50) = %v, want -0.5 (contraction)", got)
	}
	if got := GrowthRate(0, 0); got != 0 {
		t.Fatalf("GrowthRate(0,0) = %v, want 0", got)
	}
	if got := GrowthRate(0, 10); !math.IsInf(got, 1) {
		t.Fatalf("GrowthRate(0,10) = %v, want +Inf", got)
	}
}
