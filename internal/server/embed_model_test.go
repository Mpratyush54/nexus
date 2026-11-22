package server

// embed_model_test.go — local embeddings sidecar wiring: writes are tagged
// with their model, edits re-embed, and a down sidecar never fails a write
// and sends search to keyword.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	memctx "central-memory/internal/context"
	"central-memory/internal/store"
)

func fakeSidecar(t *testing.T, down *atomic.Bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() || r.URL.Path != "/v1/embeddings" {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		var body struct {
			Input string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		vec := make([]float64, 384)
		for _, w := range strings.Fields(strings.ToLower(body.Input)) {
			n := 0
			for _, b := range []byte(w) {
				n += int(b)
			}
			vec[n%384]++
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": vec}},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func storedMemory(t *testing.T, s *Server, id string) *store.MemoryItem {
	t.Helper()
	m, err := s.Store.GetMemoryItem(t.Context(), id)
	if err != nil {
		t.Fatalf("GetMemoryItem: %v", err)
	}
	return m
}

func TestLocalEmbeddingsWriteEditAndOutage(t *testing.T) {
	var down atomic.Bool
	side := fakeSidecar(t, &down)
	s := newTestServer()
	s.EmbedProvider = memctx.NewProvider(memctx.EmbeddingConfig{
		Provider: memctx.ProviderLocal, Endpoint: side.URL,
	})
	model := s.EmbedProvider.Model()
	token := loginAs(t, s, "editor")
	projectID := resolveTestProject(t, s, token, "embed-proj")

	rec := doJSON(t, s, http.MethodPost, "/memory", token, map[string]any{
		"project_id": projectID,
		"key":        "deploy/lightsail",
		"content":    "The API runs on a Lightsail instance with docker compose.",
		"level":      "project",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}
	var created store.MemoryItem
	decodeBody(t, rec, &created)
	m := storedMemory(t, s, created.ID)
	if m.EmbeddingModel != model {
		t.Fatalf("write tagged %q, want %q", m.EmbeddingModel, model)
	}

	// Edit re-embeds with the new text.
	newContent := "The API runs on Lightsail; embeddings come from a TEI sidecar."
	rec = doJSON(t, s, http.MethodPut, "/memory/"+created.ID, token, map[string]any{"content": newContent})
	if rec.Code != http.StatusOK {
		t.Fatalf("update = %d %s", rec.Code, rec.Body.String())
	}
	m = storedMemory(t, s, created.ID)
	want, err := s.EmbedProvider.EmbedStrict(t.Context(), memctx.EmbedTextForItem("deploy/lightsail", newContent))
	if err != nil {
		t.Fatal(err)
	}
	if memctx.EmbedFingerprint(m.Embedding) != memctx.EmbedFingerprint(want) || m.EmbeddingModel != model {
		t.Fatalf("edit did not re-embed (model %q)", m.EmbeddingModel)
	}

	// Sidecar down: search still answers (keyword), writes still succeed
	// with a hash vector tagged for the backfill, edits too.
	down.Store(true)
	rec = doJSON(t, s, http.MethodGet, "/memory/search?project_id="+projectID+"&q=sidecar", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("search while down = %d %s", rec.Code, rec.Body.String())
	}
	var res struct {
		Items []*store.MemoryItem `json:"items"`
	}
	decodeBody(t, rec, &res)
	if len(res.Items) == 0 {
		t.Fatal("keyword fallback returned nothing while the sidecar was down")
	}

	rec = doJSON(t, s, http.MethodPost, "/memory", token, map[string]any{
		"project_id": projectID,
		"key":        "deploy/outage",
		"content":    "Writes must never fail because embeddings are down.",
		"level":      "project",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create while down = %d %s", rec.Code, rec.Body.String())
	}
	var created2 store.MemoryItem
	decodeBody(t, rec, &created2)
	if m2 := storedMemory(t, s, created2.ID); m2.EmbeddingModel != memctx.ModelHash || len(m2.Embedding) != memctx.EmbedDims {
		t.Fatalf("write while down tagged %q len %d, want hash", m2.EmbeddingModel, len(m2.Embedding))
	}

	// Backfill after recovery converts the hash row.
	down.Store(false)
	ms := s.Store.(*store.MemStore)
	if n, _ := ms.CountMemoryForReembed(t.Context(), model); n != 1 {
		t.Fatalf("stale rows = %d, want 1 (the outage write)", n)
	}
}

func TestAgentMCPStoreTagsAndVetoesVectorWhileDown(t *testing.T) {
	var down atomic.Bool
	side := fakeSidecar(t, &down)
	s := newTestServer()
	s.EmbedProvider = memctx.NewProvider(memctx.EmbeddingConfig{
		Provider: memctx.ProviderLocal, Endpoint: side.URL,
	})
	if !s.vectorSearchUsable() {
		t.Fatal("vector search should be usable before any failure")
	}
	row := &store.MemoryItem{Key: "k", Content: "hello world"}
	row.Embedding = memctx.HashEmbed(memctx.EmbedTextForItem(row.Key, row.Content))
	s.tagEmbeddingModel(row)
	if row.EmbeddingModel != memctx.ModelHash {
		t.Fatalf("hash vector tagged %q", row.EmbeddingModel)
	}
	down.Store(true)
	if _, ok := s.embedQuery(t.Context(), "anything"); ok {
		t.Fatal("embedQuery ok while sidecar down; want keyword fallback")
	}
	if s.vectorSearchUsable() {
		t.Fatal("vector search still usable while the provider is degraded")
	}
	a := agentMCPStore{Store: s.Store, UserID: "u", Embeds: s}
	items, err := a.SearchMemoryVector(t.Context(), "p", make([]float32, memctx.EmbedDims), 5)
	if err != nil || items != nil {
		t.Fatalf("SearchMemoryVector while down = %v, %v; want nil (keyword)", items, err)
	}
}

// A bge query must never be scored against hash rows (and vice versa):
// rows written before the switch are only reachable by keyword search
// until the backfill re-embeds them.
func TestVectorSearchComparesSameModelOnly(t *testing.T) {
	var down atomic.Bool
	side := fakeSidecar(t, &down)
	s := newTestServer()
	token := loginAs(t, s, "editor")
	projectID := resolveTestProject(t, s, token, "mixed-proj")

	// Written under the hash provider (pre-switch row).
	hashSrv := memctx.NewProvider(memctx.EmbeddingConfig{})
	s.EmbedProvider = hashSrv
	rec := doJSON(t, s, http.MethodPost, "/memory", token, map[string]any{
		"project_id": projectID, "key": "old/hash",
		"content": "zebra quokka migration notes", "level": "project",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}

	// Switch to the local model: a vector query identical in text must
	// not reach the hash row through the vector path.
	s.EmbedProvider = memctx.NewProvider(memctx.EmbeddingConfig{Provider: memctx.ProviderLocal, Endpoint: side.URL})
	vec, ok := s.embedQuery(t.Context(), "zebra quokka migration notes")
	if !ok {
		t.Fatal("embedQuery failed with the sidecar up")
	}
	items, err := s.modelVectorSearch()(t.Context(), projectID, vec, 10)
	if err != nil || len(items) != 0 {
		t.Fatalf("bge query matched %d hash rows (err %v), want 0", len(items), err)
	}
	// The REST search still finds it, via keyword fallback.
	rec = doJSON(t, s, http.MethodGet, "/memory/search?project_id="+projectID+"&q=quokka", token, nil)
	var res struct {
		Items []*store.MemoryItem `json:"items"`
	}
	decodeBody(t, rec, &res)
	if len(res.Items) != 1 || res.Items[0].Key != "old/hash" {
		t.Fatalf("keyword fallback = %+v", res.Items)
	}

	// A bge row is found by a bge query, and never by a hash query.
	rec = doJSON(t, s, http.MethodPost, "/memory", token, map[string]any{
		"project_id": projectID, "key": "new/bge",
		"content": "zebra quokka migration notes", "level": "project",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d", rec.Code)
	}
	items, _ = s.modelVectorSearch()(t.Context(), projectID, vec, 10)
	if len(items) != 1 || items[0].Key != "new/bge" {
		t.Fatalf("bge query = %+v, want only new/bge", items)
	}
	s.EmbedProvider = hashSrv
	hq, _ := s.embedQuery(t.Context(), "zebra quokka migration notes")
	items, _ = s.modelVectorSearch()(t.Context(), projectID, hq, 10)
	if len(items) != 1 || items[0].Key != "old/hash" {
		t.Fatalf("hash query = %+v, want only old/hash", items)
	}

	// The cloud MCP adapter applies the same filter and the model's floor.
	s.EmbedProvider = memctx.NewProvider(memctx.EmbeddingConfig{Provider: memctx.ProviderLocal, Endpoint: side.URL})
	if q := vectorQueryFor(s.EmbedProvider, vec); q.Model != s.EmbedProvider.Model() || q.MinSimilarity != 0.6 {
		t.Fatalf("vectorQuery = model %q floor %v", q.Model, q.MinSimilarity)
	}
	a := agentMCPStore{Store: s.Store, Embeds: s}
	mitems, err := a.SearchMemoryVector(t.Context(), projectID, vec, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(mitems) != 1 || mitems[0].Key != "new/bge" {
		t.Fatalf("MCP vector search = %d items, want only new/bge", len(mitems))
	}
}

// OpenRouter wiring end to end: stored memories are embedded as passages,
// REST and cloud-MCP queries as queries, all with the shared key.
func TestOpenRouterPrefixesThroughServer(t *testing.T) {
	var inputs []string
	var auth atomic.Value
	or := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth.Store(r.Header.Get("Authorization"))
		var body struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		inputs = append(inputs, body.Input...)
		data := make([]map[string]any, len(body.Input))
		for i := range body.Input {
			v := make([]float64, 2048)
			v[1] = 1
			data[i] = map[string]any{"index": i, "embedding": v}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(or.Close)
	s := newTestServer()
	s.EmbedProvider = memctx.NewProvider(memctx.EmbeddingConfig{
		Provider: memctx.ProviderOpenRouter, APIKey: "k", Endpoint: or.URL,
	})
	token := loginAs(t, s, "editor")
	projectID := resolveTestProject(t, s, token, "or-proj")
	rec := doJSON(t, s, http.MethodPost, "/memory", token, map[string]any{
		"project_id": projectID, "key": "deploy/where", "content": "API runs on Lightsail.", "level": "project",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodGet, "/memory/search?project_id="+projectID+"&q=where+is+the+api", token, nil)
	var res struct {
		Items []*store.MemoryItem `json:"items"`
	}
	decodeBody(t, rec, &res)
	if len(res.Items) != 1 {
		t.Fatalf("vector search items = %d, want 1", len(res.Items))
	}
	if qe := s.queryEmbedder(); qe == nil {
		t.Fatal("MCP query embedder not wired")
	} else if _, err := qe(t.Context(), "mcp query"); err != nil {
		t.Fatal(err)
	}
	want := []string{"passage: deploy/where\nAPI runs on Lightsail.", "query: where is the api", "query: mcp query"}
	if strings.Join(inputs, "|") != strings.Join(want, "|") {
		t.Fatalf("inputs = %q, want %q", inputs, want)
	}
	if auth.Load() != "Bearer k" {
		t.Fatalf("auth = %v", auth.Load())
	}
}
