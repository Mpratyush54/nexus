// Embedding provider configuration (nexus issue #165, plan §14).
//
// Extends the HashEmbed interim in embed.go with pluggable backends:
//
//	CENTRAL_EMBEDDING_PROVIDER = openai | ollama | hash  (default: hash)
//	CENTRAL_EMBEDDING_API_KEY  = OpenAI / custom API key
//	CENTRAL_EMBEDDING_MODEL    = default text-embedding-3-small
//	CENTRAL_EMBEDDING_ENDPOINT = optional OpenAI base or Ollama embeddings URL
//
// NewEmbedder / EmbedderFromEnv always return an Embedder that falls back to
// HashEmbed on missing keys, network/parse errors, or wrong-width vectors so
// memory_write and memory_search never hard-fail for embeddings.
package context

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strings"
	"time"
)

// Provider names accepted by CENTRAL_EMBEDDING_PROVIDER.
const (
	ProviderHash   = "hash"
	ProviderOpenAI = "openai"
	ProviderOllama = "ollama"
)

// DefaultEmbeddingModel is OpenAI text-embedding-3-small (1536-dim).
const DefaultEmbeddingModel = "text-embedding-3-small"

// DefaultOllamaEmbeddingModel is used when provider=ollama and MODEL is unset
// or still the OpenAI default (which Ollama does not serve).
const DefaultOllamaEmbeddingModel = "nomic-embed-text"

const (
	defaultOpenAIEmbeddingsURL = "https://api.openai.com/v1/embeddings"
	defaultOllamaEmbeddingsURL = "http://localhost:11434/api/embeddings"
)

// EmbeddingConfig is the env-driven provider selection for write/search.
type EmbeddingConfig struct {
	Provider string // openai | ollama | hash
	APIKey   string
	Model    string
	Endpoint string
	// HTTPClient overrides the default 30s client (tests).
	HTTPClient *http.Client
}

// EmbeddingConfigFromEnv reads CENTRAL_EMBEDDING_* (plan §14 / issue #165).
// Empty provider defaults to hash (zero-cost, zero-permission fallback).
func EmbeddingConfigFromEnv() EmbeddingConfig {
	provider := strings.ToLower(strings.TrimSpace(os.Getenv("CENTRAL_EMBEDDING_PROVIDER")))
	if provider == "" {
		provider = ProviderHash
	}
	model := strings.TrimSpace(os.Getenv("CENTRAL_EMBEDDING_MODEL"))
	if model == "" {
		model = DefaultEmbeddingModel
	}
	return EmbeddingConfig{
		Provider: provider,
		APIKey:   strings.TrimSpace(os.Getenv("CENTRAL_EMBEDDING_API_KEY")),
		Model:    model,
		Endpoint: strings.TrimSpace(os.Getenv("CENTRAL_EMBEDDING_ENDPOINT")),
	}
}

// EmbedderFromEnv builds a fallback-safe Embedder from CENTRAL_EMBEDDING_*.
func EmbedderFromEnv() Embedder {
	return NewEmbedder(EmbeddingConfigFromEnv())
}

// SyncEmbedFunc adapts an Embedder to the synchronous EmbedFunc shape used by
// MCP Config.Embed. Errors and wrong-width results become HashEmbed.
func SyncEmbedFunc(emb Embedder) EmbedFunc {
	if emb == nil {
		return HashEmbed
	}
	return func(text string) []float32 {
		vec, err := emb(context.Background(), text)
		if err != nil || len(vec) != EmbedDims {
			return HashEmbed(text)
		}
		return vec
	}
}

// NewEmbedder returns an Embedder for cfg. Provider failures (missing key,
// HTTP errors, bad JSON, wrong width after fit) fall back to HashEmbed so
// callers never see a hard embedding error. The hash provider returns
// HashEmbed directly (no second normalize pass).
func NewEmbedder(cfg EmbeddingConfig) Embedder {
	provider := strings.ToLower(strings.TrimSpace(cfg.Provider))
	if provider == "" || provider == ProviderHash {
		return AsEmbedder(HashEmbed)
	}
	primary := primaryEmbedder(cfg)
	return func(ctx context.Context, text string) ([]float32, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if primary != nil {
			vec, err := primary(ctx, text)
			if err == nil {
				vec = FitEmbedDims(vec)
				if len(vec) == EmbedDims {
					return L2Normalize(vec), nil
				}
			}
		}
		return HashEmbed(text), nil
	}
}

