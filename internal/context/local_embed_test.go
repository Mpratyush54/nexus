package context

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"central-memory/internal/store"
)

// fakeTEI is an httptest stand-in for text-embeddings-inference's
// OpenAI-compatible /v1/embeddings route (bge-small: 384 dims).
type fakeTEI struct {
	srv   *httptest.Server
	calls atomic.Int64
	down  atomic.Bool
	delay time.Duration
	mu    sync.Mutex
	last  map[string]any
	auth  string
}

func fakeVec(text string) []float64 {
	v := make([]float64, 384)
	for _, w := range strings.Fields(strings.ToLower(text)) {
		sum := 0
		for _, b := range []byte(w) {
			sum += int(b)
		}
		v[sum%384] += 1
	}
	v[383] += 0.25
	return v
}

func newFakeTEI(t *testing.T) *fakeTEI {
	t.Helper()
	f := &fakeTEI{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		if f.delay > 0 {
			time.Sleep(f.delay)
		}
		if f.down.Load() {
			http.Error(w, "model loading", http.StatusServiceUnavailable)
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v1/embeddings" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.last = body
		f.auth = r.Header.Get("Authorization")
		f.mu.Unlock()
		input, _ := body["input"].(string)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"model":  body["model"],
			"data": []map[string]any{
				{"object": "embedding", "index": 0, "embedding": fakeVec(input)},
			},
			"usage": map[string]any{"prompt_tokens": 3, "total_tokens": 3},
		})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeTEI) cfg() EmbeddingConfig {
	return EmbeddingConfig{Provider: ProviderLocal, Model: DefaultEmbeddingModel, Endpoint: f.srv.URL}
}

func norm(v []float32) float64 {
	var s float64
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	return math.Sqrt(s)
}

