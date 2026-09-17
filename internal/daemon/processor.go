// Package daemon implements the local workspace daemon (plan §1.3).
//
// This file is the Memory Processor (plan §§2.3, 2.6–2.8, issue #10): a
// background goroutine that runs only on is_designated_processor workspaces,
// batches harvested events, extracts memories via the user's own LLM key, and
// delegates every write to the store layer through ProcessorStore.
//
// Pipeline:
//
//	Layer 1–4 events → Processor.Ingest (via Sink adapter)
//	    → batch on 5-min idle or SESSION_TRANSCRIPT_COMPLETE
//	    → BuildExtractionPrompt (§2.6) → LLMClient.Complete
//	    → ParseExtractedMemories → NormalizeLevel/Scope (default SESSION)
//	    → dedup skip when cosine > 0.9 vs CONFIRMED
//	    → SaveProposed with auto-confirm timer (24h / 4h / 1h, §2.8)
//	    → DetectEpisode hook (§2.3) → SaveEpisode
//
// Design rules for this file:
//   - Ownership: ONLY this file (+ its test) and docs/ may be touched by
//     issue #10. harvester.go, interceptor.go, watcher.go and daemon.go are
//     read-only — their EventSink type, Clock type and event-type constants
//     are reused, never redeclared.
//   - No new SDK deps (plan §1.9 + issue constraint): LLM access is the
//     LLMClient interface plus a stub and an Ollama HTTP implementation over
//     net/http. OpenAI/Anthropic callers implement the same one-method
//     interface with the user's key; no vendor SDK is imported.
//   - Persistence is delegated: ProcessorStore is the seam the store layer
//     (issue #2, internal/store) implements. The processor never imports
//     internal/store and never touches SQL.
//   - Pure/testable core: prompt building, response parsing, level/scope
//     normalization, cosine math, confirm-timer rules and episode detection
//     are pure functions. The live ticker loop is a thin shell around them,
//     and every test uses a fake LLM + fake store with no network.
package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Tuning constants (plan §§2.3, 2.6–2.8).
const (
	// ProcessorIdleAfter mirrors the harvester's 5-minute end-of-session
	// threshold (plan §§1.3, 2.2): a session quiet this long is flushed as
	// a batch even without SESSION_TRANSCRIPT_COMPLETE.
	ProcessorIdleAfter = 5 * time.Minute
	// ProcessorSweepInterval is how often the background loop checks for
	// idle-due sessions (mirrors the harvester's 1-minute idle sweep).
	ProcessorSweepInterval = time.Minute

	// DedupCosineThreshold: an extracted candidate whose cosine similarity
	// exceeds this against any CONFIRMED memory is skipped as a duplicate.
	DedupCosineThreshold = 0.9
	// HighConfidenceThreshold gates the 4h fast-confirm timer (§2.8).
	HighConfidenceThreshold = 0.9

	// ConfirmAfterDefault: PROPOSED items auto-confirm after 24h (§2.8).
	ConfirmAfterDefault = 24 * time.Hour
	// ConfirmAfterHighConfidence: items with confidence > 0.9 confirm after 4h.
	ConfirmAfterHighConfidence = 4 * time.Hour
	// ConfirmAfterExplicitUser: items from explicit user statements confirm
	// after 1h (detected by the processor via explicit_user_statement).
	ConfirmAfterExplicitUser = time.Hour

	// MinContentChars / MaxContentChars mirror the memory_items content
	// CHECK (plan §1.1: 20–2000 chars). Candidates outside the range are
	// dropped before persistence so the store never sees a CHECK violation.
	MinContentChars = 20
	MaxContentChars = 2000
)

// ---------------------------------------------------------------------------
// Events
// ---------------------------------------------------------------------------

// ProcessorEvent is one harvested event buffered for extraction. SessionID
// routes the event to its per-session batch; At anchors idle math (zero At
// is stamped with the processor clock on ingest).
type ProcessorEvent struct {
	Type      string
	Payload   map[string]any
	At        time.Time
	SessionID string
}

// sessionIDOf extracts the session id from a sink payload ("session_id").
func sessionIDOf(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	if s, ok := payload["session_id"].(string); ok {
		return s
	}
	return ""
}

// ---------------------------------------------------------------------------
// Extracted memories
// ---------------------------------------------------------------------------

