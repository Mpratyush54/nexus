package governance

import (
	"math"
	"testing"
)

func TestCostMath(t *testing.T) {
	rate := ModelRate{PromptPer1K: 0.005, CompletionPer1K: 0.015}
	got := EstimateUSD(rate, 1000, 1000)
	if math.Abs(got-0.02) > 1e-9 {
		t.Fatalf("EstimateUSD = %v, want 0.02", got)
	}
	tr := NewCostTracker(map[string]ModelRate{"test": rate})
	cost := tr.RecordBatch("test", 2000, 500)
	want := 2000.0/1000*0.005 + 500.0/1000*0.015
	if math.Abs(cost-want) > 1e-9 {
		t.Fatalf("RecordBatch = %v, want %v", cost, want)
	}
	snap := tr.Snapshot()
	if snap.PromptTokens != 2000 || snap.CompletionTokens != 500 || snap.TotalTokens != 2500 {
		t.Fatalf("bad token totals: %+v", snap)
	}
	if math.Abs(snap.EstimatedUSD-want) > 1e-9 {
		t.Fatalf("snapshot USD = %v, want %v", snap.EstimatedUSD, want)
	}
	if tr.RecordBatch("unknown-model", 1000, 1000) != 0 {
		t.Fatal("unknown model must fall back to zero rate")
	}
}

func TestQuotaHalt(t *testing.T) {
	q := NewQuotaEnforcer()
	if ok, _ := q.Allows(QuotaUsage{}); !ok {
		t.Fatal("empty usage must be allowed")
	}
	if !q.ShouldHalt(QuotaUsage{MonthTokens: DefaultMonthlyTokenCap}) {
		t.Fatal("monthly token cap must halt extraction")
	}
	if !q.ShouldHalt(QuotaUsage{MonthUSD: DefaultMonthlyUSDCap}) {
		t.Fatal("monthly USD cap must halt extraction")
	}
	if !q.ShouldHalt(QuotaUsage{DayTokens: DefaultDailyTokenCap}) {
		t.Fatal("daily token cap must halt extraction")
	}
	// Prospective batch crossing the cap is rejected even when current use is ok.
	near := QuotaUsage{MonthTokens: DefaultMonthlyTokenCap - 10}
	if ok, _ := q.AllowsBatch(near, 11, 0); ok {
		t.Fatal("batch crossing monthly cap must be rejected")
	}
	if ok, _ := q.AllowsBatch(near, 10, 0); !ok {
		t.Fatal("batch exactly filling the cap must be allowed")
	}
	// Zero caps = unlimited.
	open := QuotaEnforcer{}
	if open.ShouldHalt(QuotaUsage{MonthTokens: 1 << 30}) {
		t.Fatal("zero caps must never halt")
	}
}
