//go:build memharness

package daemon

// Synthetic transcript generator for the memory harness (memharness_test.go).
// It writes FAKE Claude Code and Codex transcripts that mirror the record
// shapes the harvester parses (see parseTurnLine, parseLineUsage, the
// noiseTypes list and the fixtures in harvester_test.go): user / assistant /
// tool_use / tool_result lines, api-request-blob noise, ai-title records,
// sidechain sub-agent files, and Codex rollout envelopes. No real data.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var synthWords = strings.Fields(`the a function returns error value handler config cache index
server client request response parse token session workspace module build test
fix update refactor memory buffer offset cursor file path line read write append
should could would because when then else return struct field method interface
goroutine channel mutex lock map slice string bytes json decode encode stream`)

type synthGen struct {
	r *rand.Rand
}

func (g *synthGen) words(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(synthWords[g.r.IntN(len(synthWords))])
	}
	return b.String()
}

// text returns roughly n bytes of word salad.
func (g *synthGen) text(n int) string {
	if n <= 0 {
		return ""
	}
	var b strings.Builder
	b.Grow(n + 16)
	for b.Len() < n {
		if b.Len() > 0 {
			if g.r.IntN(12) == 0 {
				b.WriteString(".\n")
			} else {
				b.WriteByte(' ')
			}
		}
		b.WriteString(synthWords[g.r.IntN(len(synthWords))])
	}
	return b.String()
}

func (g *synthGen) hex(n int) string {
	const digits = "0123456789abcdef"
	b := make([]byte, n)
	for i := range b {
		b[i] = digits[g.r.IntN(16)]
	}
	return string(b)
}

func (g *synthGen) uuid() string {
	h := g.hex(32)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

func mustLine(w *bufio.Writer, v any) int {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	w.Write(raw)
	w.WriteByte('\n')
	return len(raw) + 1
}

// synthSession describes one generated transcript.
type synthSession struct {
	Path   string `json:"path"`
	Agent  string `json:"agent"`
	ID     string `json:"id"`
	Cwd    string `json:"cwd"`
	Turns  int    `json:"turns"`
	Bytes  int64  `json:"bytes"`
	Live   bool   `json:"live"`
	Sidech bool   `json:"sidechain,omitempty"`
}

// claudeWriter appends Claude Code records for one session.
type claudeWriter struct {
	g         *synthGen
	sid, cwd  string
	sidechain bool
	parent    string
	at        time.Time
	history   []map[string]any // recent messages, repeated in api-request-blob
	turn      int
}

func (c *claudeWriter) base(typ string) map[string]any {
	u := c.g.uuid()
	m := map[string]any{
		"parentUuid":  c.parent,
		"isSidechain": c.sidechain,
		"userType":    "external",
		"cwd":         c.cwd,
		"sessionId":   c.sid,
		"version":     "2.0.31",
		"gitBranch":   "main",
		"type":        typ,
		"uuid":        u,
		"timestamp":   c.at.UTC().Format(time.RFC3339Nano),
	}
	c.parent = u
	c.at = c.at.Add(time.Duration(2+c.g.r.IntN(40)) * time.Second)
	return m
}

func (c *claudeWriter) remember(msg map[string]any) {
	c.history = append(c.history, msg)
	if len(c.history) > 24 {
		c.history = c.history[len(c.history)-24:]
	}
}

var synthTools = []string{"Edit", "Bash", "Read", "Write", "Grep"}

// writeTurn writes one user prompt and the assistant's tool-using reply.
// toolBytes sizes the tool_result; blobBytes > 0 adds an api-request-blob.
func (c *claudeWriter) writeTurn(w *bufio.Writer, toolBytes, blobBytes int) int {
	g := c.g
	n := 0
	c.turn++
	userMsg := map[string]any{"role": "user", "content": g.text(80 + g.r.IntN(600))}
	u := c.base("user")
	u["message"] = userMsg
	n += mustLine(w, u)
	c.remember(userMsg)

	tool := synthTools[g.r.IntN(len(synthTools))]
	toolID := "toolu_" + g.hex(24)
	var input map[string]any
	file := c.cwd + `\src\` + synthWords[g.r.IntN(len(synthWords))] + ".go"
	switch tool {
	case "Edit":
		input = map[string]any{"file_path": file, "old_string": g.text(200 + g.r.IntN(800)), "new_string": g.text(200 + g.r.IntN(1200))}
	case "Write":
		input = map[string]any{"file_path": file, "content": g.text(1000 + g.r.IntN(4000))}
	case "Bash":
		input = map[string]any{"command": "go test ./internal/" + synthWords[g.r.IntN(len(synthWords))] + "/...", "description": g.words(6)}
	case "Read":
		input = map[string]any{"file_path": file}
	default:
		input = map[string]any{"pattern": g.words(2), "path": c.cwd}
	}
	asstMsg := map[string]any{
		"id": "msg_" + g.hex(24), "type": "message", "role": "assistant", "model": "claude-sonnet-4-5",
		"content": []any{
			map[string]any{"type": "text", "text": g.text(100 + g.r.IntN(500))},
			map[string]any{"type": "tool_use", "id": toolID, "name": tool, "input": input},
		},
		"stop_reason": "tool_use",
		"usage": map[string]any{"input_tokens": 3 + g.r.IntN(50), "output_tokens": 50 + g.r.IntN(900),
			"cache_read_input_tokens": 10000 + g.r.IntN(90000), "cache_creation_input_tokens": g.r.IntN(4000)},
	}
	a := c.base("assistant")
	a["message"] = asstMsg
	a["requestId"] = "req_" + g.hex(24)
	n += mustLine(w, a)
	c.remember(asstMsg)

	resultMsg := map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "tool_result", "tool_use_id": toolID, "content": g.text(toolBytes)},
	}}
	tr := c.base("user")
	tr["message"] = resultMsg
	tr["toolUseResult"] = map[string]any{"stdout": g.text(toolBytes / 4), "stderr": "", "interrupted": false}
	n += mustLine(w, tr)
	c.remember(resultMsg)

	finalMsg := map[string]any{
		"id": "msg_" + g.hex(24), "type": "message", "role": "assistant", "model": "claude-sonnet-4-5",
		"content":     []any{map[string]any{"type": "text", "text": g.text(200 + g.r.IntN(1200))}},
		"stop_reason": "end_turn",
		"usage": map[string]any{"input_tokens": 3 + g.r.IntN(50), "output_tokens": 50 + g.r.IntN(900),
			"cache_read_input_tokens": 10000 + g.r.IntN(90000), "cache_creation_input_tokens": g.r.IntN(4000)},
	}
	f := c.base("assistant")
	f["message"] = finalMsg
	f["requestId"] = "req_" + g.hex(24)
	n += mustLine(w, f)
	c.remember(finalMsg)

	if blobBytes > 0 {
		// Claude Code's raw API request body: the recent conversation again
		// plus the system prompt. Pure noise to the harvester.
		msgs := c.history
		blob := map[string]any{
			"type":      "api-request-blob",
			"requestId": "req_" + g.hex(24),
			"sessionId": c.sid,
			"timestamp": c.at.UTC().Format(time.RFC3339Nano),
			"body": map[string]any{
				"model":    "claude-sonnet-4-5",
				"system":   g.text(blobBytes / 2),
				"messages": msgs,
			},
		}
		n += mustLine(w, blob)
	}
	if c.turn%25 == 0 {
		n += mustLine(w, map[string]any{"type": "file-history-snapshot", "messageId": g.uuid(),
			"snapshot": map[string]any{"trackedFileBackups": map[string]any{}, "timestamp": c.at.UTC().Format(time.RFC3339Nano)}})
	}
	if c.turn%40 == 1 {
		n += mustLine(w, map[string]any{"type": "ai-title", "sessionId": c.sid, "aiTitle": "Synthetic chat " + g.words(3)})
	}
	return n
}

// codexWriter appends Codex rollout records.
type codexWriter struct {
	g   *synthGen
	id  string
	cwd string
	at  time.Time
}

func (c *codexWriter) env(typ string, payload map[string]any) map[string]any {
	m := map[string]any{"timestamp": c.at.UTC().Format(time.RFC3339Nano), "type": typ, "payload": payload}
	c.at = c.at.Add(time.Duration(2+c.g.r.IntN(30)) * time.Second)
	return m
}

func (c *codexWriter) header(w *bufio.Writer) int {
	n := mustLine(w, c.env("session_meta", map[string]any{"id": c.id, "timestamp": c.at.UTC().Format(time.RFC3339Nano),
		"cwd": c.cwd, "originator": "codex_cli_rs", "cli_version": "0.58.0", "instructions": c.g.text(2000)}))
	n += mustLine(w, c.env("turn_context", map[string]any{"cwd": c.cwd, "approval_policy": "on-request",
		"model": "gpt-5-codex", "sandbox_policy": map[string]any{"mode": "workspace-write"}}))
	return n
}

func (c *codexWriter) writeTurn(w *bufio.Writer, toolBytes int) int {
	g := c.g
	n := 0
	prompt := g.text(80 + g.r.IntN(500))
	n += mustLine(w, c.env("response_item", map[string]any{"type": "message", "role": "user",
		"content": []any{map[string]any{"type": "input_text", "text": prompt}}}))
	n += mustLine(w, c.env("event_msg", map[string]any{"type": "user_message", "message": prompt}))
	n += mustLine(w, c.env("response_item", map[string]any{"type": "reasoning", "summary": []any{},
		"encrypted_content": g.hex(1500 + g.r.IntN(3000))}))
	args, _ := json.Marshal(map[string]any{"command": []string{"bash", "-lc", "rg " + g.words(2)}, "workdir": c.cwd})
	callID := "call_" + g.hex(20)
	n += mustLine(w, c.env("response_item", map[string]any{"type": "function_call", "name": "shell",
		"arguments": string(args), "call_id": callID}))
	n += mustLine(w, c.env("response_item", map[string]any{"type": "function_call_output", "call_id": callID,
		"output": g.text(toolBytes)}))
	n += mustLine(w, c.env("event_msg", map[string]any{"type": "token_count", "info": map[string]any{
		"total_token_usage": map[string]any{"input_tokens": 20000 + g.r.IntN(80000), "cached_input_tokens": 10000, "output_tokens": 500 + g.r.IntN(3000)},
		"last_token_usage":  map[string]any{"input_tokens": 2000 + g.r.IntN(8000), "cached_input_tokens": 1000, "output_tokens": 50 + g.r.IntN(900)},
	}}))
	reply := g.text(200 + g.r.IntN(1500))
	n += mustLine(w, c.env("response_item", map[string]any{"type": "message", "role": "assistant",
		"content": []any{map[string]any{"type": "output_text", "text": reply}}}))
	n += mustLine(w, c.env("event_msg", map[string]any{"type": "agent_message", "message": reply}))
	return n
}

// synthPlan sizes one session.
type synthPlan struct {
	agent     string
	cwd       string
	turns     int
	target    int64 // approximate bytes
	live      bool
	sidechain bool
	age       time.Duration
}

// generateSynthetic writes the synthetic home under base and returns the
// sessions (also saved to base/manifest.json).
func generateSynthetic(base string, seed uint64) ([]synthSession, error) {
	g := &synthGen{r: rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))}
	home := filepath.Join(base, "home")
	ws := filepath.Join(base, "work", "demoapp")
	other := filepath.Join(base, "work", "otherapp")
	for _, d := range []string{home, ws, other} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	var plans []synthPlan
	day := 24 * time.Hour
	small := func(agent, cwd string, n int) {
		for i := 0; i < n; i++ {
			turns := 30 + g.r.IntN(370)
			plans = append(plans, synthPlan{agent: agent, cwd: cwd, turns: turns, target: int64(turns) * int64(2500+g.r.IntN(3000)),
				sidechain: agent == "claude" && g.r.IntN(10) < 3, age: time.Duration(1+g.r.IntN(30*24)) * time.Hour})
		}
	}
	big := func(agent, cwd string, n, live int) {
		for i := 0; i < n; i++ {
			turns := 2000 + g.r.IntN(3001)
			mb := 20 + g.r.IntN(41)
			plans = append(plans, synthPlan{agent: agent, cwd: cwd, turns: turns, target: int64(mb) << 20,
				live: i < live, sidechain: agent == "claude", age: time.Duration(1+g.r.IntN(10)) * day})
		}
	}
	big("claude", ws, 8, 3)
	small("claude", ws, 102)
	small("claude", other, 30)
	big("codex", ws, 2, 1)
	small("codex", ws, 43)
	small("codex", other, 15)

	var out []synthSession
	for _, p := range plans {
		s, err := writeSynthSession(g, home, p)
		if err != nil {
			return nil, err
		}
		out = append(out, s...)
	}
	raw, _ := json.MarshalIndent(out, "", " ")
	if err := os.WriteFile(filepath.Join(base, "manifest.json"), raw, 0o644); err != nil {
		return nil, err
	}
	return out, nil
}

func writeSynthSession(g *synthGen, home string, p synthPlan) ([]synthSession, error) {
	id := g.uuid()
	start := time.Now().Add(-p.age)
	perTurn := int(p.target / int64(p.turns))
	var path string
	switch p.agent {
	case "claude":
		path = filepath.Join(home, ".claude", "projects", claudeProjectEncoded(p.cwd), id+".jsonl")
	default:
		path = filepath.Join(home, ".codex", "sessions", start.Format("2006"), start.Format("01"), start.Format("02"),
			"rollout-"+start.Format("2006-01-02T15-04-05")+"-"+id+".jsonl")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	w := bufio.NewWriterSize(f, 1<<20)
	var total int64
	switch p.agent {
	case "claude":
		c := &claudeWriter{g: g, sid: id, cwd: p.cwd, at: start}
		for i := 0; i < p.turns; i++ {
			// ~45% of the budget in tool output, ~45% in periodic blobs.
			blob := 0
			if i%8 == 7 {
				blob = perTurn * 8 * 30 / 100
			}
			total += int64(c.writeTurn(w, perTurn*25/100, blob))
		}
	default:
		c := &codexWriter{g: g, id: id, cwd: p.cwd, at: start}
		total += int64(c.header(w))
		for i := 0; i < p.turns; i++ {
			total += int64(c.writeTurn(w, perTurn*70/100))
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return nil, err
	}
	f.Close()
	mt := start.Add(time.Duration(p.turns) * 20 * time.Second)
	if mt.After(time.Now()) || p.live {
		mt = time.Now()
	}
	_ = os.Chtimes(path, mt, mt)
	out := []synthSession{{Path: path, Agent: p.agent, ID: id, Cwd: p.cwd, Turns: p.turns, Bytes: total, Live: p.live}}
	if p.sidechain {
		for k := 0; k < 1+g.r.IntN(3); k++ {
			sp := filepath.Join(filepath.Dir(path), id, "subagents", "agent-"+g.hex(8)+".jsonl")
			_ = os.MkdirAll(filepath.Dir(sp), 0o755)
			sf, err := os.Create(sp)
			if err != nil {
				return nil, err
			}
			sw := bufio.NewWriterSize(sf, 256<<10)
			c := &claudeWriter{g: g, sid: id, cwd: p.cwd, at: start, sidechain: true}
			var n int64
			turns := 50 + g.r.IntN(250)
			for i := 0; i < turns; i++ {
				n += int64(c.writeTurn(sw, 1500, 0))
			}
			sw.Flush()
			sf.Close()
			_ = os.Chtimes(sp, mt, mt)
			out = append(out, synthSession{Path: sp, Agent: "claude", ID: id, Cwd: p.cwd, Turns: turns, Bytes: n, Sidech: true})
		}
	}
	return out, nil
}

// liveAppender grows the live sessions like an agent that is still working.
type liveAppender struct {
	g      *synthGen
	claude map[string]*claudeWriter
	codex  map[string]*codexWriter
}

func newLiveAppender(seed uint64, sessions []synthSession) *liveAppender {
	g := &synthGen{r: rand.New(rand.NewPCG(seed, 7))}
	la := &liveAppender{g: g, claude: map[string]*claudeWriter{}, codex: map[string]*codexWriter{}}
	for _, s := range sessions {
		if !s.Live {
			continue
		}
		if s.Agent == "claude" {
			la.claude[s.Path] = &claudeWriter{g: g, sid: s.ID, cwd: s.Cwd, at: time.Now(), turn: s.Turns}
		} else {
			la.codex[s.Path] = &codexWriter{g: g, id: s.ID, cwd: s.Cwd, at: time.Now()}
		}
	}
	return la
}

// appendTurns adds turns to every live file and returns bytes written.
func (la *liveAppender) appendTurns(turns int) (int64, error) {
	var total int64
	write := func(path string, fn func(w *bufio.Writer) int) error {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		w := bufio.NewWriterSize(f, 256<<10)
		total += int64(fn(w))
		err = w.Flush()
		f.Close()
		return err
	}
	for path, c := range la.claude {
		c.at = time.Now()
		if err := write(path, func(w *bufio.Writer) int {
			n := 0
			for i := 0; i < turns; i++ {
				blob := 0
				if i%3 == 2 {
					blob = 300 << 10
				}
				n += c.writeTurn(w, 4000, blob)
			}
			return n
		}); err != nil {
			return total, err
		}
	}
	for path, c := range la.codex {
		c.at = time.Now()
		if err := write(path, func(w *bufio.Writer) int {
			n := 0
			for i := 0; i < turns; i++ {
				n += c.writeTurn(w, 6000)
			}
			return n
		}); err != nil {
			return total, err
		}
	}
	return total, nil
}

func (s synthSession) String() string {
	return fmt.Sprintf("%s %s turns=%d %.1fMB live=%v", s.Agent, filepath.Base(s.Path), s.Turns, float64(s.Bytes)/(1<<20), s.Live)
}