// ExtractedMemory is one LLM-proposed fact (plan §2.6 classification).
// Explicit marks items drawn from a direct user statement ("I prefer…",
// "we decided…") as reported by the LLM via explicit_user_statement; it
// drives the 1h fast-confirm timer (§2.8).
type ExtractedMemory struct {
	Key            string
	Content        string
	ContextSnippet string
	Level          string
	Scope          string
	Confidence     float64
	Tags           []string
	Explicit       bool
	SessionID      string
}

// NormalizeLevel maps an LLM label to the memory_items level CHECK set
// (plan §1.1). Plan §2.6: "Default to SESSION level if unsure (safer — can
// be promoted later)", so unknown/empty labels become session.
func NormalizeLevel(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case LevelOrganization:
		return LevelOrganization
	case LevelProject:
		return LevelProject
	case LevelPersonal:
		return LevelPersonal
	case LevelSession:
		return LevelSession
	default:
		return LevelSession
	}
}

// Level constants mirror the memory_items CHECK constraint (plan §1.1) and
// the store layer's vocabulary (internal/store/memory.go) without importing
// it — this package stays decoupled from internal/store by design.
const (
	LevelOrganization = "organization"
	LevelProject      = "project"
	LevelPersonal     = "personal"
	LevelSession      = "session"
)

// NormalizeScope maps an LLM label to the scope CHECK set (plan §1.1:
// fact, preference, decision, constraint, pattern, episode_summary).
// Unknown/empty labels default to "fact" — the neutral, objective bucket;
// a wrong guess here is harmless because scope never gates visibility.
func NormalizeScope(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "fact":
		return "fact"
	case "preference":
		return "preference"
	case "decision":
		return "decision"
	case "constraint":
		return "constraint"
	case "pattern":
		return "pattern"
	case "episode_summary":
		return "episode_summary"
	default:
		return "fact"
	}
}

// ConfirmAfterFor is the plan §2.8 auto-confirm rule, most-urgent first:
// explicit user statement → 1h; confidence > 0.9 → 4h; otherwise 24h.
func ConfirmAfterFor(m ExtractedMemory) time.Duration {
	if m.Explicit {
		return ConfirmAfterExplicitUser
	}
	if m.Confidence > HighConfidenceThreshold {
		return ConfirmAfterHighConfidence
	}
	return ConfirmAfterDefault
}

// ---------------------------------------------------------------------------
// Extraction prompt (plan §2.6)
// ---------------------------------------------------------------------------

// BuildExtractionPrompt renders the plan §2.6 processor prompt: level + scope
// classification rules with the SESSION default, the CONFIRMED-memory
// do-not-duplicate list, the new event batch, and the JSON output contract
// the parser (ParseExtractedMemories) consumes.
func BuildExtractionPrompt(projectName string, existing []string, batch []ProcessorEvent) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Given these events from project %q, extract memories.\n\n", projectName)
	b.WriteString(`Classify each memory's level:
- ORGANIZATION: universal policy across all projects (e.g., "all APIs use JWT")
- PROJECT: team decision or codebase fact (e.g., "we use pytest")
- PERSONAL: individual preference (e.g., "Alice prefers verbose errors")
  → Look for "I prefer", "I like", "I always"
- SESSION: temporary, task-specific (e.g., "don't touch payments/ right now")
  → Look for "for now", "right now", "during this", "in this task"

Classify each memory's scope:
- fact: objective truth about the codebase
- preference: subjective choice
- decision: deliberate team choice with reasoning
- constraint: hard rule that must not be violated
- pattern: recurring code/architecture pattern
- episode_summary: condensed bug/incident takeaway

Default to SESSION level if unsure (safer — can be promoted later).
`)
	b.WriteString("\nExisting confirmed memories (do not duplicate):\n")
	if len(existing) == 0 {
		b.WriteString("(none)\n")
	} else {
		for _, m := range existing {
			b.WriteString("- " + m + "\n")
		}
	}
	b.WriteString("\nNew events to process:\n")
	if len(batch) == 0 {
		b.WriteString("(none)\n")
	} else {
		for _, ev := range batch {
			fmt.Fprintf(&b, "[%s] %s %s\n", ev.Type, ev.SessionID, renderPayload(ev.Payload))
		}
	}
	b.WriteString(`
Reply with a JSON array only (no prose, no markdown fences). One object per memory:
[{"key": "area/name", "content": "natural-language fact, 20-500 chars",
  "context_snippet": "1-2 line provenance, e.g. decided by Alice during auth refactor",
  "level": "organization|project|personal|session",
  "scope": "fact|preference|decision|constraint|pattern|episode_summary",
  "confidence": 0.0-1.0,
  "tags": ["optional", "keywords"],
  "explicit_user_statement": true if the user stated this directly, else false}]
Omit memories with nothing durable to record — an empty array [] is a valid answer.`)
	return b.String()
}

