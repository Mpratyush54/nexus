// Package usage holds the token/cost shape shared by capture, live chats, the
// server and the desktop core, plus a small built-in price table used only to
// estimate cost when an agent does not report one.
package usage

import (
	"math"
	"sort"
	"strings"
)

// Entry is the usage of one model call or one turn. Input excludes cached
// input (CacheRead) and cache writes (CacheWrite), matching Anthropic's
// usage fields; OpenAI-style totals are split before they get here.
type Entry struct {
	Model      string   `json:"model,omitempty"`
	Input      int64    `json:"input"`
	Output     int64    `json:"output"`
	CacheRead  int64    `json:"cache_read"`
	CacheWrite int64    `json:"cache_write"`
	CostUSD    *float64 `json:"cost_usd,omitempty"` // reported by the agent; nil when unknown
}

// Empty reports whether the entry carries no tokens and no cost.
func (e Entry) Empty() bool {
	return e.Input == 0 && e.Output == 0 && e.CacheRead == 0 && e.CacheWrite == 0 && (e.CostUSD == nil || *e.CostUSD == 0)
}

// Valid rejects negative or absurd values from untrusted payloads.
func (e Entry) Valid() bool {
	const limit = 1 << 40
	for _, v := range []int64{e.Input, e.Output, e.CacheRead, e.CacheWrite} {
		if v < 0 || v > limit {
			return false
		}
	}
	if e.CostUSD != nil && (math.IsNaN(*e.CostUSD) || math.IsInf(*e.CostUSD, 0) || *e.CostUSD < 0 || *e.CostUSD > 1e6) {
		return false
	}
	return len(e.Model) <= 200
}

// Cost returns a float pointer (helper for literals and tests).
func Cost(v float64) *float64 { return &v }

// Price is USD per million tokens. Long, when set, replaces the price for a
// request whose prompt (input + cache read + cache write) is over
// LongPromptTokens (Claude Haiku 5.5 is priced by prompt length).
type Price struct {
	Input, Output, CacheRead, CacheWrite float64
	Long                                 *Price
}

// LongPromptTokens is the prompt length above which Price.Long applies.
const LongPromptTokens = 100_000

// prices only lists models whose list price was verified against
// https://platform.claude.com/docs/en/about-claude/pricing (Nov 2026).
// Unknown models show tokens only: never guess a price. Cache writes are the
// 5-minute rate (1.25x input); cache reads are 0.1x input except where the
// page lists a lower multiplier (0.05x Opus/Sonnet 5.5, 0.025x Fable/Mythos
// 5.1 and 5.2).
var prices = map[string]Price{
	"claude-fable-5-2":  {Input: 8, Output: 40, CacheRead: 0.20, CacheWrite: 10},
	"claude-mythos-5-2": {Input: 8, Output: 40, CacheRead: 0.20, CacheWrite: 10},
	"claude-fable-5-1":  {Input: 10, Output: 50, CacheRead: 0.25, CacheWrite: 12.50},
	"claude-mythos-5-1": {Input: 10, Output: 50, CacheRead: 0.25, CacheWrite: 12.50},
	"claude-fable-5":    anthropic(10, 50),
	"claude-mythos-5":   anthropic(10, 50),
	"claude-opus-5-5":   {Input: 4, Output: 20, CacheRead: 0.20, CacheWrite: 5},
	"claude-opus-5":     anthropic(5, 25),
	"claude-opus-4-8":   anthropic(5, 25),
	"claude-opus-4-7":   anthropic(5, 25),
	"claude-opus-4-6":   anthropic(5, 25),
	"claude-opus-4-5":   anthropic(5, 25),
	"claude-sonnet-5-5": {Input: 2, Output: 10, CacheRead: 0.10, CacheWrite: 2.50},
	"claude-sonnet-5":   {Input: 2, Output: 10, CacheRead: 0.20, CacheWrite: 2.50},
	"claude-sonnet-4-6": anthropic(3, 15),
	"claude-sonnet-4-5": anthropic(3, 15),
	"claude-haiku-5-5": {Input: 0.10, Output: 0.50, CacheRead: 0.01, CacheWrite: 0.125,
		Long: &Price{Input: 0.50, Output: 2.50, CacheRead: 0.05, CacheWrite: 0.625}},
	"claude-haiku-4-5": anthropic(1, 5),
}

func anthropic(in, out float64) Price {
	return Price{Input: in, Output: out, CacheRead: in * 0.1, CacheWrite: in * 1.25}
}

// IsFreeModel reports a model id that is free by name, such as
// "muse-spark-1.3-contributor-free" or "vendor/model:free". Its cost is a
// known $0, not unknown.
func IsFreeModel(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	return strings.HasSuffix(m, "-free") || strings.HasSuffix(m, ":free")
}

// NormalizeModel lowercases and strips provider prefixes such as
// "anthropic/", "anthropic." and "us.anthropic.".
func NormalizeModel(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	if i := strings.Index(m, "anthropic."); i >= 0 {
		m = m[i+len("anthropic."):]
	}
	return m
}

