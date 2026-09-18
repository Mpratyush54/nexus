package governance

import (
	"math"
	"sync"
	"testing"
)

func TestAuditTokenProxy(t *testing.T) {
	if CharsPerToken != 4 {
		t.Errorf("CharsPerToken = %d, want 4 (plan 1.6: 4000 chars ~= 1000 tokens)", CharsPerToken)
	}
	if got := CharsToTokens(4000); got != 1000 {
		t.Errorf("CharsToTokens(4000) = %d, want 1000", got)
	}
	if CharsToTokens(0) != 0 || CharsToTokens(-5) != 0 {
		t.Error("non-positive chars must map to 0")
	}
	if CharsToTokens(3) != 0 {
		t.Errorf("CharsToTokens truncates: 3 chars = %d tokens", CharsToTokens(3))
	}
	if got := TokensToChars(1000); got != 4000 {
		t.Errorf("TokensToChars(1000) = %d, want 4000", got)
	}
	if TokensToChars(0) != 0 || TokensToChars(-1) != 0 {
		t.Error("non-positive tokens must map to 0 chars")
	}
}

func TestAuditEstimateAndRates(t *testing.T) {
	r := ModelRate{PromptPer1K: 0.005, CompletionPer1K: 0.015}
	if got, want := EstimateUSD(r, 1000, 1000), 0.02; math.Abs(got-want) > 1e-9 {
		t.Errorf("EstimateUSD = %v, want %v", got, want)
	}
	if EstimateUSD(r, -10, -20) != 0 {
		t.Error("negative tokens must clamp to 0")
	}
	if got := RateFor("nope-unknown", nil); got != (ModelRate{}) {
		t.Errorf("unknown model must fall back to zero rate: %+v", got)
	}
	custom := map[string]ModelRate{"m": {PromptPer1K: 1, CompletionPer1K: 2}}
	if got := RateFor("m", custom); got.PromptPer1K != 1 {
		t.Errorf("custom table lookup failed: %+v", got)
	}
	if got := RateFor("other", custom); got != (ModelRate{}) {
		t.Errorf("missing custom entry must be zero: %+v", got)
	}
	if got := RoundUSD(0.123456789); math.Abs(got-0.123457) > 1e-9 {
		t.Errorf("RoundUSD = %v", got)
	}
}

func TestAuditCostTrackerMath(t *testing.T) {
	tr := NewCostTracker(map[string]ModelRate{"m": {PromptPer1K: 1.0, CompletionPer1K: 2.0}})
	c1 := tr.RecordBatch("m", 1000, 500) // 1.0 + 1.0 = 2.0
	if math.Abs(c1-2.0) > 1e-9 {
		t.Errorf("batch cost = %v, want 2.0", c1)
	}
	tr.RecordBatch("unknown-model", 1000, 1000) // zero rate -> $0 but tokens counted
	snap := tr.Snapshot()
	if snap.Batches != 2 || snap.PromptTokens != 2000 || snap.CompletionTokens != 1500 || snap.TotalTokens != 3500 {
		t.Errorf("snapshot wrong: %+v", snap)
	}
	if math.Abs(snap.EstimatedUSD-2.0) > 1e-9 {
		t.Errorf("usd = %v, want 2.0", snap.EstimatedUSD)
	}
	// Negative inputs clamp.
	tr.RecordBatch("m", -5, -5)
	if got := tr.Snapshot().TotalTokens; got != 3500 {
		t.Errorf("negative batch must not add tokens: %d", got)
	}
	// Caller table mutation must not affect tracker (copied).
	rates := map[string]ModelRate{"m": {PromptPer1K: 1}}
	tr2 := NewCostTracker(rates)
	rates["m"] = ModelRate{PromptPer1K: 999}
	if c := tr2.RecordBatch("m", 1000, 0); math.Abs(c-1.0) > 1e-9 {
		t.Errorf("rate table not copied: cost=%v", c)
	}
	// Nil rates -> defaults; Reset clears.
	tr3 := NewCostTracker(nil)
	tr3.RecordBatch("ollama", 10, 10)
	tr3.Reset()
	if got := tr3.Snapshot(); got.Batches != 0 || got.TotalTokens != 0 || got.EstimatedUSD != 0 {
		t.Errorf("reset failed: %+v", got)
	}
	// Concurrent use must not race (run with -race).
	var wg sync.WaitGroup
	tr4 := NewCostTracker(nil)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); tr4.RecordBatch("ollama", 5, 5); tr4.Snapshot() }()
	}
	wg.Wait()
	if got := tr4.Snapshot(); got.Batches != 20 || got.TotalTokens != 200 {
		t.Errorf("concurrent totals wrong: %+v", got)
	}
}

func TestAuditQuotaEnforcerMath(t *testing.T) {
	q := NewQuotaEnforcer()
	if q.DailyTokenCap != DefaultDailyTokenCap || q.MonthlyTokenCap != DefaultMonthlyTokenCap ||
		q.DailyUSDCap != DefaultDailyUSDCap || q.MonthlyUSDCap != DefaultMonthlyUSDCap {
		t.Errorf("defaults wrong: %+v", q)
	}
	// NOTE: enforcement wiring is server-side; these tests pin library math only.
	if ok, _ := q.Allows(QuotaUsage{}); !ok {
		t.Error("empty usage must be allowed")
	}
	// First-hit ordering: daily tokens -> monthly tokens -> daily USD -> monthly USD.
	over := QuotaUsage{DayTokens: DefaultDailyTokenCap, MonthTokens: DefaultMonthlyTokenCap + 1, DayUSD: 99, MonthUSD: 99}
	if ok, reason := q.Allows(over); ok || reason != "daily token cap reached" {
		t.Errorf("ordering: ok=%v reason=%q", ok, reason)
	}
	if !q.ShouldHalt(over) {
		t.Error("ShouldHalt must negate Allows")
	}
	if q.ShouldHalt(QuotaUsage{}) {
		t.Error("empty usage must not halt")
	}
	// Zero cap = no limit on that dimension.
	free := QuotaEnforcer{}
	if ok, _ := free.Allows(QuotaUsage{DayTokens: 1 << 30, MonthUSD: 1e9}); !ok {
		t.Error("zero-value caps must mean unlimited")
	}
	// AllowsBatch projects forward without mutating.
	u := QuotaUsage{DayTokens: DefaultDailyTokenCap - 10}
	if ok, _ := q.AllowsBatch(u, 5, 0); !ok {
		t.Error("fitting batch must be allowed")
	}
	if ok, reason := q.AllowsBatch(u, 11, 0); ok || reason != "batch would exceed daily token cap" {
		t.Errorf("overflowing batch: ok=%v reason=%q", ok, reason)
	}
	if ok, reason := q.AllowsBatch(QuotaUsage{}, 0, DefaultDailyUSDCap+0.01); ok || reason != "batch would exceed daily USD cap" {
		t.Errorf("USD overflow: ok=%v reason=%q", ok, reason)
	}
}
