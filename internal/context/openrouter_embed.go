// OpenRouter embeddings provider (CENTRAL_EMBEDDING_PROVIDER=openrouter).
//
// POST {base}/embeddings, OpenAI-compatible: {"model","input":[...]} with
// Authorization: Bearer OPENROUTER_API_KEY; response {"data":[{"index",
// "embedding"}]}. The default model is NVIDIA Nemotron 3 Embed 1B on its
// ":free" variant ($0; free-model limits: 20 requests/minute, and 50/day or
// 1,000/day depending on purchased credits). It returns 2048-dim
// L2-normalized vectors; its card allows keeping the first 1024 (or 512)
// dims and re-normalizing, which is how they fit vector(1536). Queries and
// stored passages take "query: " / "passage: " prefixes on OpenAI-compatible
// endpoints, per the model card.
package context

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultOpenRouterEmbeddingModel is free ($0/token) on OpenRouter.
const DefaultOpenRouterEmbeddingModel = "nvidia/nemotron-3-embed-1b:free"

const (
	defaultOpenRouterBase = "https://openrouter.ai/api/v1"
	// openRouterMaxBatch inputs per request: the free tier counts requests,
	// not inputs, so batching is what makes a backfill fit the budget.
	openRouterMaxBatch = 32
	// openRouterMaxInputChars keeps one input well under the free endpoint's
	// 4,096-token context (~4 chars/token).
	openRouterMaxInputChars = 12000
	// maxRateLimitCooldown caps how long a 429 Retry-After parks the provider
	// (a daily-limit reset can be hours away; retry hourly instead).
	maxRateLimitCooldown = time.Hour
)

// batchEmbedder embeds several inputs in one request, in input order.
type batchEmbedder func(ctx context.Context, inputs []string) ([][]float32, error)

// singleBatch adapts a one-text Embedder (openai/ollama/local) to the batch
// shape by calling it per input.
func singleBatch(emb Embedder) batchEmbedder {
	return func(ctx context.Context, inputs []string) ([][]float32, error) {
		out := make([][]float32, 0, len(inputs))
		for _, in := range inputs {
			vec, err := emb(ctx, in)
			if err != nil {
				return nil, err
			}
			out = append(out, vec)
		}
		return out, nil
	}
}

// RateLimitError is an HTTP 429 from the embeddings API.
type RateLimitError struct {
	RetryAfter time.Duration // 0 when the server gave no hint
}

func (e *RateLimitError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("context: embeddings rate limited (retry after %s)", e.RetryAfter.Round(time.Second))
	}
	return "context: embeddings rate limited"
}

// retryAfterFrom reads Retry-After (seconds or HTTP date), then OpenRouter's
// X-RateLimit-Reset (epoch milliseconds).
func retryAfterFrom(h http.Header, now time.Time) time.Duration {
	if v := strings.TrimSpace(h.Get("Retry-After")); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
			return time.Duration(secs) * time.Second
		}
		if t, err := http.ParseTime(v); err == nil && t.After(now) {
			return t.Sub(now)
		}
	}
	if v := strings.TrimSpace(h.Get("X-RateLimit-Reset")); v != "" {
		if ms, err := strconv.ParseInt(v, 10, 64); err == nil {
			if t := time.UnixMilli(ms); t.After(now) {
				return t.Sub(now)
			}
		}
	}
	return 0
}

func (c EmbeddingConfig) openRouterModel() string {
	m := strings.TrimSpace(c.Model)
	if m == "" || m == DefaultEmbeddingModel {
		return DefaultOpenRouterEmbeddingModel
	}
	return m
}

func (c EmbeddingConfig) openRouterURL() string {
	ep := strings.TrimSuffix(strings.TrimSpace(c.Endpoint), "/")
	if ep == "" {
		ep = defaultOpenRouterBase
	}
	if strings.HasSuffix(ep, "/embeddings") {
		return ep
	}
	return ep + "/embeddings"
}

// modelTagName is the model part of the embedding_model tag. The ":free"
// variant serves the same weights as the paid one, so switching between
// them must not force a re-embed.
func modelTagName(model string) string {
	return strings.TrimSuffix(strings.TrimSpace(model), ":free")
}