// PriceFor matches a known model id exactly or with a date / context /
// region suffix (claude-opus-5-5-20261101, claude-opus-5[1m],
// claude-opus-5@20260101). A minor version missing from the table (such as
// claude-fable-5-3) is a different model and stays unpriced.
func PriceFor(model string) (Price, bool) {
	m := NormalizeModel(model)
	if m == "" {
		return Price{}, false
	}
	keys := make([]string, 0, len(prices))
	for k := range prices {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	for _, k := range keys {
		if m == k {
			return prices[k], true
		}
		if !strings.HasPrefix(m, k) {
			continue
		}
		rest := m[len(k):]
		switch rest[0] {
		case '[', '@', ':':
			return prices[k], true
		case '-':
			if isDate(rest[1:]) {
				return prices[k], true
			}
		}
	}
	return Price{}, false
}

func isDate(s string) bool {
	if len(s) < 8 {
		return false
	}
	for _, c := range s[:8] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return len(s) == 8 || s[8] == '[' || s[8] == '@' || s[8] == ':'
}

// Estimate prices an entry from the table. ok=false for unknown models;
// free models (IsFreeModel) cost a known 0. The entry is treated as one
// request when choosing a prompt-length tier.
func Estimate(e Entry) (float64, bool) {
	return EstimateTier(e, LongPrompt(e))
}

// LongPrompt reports whether an entry's prompt is over LongPromptTokens.
func LongPrompt(e Entry) bool {
	return e.Input+e.CacheRead+e.CacheWrite > LongPromptTokens
}

// EstimateTier prices an entry (possibly a sum of several requests of the
// same tier) at the long-prompt price when long is true.
func EstimateTier(e Entry, long bool) (float64, bool) {
	if IsFreeModel(e.Model) {
		return 0, true
	}
	p, ok := PriceFor(e.Model)
	if !ok {
		return 0, false
	}
	if long && p.Long != nil {
		p = *p.Long
	}
	return (float64(e.Input)*p.Input + float64(e.Output)*p.Output +
		float64(e.CacheRead)*p.CacheRead + float64(e.CacheWrite)*p.CacheWrite) / 1e6, true
}

// Total is an aggregate row. CostUSD sums reported plus estimated cost;
// Estimated is true when any part came from the price table, and
// Unpriced counts tokens from entries with neither reported nor estimable
// cost (their cost is not included).
type Total struct {
	Key            string  `json:"key,omitempty"`
	Model          string  `json:"model,omitempty"`
	Input          int64   `json:"input"`
	Output         int64   `json:"output"`
	CacheRead      int64   `json:"cache_read"`
	CacheWrite     int64   `json:"cache_write"`
	CostUSD        float64 `json:"cost_usd"`
	ReportedUSD    float64 `json:"reported_usd"`
	EstimatedUSD   float64 `json:"estimated_usd"`
	Estimated      bool    `json:"estimated"`
	HasCost        bool    `json:"has_cost"`
	UnpricedTokens int64   `json:"unpriced_tokens"`
	Turns          int64   `json:"turns"`
}

// Add folds one entry into the total.
func (t *Total) Add(e Entry) {
	t.Input += e.Input
	t.Output += e.Output
	t.CacheRead += e.CacheRead
	t.CacheWrite += e.CacheWrite
	t.Turns++
	if e.CostUSD != nil {
		t.ReportedUSD += *e.CostUSD
		t.CostUSD += *e.CostUSD
		t.HasCost = true
		return
	}
	if est, ok := Estimate(e); ok {
		t.EstimatedUSD += est
		t.CostUSD += est
		t.Estimated = true
		t.HasCost = true
		return
	}
	t.UnpricedTokens += e.Input + e.Output + e.CacheRead + e.CacheWrite
}

// Merge folds another total into t.
func (t *Total) Merge(o Total) {
	t.Input += o.Input
	t.Output += o.Output
	t.CacheRead += o.CacheRead
	t.CacheWrite += o.CacheWrite
	t.CostUSD += o.CostUSD
	t.ReportedUSD += o.ReportedUSD
	t.EstimatedUSD += o.EstimatedUSD
	t.Estimated = t.Estimated || o.Estimated
	t.HasCost = t.HasCost || o.HasCost
	t.UnpricedTokens += o.UnpricedTokens
	t.Turns += o.Turns
}

// Sub returns a-b per field, clamped at zero (cumulative counters reset when
// an agent process restarts, so a negative delta means "start over").
func Sub(a, b Entry) Entry {
	d := func(x, y int64) int64 {
		if x < y {
			return x
		}
		return x - y
	}
	out := Entry{Model: a.Model, Input: d(a.Input, b.Input), Output: d(a.Output, b.Output),
		CacheRead: d(a.CacheRead, b.CacheRead), CacheWrite: d(a.CacheWrite, b.CacheWrite)}
	if a.CostUSD != nil {
		prev := 0.0
		if b.CostUSD != nil {
			prev = *b.CostUSD
		}
		c := *a.CostUSD - prev
		if c < 0 {
			c = *a.CostUSD
		}
		out.CostUSD = &c
	}
	return out
}
