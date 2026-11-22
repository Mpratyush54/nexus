package usage

import (
	"math"
	"testing"
)

func TestPriceForMatchesOnlyKnownModels(t *testing.T) {
	for _, m := range []string{"claude-opus-5", "anthropic/claude-opus-5", "us.anthropic.claude-sonnet-4-6", "claude-opus-5[1m]", "claude-opus-4-6-20260101", "CLAUDE-FABLE-5"} {
		if _, ok := PriceFor(m); !ok {
			t.Errorf("%s should be priced", m)
		}
	}
	for _, m := range []string{"", "claude-fable-5-2", "claude-opus-4-60", "gpt-6.1-sol", "big-pickle", "claude-haiku-4-5", "gemini-3-pro"} {
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
