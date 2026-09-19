package extract

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const defaultOpenRouterBase = "https://openrouter.ai/api/v1"
// openrouter/free can route to safety-only models that return non-JSON.
// Prefer an instruct-capable free model; callers can override via OPENROUTER_MODEL.
const defaultOpenRouterModel = "nvidia/nemotron-3.5-lightning:free"

// defaultOpenRouterFallbacks are tried when the primary model is unavailable.
var defaultOpenRouterFallbacks = []string{
	"liquid/lfm-2.5-2.6b:free",
	"google/gemma-4-31b-it:free",
	"qwen/qwen3.8-27b:free",
}

// Client calls OpenRouter's OpenAI-compatible chat completions API.
type Client struct {
	Cfg    Config
	HTTP   *http.Client
	// BaseURLOverride is used by tests (httptest).
	BaseURLOverride string
}

func (c *Client) httpClient() *http.Client {
	if c != nil && c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 45 * time.Second}
}

func (c *Client) baseURL() string {
	if c != nil && c.BaseURLOverride != "" {
		return strings.TrimSuffix(c.BaseURLOverride, "/")
	}
	base := defaultOpenRouterBase
	if c != nil && strings.TrimSpace(c.Cfg.BaseURL) != "" {
		base = strings.TrimSpace(c.Cfg.BaseURL)
	}
	return strings.TrimSuffix(base, "/")
}

func (c *Client) model() string {
	if c != nil && strings.TrimSpace(c.Cfg.Model) != "" {
		return strings.TrimSpace(c.Cfg.Model)
	}
	return defaultOpenRouterModel
}

func (c *Client) compressModel() string {
	if c != nil && strings.TrimSpace(c.Cfg.CompressModel) != "" {
		return strings.TrimSpace(c.Cfg.CompressModel)
	}
	// Prefer a stronger paid instruct model when unset; falls back via models[].
	if c != nil && strings.TrimSpace(c.Cfg.Model) != "" {
		return strings.TrimSpace(c.Cfg.Model)
	}
	return "anthropic/claude-sonnet-4"
}

// Complete sends the extraction prompt and returns parsed proposals.
func (c *Client) Complete(ctx context.Context, prompt string) ([]Proposal, error) {
	content, err := c.chat(ctx, c.model(), defaultOpenRouterFallbacks,
		"You extract durable project memories including decisions, actions, files touched, and outcomes. "+
			"Prefer empty only when the batch is pure noise. "+
			"level must be one of: organization, project, personal, session (default project). "+
			"scope must be one of: fact, preference, decision, constraint, pattern, episode_summary. "+
			"Reply with JSON only: {\"memories\":[{\"key\",\"content\",\"level\",\"scope\",\"confidence\",\"explicit\"}]}.",
		prompt)
	if err != nil {
		return nil, err
	}
	return parseProposals([]byte(content)), nil
}

// CompleteCompress runs session-compress and returns summary + decision proposals.
func (c *Client) CompleteCompress(ctx context.Context, prompt string) (summary string, decisions []Proposal, err error) {
	content, err := c.chat(ctx, c.compressModel(), append([]string{c.model()}, defaultOpenRouterFallbacks...),
		"You compress a coding chat session into one rich episode summary plus optional sharp decisions. "+
			"Require actions (files/commands), outcomes, and decisions. Keep concrete nouns. "+
			"Empty session_summary only if transcript is pure noise. "+
			"Reply with JSON only: {\"session_summary\":\"...\",\"decisions\":[{\"key\",\"content\",\"level\",\"scope\",\"confidence\",\"explicit\"}]}.",
		prompt)
	if err != nil {
		return "", nil, err
	}
	return parseCompressResult([]byte(content))
}

func (c *Client) chat(ctx context.Context, model string, fallbacks []string, system, prompt string) (string, error) {
	if c == nil || strings.TrimSpace(c.Cfg.APIKey) == "" {
		return "", fmt.Errorf("extract: openrouter api key missing")
	}
	if strings.TrimSpace(model) == "" {
		model = defaultOpenRouterModel
	}
	body, _ := json.Marshal(map[string]any{
		"model":  model,
		"models": fallbacks,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": prompt},
		},
		"temperature": 0.1,
		"response_format": map[string]string{"type": "json_object"},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL()+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.Cfg.APIKey))
	referer := strings.TrimSpace(c.Cfg.HTTPReferer)
	if referer == "" {
		referer = "https://nexus.pratyushes.dev"
	}
	req.Header.Set("HTTP-Referer", referer)
	title := strings.TrimSpace(c.Cfg.AppTitle)
	if title == "" {
		title = "Nexus"
	}
	req.Header.Set("X-Title", title)

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(raw))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return "", fmt.Errorf("extract: openrouter %d: %s", resp.StatusCode, msg)
	}
	return openAIMessageContent(raw)
}

