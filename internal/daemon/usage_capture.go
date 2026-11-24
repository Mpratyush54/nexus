package daemon

// Token usage from agent transcripts. Each agent records it differently:
//
//	Claude   every assistant line carries message.usage + message.model;
//	         one API message is split across several lines that repeat the
//	         same usage, so the key is the message id.
//	Codex    event_msg/token_count carries info.last_token_usage (one model
//	         call) and info.total_token_usage (cumulative). Codex repeats
//	         token_count events, so the key is the cumulative total. The
//	         model comes from the preceding turn_context line.
//	Gemini   gemini messages carry tokens{input,output,cached,thoughts} and
//	         model; keyed by message id.
//	OpenCode message rows carry tokens and cost (opencode_sqlite.go).
//
// OpenAI-style input counts include cached input; usage.Entry.Input does not.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"central-memory/internal/usage"
)

func numField(m map[string]any, key string) int64 {
	switch v := m[key].(type) {
	case float64:
		if v > 0 {
			return int64(v)
		}
	case json.Number:
		n, _ := v.Int64()
		if n > 0 {
			return n
		}
	}
	return 0
}

// parseLineUsage extracts token usage from one JSONL line. model is set
// (with u == nil) for lines that only announce the model in use.
func parseLineUsage(line []byte) (u *usage.Entry, key, model string) {
	if !jsonMayHaveUsage(line) {
		return nil, "", ""
	}
	var m map[string]any
	if json.Unmarshal(line, &m) != nil {
		return nil, "", ""
	}
	typ := strField(m, "type")
	switch typ {
	case "assistant":
		msg, _ := m["message"].(map[string]any)
		raw, _ := msg["usage"].(map[string]any)
		if raw == nil {
			return nil, "", ""
		}
		mdl := strField(msg, "model")
		if mdl == "<synthetic>" {
			return nil, "", ""
		}
		e := usage.Entry{
			Model:      mdl,
			Input:      numField(raw, "input_tokens"),
			Output:     numField(raw, "output_tokens"),
			CacheRead:  numField(raw, "cache_read_input_tokens"),
			CacheWrite: numField(raw, "cache_creation_input_tokens"),
		}
		id := firstNonEmpty(strField(msg, "id"), strField(m, "requestId"), strField(m, "uuid"))
		if e.Empty() || id == "" {
			return nil, "", ""
		}
		return &e, "claude:" + id, ""
	case "turn_context":
		p, _ := m["payload"].(map[string]any)
		return nil, "", strField(p, "model")
	case "event_msg":
		p, _ := m["payload"].(map[string]any)
		if strField(p, "type") != "token_count" {
			return nil, "", ""
		}
		info, _ := p["info"].(map[string]any)
		last, _ := info["last_token_usage"].(map[string]any)
		total, _ := info["total_token_usage"].(map[string]any)
		if last == nil || total == nil {
			return nil, "", "" // rate-limit-only update
		}
		e := openAIUsage(last["input_tokens"], last["cached_input_tokens"], last["output_tokens"])
		e.CacheWrite = numField(last, "cache_write_input_tokens")
		if e.Input >= e.CacheWrite {
			e.Input -= e.CacheWrite
		}
		cum := numField(total, "total_tokens")
		if e.Empty() || cum == 0 {
			return nil, "", ""
		}
		return &e, "codex-total:" + strconv.FormatInt(cum, 10), ""
	case "gemini":
		raw, _ := m["tokens"].(map[string]any)
		id := strField(m, "id")
		if raw == nil || id == "" {
			return nil, "", ""
		}
		e := openAIUsage(raw["input"], raw["cached"], raw["output"])
		e.Output += numField(raw, "thoughts")
		e.Model = strField(m, "model")
		if e.Empty() {
			return nil, "", ""
		}
		return &e, "gemini:" + id, ""
	}
	return nil, "", ""
}

// openAIUsage splits an input count that includes cached input.
func openAIUsage(input, cached, output any) usage.Entry {
	m := map[string]any{"i": input, "c": cached, "o": output}
	in, c := numField(m, "i"), numField(m, "c")
	if c > in {
		c = in
	}
	return usage.Entry{Input: in - c, CacheRead: c, Output: numField(m, "o")}
}

// jsonMayHaveUsage is a cheap pre-filter so ordinary lines are not decoded twice.
func jsonMayHaveUsage(line []byte) bool {
	return bytes.Contains(line, []byte(`"usage"`)) || bytes.Contains(line, []byte(`"token_count"`)) ||
		bytes.Contains(line, []byte(`"turn_context"`)) || bytes.Contains(line, []byte(`"tokens"`))
}

// lineTime reads the timestamp of a usage-only line.
func lineTime(line []byte) time.Time {
	var m map[string]any
	if json.Unmarshal(line, &m) != nil {
		return time.Time{}
	}
	return extractTime(m)
}

func (h *Harvester) setModel(path, model string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.models == nil {
		h.models = make(map[string]string)
	}
	h.models[path] = model
}

// modelFor returns the model last announced in path. Tailing starts near
// the end of large files, past the turn_context lines, so on a miss the
// head of the file is scanned once.
func (h *Harvester) modelFor(path string) string {
	h.mu.Lock()
	model, seen := h.models[path]
	h.mu.Unlock()
	if seen {
		return model
	}
	model = peekModel(path)
	h.setModel(path, model)
	return model
}

func peekModel(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(io.LimitReader(f, 4<<20))
	sc.Buffer(make([]byte, 64<<10), maxJSONLLineBytes)
	model := ""
	for sc.Scan() {
		if !strings.Contains(sc.Text(), `"turn_context"`) {
			continue
		}
		if _, _, m := parseLineUsage(sc.Bytes()); m != "" {
			model = m
		}
	}
	return model
}

// usagePayload renders usage for the CONVERSATION_TURN payload.
func usagePayload(u usage.Entry, key string) map[string]any {
	p := map[string]any{
		"model": u.Model, "input": u.Input, "output": u.Output,
		"cache_read": u.CacheRead, "cache_write": u.CacheWrite,
	}
	if u.CostUSD != nil {
		p["cost_usd"] = *u.CostUSD
	}
	if key != "" {
		p["key"] = key
	}
	return p
}

// usageFromPayload is the inverse of usagePayload (the runtime receives
// events, not Turns).
func usageFromPayload(v any) (*usage.Entry, string) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, ""
	}
	num := func(k string) int64 {
		switch n := m[k].(type) {
		case int64:
			return n
		case int:
			return int64(n)
		case float64:
			return int64(n)
		}
		return 0
	}
	e := usage.Entry{Input: num("input"), Output: num("output"), CacheRead: num("cache_read"), CacheWrite: num("cache_write")}
	e.Model, _ = m["model"].(string)
	if c, ok := m["cost_usd"].(float64); ok {
		e.CostUSD = &c
	}
	key, _ := m["key"].(string)
	if e.Empty() {
		return nil, key
	}
	return &e, key
}
