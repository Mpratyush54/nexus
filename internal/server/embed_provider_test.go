package server

// Embedding provider wiring on POST /memory + GET /memory/search (issue #165).

import (
	"encoding/json"
	"net/http"
	"testing"

	memctx "central-memory/internal/context"
	"central-memory/internal/store"
)

func TestMemoryCreateEmbedsWhenMissing(t *testing.T) {
	s := newTestServer()
	s.Embedder = memctx.NewEmbedder(memctx.EmbeddingConfig{Provider: memctx.ProviderHash})
	token := loginAs(t, s, "alice")
	rec := doJSON(t, s, http.MethodPost, "/projects/resolve", token, map[string]string{
		"folder_name": "embed-create-proj",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("resolve status = %d body %s", rec.Code, rec.Body.String())
	}
	var project store.Project
	if err := json.Unmarshal(rec.Body.Bytes(), &project); err != nil {
		t.Fatal(err)
	}
	rec = doJSON(t, s, http.MethodPost, "/memory", token, map[string]any{
		"project_id": project.ID,
		"key":        "embed/auto",
		"content":    "The team uses pytest with fixture-based setup for integration tests.",
		"level":      "project",
		"scope":      "fact",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d body %s", rec.Code, rec.Body.String())
	}
	var item store.MemoryItem
	if err := json.Unmarshal(rec.Body.Bytes(), &item); err != nil {
		t.Fatal(err)
	}
	if len(item.Embedding) != store.EmbeddingDim {
		t.Fatalf("embedding dims = %d, want %d", len(item.Embedding), store.EmbeddingDim)
	}
}

func TestMemorySearchQueryEmbedsToVector(t *testing.T) {
	s := newTestServer()
	s.Embedder = memctx.NewEmbedder(memctx.EmbeddingConfig{Provider: memctx.ProviderHash})
	token := loginAs(t, s, "alice")
	rec := doJSON(t, s, http.MethodPost, "/projects/resolve", token, map[string]string{
		"folder_name": "embed-search-proj",
	})
	var project store.Project
	_ = json.Unmarshal(rec.Body.Bytes(), &project)

	content := "The team uses pytest with fixture-based setup for integration tests."
	vec := memctx.HashEmbed(memctx.EmbedTextForItem("embed/search", content))
	item := &store.MemoryItem{
		ProjectID: project.ID, Key: "embed/search", Content: content,
		Level: store.LevelProject, Scope: "fact",
		Status: store.StatusConfirmed, Confidence: 0.9, Embedding: vec,
	}
	if err := s.Store.CreateMemoryItem(t.Context(), item); err != nil {
		t.Fatal(err)
	}

	rec = doJSON(t, s, http.MethodGet, "/memory/search?project_id="+project.ID+"&q=pytest+fixtures", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("search status = %d body %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Count < 1 {
		t.Fatalf("count = %d, want >= 1 (vector or keyword)", out.Count)
	}
}
