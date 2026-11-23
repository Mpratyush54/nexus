package usage

import (
	"math"
	"testing"
)

func TestPriceForMatchesOnlyKnownModels(t *testing.T) {
	for _, m := range []string{"claude-opus-5", "anthropic/claude-opus-5", "us.anthropic.claude-sonnet-4-6", "claude-opus-5[1m]", "claude-opus-4-6-20260101", "CLAUDE-FABLE-5",
		"claude-opus-5-5", "claude-opus-5-5-20261101", "claude-opus-5-5[1m]", "claude-sonnet-5-5", "claude-haiku-5-5",
		"claude-fable-5-2", "claude-sonnet-5", "claude-haiku-4-5"} {
		if _, ok := PriceFor(m); !ok {
			t.Errorf("%s should be priced", m)
		}
	}
	for _, m := range []string{"", "claude-fable-5-3", "claude-opus-5-6", "claude-opus-4-60", "gpt-6.1-sol", "big-pickle", "gemini-3-pro"} {
		if _, ok := PriceFor(m); ok {
			t.Errorf("%s must stay unpriced", m)
		}
	}
}

func TestTotalPrefersReportedCostAndNeverPricesUnknown(t *testing.T) {
	var tot Total
	tot.Add(Entry{Model: "claude-opus-5", Input: 1_000_000, Output: 1_000_000, CacheRead: 1_000_000, CacheWrite: 1_000_000})
	want := 5 + 25 + 0.5 + 6.25
	if math.Abs(tot.EstimatedUSD-want) > 1e-9 || !tot.Estimated {
		t.Fatalf("estimate = %+v want %v", tot, want)
	}
	tot.Add(Entry{Model: "gpt-6.1-sol", Input: 10, Output: 5, CostUSD: Cost(0.5)})
	tot.Add(Entry{Model: "gpt-6.1-sol", Input: 10, Output: 5})
	if tot.ReportedUSD != 0.5 || tot.UnpricedTokens != 15 || tot.Turns != 3 {
		t.Fatalf("reported/unpriced = %+v", tot)
	}
	if math.Abs(tot.CostUSD-(want+0.5)) > 1e-9 {
		t.Fatalf("cost = %v", tot.CostUSD)
	}
}

func TestSubHandlesCounterReset(t *testing.T) {
	d := Sub(Entry{Input: 10, Output: 4, CostUSD: Cost(1)}, Entry{Input: 4, Output: 1, CostUSD: Cost(0.25)})
	if d.Input != 6 || d.Output != 3 || *d.CostUSD != 0.75 {
		t.Fatalf("delta = %+v", d)
	}
	r := Sub(Entry{Input: 3, CostUSD: Cost(0.1)}, Entry{Input: 40, CostUSD: Cost(2)})
	if r.Input != 3 || *r.CostUSD != 0.1 {
		t.Fatalf("reset = %+v", r)
	}
}

func TestPricesFromAnthropicPricingPage(t *testing.T) {
	// https://platform.claude.com/docs/en/about-claude/pricing (Nov 2026):
	// base input, output, cache hit, 5m cache write per MTok.
	cases := map[string]Price{
		"claude-opus-5-5":   {Input: 4, Output: 20, CacheRead: 0.20, CacheWrite: 5},
		"claude-sonnet-5-5": {Input: 2, Output: 10, CacheRead: 0.10, CacheWrite: 2.50},
		"claude-haiku-5-5":  {Input: 0.10, Output: 0.50, CacheRead: 0.01, CacheWrite: 0.125},
		"claude-fable-5-2":  {Input: 8, Output: 40, CacheRead: 0.20, CacheWrite: 10},
		"claude-opus-5":     {Input: 5, Output: 25, CacheRead: 0.50, CacheWrite: 6.25},
		"claude-sonnet-5":   {Input: 2, Output: 10, CacheRead: 0.20, CacheWrite: 2.50},
		"claude-fable-5":    {Input: 10, Output: 50, CacheRead: 1, CacheWrite: 12.50},
	}
	for m, want := range cases {
		got, ok := PriceFor(m)
		got.Long = nil
		if !ok || math.Abs(got.Input-want.Input) > 1e-12 || math.Abs(got.Output-want.Output) > 1e-12 ||
			math.Abs(got.CacheRead-want.CacheRead) > 1e-12 || math.Abs(got.CacheWrite-want.CacheWrite) > 1e-12 {
			t.Errorf("%s = %+v (ok=%v) want %+v", m, got, ok, want)
		}
	}
	// 18M Opus 5.5 tokens are no longer unpriced.
	var tot Total
	tot.Add(Entry{Model: "claude-opus-5-5", Input: 1_000_000, Output: 1_000_000, CacheRead: 15_000_000, CacheWrite: 1_000_000})
	if want := 4 + 20 + 3 + 5.0; !tot.HasCost || tot.UnpricedTokens != 0 || math.Abs(tot.CostUSD-want) > 1e-9 {
		t.Fatalf("opus 5.5 total = %+v want %v", tot, want)
	}
}

func TestHaikuLongPromptTier(t *testing.T) {
	short, _ := Estimate(Entry{Model: "claude-haiku-5-5", Input: 50_000, Output: 1_000_000})
	if math.Abs(short-(0.005+0.5)) > 1e-9 {
		t.Fatalf("short = %v", short)
	}
	long, _ := Estimate(Entry{Model: "claude-haiku-5-5", Input: 1_000, CacheRead: 200_000, Output: 1_000_000})
	if want := (1_000*0.5 + 200_000*0.05 + 1_000_000*2.5) / 1e6; math.Abs(long-want) > 1e-9 {
		t.Fatalf("long = %v want %v", long, want)
	}
	if LongPromptTokens != 100000 {
		t.Fatal("store SQL hardcodes 100000 as the long-prompt threshold")
	}
}

func TestFreeModelsArePricedAtZero(t *testing.T) {
	for _, m := range []string{"muse-spark-1.3-contributor-free", "openrouter/some-model:free", "X-FREE"} {
		if c, ok := Estimate(Entry{Model: m, Input: 300_000_000}); !ok || c != 0 {
			t.Errorf("%s = %v ok=%v", m, c, ok)
		}
	}
	if _, ok := Estimate(Entry{Model: "freeform-1", Input: 1}); ok {
		t.Error("freeform-1 must stay unpriced")
	}
	var tot Total
	tot.Add(Entry{Model: "muse-spark-1.3-contributor-free", Input: 300_000_000})
	if !tot.HasCost || tot.CostUSD != 0 || tot.UnpricedTokens != 0 {
		t.Fatalf("free total = %+v", tot)
	}
}
