// Embedding provider configuration (nexus issue #165, plan §14).
//
// Extends the HashEmbed interim in embed.go with pluggable backends:
//
//	CENTRAL_EMBEDDING_PROVIDER = openrouter | openai | ollama | local | hash
//	                             (default: hash)
//	CENTRAL_EMBEDDING_API_KEY  = OpenAI / custom API key (openrouter falls back
//	                             to OPENROUTER_API_KEY; optional for local)
//	CENTRAL_EMBEDDING_MODEL    = default text-embedding-3-small
//	                             (openrouter: nvidia/nemotron-3-embed-1b:free,
//	                             local: BAAI/bge-small-en-v1.5)
//	CENTRAL_EMBEDDING_ENDPOINT = optional OpenAI base or Ollama embeddings URL
//	                             (openrouter: OPENROUTER_BASE_URL or
//	                             https://openrouter.ai/api/v1; local: default
//	                             http://embeddings:80)
//	CENTRAL_EMBEDDING_DIMS     = keep only the first N dims (Matryoshka models)
//	                             and re-normalize (default 1024 for Nemotron
//	                             embed models, which return 2048 > vector(1536))
//	CENTRAL_EMBEDDING_TIMEOUT  = per-call timeout, Go duration (default 5s
//	                             for local, 30s otherwise)
//	CENTRAL_EMBEDDING_MIN_SIMILARITY = cosine floor for vector search with
//	                             the active model, 0<v<1 (default per model:
//	                             see DefaultMinSimilarity)
//	CENTRAL_EMBEDDING_DATA_COLLECTION = openrouter only: allow | deny, sent as
//	                             provider.data_collection (unset = account
//	                             privacy setting decides)
//
// "openrouter" calls OpenRouter's OpenAI-compatible POST /embeddings with
// batched input, honours 429 Retry-After (the provider cools down for that
// long), adds the model family's query/passage prefixes, and truncates
// Matryoshka vectors. "local" speaks the same wire format to a self-hosted
// server without a "dimensions" field. Narrower vectors are zero-padded into
// vector(1536), which leaves cosine similarity unchanged.
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
	// ProviderOpenRouter is OpenRouter's OpenAI-compatible embeddings API.
	ProviderOpenRouter = "openrouter"
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
	Provider string // openrouter | openai | ollama | local | hash
	APIKey   string
	Model    string
	Endpoint string
	// Dims keeps only the first Dims components (Matryoshka truncation) and
	// re-normalizes; 0 = the model family default (see defaultTruncateDims).
	Dims int
	// DataCollection is sent to OpenRouter as provider.data_collection
	// ("allow" | "deny"); "" omits it.
	DataCollection string
	// Referer / AppTitle are OpenRouter's optional attribution headers.
	Referer  string
	AppTitle string
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
	cfg := EmbeddingConfig{
		Provider:       provider,
		APIKey:         strings.TrimSpace(os.Getenv("CENTRAL_EMBEDDING_API_KEY")),
		Model:          model,
		Endpoint:       strings.TrimSpace(os.Getenv("CENTRAL_EMBEDDING_ENDPOINT")),
		Timeout:        timeout,
		MinSimilarity:  parseMinSimilarity(os.Getenv("CENTRAL_EMBEDDING_MIN_SIMILARITY")),
		DataCollection: strings.ToLower(strings.TrimSpace(os.Getenv("CENTRAL_EMBEDDING_DATA_COLLECTION"))),
	}
	if raw := strings.TrimSpace(os.Getenv("CENTRAL_EMBEDDING_DIMS")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= EmbedDims {
			cfg.Dims = n
		} else {
			log.Printf("embeddings: ignoring CENTRAL_EMBEDDING_DIMS=%q (want 1..%d)", raw, EmbedDims)
		}
	}
	if provider == ProviderOpenRouter {
		// Reuse the extraction secret and base URL (internal/extract).
		if cfg.APIKey == "" {
			cfg.APIKey = strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY"))
		}
		if cfg.Endpoint == "" {
			cfg.Endpoint = strings.TrimSpace(os.Getenv("OPENROUTER_BASE_URL"))
		}
		cfg.Referer = strings.TrimSpace(os.Getenv("OPENROUTER_HTTP_REFERER"))
		cfg.AppTitle = strings.TrimSpace(os.Getenv("OPENROUTER_APP_TITLE"))
	}
	return cfg
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
//   - NVIDIA Nemotron embed (nemotron-*-embed-*): scores are spread out.
//     The Nemotron-3-Embed-1B model card's examples put relevant
//     query/passage pairs at ~0.65-0.81 and unrelated pairs at ~-0.05-0.12,
//     so 0.3 sits in the gap with margin for 1024-dim truncation.
//   - Other models: 0.2 until tuned (override with the env var).
const (
	MinSimilarityHash     = 0.2
	MinSimilarityBGE      = 0.6
	MinSimilarityNemotron = 0.3
	MinSimilarityDefault  = 0.2
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
	case strings.Contains(m, "nemotron") && strings.Contains(m, "embed"):
		return MinSimilarityNemotron
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
	primary  batchEmbedder // nil for hash
	model    string
	minSim   float64
	timeout  time.Duration
	cooldown time.Duration

	dims         int    // Matryoshka truncation (0 = none)
	queryPrefix  string // prepended to search queries
	docPrefix    string // prepended to stored memories
	maxBatch     int    // inputs per request
	maxInputRune int    // per-input character cap (0 = none)

	qcache *queryCache

	mu        sync.Mutex
	downUntil time.Time
	degraded  bool

	now  func() time.Time
	logf func(format string, args ...any)
}