func openAIMessageContent(raw []byte) (string, error) {
	var envelope struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return "", fmt.Errorf("extract: decode response: %w", err)
	}
	if len(envelope.Choices) == 0 {
		return "", fmt.Errorf("extract: empty choices")
	}
	content := strings.TrimSpace(envelope.Choices[0].Message.Content)
	if content == "" {
		return "", fmt.Errorf("extract: empty message content")
	}
	// Strip markdown fences if present.
	if strings.HasPrefix(content, "```") {
		content = strings.TrimPrefix(content, "```json")
		content = strings.TrimPrefix(content, "```")
		if i := strings.LastIndex(content, "```"); i >= 0 {
			content = content[:i]
		}
		content = strings.TrimSpace(content)
	}
	return content, nil
}

type llmMemory struct {
	Key        string  `json:"key"`
	Content    string  `json:"content"`
	Level      string  `json:"level"`
	Scope      string  `json:"scope"`
	Confidence float64 `json:"confidence"`
	Explicit   bool    `json:"explicit"`
}

func parseProposals(data []byte) []Proposal {
	data = extractJSONPayload(data)
	var wrapped struct {
		Memories []llmMemory `json:"memories"`
	}
	var out []Proposal
	if err := json.Unmarshal(data, &wrapped); err == nil && len(wrapped.Memories) > 0 {
		for _, m := range wrapped.Memories {
			if p, ok := llmToProposal(m); ok {
				out = append(out, p)
			}
		}
		return out
	}
	var bare []llmMemory
	if err := json.Unmarshal(data, &bare); err == nil {
		for _, m := range bare {
			if p, ok := llmToProposal(m); ok {
				out = append(out, p)
			}
		}
	}
	return out
}

// extractJSONPayload finds the first JSON object/array in model output.
func extractJSONPayload(data []byte) []byte {
	s := strings.TrimSpace(string(data))
	if s == "" {
		return data
	}
	if (strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}")) ||
		(strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]")) {
		return []byte(s)
	}
	if i := strings.Index(s, "{"); i >= 0 {
		if j := strings.LastIndex(s, "}"); j > i {
			return []byte(s[i : j+1])
		}
	}
	if i := strings.Index(s, "["); i >= 0 {
		if j := strings.LastIndex(s, "]"); j > i {
			return []byte(s[i : j+1])
		}
	}
	return data
}

func llmToProposal(m llmMemory) (Proposal, bool) {
	content := strings.TrimSpace(m.Content)
	if len(content) < 20 || len(content) > 2000 {
		return Proposal{}, false
	}
	if isJunk(content) {
		return Proposal{}, false
	}
	level := NormalizeLevel(m.Level, content)
	scope := NormalizeScope(m.Scope, content)
	conf := m.Confidence
	if conf <= 0 || conf > 1 {
		conf = 0.8
		if m.Explicit {
			conf = 0.95
		}
	}
	key := strings.TrimSpace(m.Key)
	if key == "" {
		key = KeyFromContent(content)
	}
	return Proposal{
		Key:        key,
		Content:    content,
		Level:      level,
		Scope:      scope,
		Confidence: conf,
		Explicit:   m.Explicit || isExplicit(content),
		Source:     "processor:openrouter",
	}, true
}

func parseCompressResult(data []byte) (summary string, decisions []Proposal, err error) {
	data = extractJSONPayload(data)
	var wrapped struct {
		SessionSummary string      `json:"session_summary"`
		Summary        string      `json:"summary"`
		Decisions      []llmMemory `json:"decisions"`
		Memories       []llmMemory `json:"memories"`
	}
	if err := json.Unmarshal(data, &wrapped); err != nil {
		// Fall back to memories[]-only shape.
		props := parseProposals(data)
		return "", props, nil
	}
	summary = strings.TrimSpace(wrapped.SessionSummary)
	if summary == "" {
		summary = strings.TrimSpace(wrapped.Summary)
	}
	for _, m := range wrapped.Decisions {
		if p, ok := llmToProposal(m); ok {
			decisions = append(decisions, p)
		}
	}
	if len(decisions) == 0 {
		for _, m := range wrapped.Memories {
			if p, ok := llmToProposal(m); ok {
				decisions = append(decisions, p)
			}
		}
	}
	return summary, decisions, nil
}

// ClampMemoryContent truncates to the store content CHECK (20–2000).
func ClampMemoryContent(content string) string {
	content = strings.TrimSpace(content)
	if len(content) <= 2000 {
		return content
	}
	if len(content) > 1997 {
		return content[:1997] + "…"
	}
	return content
}
