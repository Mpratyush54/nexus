// Embedding provider configuration (nexus issue #165, plan §14).
//
// Extends the HashEmbed interim in embed.go with pluggable backends:
//
//	CENTRAL_EMBEDDING_PROVIDER = openai | ollama | local | hash  (default: hash)
//	CENTRAL_EMBEDDING_API_KEY  = OpenAI / custom API key (optional for local)
//	CENTRAL_EMBEDDING_MODEL    = default text-embedding-3-small
//	                             (local: BAAI/bge-small-en-v1.5)
//	CENTRAL_EMBEDDING_ENDPOINT = optional OpenAI base or Ollama embeddings URL
//	                             (local: default http://embeddings:80, the
//	                             compose sidecar)
//	CENTRAL_EMBEDDING_TIMEOUT  = per-call timeout, Go duration (default 5s
//	                             for local, 30s otherwise)
//	CENTRAL_EMBEDDING_MIN_SIMILARITY = cosine floor for vector search with
//	                             the active model, 0<v<1 (default per model:
//	                             see DefaultMinSimilarity)
//
// "local" speaks the OpenAI /v1/embeddings wire format to a self-hosted
// server (HuggingFace text-embeddings-inference sidecar) without sending a
// "dimensions" field; narrower vectors (384 for bge-small) are zero-padded
// into vector(1536), which leaves cosine similarity unchanged.
//
// NewEmbedder / EmbedderFromEnv always return an Embedder that falls back to
// HashEmbed on missing keys, network/parse errors, or wrong-width vectors so
// memory_write and memory_search never hard-fail for embeddings. Provider
// adds the model tag (stored in memory_items.embedding_model so the backfill
// knows which rows to redo), a strict path that never substitutes HashEmbed,
// a short cooldown after a failure, and one log line per outage.
package context

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Provider names accepted by CENTRAL_EMBEDDING_PROVIDER.
const (
	ProviderHash   = "hash"
	ProviderOpenAI = "openai"
	ProviderOllama = "ollama"
	// ProviderLocal is a self-hosted OpenAI-compatible embeddings server
	// (text-embeddings-inference sidecar). No API key, no "dimensions".
	ProviderLocal = "local"
)

// ModelHash tags vectors produced by HashEmbed (memory_items.embedding_model).
const ModelHash = "hash"

// DefaultLocalEmbeddingModel is the sidecar model: 33.4M params, 384 dims.
const DefaultLocalEmbeddingModel = "BAAI/bge-small-en-v1.5"

// DefaultEmbeddingModel is OpenAI text-embedding-3-small (1536-dim).
const DefaultEmbeddingModel = "text-embedding-3-small"

// DefaultOllamaEmbeddingModel is used when provider=ollama and MODEL is unset
// or still the OpenAI default (which Ollama does not serve).
const DefaultOllamaEmbeddingModel = "nomic-embed-text"

const (
	defaultOpenAIEmbeddingsURL = "https://api.openai.com/v1/embeddings"
	defaultOllamaEmbeddingsURL = "http://localhost:11434/api/embeddings"
	defaultLocalEmbeddingsURL  = "http://embeddings:80"

	defaultLocalTimeout  = 5 * time.Second
	defaultRemoteTimeout = 30 * time.Second
	// providerCooldown is how long a failed provider is skipped before the
	// next attempt, so a hung sidecar cannot add a timeout to every write.
	providerCooldown = 15 * time.Second
)

// EmbeddingConfig is the env-driven provider selection for write/search.
type EmbeddingConfig struct {
	Provider string // openai | ollama | hash
	APIKey   string
	Model    string
	Endpoint string
	// Timeout bounds one embedding call (0 = provider default).
	Timeout time.Duration
	// MinSimilarity overrides the model's vector-search floor; values
	// outside (0,1) mean DefaultMinSimilarity(model).
	MinSimilarity float64
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
	var timeout time.Duration
	if raw := strings.TrimSpace(os.Getenv("CENTRAL_EMBEDDING_TIMEOUT")); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			timeout = d
		}
	}
	return EmbeddingConfig{
		Provider:      provider,
		APIKey:        strings.TrimSpace(os.Getenv("CENTRAL_EMBEDDING_API_KEY")),
		Model:         model,
		Endpoint:      strings.TrimSpace(os.Getenv("CENTRAL_EMBEDDING_ENDPOINT")),
		Timeout:       timeout,
		MinSimilarity: parseMinSimilarity(os.Getenv("CENTRAL_EMBEDDING_MIN_SIMILARITY")),
	}
}

// parseMinSimilarity validates CENTRAL_EMBEDDING_MIN_SIMILARITY: a float
// strictly between 0 and 1. Anything else is ignored (0 = model default)
// and logged, so a typo can never disable or saturate the floor.
func parseMinSimilarity(raw string) float64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(v) || v <= 0 || v >= 1 {
		log.Printf("embeddings: ignoring CENTRAL_EMBEDDING_MIN_SIMILARITY=%q (want 0<v<1); using the model default", raw)
		return 0
	}
	return v
}

