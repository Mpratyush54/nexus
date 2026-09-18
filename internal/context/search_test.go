package context

import (
	"math"
	"testing"
	"time"

	"central-memory/internal/store"
)

func TestCosineSimilarity(t *testing.T) {
	if got := CosineSimilarity([]float32{1, 0}, []float32{1, 0}); math.Abs(got-1) > 1e-6 {
		t.Fatalf("identical vectors = %v, want 1", got)
	}
	if got := CosineSimilarity([]float32{1, 0}, []float32{0, 1}); math.Abs(got) > 1e-6 {
		t.Fatalf("orthogonal vectors = %v, want 0", got)
	}
	if got := CosineSimilarity([]float32{1, 0}, []float32{-1, 0}); math.Abs(got+1) > 1e-6 {
		t.Fatalf("opposite vectors = %v, want -1", got)
	}
	if got := CosineSimilarity(nil, []float32{1}); got != 0 {
		t.Fatalf("empty vector = %v, want 0", got)
	}
	if got := CosineSimilarity([]float32{0, 0}, []float32{1, 2}); got != 0 {
		t.Fatalf("zero vector = %v, want 0", got)
	}
	// Unequal lengths score strict 0 (issue #105: pgvector rejects
	// dimension mismatches; prefix-matching minted bogus positives).
	if got := CosineSimilarity([]float32{1, 0}, []float32{1, 0, 0}); got != 0 {
		t.Fatalf("dim mismatch = %v, want 0", got)
	}
}

func TestHybridSearchVectorPrimary(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	mk := func(key string, emb []float32) *store.MemoryItem {
		return &store.MemoryItem{
			Key: key, Content: "content for " + key, Level: "project",
			Status: "CONFIRMED", Confidence: 1.0, Embedding: emb,
			LastUsedAt: now,
		}
	}
	cands := []*store.MemoryItem{
		mk("unrelated", []float32{0, 1}),
		mk("similar", []float32{1, 0}),
	}
	got := HybridSearch([]float32{1, 0}, nil, "", "", cands, now, 10)
	if len(got) != 2 {
		t.Fatalf("got %d results, want 2", len(got))
	}
	if got[0].Item.Key != "similar" {
		t.Fatalf("first result = %q, want similar", got[0].Item.Key)
	}
}

func TestHybridSearchTagBoost(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	mk := func(key string, emb []float32, tags []string) *store.MemoryItem {
		return &store.MemoryItem{
			Key: key, Content: "content " + key, Level: "project",
			Status: "CONFIRMED", Confidence: 1.0, Embedding: emb,
			Tags: tags, LastUsedAt: now,
		}
	}
	// plain: sim=1, no boost -> 0.7 + 0.1*1 = 0.8
	// tagged: sim=0.9 along query axis? Use embeddings with cosine 0.9:
	// cos between (1,0) and (0.9, sqrt(1-0.81)) ~ 0.9.
	plain := mk("plain", []float32{1, 0}, nil)
	tagged := mk("tagged", []float32{0.9, 0.4358899}, []string{"auth"})
	got := HybridSearch([]float32{1, 0}, []string{"auth"}, "", "", []*store.MemoryItem{plain, tagged}, now, 10)
	if len(got) != 2 {
		t.Fatalf("got %d results, want 2", len(got))
	}
	// tagged: 0.7*0.9 + 0.2*1 + 0.1*1 = 0.93 > plain 0.8
	if got[0].Item.Key != "tagged" {
		t.Fatalf("tag boost failed: first = %q (scores %.3f vs %.3f)",
			got[0].Item.Key, got[0].Score, got[1].Score)
	}
}

func TestHybridSearchSkipsRejected(t *testing.T) {
	now := time.Now().UTC()
	bad := &store.MemoryItem{Key: "bad", Content: "rejected memory item", Level: "project", Status: "REJECTED", Confidence: 1.0, Embedding: []float32{1, 0}}
	good := &store.MemoryItem{Key: "good", Content: "confirmed memory item", Level: "project", Status: "CONFIRMED", Confidence: 1.0, Embedding: []float32{1, 0}}
	got := HybridSearch([]float32{1, 0}, nil, "", "", []*store.MemoryItem{bad, good}, now, 10)
	if len(got) != 1 || got[0].Item.Key != "good" {
		t.Fatalf("REJECTED item served: %+v", got)
	}
}

func TestHybridSearchTextFallback(t *testing.T) {
	now := time.Now().UTC()
	a := &store.MemoryItem{Key: "a", Content: "postgres connection pooling tunables", Level: "project", Status: "CONFIRMED", Confidence: 1.0}
	b := &store.MemoryItem{Key: "b", Content: "frontend button color palette", Level: "project", Status: "CONFIRMED", Confidence: 1.0}
	got := HybridSearch(nil, nil, "", "postgres pooling", []*store.MemoryItem{b, a}, now, 10)
	if len(got) != 2 || got[0].Item.Key != "a" {
		t.Fatalf("text fallback ranking wrong: %+v", got)
	}
}
