// Package governance implements local LLM cost tracking, token quota
// enforcement, and memory health auditing for the designated-device Memory
// Processor (GitHub Mpratyush54/nexus issue #26).
//
// Alignment with existing code (read first per issue brief):
//   - internal/daemon/processor.go: per-batch char Budget (DefaultBudget =
//     4000 chars ≈ 1000 tokens, plan §1.6 scale) and BatchSize caps. This
//     package uses the same chars/4 token proxy (CharsToTokens) so quota
//     accounting speaks the same units as the processor and context builder.
//   - internal/context/builder.go: TokenCount/BudgetRemaining are char
//     lengths; CostTracker likewise treats 1 token ≈ 4 chars.
//   - internal/context/decay.go: effective confidence = base * 0.95^(days/30),
//     stale = effective < 0.2 AND idle >= 180d. ConfidenceAuditor reuses those
//     exact constants (duplicated, not imported, so this package stays
//     stdlib-only and importable from the daemon without pulling pgx).
//
// Stdlib only: sync, time, strings, math. All analytics/auditor functions are
// pure funcs over DTOs (no store/DB imports) so they are deterministic in CI.
package governance

import (
	"math"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Token proxy (aligns with processor.go DefaultBudget + builder.go)
// ---------------------------------------------------------------------------

// CharsPerToken is the shared estimate: 4000 chars ≈ 1000 tokens (plan §1.6).
const CharsPerToken = 4

// CharsToTokens converts a char budget/length to the token proxy used across
// the processor and context builder.
func CharsToTokens(chars int) int {
	if chars <= 0 {
		return 0
	}
	return chars / CharsPerToken
}

// TokensToChars converts back (rounds up so a token budget never truncates
// to zero chars).
func TokensToChars(tokens int) int {
	if tokens <= 0 {
		return 0
	}
	return tokens * CharsPerToken
}

// ---------------------------------------------------------------------------
// Per-model rate table + CostTracker
// ---------------------------------------------------------------------------

// ModelRate is USD per 1K prompt / 1K completion tokens.
type ModelRate struct {
	PromptPer1K     float64
	CompletionPer1K float64
}

// DefaultModelRates is the local estimate table. Ollama/local models cost $0
// (electricity only); hosted keys use list-price approximations so the
// processor can show spend without calling any billing API.
var DefaultModelRates = map[string]ModelRate{
	"heuristic":          {PromptPer1K: 0, CompletionPer1K: 0},
	"ollama":             {PromptPer1K: 0, CompletionPer1K: 0},
	"ollama:local":       {PromptPer1K: 0, CompletionPer1K: 0},
	"anthropic:claude":   {PromptPer1K: 0.003, CompletionPer1K: 0.015},
	"openai:gpt-4o":      {PromptPer1K: 0.005, CompletionPer1K: 0.015},
	"openai:gpt-4o-mini": {PromptPer1K: 0.00015, CompletionPer1K: 0.0006},
}

// EstimateUSD returns the USD cost for a token split under a rate.
func EstimateUSD(rate ModelRate, promptTokens, completionTokens int) float64 {
	if promptTokens < 0 {
		promptTokens = 0
	}
	if completionTokens < 0 {
		completionTokens = 0
	}
	return float64(promptTokens)/1000*rate.PromptPer1K +
		float64(completionTokens)/1000*rate.CompletionPer1K
}

// RateFor looks up a model rate, falling back to the zero (free/local) rate
// for unknown models so untracked backends never inflate spend.
func RateFor(model string, table map[string]ModelRate) ModelRate {
	if table == nil {
		table = DefaultModelRates
	}
	if r, ok := table[model]; ok {
		return r
	}
	return ModelRate{}
}

// BatchUsage is one extraction batch's token spend.
type BatchUsage struct {
	Model            string
	PromptTokens     int
	CompletionTokens int
	CostUSD          float64
	At               time.Time
}

// CostTracker accumulates prompt/completion tokens and estimated USD per
// extraction batch. Mutex-guarded; safe for the background processor goroutine.
type CostTracker struct {
	mu       sync.Mutex
	rates    map[string]ModelRate
	batches  int
	prompt   int
	complete int
	usd      float64
	history  []BatchUsage
}

// NewCostTracker builds a tracker; nil rates selects DefaultModelRates.
// The table is copied so callers cannot mutate tracker state.
func NewCostTracker(rates map[string]ModelRate) *CostTracker {
	if rates == nil {
		rates = DefaultModelRates
	}
	cp := make(map[string]ModelRate, len(rates))
	for k, v := range rates {
		cp[k] = v
	}
	return &CostTracker{rates: cp}
}

// RecordBatch records one extraction batch and returns its USD estimate.
func (c *CostTracker) RecordBatch(model string, promptTokens, completionTokens int) float64 {
	if promptTokens < 0 {
		promptTokens = 0
	}
	if completionTokens < 0 {
		completionTokens = 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	cost := EstimateUSD(c.rates[model], promptTokens, completionTokens)
	c.batches++
	c.prompt += promptTokens
	c.complete += completionTokens
	c.usd += cost
	c.history = append(c.history, BatchUsage{
		Model: model, PromptTokens: promptTokens,
		CompletionTokens: completionTokens, CostUSD: cost,
		At: time.Now().UTC(),
	})
	return cost
}

// Snapshot is the tracker's accumulated totals (a copy; safe to read).
type Snapshot struct {
	Batches          int
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	EstimatedUSD     float64
}

// Snapshot returns current totals.
func (c *CostTracker) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Snapshot{
		Batches: c.batches, PromptTokens: c.prompt,
		CompletionTokens: c.complete, TotalTokens: c.prompt + c.complete,
		EstimatedUSD: c.usd,
	}
}

// Reset clears totals and history (e.g. month rollover in tests/ops).
func (c *CostTracker) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.batches, c.prompt, c.complete, c.usd = 0, 0, 0, 0
	c.history = nil
}