func TestLocalProviderOpenAICompatibleRequestAndPadding(t *testing.T) {
	f := newFakeTEI(t)
	p := NewProvider(f.cfg())
	if got, want := p.Model(), "local:BAAI/bge-small-en-v1.5"; got != want {
		t.Fatalf("Model() = %q, want %q", got, want)
	}
	vec, model := p.Embed(context.Background(), "pytest fixtures for integration")
	if model != p.Model() {
		t.Fatalf("model tag = %q, want %q", model, p.Model())
	}
	if len(vec) != EmbedDims {
		t.Fatalf("len = %d, want %d", len(vec), EmbedDims)
	}
	if math.Abs(norm(vec)-1) > 1e-5 {
		t.Fatalf("norm = %v, want 1", norm(vec))
	}
	for i := 384; i < EmbedDims; i++ {
		if vec[i] != 0 {
			t.Fatalf("dim %d = %v, want zero padding", i, vec[i])
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.last["model"] != DefaultLocalEmbeddingModel || f.last["input"] != "pytest fixtures for integration" {
		t.Fatalf("request body = %#v", f.last)
	}
	if _, ok := f.last["dimensions"]; ok {
		t.Fatal("local provider must not send dimensions (TEI rejects widths above the model's)")
	}
	if f.auth != "" {
		t.Fatalf("no API key configured, but Authorization = %q", f.auth)
	}
}

func TestZeroPaddingPreservesCosine(t *testing.T) {
	a, b := fakeVec("alpha beta gamma"), fakeVec("beta gamma delta")
	cos := func(x, y []float32) float64 {
		var d float64
		for i := range x {
			d += float64(x[i]) * float64(y[i])
		}
		return d / (norm(x) * norm(y))
	}
	raw := cos(float64To32(a), float64To32(b))
	padded := cos(FitEmbedDims(float64To32(a)), FitEmbedDims(float64To32(b)))
	if math.Abs(raw-padded) > 1e-6 {
		t.Fatalf("cosine changed by padding: %v vs %v", raw, padded)
	}
}

func TestLocalEndpointURLForms(t *testing.T) {
	for in, want := range map[string]string{
		"":                                    "http://embeddings:80/v1/embeddings",
		"http://embeddings:80":                "http://embeddings:80/v1/embeddings",
		"http://embeddings:80/v1":             "http://embeddings:80/v1/embeddings",
		"http://embeddings:80/v1/embeddings/": "http://embeddings:80/v1/embeddings",
	} {
		if got := (EmbeddingConfig{Endpoint: in}).localURL(); got != want {
			t.Errorf("localURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseEmbeddingResponseShapes(t *testing.T) {
	for name, body := range map[string]string{
		"openai": `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2,0.3]}]}`,
		"native": `[[0.1,0.2,0.3]]`,
		"single": `{"embedding":[0.1,0.2,0.3]}`,
	} {
		vec, err := parseEmbeddingResponse([]byte(body))
		if err != nil || len(vec) != 3 {
			t.Errorf("%s: vec=%v err=%v", name, vec, err)
		}
	}
	for name, body := range map[string]string{
		"empty data": `{"data":[]}`,
		"empty arr":  `[]`,
		"garbage":    `<html>`,
	} {
		if _, err := parseEmbeddingResponse([]byte(body)); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestLocalProviderDownFallsBackLogsOnceAndCoolsDown(t *testing.T) {
	f := newFakeTEI(t)
	f.down.Store(true)
	p := NewProvider(f.cfg())
	var logs []string
	p.logf = func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }
	clock := time.Unix(1_700_000_000, 0)
	p.now = func() time.Time { return clock }

	text := "deploy with docker compose"
	for i := 0; i < 5; i++ {
		vec, model := p.Embed(context.Background(), text)
		if model != ModelHash {
			t.Fatalf("model = %q, want hash fallback", model)
		}
		if EmbedFingerprint(vec) != EmbedFingerprint(HashEmbed(text)) {
			t.Fatal("fallback vector is not HashEmbed")
		}
	}
	if got := f.calls.Load(); got != 1 {
		t.Fatalf("sidecar calls during cooldown = %d, want 1", got)
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "unavailable") {
		t.Fatalf("logs = %q, want exactly one outage line", logs)
	}
	if !p.Degraded() {
		t.Fatal("Degraded() = false while down")
	}
	if _, err := p.EmbedStrict(context.Background(), text); err != ErrProviderUnavailable {
		t.Fatalf("EmbedStrict during cooldown err = %v, want ErrProviderUnavailable", err)
	}

	// Still down after the cooldown: one more call, still no new log line.
	clock = clock.Add(providerCooldown + time.Second)
	p.Embed(context.Background(), text)
	if len(logs) != 1 {
		t.Fatalf("logs = %q, want no repeat while the outage lasts", logs)
	}

	// Recovery.
	f.down.Store(false)
	clock = clock.Add(providerCooldown + time.Second)
	if _, model := p.Embed(context.Background(), text); model != p.Model() {
		t.Fatalf("model after recovery = %q", model)
	}
	if len(logs) != 2 || !strings.Contains(logs[1], "recovered") || p.Degraded() {
		t.Fatalf("logs = %q degraded=%v, want a recovery line", logs, p.Degraded())
	}
}

func TestLocalProviderTimeout(t *testing.T) {
	f := newFakeTEI(t)
	f.delay = 300 * time.Millisecond
	cfg := f.cfg()
	cfg.Timeout = 30 * time.Millisecond
	p := NewProvider(cfg)
	p.logf = func(string, ...any) {}
	start := time.Now()
	_, model := p.Embed(context.Background(), "slow")
	if model != ModelHash {
		t.Fatalf("model = %q, want hash after timeout", model)
	}
	if el := time.Since(start); el > 250*time.Millisecond {
		t.Fatalf("Embed took %v; timeout not applied", el)
	}
}

func TestLocalProviderUnreachableNeverFailsLegacyEmbedder(t *testing.T) {
	cfg := EmbeddingConfig{Provider: ProviderLocal, Endpoint: "http://127.0.0.1:1", Timeout: time.Second}
	p := NewProvider(cfg)
	p.logf = func(string, ...any) {}
	vec, err := p.Embedder()(context.Background(), "anything at all")
	if err != nil || len(vec) != EmbedDims {
		t.Fatalf("legacy embedder err=%v len=%d, want hash vector", err, len(vec))
	}
}

func TestProviderModelOf(t *testing.T) {
	f := newFakeTEI(t)
	p := NewProvider(f.cfg())
	text := EmbedTextForItem("k", "some content")
	vec, _ := p.Embed(context.Background(), text)
	if got := p.ModelOf(vec, text); got != p.Model() {
		t.Fatalf("ModelOf(model vec) = %q", got)
	}
	if got := p.ModelOf(HashEmbed(text), text); got != ModelHash {
		t.Fatalf("ModelOf(hash vec) = %q", got)
	}
	if got := p.ModelOf(nil, text); got != "" {
		t.Fatalf("ModelOf(nil) = %q", got)
	}
	if got := NewProvider(EmbeddingConfig{}).Model(); got != ModelHash {
		t.Fatalf("hash provider Model() = %q", got)
	}
}

func seedMemories(t *testing.T, st *store.MemStore, n int) []string {
	t.Helper()
	var ids []string
	for i := 0; i < n; i++ {
		text := fmt.Sprintf("memory number %d about deploys", i)
		item := &store.MemoryItem{
			ProjectID: "p1", Key: fmt.Sprintf("k/%d", i), Content: text,
			Status: store.StatusConfirmed, Embedding: HashEmbed(text),
		}
		if i%3 == 0 {
			item.Embedding = nil // never embedded
		}
		if err := st.CreateMemoryItem(context.Background(), item); err != nil {
			t.Fatalf("seed: %v", err)
		}
		ids = append(ids, item.ID)
	}
	return ids
}

func TestReembedBackfillsStaleRowsIdempotently(t *testing.T) {
	f := newFakeTEI(t)
	p := NewProvider(f.cfg())
	st := store.NewMemStore()
	ids := seedMemories(t, st, 7)

	stats, err := Reembed(context.Background(), p, st, ReembedOptions{RequestsPerMinute: -1})
	if err != nil || stats.Embedded != 7 {
		t.Fatalf("first run stats=%+v err=%v, want 7 embedded", stats, err)
	}
	for _, id := range ids {
		m, err := st.GetMemoryItem(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if m.EmbeddingModel != p.Model() || len(m.Embedding) != EmbedDims {
			t.Fatalf("%s: model=%q len=%d", id, m.EmbeddingModel, len(m.Embedding))
		}
		want, _ := p.EmbedStrict(context.Background(), EmbedTextForItem(m.Key, m.Content))
		if EmbedFingerprint(m.Embedding) != EmbedFingerprint(want) {
			t.Fatalf("%s: stored vector is not the model vector", id)
		}
	}
	if n, _ := st.CountMemoryForReembed(context.Background(), p.Model()); n != 0 {
		t.Fatalf("stale rows after run = %d", n)
	}
	before := f.calls.Load()
	stats, err = Reembed(context.Background(), p, st, ReembedOptions{RequestsPerMinute: -1})
	if err != nil || stats.Embedded != 0 || f.calls.Load() != before {
		t.Fatalf("second run stats=%+v err=%v calls=%d, want a no-op", stats, err, f.calls.Load()-before)
	}
}

func TestReembedStopsWithoutWritingHashWhenProviderDown(t *testing.T) {
	f := newFakeTEI(t)
	f.down.Store(true)
	p := NewProvider(f.cfg())
	p.logf = func(string, ...any) {}
	st := store.NewMemStore()
	ids := seedMemories(t, st, 4)

	stats, err := Reembed(context.Background(), p, st, ReembedOptions{RequestsPerMinute: -1})
	if err == nil || stats.Embedded != 0 {
		t.Fatalf("stats=%+v err=%v, want provider error and nothing written", stats, err)
	}
	for _, id := range ids {
		m, _ := st.GetMemoryItem(context.Background(), id)
		if m.EmbeddingModel != "" {
			t.Fatalf("%s tagged %q while provider was down", id, m.EmbeddingModel)
		}
	}
}

func TestReembedRateLimitAndMax(t *testing.T) {
	f := newFakeTEI(t)
	p := NewProvider(f.cfg())
	st := store.NewMemStore()
	seedMemories(t, st, 6)
	start := time.Now()
	stats, err := Reembed(context.Background(), p, st, ReembedOptions{RequestsPerMinute: 3000, Max: 4})
	if err != nil || stats.Embedded != 4 {
		t.Fatalf("stats=%+v err=%v, want 4 (Max)", stats, err)
	}
	// 4 single-input requests 20ms apart: at least 3 gaps.
	if el := time.Since(start); el < 55*time.Millisecond {
		t.Fatalf("run took %v; rate limit not applied", el)
	}
	if n, _ := st.CountMemoryForReembed(context.Background(), p.Model()); n != 2 {
		t.Fatalf("remaining = %d, want 2", n)
	}
}

func TestMinSimilarityPerModel(t *testing.T) {
	if store.LegacyEmbeddingModel != ModelHash {
		t.Fatalf("store legacy tag %q != ModelHash %q", store.LegacyEmbeddingModel, ModelHash)
	}
	cases := map[string]float64{
		"":                              MinSimilarityHash,
		ModelHash:                       0.2,
		"local:BAAI/bge-small-en-v1.5":  0.6,
		"local:BAAI/bge-base-en-v1.5":   0.6,
		"openai:text-embedding-3-small": MinSimilarityDefault,
	}
	for model, want := range cases {
		if got := DefaultMinSimilarity(model); got != want {
			t.Errorf("DefaultMinSimilarity(%q) = %v, want %v", model, got, want)
		}
	}
	if got := NewProvider(EmbeddingConfig{}).MinSimilarity(); got != 0.2 {
		t.Errorf("hash provider floor = %v", got)
	}
	if got := NewProvider(EmbeddingConfig{Provider: ProviderLocal}).MinSimilarity(); got != 0.6 {
		t.Errorf("local bge provider floor = %v", got)
	}
}

func TestMinSimilarityEnvOverride(t *testing.T) {
	t.Setenv("CENTRAL_EMBEDDING_PROVIDER", "local")
	t.Setenv("CENTRAL_EMBEDDING_MIN_SIMILARITY", "0.72")
	if got := ProviderFromEnv().MinSimilarity(); got != 0.72 {
		t.Fatalf("override floor = %v, want 0.72", got)
	}
	for _, bad := range []string{"0", "1", "-0.3", "1.5", "NaN", "abc"} {
		t.Setenv("CENTRAL_EMBEDDING_MIN_SIMILARITY", bad)
		if got := ProviderFromEnv().MinSimilarity(); got != MinSimilarityBGE {
			t.Errorf("invalid override %q gave floor %v, want model default %v", bad, got, MinSimilarityBGE)
		}
	}
}