func primaryEmbedder(cfg EmbeddingConfig) Embedder {
	switch strings.ToLower(strings.TrimSpace(cfg.Provider)) {
	case "", ProviderHash:
		return AsEmbedder(HashEmbed)
	case ProviderOpenAI:
		return openAIEmbedder(cfg)
	case ProviderOllama:
		return ollamaEmbedder(cfg)
	default:
		// Unknown provider (e.g. bedrock placeholder): hash-only until wired.
		return AsEmbedder(HashEmbed)
	}
}

func (c EmbeddingConfig) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (c EmbeddingConfig) openAIModel() string {
	if strings.TrimSpace(c.Model) == "" {
		return DefaultEmbeddingModel
	}
	return strings.TrimSpace(c.Model)
}

func (c EmbeddingConfig) ollamaModel() string {
	m := strings.TrimSpace(c.Model)
	if m == "" || m == DefaultEmbeddingModel {
		return DefaultOllamaEmbeddingModel
	}
	return m
}

func (c EmbeddingConfig) openAIURL() string {
	ep := strings.TrimSpace(c.Endpoint)
	if ep == "" {
		return defaultOpenAIEmbeddingsURL
	}
	ep = strings.TrimSuffix(ep, "/")
	if strings.HasSuffix(ep, "/embeddings") {
		return ep
	}
	if strings.HasSuffix(ep, "/v1") {
		return ep + "/embeddings"
	}
	return ep + "/v1/embeddings"
}

func (c EmbeddingConfig) ollamaURL() string {
	ep := strings.TrimSpace(c.Endpoint)
	if ep == "" {
		return defaultOllamaEmbeddingsURL
	}
	ep = strings.TrimSuffix(ep, "/")
	if strings.HasSuffix(ep, "/api/embeddings") {
		return ep
	}
	if strings.HasSuffix(ep, "/api") {
		return ep + "/embeddings"
	}
	return ep + "/api/embeddings"
}

func openAIEmbedder(cfg EmbeddingConfig) Embedder {
	return func(ctx context.Context, text string) ([]float32, error) {
		key := strings.TrimSpace(cfg.APIKey)
		if key == "" {
			return nil, fmt.Errorf("context: CENTRAL_EMBEDDING_API_KEY required for openai")
		}
		body, _ := json.Marshal(map[string]any{
			"model":      cfg.openAIModel(),
			"input":      text,
			"dimensions": EmbedDims,
		})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.openAIURL(), bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+key)
		resp, err := cfg.httpClient().Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			return nil, err
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("context: openai embeddings status %s", resp.Status)
		}
		var out struct {
			Data []struct {
				Embedding []float64 `json:"embedding"`
			} `json:"data"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, err
		}
		if len(out.Data) == 0 || len(out.Data[0].Embedding) == 0 {
			return nil, fmt.Errorf("context: openai empty embedding")
		}
		return float64To32(out.Data[0].Embedding), nil
	}
}

func ollamaEmbedder(cfg EmbeddingConfig) Embedder {
	return func(ctx context.Context, text string) ([]float32, error) {
		body, _ := json.Marshal(map[string]any{
			"model":  cfg.ollamaModel(),
			"prompt": text,
		})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.ollamaURL(), bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := cfg.httpClient().Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			return nil, err
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("context: ollama embeddings status %s", resp.Status)
		}
		var out struct {
			Embedding []float64 `json:"embedding"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, err
		}
		if len(out.Embedding) == 0 {
			return nil, fmt.Errorf("context: ollama empty embedding")
		}
		return float64To32(out.Embedding), nil
	}
}

func float64To32(in []float64) []float32 {
	out := make([]float32, len(in))
	for i, v := range in {
		out[i] = float32(v)
	}
	return out
}

// FitEmbedDims pads with zeros or truncates to EmbedDims so Ollama models
// with smaller widths still land in the pgvector(1536) index. Empty input
// returns nil (caller should fall back).
func FitEmbedDims(vec []float32) []float32 {
	if len(vec) == 0 {
		return nil
	}
	if len(vec) == EmbedDims {
		return vec
	}
	out := make([]float32, EmbedDims)
	copy(out, vec)
	return out
}

// L2Normalize returns a unit-length copy of vec. Zero/empty vectors are
// returned as a zero EmbedDims slice (NeedsEmbedding treats empty as missing
// only when length != EmbedDims; zero unit vectors are valid full-width).
func L2Normalize(vec []float32) []float32 {
	if len(vec) == 0 {
		return make([]float32, EmbedDims)
	}
	out := append([]float32(nil), vec...)
	var sum float64
	for _, v := range out {
		sum += float64(v) * float64(v)
	}
	if sum == 0 {
		return out
	}
	norm := float32(math.Sqrt(sum))
	for i := range out {
		out[i] /= norm
	}
	return out
}