// Vector-search similarity floors per model family. A floor must sit above
// the similarity that unrelated texts get from that model, or the vector
// path always returns its nearest rows, however unrelated, and the keyword
// fallback never runs.
//   - HashEmbed (bag of hashed words): unrelated texts share almost no
//     buckets and score ~0, so 0.2 keeps loose matches and drops noise.
//   - bge (BAAI/bge-*-en-v1.5): contrastive training compresses scores
//     into a high band. BAAI's model card FAQ says the BGE similarity
//     distribution is "about in the interval [0.6, 1]" and that dissimilar
//     sentences score above 0.5; v1.5 only "alleviates" this. Unrelated
//     texts therefore land around 0.4-0.6, so 0.6 is the lowest floor that
//     drops them while keeping related rows (the card suggests tuning on
//     real data; override with CENTRAL_EMBEDDING_MIN_SIMILARITY).
//   - Other models: 0.2 until tuned (override with the env var).
const (
	MinSimilarityHash    = 0.2
	MinSimilarityBGE     = 0.6
	MinSimilarityDefault = 0.2
)

// DefaultMinSimilarity is the vector-search floor for a model tag such as
// "hash" or "local:BAAI/bge-small-en-v1.5".
func DefaultMinSimilarity(model string) float64 {
	m := strings.ToLower(strings.TrimSpace(model))
	switch {
	case m == "" || m == ModelHash:
		return MinSimilarityHash
	case strings.Contains(m, "bge-"):
		return MinSimilarityBGE
	default:
		return MinSimilarityDefault
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
	return NewProvider(cfg).Embedder()
}

// ErrProviderUnavailable is returned by Provider.EmbedStrict while the
// provider is cooling down after a failure.
var ErrProviderUnavailable = errors.New("context: embedding provider unavailable (cooling down)")

// Provider is the configured embedding backend plus its model tag, outage
// state, and fallback policy. Safe for concurrent use.
type Provider struct {
	primary  Embedder // nil for hash
	model    string
	minSim   float64
	timeout  time.Duration
	cooldown time.Duration

	mu        sync.Mutex
	downUntil time.Time
	degraded  bool

	now  func() time.Time
	logf func(format string, args ...any)
}

// NewProvider builds a Provider for cfg. Unknown providers behave as hash.
func NewProvider(cfg EmbeddingConfig) *Provider {
	p := &Provider{model: ModelHash, cooldown: providerCooldown, now: time.Now, logf: log.Printf}
	name := strings.ToLower(strings.TrimSpace(cfg.Provider))
	switch name {
	case ProviderOpenAI:
		p.primary = openAIEmbedder(cfg)
		p.model = ProviderOpenAI + ":" + cfg.openAIModel()
	case ProviderOllama:
		p.primary = ollamaEmbedder(cfg)
		p.model = ProviderOllama + ":" + cfg.ollamaModel()
	case ProviderLocal:
		p.primary = localEmbedder(cfg)
		p.model = ProviderLocal + ":" + cfg.localModel()
	}
	p.minSim = DefaultMinSimilarity(p.model)
	if cfg.MinSimilarity > 0 && cfg.MinSimilarity < 1 {
		p.minSim = cfg.MinSimilarity
	}
	p.timeout = cfg.Timeout
	if p.timeout <= 0 {
		p.timeout = defaultRemoteTimeout
		if name == ProviderLocal {
			p.timeout = defaultLocalTimeout
		}
	}
	return p
}

// ProviderFromEnv builds a Provider from CENTRAL_EMBEDDING_*.
func ProviderFromEnv() *Provider {
	return NewProvider(EmbeddingConfigFromEnv())
}

// Model is the tag stored with vectors this provider produces on its
// primary path ("hash" for the hash provider), e.g.
// "local:BAAI/bge-small-en-v1.5".
func (p *Provider) Model() string {
	if p == nil {
		return ModelHash
	}
	return p.model
}

// MinSimilarity is the cosine floor for vector search with this provider's
// vectors: CENTRAL_EMBEDDING_MIN_SIMILARITY when valid, else
// DefaultMinSimilarity(Model()).
func (p *Provider) MinSimilarity() float64 {
	if p == nil {
		return MinSimilarityHash
	}
	return p.minSim
}

// IsHash reports whether the provider is HashEmbed only.
func (p *Provider) IsHash() bool { return p == nil || p.primary == nil }

// EmbedStrict embeds text with the primary provider only: it never
// substitutes HashEmbed. The result is fit to EmbedDims and L2-normalized.
// The backfill uses it (it must not overwrite rows with hash vectors) and so
// does search (a hash query vector is meaningless against model vectors).
func (p *Provider) EmbedStrict(ctx context.Context, text string) ([]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.IsHash() {
		return HashEmbed(text), nil
	}
	p.mu.Lock()
	cooling := p.now().Before(p.downUntil)
	p.mu.Unlock()
	if cooling {
		return nil, ErrProviderUnavailable
	}
	cctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	vec, err := p.primary(cctx, text)
	if err == nil {
		vec = FitEmbedDims(vec)
		if len(vec) != EmbedDims {
			err = fmt.Errorf("context: embedding provider returned an empty vector")
		}
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err() // the caller gave up; not a provider outage
		}
		p.markDown(err)
		return nil, err
	}
	p.markUp()
	return L2Normalize(vec), nil
}