// RoundUSD rounds to 6dp for stable test comparisons.
func RoundUSD(v float64) float64 { return math.Round(v*1e6) / 1e6 }

// ---------------------------------------------------------------------------
// QuotaEnforcer: daily/monthly caps that halt extraction
// ---------------------------------------------------------------------------

// Default quota ceilings from issue #26 (runaway-loop guardrails).
const (
	DefaultDailyTokenCap   = 100_000
	DefaultMonthlyTokenCap = 500_000
	DefaultDailyUSDCap     = 1.0
	DefaultMonthlyUSDCap   = 5.0
)

// QuotaEnforcer holds user-configured ceilings. Zero value on any cap means
// "no limit" for that dimension; use NewQuotaEnforcer for issue defaults.
type QuotaEnforcer struct {
	DailyTokenCap   int
	MonthlyTokenCap int
	DailyUSDCap     float64
	MonthlyUSDCap   float64
}

// NewQuotaEnforcer returns the issue's example caps ($5 / 500k monthly).
func NewQuotaEnforcer() QuotaEnforcer {
	return QuotaEnforcer{
		DailyTokenCap:   DefaultDailyTokenCap,
		MonthlyTokenCap: DefaultMonthlyTokenCap,
		DailyUSDCap:     DefaultDailyUSDCap,
		MonthlyUSDCap:   DefaultMonthlyUSDCap,
	}
}

// QuotaUsage is accumulated spend in the current day/month windows.
// Pure DTO: the caller owns window resets; this type only carries numbers.
type QuotaUsage struct {
	DayTokens   int
	MonthTokens int
	DayUSD      float64
	MonthUSD    float64
}

// Allows reports whether another batch may run. ok=false + reason names the
// first ceiling hit (daily tokens → monthly tokens → daily USD → monthly USD).
func (q QuotaEnforcer) Allows(u QuotaUsage) (ok bool, reason string) {
	if q.DailyTokenCap > 0 && u.DayTokens >= q.DailyTokenCap {
		return false, "daily token cap reached"
	}
	if q.MonthlyTokenCap > 0 && u.MonthTokens >= q.MonthlyTokenCap {
		return false, "monthly token cap reached"
	}
	if q.DailyUSDCap > 0 && u.DayUSD >= q.DailyUSDCap {
		return false, "daily USD cap reached"
	}
	if q.MonthlyUSDCap > 0 && u.MonthUSD >= q.MonthlyUSDCap {
		return false, "monthly USD cap reached"
	}
	return true, ""
}

// ShouldHalt is the processor-facing predicate: true means halt extraction
// (quota ceiling reached). It is the negation of Allows, kept as a named
// helper so call sites read as `if enforcer.ShouldHalt(usage) { return }`.
func (q QuotaEnforcer) ShouldHalt(u QuotaUsage) bool {
	ok, _ := q.Allows(u)
	return !ok
}

// AllowsBatch checks whether a prospective batch of n tokens / $c fits inside
// the remaining quota without exceeding it.
func (q QuotaEnforcer) AllowsBatch(u QuotaUsage, tokens int, usd float64) (bool, string) {
	proj := QuotaUsage{
		DayTokens: u.DayTokens + tokens, MonthTokens: u.MonthTokens + tokens,
		DayUSD: u.DayUSD + usd, MonthUSD: u.MonthUSD + usd,
	}
	// A batch that would cross a cap is rejected even if current usage is ok.
	if q.DailyTokenCap > 0 && proj.DayTokens > q.DailyTokenCap {
		return false, "batch would exceed daily token cap"
	}
	if q.MonthlyTokenCap > 0 && proj.MonthTokens > q.MonthlyTokenCap {
		return false, "batch would exceed monthly token cap"
	}
	if q.DailyUSDCap > 0 && proj.DayUSD > q.DailyUSDCap {
		return false, "batch would exceed daily USD cap"
	}
	if q.MonthlyUSDCap > 0 && proj.MonthUSD > q.MonthlyUSDCap {
		return false, "batch would exceed monthly USD cap"
	}
	return true, ""
}
