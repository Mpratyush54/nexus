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
const defaultOpenRouterModel = "openrouter/free"

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

// Complete sends the extraction prompt and returns parsed proposals.
func (c *Client) Complete(ctx context.Context, prompt string) ([]Proposal, error) {
	if c == nil || strings.TrimSpace(c.Cfg.APIKey) == "" {
		return nil, fmt.Errorf("extract: openrouter api key missing")
	}
	body, _ := json.Marshal(map[string]any{
		"model": c.model(),
		"messages": []map[string]string{
			{
				"role":    "system",
				"content": "You extract durable project memories. Prefer empty over junk. Reply with JSON only: {\"memories\":[{\"key\",\"content\",\"level\",\"scope\",\"confidence\",\"explicit\"}]}.",
			},
			{"role": "user", "content": prompt},
		},
		"temperature": 0.1,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL()+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
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
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(raw))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return nil, fmt.Errorf("extract: openrouter %d: %s", resp.StatusCode, msg)
	}
	content, err := openAIMessageContent(raw)
	if err != nil {
		return nil, err
	}
	return parseProposals([]byte(content)), nil
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

func llmToProposal(m llmMemory) (Proposal, bool) {
	content := strings.TrimSpace(m.Content)
	if len(content) < 20 || len(content) > 2000 {
		return Proposal{}, false
	}
	if isJunk(content) {
		return Proposal{}, false
	}
	level := strings.ToLower(strings.TrimSpace(m.Level))
	switch level {
	case "organization", "project", "personal", "session":
	default:
		level = classifyLevel(content)
	}
	scope := strings.ToLower(strings.TrimSpace(m.Scope))
	switch scope {
	case "fact", "preference", "decision", "constraint", "pattern", "episode_summary":
	default:
		scope = classifyScope(content)
	}
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