// Embed never fails: the primary vector and its model tag, or HashEmbed and
// "hash" when the primary is unavailable. Writes use this so a down sidecar
// never blocks a memory write; the backfill later replaces hash rows.
func (p *Provider) Embed(ctx context.Context, text string) ([]float32, string) {
	if vec, err := p.EmbedStrict(ctx, text); err == nil {
		return vec, p.Model()
	}
	return HashEmbed(text), ModelHash
}

// Embedder adapts Embed to the legacy Embedder seam (errors only on a
// cancelled context).
func (p *Provider) Embedder() Embedder {
	if p.IsHash() {
		return AsEmbedder(HashEmbed)
	}
	return func(ctx context.Context, text string) ([]float32, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		vec, _ := p.Embed(ctx, text)
		return vec, nil
	}
}

// Degraded reports whether the last primary call failed (writes are
// getting HashEmbed vectors; search should use keyword).
func (p *Provider) Degraded() bool {
	if p.IsHash() {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.degraded
}

// ModelOf tags a vector that came from this provider's Embedder() for text:
// "hash" when it equals HashEmbed(text) (the fallback fired, or the hash
// provider), the provider's model otherwise, "" for no vector. HashEmbed is
// deterministic, so the comparison is exact.
func (p *Provider) ModelOf(vec []float32, text string) string {
	if len(vec) == 0 {
		return ""
	}
	if p.IsHash() {
		return ModelHash
	}
	h := HashEmbed(text)
	if len(h) == len(vec) {
		same := true
		for i := range h {
			if h[i] != vec[i] {
				same = false
				break
			}
		}
		if same {
			return ModelHash
		}
	}
	return p.Model()
}

func (p *Provider) markDown(err error) {
	p.mu.Lock()
	p.downUntil = p.now().Add(p.cooldown)
	first := !p.degraded
	p.degraded = true
	p.mu.Unlock()
	if first {
		p.logf("embeddings: %s unavailable (%v); writes fall back to HashEmbed and search to keyword until it recovers", p.model, err)
	}
}

func (p *Provider) markUp() {
	p.mu.Lock()
	was := p.degraded
	p.degraded = false
	p.downUntil = time.Time{}
	p.mu.Unlock()
	if was {
		p.logf("embeddings: %s recovered", p.model)
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

func (c EmbeddingConfig) localModel() string {
	m := strings.TrimSpace(c.Model)
	if m == "" || m == DefaultEmbeddingModel {
		return DefaultLocalEmbeddingModel
	}
	return m
}

// localURL resolves the sidecar's OpenAI-compatible route.
func (c EmbeddingConfig) localURL() string {
	ep := strings.TrimSpace(c.Endpoint)
	if ep == "" {
		ep = defaultLocalEmbeddingsURL
	}
	return EmbeddingConfig{Endpoint: ep}.openAIURL()
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
		return parseEmbeddingResponse(raw)
	}
}

// localEmbedder posts {"model","input"} to a self-hosted OpenAI-compatible
// server (TEI /v1/embeddings). The API key is optional (sent only when set).
func localEmbedder(cfg EmbeddingConfig) Embedder {
	return func(ctx context.Context, text string) ([]float32, error) {
		body, _ := json.Marshal(map[string]any{
			"model": cfg.localModel(),
			"input": text,
		})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.localURL(), bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		if key := strings.TrimSpace(cfg.APIKey); key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
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
			return nil, fmt.Errorf("context: local embeddings status %s", resp.Status)
		}
		return parseEmbeddingResponse(raw)
	}
}

// parseEmbeddingResponse accepts the OpenAI shape {"data":[{"embedding":[..]}]}
// (OpenAI, TEI /v1/embeddings), TEI's native /embed shape [[..]], and the
// single-vector {"embedding":[..]} shape.
func parseEmbeddingResponse(raw []byte) ([]float32, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) > 0 && raw[0] == '[' {
		var nested [][]float64
		if err := json.Unmarshal(raw, &nested); err != nil {
			return nil, err
		}
		if len(nested) == 0 || len(nested[0]) == 0 {
			return nil, fmt.Errorf("context: empty embedding")
		}
		return float64To32(nested[0]), nil
	}
	var out struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
		Embedding []float64 `json:"embedding"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	if len(out.Data) > 0 && len(out.Data[0].Embedding) > 0 {
		return float64To32(out.Data[0].Embedding), nil
	}
	if len(out.Embedding) > 0 {
		return float64To32(out.Embedding), nil
	}
	return nil, fmt.Errorf("context: empty embedding")
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