// NewProvider builds a Provider for cfg. Unknown providers behave as hash.
func NewProvider(cfg EmbeddingConfig) *Provider {
	p := &Provider{model: ModelHash, cooldown: providerCooldown, now: time.Now, logf: log.Printf, maxBatch: 1}
	name := strings.ToLower(strings.TrimSpace(cfg.Provider))
	switch name {
	case ProviderOpenAI:
		p.primary = singleBatch(openAIEmbedder(cfg))
		p.model = ProviderOpenAI + ":" + cfg.openAIModel()
	case ProviderOllama:
		p.primary = singleBatch(ollamaEmbedder(cfg))
		p.model = ProviderOllama + ":" + cfg.ollamaModel()
	case ProviderLocal:
		p.primary = singleBatch(localEmbedder(cfg))
		p.model = ProviderLocal + ":" + cfg.localModel()
	case ProviderOpenRouter:
		model := cfg.openRouterModel()
		p.primary = openRouterEmbedder(cfg, model)
		p.model = ProviderOpenRouter + ":" + modelTagName(model)
		p.maxBatch = openRouterMaxBatch
		p.maxInputRune = openRouterMaxInputChars
		p.queryPrefix, p.docPrefix = modelPrefixes(model)
		p.dims = defaultTruncateDims(model)
		p.qcache = newQueryCache(256, 10*time.Minute)
	}
	if cfg.Dims > 0 && p.primary != nil {
		p.dims = cfg.Dims
	}
	if p.dims > 0 && p.primary != nil {
		// The kept width is part of the vector space: vectors truncated to
		// different widths must never be compared.
		p.model = fmt.Sprintf("%s@%d", p.model, p.dims)
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
	if p.IsHash() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return HashEmbed(text), nil
	}
	out, err := p.embedTexts(ctx, []string{text}, p.docPrefix)
	if err != nil {
		return nil, err
	}
	return out[0], nil
}

// EmbedQueryStrict is EmbedStrict for a search query: it applies the model's
// query prefix (asymmetric retrieval models embed queries and passages
// differently) and serves repeats from a short cache to save rate limit.
func (p *Provider) EmbedQueryStrict(ctx context.Context, query string) ([]float32, error) {
	if p.IsHash() {
		return p.EmbedStrict(ctx, query)
	}
	if vec, ok := p.qcache.get(query, p.now()); ok {
		return vec, nil
	}
	out, err := p.embedTexts(ctx, []string{query}, p.queryPrefix)
	if err != nil {
		return nil, err
	}
	p.qcache.put(query, out[0], p.now())
	return out[0], nil
}