// isNemotronEmbed matches NVIDIA's Nemotron embedding family.
func isNemotronEmbed(model string) bool {
	m := strings.ToLower(model)
	return strings.Contains(m, "nemotron") && strings.Contains(m, "embed")
}

// modelPrefixes returns the query and passage prefixes the model family
// expects on OpenAI-compatible endpoints ("" for symmetric models).
func modelPrefixes(model string) (query, passage string) {
	if isNemotronEmbed(model) {
		return "query: ", "passage: "
	}
	return "", ""
}

// defaultTruncateDims is the Matryoshka width kept for models wider than
// vector(1536): Nemotron embed returns 2048 and supports the first 1024.
func defaultTruncateDims(model string) int {
	if isNemotronEmbed(model) {
		return 1024
	}
	return 0
}

func capRunes(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

func openRouterEmbedder(cfg EmbeddingConfig, model string) batchEmbedder {
	return func(ctx context.Context, inputs []string) ([][]float32, error) {
		key := strings.TrimSpace(cfg.APIKey)
		if key == "" {
			return nil, fmt.Errorf("context: OPENROUTER_API_KEY (or CENTRAL_EMBEDDING_API_KEY) required for openrouter embeddings")
		}
		payload := map[string]any{
			"model":           model,
			"input":           inputs,
			"encoding_format": "float",
		}
		if dc := strings.TrimSpace(cfg.DataCollection); dc == "allow" || dc == "deny" {
			payload["provider"] = map[string]any{"data_collection": dc}
		}
		body, _ := json.Marshal(payload)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.openRouterURL(), bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+key)
		if cfg.Referer != "" {
			req.Header.Set("HTTP-Referer", cfg.Referer)
		}
		if cfg.AppTitle != "" {
			req.Header.Set("X-Title", cfg.AppTitle)
		}
		resp, err := cfg.httpClient().Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			return nil, &RateLimitError{RetryAfter: retryAfterFrom(resp.Header, time.Now())}
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("context: openrouter embeddings status %s", resp.Status)
		}
		var out struct {
			Data []struct {
				Index     int       `json:"index"`
				Embedding []float64 `json:"embedding"`
			} `json:"data"`
			Error *struct {
				Code    any    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, err
		}
		if out.Error != nil {
			if fmt.Sprint(out.Error.Code) == "429" {
				return nil, &RateLimitError{}
			}
			return nil, fmt.Errorf("context: openrouter embeddings error: %s", out.Error.Message)
		}
		if len(out.Data) != len(inputs) {
			return nil, fmt.Errorf("context: openrouter returned %d embeddings for %d inputs", len(out.Data), len(inputs))
		}
		sort.SliceStable(out.Data, func(i, j int) bool { return out.Data[i].Index < out.Data[j].Index })
		vecs := make([][]float32, len(out.Data))
		for i, d := range out.Data {
			if len(d.Embedding) == 0 {
				return nil, fmt.Errorf("context: openrouter empty embedding at %d", i)
			}
			vecs[i] = float64To32(d.Embedding)
		}
		return vecs, nil
	}
}

// queryCache keeps recent query vectors so agents repeating a search do not
// spend free-tier requests. Bounded, TTL'd, safe for concurrent use.
type queryCache struct {
	mu    sync.Mutex
	max   int
	ttl   time.Duration
	items map[string]queryCacheEntry
	order []string
}

type queryCacheEntry struct {
	vec []float32
	at  time.Time
}

func newQueryCache(max int, ttl time.Duration) *queryCache {
	return &queryCache{max: max, ttl: ttl, items: map[string]queryCacheEntry{}}
}

func (c *queryCache) get(q string, now time.Time) ([]float32, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[q]
	if !ok || now.Sub(e.at) > c.ttl {
		return nil, false
	}
	return e.vec, true
}

func (c *queryCache) put(q string, vec []float32, now time.Time) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.items[q]; !ok {
		c.order = append(c.order, q)
	}
	c.items[q] = queryCacheEntry{vec: vec, at: now}
	for len(c.order) > c.max {
		delete(c.items, c.order[0])
		c.order = c.order[1:]
	}
}