// renderPayload flattens an event payload to one stable, prompt-safe line.
func renderPayload(payload map[string]any) string {
	if len(payload) == 0 {
		return "{}"
	}
	keys := make([]string, 0, len(payload))
	for k := range payload {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("{")
	for i, k := range keys {
		if i > 0 {
			b.WriteString(", ")
		}
		s := fmt.Sprint(payload[k])
		if len([]rune(s)) > 400 {
			s = string([]rune(s)[:400]) + "…[truncated]"
		}
		s = strings.ReplaceAll(s, "\n", " ")
		fmt.Fprintf(&b, "%s: %s", k, s)
	}
	b.WriteString("}")
	return b.String()
}

// extractedJSON is the wire shape ParseExtractedMemories accepts (a subset
// of ExtractedMemory with JSON tags matching the prompt contract).
type extractedJSON struct {
	Key              string   `json:"key"`
	Content          string   `json:"content"`
	ContextSnippet   string   `json:"context_snippet"`
	Level            string   `json:"level"`
	Scope            string   `json:"scope"`
	Confidence       float64  `json:"confidence"`
	Tags             []string `json:"tags"`
	ExplicitUserStmt bool     `json:"explicit_user_statement"`
}

// ParseExtractedMemories parses one LLM completion into normalized memories.
// It tolerates prose around the JSON array (takes the outermost [...] span),
// normalizes level/scope, clamps confidence to [0,1], trims over-long
// content to MaxContentChars, and drops entries with an empty key or empty
// content — those can never satisfy the memory_items constraints.
func ParseExtractedMemories(resp, sessionID string) []ExtractedMemory {
	start := strings.Index(resp, "[")
	end := strings.LastIndex(resp, "]")
	if start < 0 || end < 0 || end <= start {
		return nil
	}
	var raw []extractedJSON
	if err := json.Unmarshal([]byte(resp[start:end+1]), &raw); err != nil {
		return nil
	}
	out := make([]ExtractedMemory, 0, len(raw))
	for _, r := range raw {
		key := strings.TrimSpace(r.Key)
		content := strings.TrimSpace(r.Content)
		if key == "" || content == "" {
			continue
		}
		if n := len([]rune(content)); n > MaxContentChars {
			content = string([]rune(content)[:MaxContentChars])
		}
		conf := r.Confidence
		if math.IsNaN(conf) {
			conf = 0
		}
		if conf < 0 {
			conf = 0
		}
		if conf > 1 {
			conf = 1
		}
		out = append(out, ExtractedMemory{
			Key:            key,
			Content:        content,
			ContextSnippet: strings.TrimSpace(r.ContextSnippet),
			Level:          NormalizeLevel(r.Level),
			Scope:          NormalizeScope(r.Scope),
			Confidence:     conf,
			Tags:           append([]string(nil), r.Tags...),
			Explicit:       r.ExplicitUserStmt,
			SessionID:      sessionID,
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// Dedup (cosine > 0.9 vs CONFIRMED skips)
// ---------------------------------------------------------------------------

// VecCosine is cosine similarity in [-1,1] over embedding vectors; it
// returns 0 for empty, mismatched or zero-norm inputs (never NaN), matching
// the store layer's CosineSimilarity semantics without importing it.
func VecCosine(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// TokenCosineSimilarity is the embedding-free fallback: cosine over
// lowercase word-frequency vectors. Identical texts score exactly 1;
// disjoint texts score 0. Used when either side lacks an embedding.
func TokenCosineSimilarity(a, b string) float64 {
	freq := func(s string) map[string]float64 {
		m := map[string]float64{}
		for _, tok := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_')
		}) {
			m[tok]++
		}
		return m
	}
	fa, fb := freq(a), freq(b)
	if len(fa) == 0 || len(fb) == 0 {
		return 0
	}
	var dot, na, nb float64
	for tok, x := range fa {
		na += x * x
		if y, ok := fb[tok]; ok {
			dot += x * y
		}
	}
	for _, y := range fb {
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// ConfirmedMemory is the dedup surface the processor needs from the store:
// content plus an optional embedding. The store owner maps store.MemoryItem
// onto this at the boundary (same decoupling as context.Item).
type ConfirmedMemory struct {
	Key       string
	Content   string
	Embedding []float32
}

// IsNearDuplicate reports whether candidate content matches any CONFIRMED
// memory with cosine > DedupCosineThreshold (0.9). Embedding cosine wins
// when both sides carry same-length embeddings; otherwise the token
// fallback compares raw text.
func IsNearDuplicate(content string, embedding []float32, existing []ConfirmedMemory) bool {
	for _, e := range existing {
		var sim float64
		if len(embedding) > 0 && len(e.Embedding) == len(embedding) {
			sim = VecCosine(embedding, e.Embedding)
		} else {
			sim = TokenCosineSimilarity(content, e.Content)
		}
		if sim > DedupCosineThreshold {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Episode auto-detection (plan §2.3)
// ---------------------------------------------------------------------------

// EpisodeDraft is the bug/incident arc DetectEpisode assembles from an event
// batch. Persistence (episodes row + episode_events links + embedding) is
// the store layer's job via ProcessorStore.SaveEpisode.
type EpisodeDraft struct {
	Title         string
	EpisodeType   string // "bug_fix" for the auto-detected arc (§2.3)
	Trigger       string
	Investigation string
	RootCause     string
	Resolution    string
	Verification  string
	ErrorPatterns []string
	FilesInvolved []string
	SessionID     string
}

// EpisodeEventRef links a batch index to its episode_events role (plan §1.1
// CHECK: trigger, investigation, attempt, fix, verification, context).
type EpisodeEventRef struct {
	Index int
	Role  string
}

// payloadString coerces payload values to string.
func payloadString(payload map[string]any, key string) string {
	if payload == nil {
		return ""
	}
	if s, ok := payload[key].(string); ok {
		return s
	}
	if v, ok := payload[key]; ok && v != nil {
		return fmt.Sprint(v)
	}
	return ""
}

// payloadExitCode coerces exit_code across int/float64/json.Number encodings;
// ok=false when absent or unparseable.
func payloadExitCode(payload map[string]any) (code int, ok bool) {
	if payload == nil {
		return 0, false
	}
	switch v := payload["exit_code"].(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return int(i), true
		}
	}
	return 0, false
}

// DetectEpisode scans a session batch for the plan §2.3 bug arc:
//
//	COMMAND_EXECUTED exit≠0 (trigger) → FILE_READ series (investigation) →
//	FILE_MODIFIED (fix) → COMMAND_EXECUTED exit=0 (verification) →
//	GIT_COMMITTED (resolution)
//
// It returns nil when there is no failure trigger, or when the failure
// stands alone with no follow-up (a lone failing command is noise, not an
// episode — the arc needs at least one of investigation/fix/verification).
// RootCause is left empty for the LLM/store to fill: raw tool events show
// what happened, not why.
func DetectEpisode(batch []ProcessorEvent) (*EpisodeDraft, []EpisodeEventRef) {
	triggerIdx := -1
	for i, ev := range batch {
		if ev.Type != EventCommandExecuted {
			continue
		}
		if code, ok := payloadExitCode(ev.Payload); ok && code != 0 {
			triggerIdx = i
			break
		}
	}
	if triggerIdx < 0 {
		return nil, nil
	}
	trigger := batch[triggerIdx]
	triggerCmd := payloadString(trigger.Payload, "cmdline")
	stderr := payloadString(trigger.Payload, "stderr")

	var reads, fixes []string
	seen := map[string]bool{}
	verifyIdx, commitIdx := -1, -1
	var verifyCmd string
	for i := triggerIdx + 1; i < len(batch); i++ {
		ev := batch[i]
		switch ev.Type {
		case EventFileRead:
			if p := payloadString(ev.Payload, "path"); p != "" && !seen["r"+p] {
				seen["r"+p] = true
				reads = append(reads, p)
			}
		case EventFileModified:
			if p := payloadString(ev.Payload, "path"); p != "" && !seen["w"+p] {
				seen["w"+p] = true
				fixes = append(fixes, p)
			}
		case EventCommandExecuted:
			code, ok := payloadExitCode(ev.Payload)
			if !ok || code != 0 || verifyIdx >= 0 {
				continue
			}
			cmd := payloadString(ev.Payload, "cmdline")
			if triggerCmd == "" || cmd == "" || cmd == triggerCmd {
				verifyIdx = i
				verifyCmd = cmd
			} else if verifyIdx < 0 {
				// A different command passing still verifies recovery when
				// nothing else does; prefer the same-command rerun below.
				verifyIdx = i
				verifyCmd = cmd
			}
		case EventGitCommitted:
			if commitIdx < 0 {
				commitIdx = i
			}
		}
	}
	if len(reads) == 0 && len(fixes) == 0 && verifyIdx < 0 && commitIdx < 0 {
		return nil, nil // bare failure, no arc
	}

	var refs []EpisodeEventRef
	refs = append(refs, EpisodeEventRef{Index: triggerIdx, Role: "trigger"})
	for i := triggerIdx + 1; i < len(batch); i++ {
		switch batch[i].Type {
		case EventFileRead:
			refs = append(refs, EpisodeEventRef{Index: i, Role: "investigation"})
		case EventFileModified:
			refs = append(refs, EpisodeEventRef{Index: i, Role: "fix"})
		case EventCommandExecuted:
			if i == verifyIdx {
				refs = append(refs, EpisodeEventRef{Index: i, Role: "verification"})
			}
		case EventGitCommitted:
			if i == commitIdx {
				refs = append(refs, EpisodeEventRef{Index: i, Role: "verification"})
			}
		}
	}

	draft := &EpisodeDraft{
		Title:         "Bug fix: " + episodeFirstLine(triggerCmdOrErr(triggerCmd, stderr)),
		EpisodeType:   "bug_fix",
		Trigger:       describeTrigger(triggerCmd, stderr),
		Investigation: describeList("Read", reads),
		Resolution:    describeList("Patched", fixes),
		FilesInvolved: append(append([]string(nil), reads...), fixes...),
		SessionID:     trigger.SessionID,
	}
	if patterns := errorPatterns(stderr); len(patterns) > 0 {
		draft.ErrorPatterns = patterns
	}
	if verifyIdx >= 0 {
		draft.Verification = "Reran " + quoteOr(verifyCmd, "failing command") + ": exit 0."
	}
	if commitIdx >= 0 {
		hash := payloadString(batch[commitIdx].Payload, "hash")
		msg := payloadString(batch[commitIdx].Payload, "message")
		note := "Committed"
		if hash != "" {
			note += " " + hash
		}
		if msg != "" {
			note += " — " + firstLine(msg)
		}
		note += "."
		if draft.Verification != "" {
			draft.Verification += " " + note
		} else {
			draft.Verification = note
		}
		if draft.Resolution != "" {
			draft.Resolution += " " + note
		} else {
			draft.Resolution = note
		}
	}
	return draft, refs
}

func triggerCmdOrErr(cmd, stderr string) string {
	if cmd != "" {
		return cmd
	}
	return episodeFirstLine(stderr)
}

func describeTrigger(cmd, stderr string) string {
	if cmd == "" && stderr == "" {
		return "A command failed."
	}
	s := "Command failed: " + quoteOr(cmd, "unknown command") + "."
	if fl := episodeFirstLine(stderr); fl != "" {
		s += " Stderr: " + fl
	}
	return s
}

func describeList(verb string, files []string) string {
	if len(files) == 0 {
		return ""
	}
	return verb + ": " + strings.Join(files, ", ") + "."
}

func quoteOr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return "`" + s + "`"
}

func episodeFirstLine(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if i := strings.Index(s, "\n"); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	if len([]rune(s)) > 160 {
		s = string([]rune(s)[:160]) + "…"
	}
	return s
}

// errorPatterns extracts a stable error signature from stderr: the first
// non-empty line, capped at 160 chars. The store layer may refine this with
// embedding similarity; the processor only needs a deterministic seed.
func errorPatterns(stderr string) []string {
	if fl := episodeFirstLine(stderr); fl != "" {
		return []string{fl}
	}
	return nil
}

// ---------------------------------------------------------------------------
// LLM clients (user key, no SDK deps)
// ---------------------------------------------------------------------------

// LLMClient is the single-method seam for memory extraction. Production
// callers inject an OllamaClient (below) or their own OpenAI/Anthropic HTTP
// implementation holding the user's key — the interface is deliberately
// provider-agnostic so no vendor SDK ever enters go.mod.
type LLMClient interface {
	Complete(ctx context.Context, prompt string) (string, error)
}

// LLMFunc adapts a plain func to LLMClient (handy for fakes and for wiring
// user-key HTTP calls without a struct).
type LLMFunc func(ctx context.Context, prompt string) (string, error)

// Complete implements LLMClient.
func (f LLMFunc) Complete(ctx context.Context, prompt string) (string, error) {
	return f(ctx, prompt)
}

// StubLLMClient returns a canned response (tests, offline runs). Every call
// records its prompt so tests can assert on classification input.
type StubLLMClient struct {
	Response string
	Err      error

	mu      sync.Mutex
	Prompts []string
	Calls   int
}

// Complete implements LLMClient.
func (s *StubLLMClient) Complete(_ context.Context, prompt string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Prompts = append(s.Prompts, prompt)
	s.Calls++
	return s.Response, s.Err
}

// OllamaClient runs extraction against a local Ollama server
// (POST {BaseURL}/api/generate, {"stream": false}) over plain net/http —
// no SDK dependency. The model runs on the user's device alongside the
// daemon (plan: "User's device … user supplies own API keys").
type OllamaClient struct {
	BaseURL string // e.g. "http://localhost:11434"
	Model   string // e.g. "llama3.1"
	HTTP    *http.Client
}

func (c *OllamaClient) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 120 * time.Second}
}

// Complete implements LLMClient.
func (c *OllamaClient) Complete(ctx context.Context, prompt string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if base == "" {
		return "", fmt.Errorf("processor: ollama: empty base URL")
	}
	if c.Model == "" {
		return "", fmt.Errorf("processor: ollama: empty model")
	}
	body, _ := json.Marshal(map[string]any{
		"model":  c.Model,
		"prompt": prompt,
		"stream": false,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("processor: ollama: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("processor: ollama: post: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", fmt.Errorf("processor: ollama: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("processor: ollama: server status %s", resp.Status)
	}
	var out struct {
		Response string `json:"response"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("processor: ollama: decode response: %w", err)
	}
	return out.Response, nil
}

// ---------------------------------------------------------------------------
// Store seam (persistence delegated to the store layer)
// ---------------------------------------------------------------------------

// ProposedMemory is one deduped candidate ready for persistence with its
// §2.8 auto-confirm delay attached.
type ProposedMemory struct {
	ExtractedMemory
	ConfirmAfter time.Duration
}

// ProcessorStore is the persistence seam. The store layer (internal/store,
// issue #2) implements it: ListConfirmed serves CONFIRMED items for the
// prompt + dedup, SaveProposed inserts a PROPOSED row with its confirm
// timer, SaveEpisode inserts the episode row + episode_events links +
// embedding. The processor never touches SQL.
type ProcessorStore interface {
	ListConfirmed(ctx context.Context) ([]ConfirmedMemory, error)
	SaveProposed(ctx context.Context, m ProposedMemory) error
	SaveEpisode(ctx context.Context, e EpisodeDraft, refs []EpisodeEventRef) error
}

// ---------------------------------------------------------------------------
// Processor: batching + background loop
// ---------------------------------------------------------------------------

// ProcessResult reports one flushed session batch.
type ProcessResult struct {
	SessionID         string
	Proposed          []ProposedMemory
	SkippedDuplicates int
	SkippedInvalid    int
	Episode           *EpisodeDraft
}

// Processor buffers per-session events and flushes them through the LLM.
// It runs only when designated (plan: "Designated processor (project
// owner's daemon) for v1"); a non-designated instance buffers nothing and
// its Start returns immediately.
type Processor struct {
	project    string
	designated bool
	llm        LLMClient
	store      ProcessorStore
	clock      Clock
	idleAfter  time.Duration
	sweep      time.Duration

	mu         sync.Mutex
	pending    map[string][]ProcessorEvent
	lastActive map[string]time.Time
	complete   map[string]bool // SESSION_TRANSCRIPT_COMPLETE seen
}

// ProcessorOption customizes a Processor.
type ProcessorOption func(*Processor)

// WithProcessorClock injects the time source (tests).
func WithProcessorClock(c Clock) ProcessorOption {
	return func(p *Processor) { p.clock = c }
}

// WithProcessorIdleAfter overrides the 5-minute batch threshold (tests).
func WithProcessorIdleAfter(d time.Duration) ProcessorOption {
	return func(p *Processor) { p.idleAfter = d }
}

// WithProcessorSweepInterval overrides the background sweep period (tests).
func WithProcessorSweepInterval(d time.Duration) ProcessorOption {
	return func(p *Processor) { p.sweep = d }
}

// NewProcessor builds a processor for projectName. llm and store may be nil
// only for dry-run track-only use — FlushSession reports an error instead of
// calling the network/database.
func NewProcessor(projectName string, designated bool, llm LLMClient, store ProcessorStore, opts ...ProcessorOption) *Processor {
	p := &Processor{
		project:    projectName,
		designated: designated,
		llm:        llm,
		store:      store,
		clock:      time.Now,
		idleAfter:  ProcessorIdleAfter,
		sweep:      ProcessorSweepInterval,
		pending:    map[string][]ProcessorEvent{},
		lastActive: map[string]time.Time{},
		complete:   map[string]bool{},
	}
	for _, o := range opts {
		o(p)
	}
	if p.clock == nil {
		p.clock = time.Now
	}
	return p
}

// IsDesignated reports whether this instance is the project's Memory
// Processor (workspaces.is_designated_processor).
func (p *Processor) IsDesignated() bool { return p.designated }

// Ingest buffers one event into its session batch. SESSION_TRANSCRIPT_
// COMPLETE (harvester.go) marks the session for immediate flush; every other
// event just refreshes the session's idle clock. Non-designated instances
// drop everything — only the owner's daemon extracts.
func (p *Processor) Ingest(ev ProcessorEvent) {
	if !p.designated {
		return
	}
	if ev.At.IsZero() {
		ev.At = p.clock()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if ev.Type == EventSessionTranscriptComplete {
		if id := sessionIDOf(ev.Payload); id != "" {
			ev.SessionID = id
		}
		if ev.SessionID != "" {
			p.complete[ev.SessionID] = true
			if _, ok := p.lastActive[ev.SessionID]; !ok {
				p.lastActive[ev.SessionID] = ev.At
			}
		}
		return // the marker itself carries no extractable content
	}
	if ev.SessionID == "" {
		if id := sessionIDOf(ev.Payload); id != "" {
			ev.SessionID = id
		} else {
			ev.SessionID = "default"
		}
	}
	p.pending[ev.SessionID] = append(p.pending[ev.SessionID], ev)
	p.lastActive[ev.SessionID] = ev.At
}

// Sink adapts Ingest to the shared EventSink signature so the daemon core
// can wire harvester/interceptor/watcher output straight in:
//
//	harvester := NewHarvester(root, proc.Sink())
func (p *Processor) Sink() EventSink {
	return func(eventType string, payload map[string]any) {
		p.Ingest(ProcessorEvent{Type: eventType, Payload: payload})
	}
}

// PendingSessions returns session ids with buffered events (tests/introspection).
func (p *Processor) PendingSessions() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, 0, len(p.pending))
	for id, evs := range p.pending {
		if len(evs) > 0 {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// FlushSession extracts, dedups and persists one session batch. The buffer
// is cleared only on success — an LLM or store error retains the events for
// the next sweep. Non-designated instances and empty batches return a zero
// result without touching the LLM or store.
func (p *Processor) FlushSession(ctx context.Context, sessionID string) (ProcessResult, error) {
	if !p.designated {
		return ProcessResult{SessionID: sessionID}, nil
	}
	p.mu.Lock()
	batch := p.pending[sessionID]
	delete(p.pending, sessionID)
	delete(p.complete, sessionID)
	p.mu.Unlock()

	if len(batch) == 0 {
		return ProcessResult{SessionID: sessionID}, nil
	}
	if p.llm == nil {
		return ProcessResult{}, fmt.Errorf("processor: nil LLM client")
	}
	if p.store == nil {
		return ProcessResult{}, fmt.Errorf("processor: nil store")
	}

	var res ProcessResult
	res.SessionID = sessionID
	if err := p.processBatch(ctx, sessionID, batch, &res); err != nil {
		// Retain for retry: re-queue at the front of the session buffer.
		p.mu.Lock()
		p.pending[sessionID] = append(batch, p.pending[sessionID]...)
		if _, ok := p.lastActive[sessionID]; !ok {
			p.lastActive[sessionID] = p.clock()
		}
		p.mu.Unlock()
		return ProcessResult{SessionID: sessionID}, err
	}
	p.mu.Lock()
	delete(p.lastActive, sessionID)
	p.mu.Unlock()
	return res, nil
}

// processBatch is the single flush path shared by FlushSession and
// CheckIdle: episode hook (pure, no LLM) → prompt → LLM → parse → validate
// → dedup → persist with timers → persist episode.
func (p *Processor) processBatch(ctx context.Context, sessionID string, batch []ProcessorEvent, res *ProcessResult) error {
	draft, refs := DetectEpisode(batch)

	confirmed, err := p.store.ListConfirmed(ctx)
	if err != nil {
		return fmt.Errorf("processor: list confirmed: %w", err)
	}
	existing := make([]string, 0, len(confirmed))
	for _, c := range confirmed {
		existing = append(existing, c.Key+": "+c.Content)
	}
	prompt := BuildExtractionPrompt(p.project, existing, batch)
	resp, err := p.llm.Complete(ctx, prompt)
	if err != nil {
		return fmt.Errorf("processor: llm complete: %w", err)
	}
	for _, m := range ParseExtractedMemories(resp, sessionID) {
		if len([]rune(m.Content)) < MinContentChars {
			res.SkippedInvalid++
			continue
		}
		if IsNearDuplicate(m.Content, nil, confirmed) {
			res.SkippedDuplicates++
			continue
		}
		pm := ProposedMemory{ExtractedMemory: m, ConfirmAfter: ConfirmAfterFor(m)}
		if err := p.store.SaveProposed(ctx, pm); err != nil {
			return fmt.Errorf("processor: save proposed: %w", err)
		}
		res.Proposed = append(res.Proposed, pm)
	}
	if draft != nil {
		draft.SessionID = sessionID
		if err := p.store.SaveEpisode(ctx, *draft, refs); err != nil {
			return fmt.Errorf("processor: save episode: %w", err)
		}
		res.Episode = draft
	}
	return nil
}

// CheckIdle flushes sessions that completed (SESSION_TRANSCRIPT_COMPLETE)
// or sat idle longer than idleAfter. It returns the number of sessions
// flushed; a session whose flush fails is retained and reported via the
// returned error (first failure; remaining sessions are still attempted).
func (p *Processor) CheckIdle(ctx context.Context) (int, error) {
	now := p.clock()
	var due []string
	p.mu.Lock()
	for id, evs := range p.pending {
		if len(evs) == 0 {
			continue
		}
		if p.complete[id] {
			due = append(due, id)
			continue
		}
		if last, ok := p.lastActive[id]; ok && now.Sub(last) >= p.idleAfter {
			due = append(due, id)
		}
	}
	// A completed session with no buffered turns (e.g. SQLite-liveness only)
	// still deserves a flush attempt — it is a no-op returning zero result.
	for id := range p.complete {
		found := false
		for _, d := range due {
			if d == id {
				found = true
				break
			}
		}
		if !found {
			due = append(due, id)
		}
	}
	sort.Strings(due)
	p.mu.Unlock()

	flushed := 0
	var firstErr error
	for _, id := range due {
		if _, err := p.FlushSession(ctx, id); err != nil && firstErr == nil {
			firstErr = err
			continue
		}
		flushed++
	}
	return flushed, firstErr
}

// Start runs the background loop until ctx is done: every sweep interval it
// flushes completed/idle sessions. It returns immediately on non-designated
// instances — only the project owner's daemon burns LLM calls. Blocks;
// returns nil on clean context cancellation.
func (p *Processor) Start(ctx context.Context) error {
	if !p.designated {
		return nil
	}
	sweep := p.sweep
	if sweep <= 0 {
		sweep = ProcessorSweepInterval
	}
	t := time.NewTicker(sweep)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			_, _ = p.CheckIdle(ctx) // errors surface on the next sweep
		}
	}
}
