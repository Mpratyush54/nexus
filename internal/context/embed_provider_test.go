package context

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEmbeddingConfigFromEnvDefaults(t *testing.T) {
	t.Setenv("CENTRAL_EMBEDDING_PROVIDER", "")
	t.Setenv("CENTRAL_EMBEDDING_API_KEY", "")
	t.Setenv("CENTRAL_EMBEDDING_MODEL", "")
	t.Setenv("CENTRAL_EMBEDDING_ENDPOINT", "")
	cfg := EmbeddingConfigFromEnv()
	if cfg.Provider != ProviderHash {
		t.Fatalf("provider = %q, want hash", cfg.Provider)
	}
	if cfg.Model != DefaultEmbeddingModel {
		t.Fatalf("model = %q, want %q", cfg.Model, DefaultEmbeddingModel)
	}
}

func TestNewEmbedderHashProvider(t *testing.T) {
	emb := NewEmbedder(EmbeddingConfig{Provider: ProviderHash})
	got, err := emb(context.Background(), "pytest fixtures for integration")
	if err != nil {
		t.Fatal(err)
	}
	want := HashEmbed("pytest fixtures for integration")
	if EmbedFingerprint(got) != EmbedFingerprint(want) {
		t.Fatal("hash provider must match HashEmbed")
	}
}

func TestNewEmbedderFallsBackWhenOpenAIKeyMissing(t *testing.T) {
	emb := NewEmbedder(EmbeddingConfig{Provider: ProviderOpenAI, APIKey: ""})
	got, err := emb(context.Background(), "pytest fixtures for integration")
	if err != nil {
		t.Fatal(err)
	}
	want := HashEmbed("pytest fixtures for integration")
	if EmbedFingerprint(got) != EmbedFingerprint(want) {
		t.Fatal("missing OpenAI key must fall back to HashEmbed")
	}
}

func TestNewEmbedderOpenAISuccess(t *testing.T) {
	vec := make([]float64, EmbedDims)
	vec[0] = 3
	vec[1] = 4 // norm = 5 → unit (0.6, 0.8, ...)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("auth = %q", r.Header.Get("Authorization"))
		}
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req["model"] != DefaultEmbeddingModel {
			t.Errorf("model = %v", req["model"])
		}
		if dims, _ := req["dimensions"].(float64); int(dims) != EmbedDims {
			t.Errorf("dimensions = %v, want %d", req["dimensions"], EmbedDims)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": vec}},
		})
	}))
	defer srv.Close()

	emb := NewEmbedder(EmbeddingConfig{
		Provider:   ProviderOpenAI,
		APIKey:     "test-key",
		Model:      DefaultEmbeddingModel,
		Endpoint:   srv.URL,
		HTTPClient: srv.Client(),
	})
	got, err := emb(context.Background(), "hello embeddings")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != EmbedDims {
		t.Fatalf("dims = %d", len(got))
	}
	if math.Abs(float64(got[0])-0.6) > 1e-5 || math.Abs(float64(got[1])-0.8) > 1e-5 {
		t.Fatalf("got[0],got[1] = %v,%v, want 0.6,0.8", got[0], got[1])
	}
}

func TestNewEmbedderOpenAIHTTPErrorFallsBack(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	emb := NewEmbedder(EmbeddingConfig{
		Provider:   ProviderOpenAI,
		APIKey:     "test-key",
		Endpoint:   srv.URL,
		HTTPClient: srv.Client(),
	})
	got, err := emb(context.Background(), "fallback path text content")
	if err != nil {
		t.Fatal(err)
	}
	want := HashEmbed("fallback path text content")
	if EmbedFingerprint(got) != EmbedFingerprint(want) {
		t.Fatal("HTTP failure must fall back to HashEmbed")
	}
}

func TestNewEmbedderOllamaPadsDims(t *testing.T) {
	// 4-dim model: FitEmbedDims pads to 1536, then L2-normalizes.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/api/embeddings") && r.URL.Path != "/" {
			// Endpoint helper appends /api/embeddings when base URL given.
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"embedding": []float64{3, 4, 0, 0},
		})
	}))
	defer srv.Close()
	emb := NewEmbedder(EmbeddingConfig{
		Provider:   ProviderOllama,
		Model:      "nomic-embed-text",
		Endpoint:   srv.URL,
		HTTPClient: srv.Client(),
	})
	got, err := emb(context.Background(), "ollama pad test")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != EmbedDims {
		t.Fatalf("dims = %d, want %d", len(got), EmbedDims)
	}
	var sum float64
	for _, v := range got {
		sum += float64(v) * float64(v)
	}
	if math.Abs(sum-1) > 1e-5 {
		t.Fatalf("norm² = %v, want 1", sum)
	}
	if math.Abs(float64(got[0])-0.6) > 1e-5 || math.Abs(float64(got[1])-0.8) > 1e-5 {
		t.Fatalf("got[0],got[1] = %v,%v", got[0], got[1])
	}
}

func TestFitEmbedDimsAndL2Normalize(t *testing.T) {
	if FitEmbedDims(nil) != nil {
		t.Fatal("empty must stay nil")
	}
	short := FitEmbedDims([]float32{1, 2})
	if len(short) != EmbedDims || short[0] != 1 || short[1] != 2 {
		t.Fatal("pad failed")
	}
	long := make([]float32, EmbedDims+10)
	long[0] = 9
	trimmed := FitEmbedDims(long)
	if len(trimmed) != EmbedDims || trimmed[0] != 9 {
		t.Fatal("truncate failed")
	}
	unit := L2Normalize([]float32{3, 4})
	if len(unit) != 2 {
		t.Fatalf("L2Normalize must preserve length for non-empty, got %d", len(unit))
	}
	if math.Abs(float64(unit[0])-0.6) > 1e-5 {
		t.Fatalf("unit[0] = %v", unit[0])
	}
}

func TestSyncEmbedFunc(t *testing.T) {
	fn := SyncEmbedFunc(NewEmbedder(EmbeddingConfig{Provider: ProviderHash}))
	a := fn("same text for sync adapt")
	b := HashEmbed("same text for sync adapt")
	if EmbedFingerprint(a) != EmbedFingerprint(b) {
		t.Fatal("SyncEmbedFunc must match HashEmbed for hash provider")
	}
	if SyncEmbedFunc(nil)("x") == nil {
		t.Fatal("nil embedder must still return HashEmbed vectors")
	}
}

func TestUnknownProviderFallsBackToHash(t *testing.T) {
	emb := NewEmbedder(EmbeddingConfig{Provider: "bedrock"})
	got, err := emb(context.Background(), "unknown provider text")
	if err != nil {
		t.Fatal(err)
	}
	want := HashEmbed("unknown provider text")
	if EmbedFingerprint(got) != EmbedFingerprint(want) {
		t.Fatal("unknown provider must use HashEmbed")
	}
}
