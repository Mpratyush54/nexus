package continuex

// Token usage for live chats. Each driver reports usage differently:
//
//	Claude   assistant messages carry message.usage (one API call, repeated
//	         on every content line of that message); the result event
//	         carries modelUsage and total_cost_usd, which are cumulative for
//	         the process (the CLI's session counters), so a turn is the
//	         difference from the previous result.
//	Codex    thread/tokenUsage/updated carries tokenUsage.total (cumulative
//	         for the thread); a turn is total minus the total at turn start.
//	ACP      session/prompt results may carry usage (unstable in ACP);
//	         used when present.
//
// Drivers send Kind "usage" while a turn streams (Usage = turn so far) and
// put the turn's final usage on turn_done.

import (
	"encoding/json"
	"sort"
	"sync"

	"central-memory/internal/usage"
)

// turnUsage accumulates one turn's usage keyed by call (message id) and
// reports it by model.
type turnUsage struct {
	mu    sync.Mutex
	calls map[string]usage.Entry
	order []string
}

// observe records one call's usage; repeated keys keep the larger counts.
// It reports whether anything changed.
func (t *turnUsage) observe(key string, e usage.Entry) bool {
	if e.Empty() || key == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.calls == nil {
		t.calls = map[string]usage.Entry{}
	}
	prev, seen := t.calls[key]
	if !seen {
		t.order = append(t.order, key)
		t.calls[key] = e
		return true
	}
	next := prev
	next.Input, next.Output = max(prev.Input, e.Input), max(prev.Output, e.Output)
	next.CacheRead, next.CacheWrite = max(prev.CacheRead, e.CacheRead), max(prev.CacheWrite, e.CacheWrite)
	if next == prev {
		return false
	}
	t.calls[key] = next
	return true
}

// byModel sums the turn so far per model, in first-seen order.
func (t *turnUsage) byModel() []usage.Entry {
	t.mu.Lock()
	defer t.mu.Unlock()
	return sumByModel(t.order, t.calls)
}

func (t *turnUsage) reset() {
	t.mu.Lock()
	t.calls, t.order = nil, nil
	t.mu.Unlock()
}

func sumByModel(order []string, calls map[string]usage.Entry) []usage.Entry {
	idx := map[string]int{}
	var out []usage.Entry
	for _, k := range order {
		e := calls[k]
		i, ok := idx[e.Model]
		if !ok {
			idx[e.Model] = len(out)
			out = append(out, usage.Entry{Model: e.Model})
			i = len(out) - 1
		}
		out[i].Input += e.Input
		out[i].Output += e.Output
		out[i].CacheRead += e.CacheRead
		out[i].CacheWrite += e.CacheWrite
		if e.CostUSD != nil {
			c := *e.CostUSD
			if out[i].CostUSD != nil {
				c += *out[i].CostUSD
			}
			out[i].CostUSD = &c
		}
	}
	return out
}

// claudeModelUsage is one entry of the result event's modelUsage map.
type claudeModelUsage struct {
	InputTokens              int64   `json:"inputTokens"`
	OutputTokens             int64   `json:"outputTokens"`
	CacheReadInputTokens     int64   `json:"cacheReadInputTokens"`
	CacheCreationInputTokens int64   `json:"cacheCreationInputTokens"`
	CostUSD                  float64 `json:"costUSD"`
}

// claudeTurnDelta turns the cumulative modelUsage of a result into this
// turn's usage per model, updating prev. A process restart resets the
// counters; usage.Sub then treats the new value as the whole turn.
func claudeTurnDelta(raw map[string]claudeModelUsage, prev map[string]usage.Entry) []usage.Entry {
	models := make([]string, 0, len(raw))
	for m := range raw {
		models = append(models, m)
	}
	sort.Strings(models)
	var out []usage.Entry
	for _, m := range models {
		r := raw[m]
		cost := r.CostUSD
		cur := usage.Entry{Model: m, Input: r.InputTokens, Output: r.OutputTokens,
			CacheRead: r.CacheReadInputTokens, CacheWrite: r.CacheCreationInputTokens, CostUSD: &cost}
		d := usage.Sub(cur, prev[m])
		prev[m] = cur
		if !d.Empty() {
			out = append(out, d)
		}
	}
	return out
}

// codexTokenUsage is the v2 TokenUsageBreakdown (codex-cli 0.167.0 schema).
type codexTokenUsage struct {
	InputTokens           int64 `json:"inputTokens"`
	CachedInputTokens     int64 `json:"cachedInputTokens"`
	CacheWriteInputTokens int64 `json:"cacheWriteInputTokens"`
	OutputTokens          int64 `json:"outputTokens"`
	TotalTokens           int64 `json:"totalTokens"`
}

// entry splits OpenAI-style input (which includes cached input).
func (c codexTokenUsage) entry(model string) usage.Entry {
	cached := min(c.CachedInputTokens, c.InputTokens)
	in := c.InputTokens - cached
	write := min(c.CacheWriteInputTokens, in)
	return usage.Entry{Model: model, Input: in - write, CacheRead: cached, CacheWrite: write, Output: c.OutputTokens}
}

// acpPromptUsage reads the optional usage of an ACP session/prompt result.
func acpPromptUsage(result json.RawMessage, model string) []usage.Entry {
	var r struct {
		Usage *struct {
			InputTokens       int64 `json:"inputTokens"`
			OutputTokens      int64 `json:"outputTokens"`
			ThoughtTokens     int64 `json:"thoughtTokens"`
			CachedReadTokens  int64 `json:"cachedReadTokens"`
			CachedWriteTokens int64 `json:"cachedWriteTokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(result, &r) != nil || r.Usage == nil {
		return nil
	}
	e := usage.Entry{Model: model, Input: r.Usage.InputTokens, Output: r.Usage.OutputTokens + r.Usage.ThoughtTokens,
		CacheRead: r.Usage.CachedReadTokens, CacheWrite: r.Usage.CachedWriteTokens}
	if e.Empty() {
		return nil
	}
	return []usage.Entry{e}
}
