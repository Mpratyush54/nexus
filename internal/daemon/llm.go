package daemon

// Local LLM provider client (issue #75, daemon side).
//
// Stdlib net/http only, no new deps. Backend selection via ProviderKeyFromEnv:
//   - OLLAMA_HOST set -> Ollama (POST $OLLAMA_HOST/api/chat)
//   - OPENAI_API_KEY set -> OpenAI-compatible (POST $OPENAI_BASE_URL or
//     https://api.openai.com/v1/chat/completions)
//   - ANTHROPIC_API_KEY set -> Anthropic (POST https://api.anthropic.com/v1/messages)
//
// Failures (no key, network error, bad JSON) fall back to HeuristicProvider
// so extraction never hard-fails. Per-project spend is throttled to
// 1 LLM call / 5min (issue #115): throttled batches use the heuristic and
// count a skip.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// LLMThrottleInterval is the per-project LLM spend guard (issue #115):
// at most 1 network LLM call per project per 5 minutes.
const LLMThrottleInterval = 5 * time.Minute

// llmThrottle tracks last LLM call per project. Zero value is usable.
type llmThrottle struct {
	mu   sync.Mutex
	last map[string]time.Time
	// now is injectable for tests.
	now func() time.Time
	// skips counts throttle skips per project (metric).
	skips map[string]int64
}

func (t *llmThrottle) allow(project string) bool {
	now := time.Now
	if t.now != nil {
		now = t.now
	}
	cur := now().UTC()
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.last == nil {
		t.last = map[string]time.Time{}
	}
	if last, ok := t.last[project]; ok && cur.Sub(last) < LLMThrottleInterval {
		if t.skips == nil {
			t.skips = map[string]int64{}
		}
		t.skips[project]++
		return false
	}
	t.last[project] = cur
	return true
}

// Skips reports throttle skips for project (metric).
func (t *llmThrottle) Skips(project string) int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.skips[project]
}

// sharedLLMThrottle is the process-wide per-project throttle used by
// Processor when its provider is network-backed.
var sharedLLMThrottle = &llmThrottle{}

// LLMProvider is a network-backed Provider that calls the user's local
// model (Ollama) or their own API key (OpenAI/Anthropic). Data stays local:
// prompts/keys never go to the central server.
type LLMProvider struct {
	// Backend overrides auto-detection ("ollama"|"openai"|"anthropic").
	// Empty means detect via ProviderKeyFromEnv.
	Backend string
	// Client overrides the HTTP client (tests). Nil means default 30s timeout.
	Client *http.Client
	// Throttle overrides the spend guard (tests). Nil means shared.
	Throttle *llmThrottle
}

// Extract implements Provider: throttled per project, heuristic fallback on
// any failure or skip. Backend resolution runs BEFORE the throttle check
// (issue #145): heuristic-fallback batches must not burn the 5-min network
// quota they never use.
func (p LLMProvider) Extract(ctx context.Context, project string, events []Event, existing []MemoryRecord) ([]Proposal, error) {
	backend := strings.ToLower(strings.TrimSpace(p.Backend))
	if backend == "" {
		b, ok := ProviderKeyFromEnv()
		if !ok {
			return HeuristicProvider{}.Extract(ctx, project, events, existing)
		}
		backend = b
	}
	switch backend {
	case "ollama", "openai", "anthropic":
	default:
		return HeuristicProvider{}.Extract(ctx, project, events, existing)
	}
	th := p.Throttle
	if th == nil {
		th = sharedLLMThrottle
	}
	if !th.allow(project) {
		return HeuristicProvider{}.Extract(ctx, project, events, existing)
	}
	prompt := BuildExtractionPrompt(project, existing, events)
	var proposals []Proposal
	var err error
	switch backend {
	case "ollama":
		proposals, err = p.callOllama(ctx, prompt)
	case "openai":
		proposals, err = p.callOpenAI(ctx, prompt)
	case "anthropic":
		proposals, err = p.callAnthropic(ctx, prompt)
	}
	if err != nil || len(proposals) == 0 {
		// Network/parse failure: heuristic fallback keeps extraction alive.
		h, herr := HeuristicProvider{}.Extract(ctx, project, events, existing)
		if herr != nil {
			return nil, err
		}
		return h, nil
	}
	for i := range proposals {
		proposals[i].ConfirmAfter = ConfirmationDelay(proposals[i])
		if proposals[i].Source == "" {
			proposals[i].Source = "processor:llm:" + backend
		}
	}
	return proposals, nil
}

// AutoProvider returns an LLMProvider when a key is configured, else the
// heuristic stub (issue #75: real client with heuristic fallback).
func AutoProvider() Provider {
	if _, ok := ProviderKeyFromEnv(); ok {
		return LLMProvider{}
	}
	return HeuristicProvider{}
}

// llmMemory is the model's JSON record per memory.
type llmMemory struct {
	Key        string  `json:"key"`
	Content    string  `json:"content"`
	Level      string  `json:"level"`
	Scope      string  `json:"scope"`
	Confidence float64 `json:"confidence"`
	Explicit   bool    `json:"explicit"`
}

