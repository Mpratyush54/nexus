package context

import (
	"context"
	"encoding/json"
	"errors"
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

// fakeOpenRouter mimics POST /api/v1/embeddings: Bearer auth, batched
// input, 2048-dim vectors (Nemotron), and scripted 429s.
type fakeOpenRouter struct {
	srv      *httptest.Server
	key      string
	calls    atomic.Int64
	limit429 atomic.Int64 // respond 429 to this many upcoming calls
	retry    string       // Retry-After header for 429s
	mu       sync.Mutex
	bodies   []map[string]any
	headers  []http.Header
}

func orVec(text string) []float64 {
	v := make([]float64, 2048)
	for _, w := range strings.Fields(strings.ToLower(text)) {
		n := 0
		for _, b := range []byte(w) {
			n += int(b)
		}
		v[n%1024]++ // signal in the kept Matryoshka prefix
	}
	v[2000] = 5 // tail noise that truncation must drop
	return v
}

func newFakeOpenRouter(t *testing.T) *fakeOpenRouter {
	t.Helper()
	f := &fakeOpenRouter{key: "test-key"}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		if r.URL.Path != "/api/v1/embeddings" || r.Method != http.MethodPost {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+f.key {
			http.Error(w, `{"error":{"code":401,"message":"No auth"}}`, http.StatusUnauthorized)
			return
		}
		if f.limit429.Load() > 0 {
			f.limit429.Add(-1)
			if f.retry != "" {
				w.Header().Set("Retry-After", f.retry)
			}
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"code":429,"message":"Rate limit exceeded: free-models-per-min"}}`))
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.bodies = append(f.bodies, body)
		f.headers = append(f.headers, r.Header.Clone())
		f.mu.Unlock()
		inputs, _ := body["input"].([]any)
		data := make([]map[string]any, len(inputs))
		// Reverse order on purpose: the client must sort by index.
		for i := range inputs {
			j := len(inputs) - 1 - i
			s, _ := inputs[j].(string)
			data[i] = map[string]any{"object": "embedding", "index": j, "embedding": orVec(s)}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": "list", "model": body["model"], "data": data,
			"usage": map[string]any{"prompt_tokens": 3, "total_tokens": 3},
		})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeOpenRouter) cfg() EmbeddingConfig {
	return EmbeddingConfig{
		Provider: ProviderOpenRouter, APIKey: f.key, Model: DefaultEmbeddingModel,
		Endpoint: f.srv.URL + "/api/v1", Referer: "https://nexus.example", AppTitle: "Nexus",
	}
}

func (f *fakeOpenRouter) lastBody() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bodies[len(f.bodies)-1]
}

func TestOpenRouterRequestAuthModelPrefixAndTruncation(t *testing.T) {
	f := newFakeOpenRouter(t)
	p := NewProvider(f.cfg())
	if got, want := p.Model(), "openrouter:nvidia/nemotron-3-embed-1b@1024"; got != want {
		t.Fatalf("Model() = %q, want %q (no :free, width in tag)", got, want)
	}
	if p.MinSimilarity() != MinSimilarityNemotron {
		t.Fatalf("floor = %v, want %v", p.MinSimilarity(), MinSimilarityNemotron)
	}
	vec, model := p.Embed(context.Background(), "deploys run on lightsail")
	if model != p.Model() || len(vec) != EmbedDims {
		t.Fatalf("model=%q len=%d", model, len(vec))
	}
	for i := 1024; i < EmbedDims; i++ {
		if vec[i] != 0 {
			t.Fatalf("dim %d = %v: truncation to 1024 + zero padding expected", i, vec[i])
		}
	}
	if math.Abs(norm(vec)-1) > 1e-5 {
		t.Fatalf("norm %v, want re-normalized after truncation", norm(vec))
	}
	body := f.lastBody()
	if body["model"] != DefaultOpenRouterEmbeddingModel {
		t.Fatalf("model sent = %v", body["model"])
	}
	in, _ := body["input"].([]any)
	if len(in) != 1 || in[0] != "passage: deploys run on lightsail" {
		t.Fatalf("input = %#v, want passage-prefixed array", body["input"])
	}
	if _, ok := body["provider"]; ok {
		t.Fatal("provider prefs sent without CENTRAL_EMBEDDING_DATA_COLLECTION")
	}
	f.mu.Lock()
	h := f.headers[len(f.headers)-1]
	f.mu.Unlock()
	if h.Get("HTTP-Referer") != "https://nexus.example" || h.Get("X-Title") != "Nexus" {
		t.Fatalf("attribution headers = %q / %q", h.Get("HTTP-Referer"), h.Get("X-Title"))
	}

	// Queries use the query prefix and are cached.
	before := f.calls.Load()
	q1, err := p.EmbedQueryStrict(context.Background(), "where do deploys run")
	if err != nil {
		t.Fatal(err)
	}
	if in, _ := f.lastBody()["input"].([]any); in[0] != "query: where do deploys run" {
		t.Fatalf("query input = %v", in)
	}
	q2, _ := p.EmbedQueryStrict(context.Background(), "where do deploys run")
	if f.calls.Load() != before+1 || EmbedFingerprint(q1) != EmbedFingerprint(q2) {
		t.Fatalf("repeat query made %d calls, want 1 (cached)", f.calls.Load()-before)
	}
}

func TestOpenRouterKeyFromEnvAndDataCollection(t *testing.T) {
	f := newFakeOpenRouter(t)
	t.Setenv("CENTRAL_EMBEDDING_PROVIDER", "openrouter")
	t.Setenv("CENTRAL_EMBEDDING_API_KEY", "")
	t.Setenv("CENTRAL_EMBEDDING_MODEL", "")
	t.Setenv("CENTRAL_EMBEDDING_ENDPOINT", "")
	t.Setenv("OPENROUTER_API_KEY", f.key)
	t.Setenv("OPENROUTER_BASE_URL", f.srv.URL+"/api/v1")
	t.Setenv("CENTRAL_EMBEDDING_DATA_COLLECTION", "deny")
	p := ProviderFromEnv()
	if _, err := p.EmbedStrict(context.Background(), "x y"); err != nil {
		t.Fatalf("EmbedStrict with OPENROUTER_API_KEY: %v", err)
	}
	prov, _ := f.lastBody()["provider"].(map[string]any)
	if prov["data_collection"] != "deny" {
		t.Fatalf("provider prefs = %#v", f.lastBody()["provider"])
	}

	// Wrong key: strict errors, Embed falls back to a hash vector.
	t.Setenv("OPENROUTER_API_KEY", "wrong")
	p = ProviderFromEnv()
	p.logf = func(string, ...any) {}
	if _, err := p.EmbedStrict(context.Background(), "x y"); err == nil {
		t.Fatal("want auth error")
	}
	if _, model := p.Embed(context.Background(), "x y"); model != ModelHash {
		t.Fatalf("fallback model = %q", model)
	}
}

func TestOpenRouterDimsOverrideAndTooWide(t *testing.T) {
	f := newFakeOpenRouter(t)
	cfg := f.cfg()
	cfg.Dims = 512
	p := NewProvider(cfg)
	if !strings.HasSuffix(p.Model(), "@512") {
		t.Fatalf("model %q should carry @512", p.Model())
	}
	if _, err := p.EmbedStrict(context.Background(), "a b"); err != nil {
		t.Fatal(err)
	}
	// A non-Matryoshka model wider than the column is an error, never a
	// silent slice.
	cfg = f.cfg()
	cfg.Model = "some/wide-model"
	p = NewProvider(cfg)
	p.logf = func(string, ...any) {}
	if _, err := p.EmbedStrict(context.Background(), "a b"); err == nil || !strings.Contains(err.Error(), "CENTRAL_EMBEDDING_DIMS") {
		t.Fatalf("err = %v, want width error", err)
	}
}

func TestOpenRouter429CoolsDownForRetryAfter(t *testing.T) {
	f := newFakeOpenRouter(t)
	f.limit429.Store(1)
	f.retry = "120"
	p := NewProvider(f.cfg())
	var logs []string
	p.logf = func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }
	clock := time.Unix(1_700_000_000, 0)
	p.now = func() time.Time { return clock }

	_, err := p.EmbedStrict(context.Background(), "a")
	var rl *RateLimitError
	if !errors.As(err, &rl) || rl.RetryAfter != 120*time.Second {
		t.Fatalf("err = %v, want RateLimitError(120s)", err)
	}
	if vec, model := p.Embed(context.Background(), "a"); model != ModelHash || len(vec) != EmbedDims {
		t.Fatal("write during 429 cooldown must fall back to hash")
	}
	if f.calls.Load() != 1 {
		t.Fatalf("calls during cooldown = %d, want 1", f.calls.Load())
	}
	clock = clock.Add(60 * time.Second) // past the 15s default, inside Retry-After
	if _, err := p.EmbedStrict(context.Background(), "a"); err != ErrProviderUnavailable {
		t.Fatalf("err = %v, want still cooling down (Retry-After 120s)", err)
	}
	clock = clock.Add(61 * time.Second)
	if _, err := p.EmbedStrict(context.Background(), "a"); err != nil {
		t.Fatalf("after Retry-After: %v", err)
	}
	if len(logs) != 2 {
		t.Fatalf("logs = %q, want one outage + one recovery line", logs)
	}
}

func TestRetryAfterParsing(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	h := http.Header{}
	h.Set("Retry-After", "7")
	if d := retryAfterFrom(h, now); d != 7*time.Second {
		t.Errorf("seconds: %v", d)
	}
	h = http.Header{}
	h.Set("X-RateLimit-Reset", fmt.Sprint(now.Add(42*time.Second).UnixMilli()))
	if d := retryAfterFrom(h, now); d != 42*time.Second {
		t.Errorf("reset ms: %v", d)
	}
	if d := retryAfterFrom(http.Header{}, now); d != 0 {
		t.Errorf("none: %v", d)
	}
}

func seedOR(t *testing.T, st *store.MemStore, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		text := fmt.Sprintf("memory %d about deploy order", i)
		if err := st.CreateMemoryItem(context.Background(), &store.MemoryItem{
			ProjectID: "p1", Key: fmt.Sprintf("k/%03d", i), Content: text,
			Status: store.StatusConfirmed, Embedding: HashEmbed(text), EmbeddingModel: ModelHash,
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReembedBatchesAndBacksOffOn429(t *testing.T) {
	f := newFakeOpenRouter(t)
	f.limit429.Store(2)
	f.retry = "3"
	p := NewProvider(f.cfg())
	p.logf = func(string, ...any) {}
	st := store.NewMemStore()
	seedOR(t, st, 70) // 3 batches of <=32

	var waits []time.Duration
	stats, err := Reembed(context.Background(), p, st, ReembedOptions{
		RequestsPerMinute: -1,
		sleep: func(_ context.Context, d time.Duration) error {
			waits = append(waits, d)
			return nil
		},
	})
	if err != nil || stats.Embedded != 70 {
		t.Fatalf("stats=%+v err=%v, want 70 embedded", stats, err)
	}
	if stats.Requests != 5 { // 2 rate-limited + 3 batches
		t.Fatalf("requests = %d, want 5 (batched, 2 retries)", stats.Requests)
	}
	if len(waits) != 2 || waits[0] < 3*time.Second || waits[1] < 3*time.Second {
		t.Fatalf("backoff waits = %v, want 2 waits >= Retry-After (3s)", waits)
	}
	f.mu.Lock()
	first, _ := f.bodies[0]["input"].([]any)
	f.mu.Unlock()
	if len(first) != openRouterMaxBatch || !strings.HasPrefix(first[0].(string), "passage: ") {
		t.Fatalf("first batch = %d inputs, want %d passage-prefixed", len(first), openRouterMaxBatch)
	}
	if n, _ := st.CountMemoryForReembed(context.Background(), p.Model()); n != 0 {
		t.Fatalf("%d rows still stale", n)
	}
}

func TestReembedStopsOnDailyLimitWithoutWritingHash(t *testing.T) {
	f := newFakeOpenRouter(t)
	f.limit429.Store(100)
	f.retry = "7200" // daily limit: hours away
	p := NewProvider(f.cfg())
	p.logf = func(string, ...any) {}
	st := store.NewMemStore()
	seedOR(t, st, 5)
	stats, err := Reembed(context.Background(), p, st, ReembedOptions{
		RequestsPerMinute: -1,
		sleep:             func(context.Context, time.Duration) error { t.Fatal("must not sleep for hours"); return nil },
	})
	if err == nil || !strings.Contains(err.Error(), "paused") || stats.Embedded != 0 {
		t.Fatalf("stats=%+v err=%v, want paused with nothing written", stats, err)
	}
	if n, _ := st.CountMemoryForReembed(context.Background(), p.Model()); n != 5 {
		t.Fatalf("stale = %d, want all 5 left for the next pass", n)
	}
}

func TestReembedRequestRateLimit(t *testing.T) {
	f := newFakeOpenRouter(t)
	p := NewProvider(f.cfg())
	st := store.NewMemStore()
	seedOR(t, st, 70)
	var waits []time.Duration
	stats, err := Reembed(context.Background(), p, st, ReembedOptions{
		RequestsPerMinute: 10,
		sleep: func(_ context.Context, d time.Duration) error {
			waits = append(waits, d)
			return nil
		},
	})
	if err != nil || stats.Requests != 3 {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
	if len(waits) != 2 || waits[0] < 5*time.Second || waits[0] > 6*time.Second {
		t.Fatalf("waits = %v, want 2 gaps of ~6s (10 rpm)", waits)
	}
}