// EmbedBatchStrict embeds many memory texts with as few requests as the
// provider allows (OpenRouter: up to openRouterMaxBatch inputs each). Used
// by the backfill so a free-tier request budget stretches across rows.
func (p *Provider) EmbedBatchStrict(ctx context.Context, texts []string) ([][]float32, error) {
	if p.IsHash() {
		out := make([][]float32, len(texts))
		for i, t := range texts {
			out[i] = HashEmbed(t)
		}
		return out, ctx.Err()
	}
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += p.maxBatch {
		end := start + p.maxBatch
		if end > len(texts) {
			end = len(texts)
		}
		part, err := p.embedTexts(ctx, texts[start:end], p.docPrefix)
		if err != nil {
			return out, err
		}
		out = append(out, part...)
	}
	return out, nil
}

// MaxBatch is the number of inputs one provider request carries.
func (p *Provider) MaxBatch() int {
	if p == nil || p.maxBatch < 1 {
		return 1
	}
	return p.maxBatch
}

// clearCooldown lets the next call through after the backfill has waited
// out a rate limit itself (degraded stays set until a call succeeds).
func (p *Provider) clearCooldown() {
	if p.IsHash() {
		return
	}
	p.mu.Lock()
	p.downUntil = time.Time{}
	p.mu.Unlock()
}

// RetryAt is when the cooling-down provider accepts calls again (zero when
// it is not cooling down).
func (p *Provider) RetryAt() time.Time {
	if p.IsHash() {
		return time.Time{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.now().Before(p.downUntil) {
		return p.downUntil
	}
	return time.Time{}
}

// embedTexts is the single request path: cooldown gate, timeout, prefix,
// input cap, then truncate/fit/normalize every vector.
func (p *Provider) embedTexts(ctx context.Context, texts []string, prefix string) ([][]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	cooling := p.now().Before(p.downUntil)
	p.mu.Unlock()
	if cooling {
		return nil, ErrProviderUnavailable
	}
	inputs := make([]string, len(texts))
	for i, t := range texts {
		inputs[i] = prefix + capRunes(t, p.maxInputRune)
	}
	cctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	raw, err := p.primary(cctx, inputs)
	if err == nil && len(raw) != len(inputs) {
		err = fmt.Errorf("context: embedding provider returned %d vectors for %d inputs", len(raw), len(inputs))
	}
	var out [][]float32
	if err == nil {
		out = make([][]float32, len(raw))
		for i, vec := range raw {
			if p.dims > 0 && len(vec) > p.dims {
				vec = vec[:p.dims]
			}
			if len(vec) == 0 || len(vec) > EmbedDims {
				err = fmt.Errorf("context: embedding provider returned %d dims (column holds %d; set CENTRAL_EMBEDDING_DIMS for Matryoshka models)", len(vec), EmbedDims)
				break
			}
			out[i] = L2Normalize(FitEmbedDims(vec))
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
	return out, nil
}

// Embed never fails: the primary vector and its model tag, or HashEmbed and
// "hash" when the primary is unavailable. Writes use this so a down or rate-limited provider
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

// QueryEmbedder adapts EmbedQueryStrict to the Embedder seam with the same
// never-fail HashEmbed fallback as Embedder() (MCP query embedding; the
// server vetoes vector search while Degraded()).
func (p *Provider) QueryEmbedder() Embedder {
	if p.IsHash() {
		return AsEmbedder(HashEmbed)
	}
	return func(ctx context.Context, text string) ([]float32, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if vec, err := p.EmbedQueryStrict(ctx, text); err == nil {
			return vec, nil
		}
		return HashEmbed(text), nil
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
	wait := p.cooldown
	var rl *RateLimitError
	if errors.As(err, &rl) && rl.RetryAfter > wait {
		wait = rl.RetryAfter
		if wait > maxRateLimitCooldown {
			wait = maxRateLimitCooldown
		}
	}
	p.mu.Lock()
	p.downUntil = p.now().Add(wait)
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