func llmResponseToProposals(data []byte, backend string) []Proposal {
	// Accept {"memories":[...]} or bare [...].
	var wrapped struct {
		Memories []llmMemory `json:"memories"`
	}
	var out []Proposal
	if err := json.Unmarshal(data, &wrapped); err == nil && len(wrapped.Memories) > 0 {
		for _, m := range wrapped.Memories {
			if p, ok := llmMemoryToProposal(m, backend); ok {
				out = append(out, p)
			}
		}
		return out
	}
	var bare []llmMemory
	if err := json.Unmarshal(data, &bare); err == nil {
		for _, m := range bare {
			if p, ok := llmMemoryToProposal(m, backend); ok {
				out = append(out, p)
			}
		}
		return out
	}
	// Ollama /api/generate wraps in {"response":"...json..."}: try nested.
	var gen struct {
		Response string `json:"response"`
	}
	if err := json.Unmarshal(data, &gen); err == nil && gen.Response != "" {
		return llmResponseToProposals([]byte(gen.Response), backend)
	}
	return nil
}

func llmMemoryToProposal(m llmMemory, backend string) (Proposal, bool) {
	content := strings.TrimSpace(m.Content)
	if len(content) < 20 || len(content) > 2000 {
		return Proposal{}, false
	}
	level := MemoryLevel(strings.ToLower(strings.TrimSpace(m.Level)))
	switch level {
	case LevelOrganization, LevelProject, LevelPersonal, LevelSession:
	default:
		level = ClassifyLevel(content)
	}
	scope := MemoryScope(strings.ToLower(strings.TrimSpace(m.Scope)))
	switch scope {
	case ScopeFact, ScopePreference, ScopeDecision, ScopeConstraint, ScopePattern, ScopeEpisodeSummary:
	default:
		scope = ClassifyScope(content)
	}
	conf := m.Confidence
	if conf <= 0 || conf > 1 {
		conf = 0.7
		if m.Explicit {
			conf = 0.95
		}
	}
	return Proposal{
		Key:        firstNonEmpty(strings.TrimSpace(m.Key), KeyFromContent(content)),
		Content:    content,
		Level:      level,
		Scope:      scope,
		Confidence: conf,
		Source:     "processor:llm:" + backend,
		Explicit:   m.Explicit || IsExplicitStatement(content),
	}, true
}

func (p LLMProvider) httpClient() *http.Client {
	if p.Client != nil {
		return p.Client
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (p LLMProvider) callOllama(ctx context.Context, prompt string) ([]Proposal, error) {
	host := strings.TrimSpace(os.Getenv("OLLAMA_HOST"))
	if host == "" {
		host = "http://localhost:11434"
	}
	host = strings.TrimSuffix(host, "/")
	model := strings.TrimSpace(os.Getenv("OLLAMA_MODEL"))
	if model == "" {
		model = "llama3.1"
	}
	body, _ := json.Marshal(map[string]any{
		"model":  model,
		"format": "json",
		"messages": []map[string]string{
			{"role": "system", "content": "Extract memories as JSON: {\"memories\":[{\"key\",\"content\",\"level\",\"scope\",\"confidence\",\"explicit\"}]}. Levels: organization/project/personal/session. Scopes: fact/preference/decision/constraint/pattern/episode_summary."},
			{"role": "user", "content": prompt},
		},
		"stream": false,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, host+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("daemon: ollama status %s", resp.Status)
	}
	var out struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return llmResponseToProposals([]byte(out.Message.Content), "ollama"), nil
}

func (p LLMProvider) callOpenAI(ctx context.Context, prompt string) ([]Proposal, error) {
	key := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	if key == "" {
		return nil, fmt.Errorf("daemon: missing OPENAI_API_KEY")
	}
	base := strings.TrimSpace(os.Getenv("OPENAI_BASE_URL"))
	if base == "" {
		base = "https://api.openai.com"
	}
	base = strings.TrimSuffix(base, "/")
	model := strings.TrimSpace(os.Getenv("OPENAI_MODEL"))
	if model == "" {
		model = "gpt-4o-mini"
	}
	body, _ := json.Marshal(map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "system", "content": "Extract memories as JSON: {\"memories\":[{\"key\",\"content\",\"level\",\"scope\",\"confidence\",\"explicit\"}]}."},
			{"role": "user", "content": prompt},
		},
		"response_format": map[string]string{"type": "json_object"},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := p.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("daemon: openai status %s", resp.Status)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if len(out.Choices) == 0 {
		return nil, fmt.Errorf("daemon: openai empty choices")
	}
	return llmResponseToProposals([]byte(out.Choices[0].Message.Content), "openai"), nil
}

func (p LLMProvider) callAnthropic(ctx context.Context, prompt string) ([]Proposal, error) {
	key := strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY"))
	if key == "" {
		return nil, fmt.Errorf("daemon: missing ANTHROPIC_API_KEY")
	}
	model := strings.TrimSpace(os.Getenv("ANTHROPIC_MODEL"))
	if model == "" {
		model = "claude-3-5-haiku-latest"
	}
	body, _ := json.Marshal(map[string]any{
		"model":      model,
		"max_tokens": 1024,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.anthropic.com/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", key)
	req.Header.Set("anthropic-version", "2023-06-01")
	resp, err := p.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("daemon: anthropic status %s", resp.Status)
	}
	var out struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	var sb strings.Builder
	for _, c := range out.Content {
		sb.WriteString(c.Text)
	}
	return llmResponseToProposals([]byte(sb.String()), "anthropic"), nil
}
