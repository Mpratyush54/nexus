package store

import (
	"context"
	"testing"
)

func TestMemStoreSearchMemoryVector(t *testing.T) {
	s := NewMemStore()
	ctx := context.Background()
	p, err := s.ResolveProject(ctx, "https://example.com/embed.git", "abc", "embed-vec")
	if err != nil {
		t.Fatal(err)
	}
	vec := make([]float32, EmbeddingDim)
	vec[0] = 1
	hit := &MemoryItem{
		ProjectID: p.ID, Key: "vec/hit", Content: "The team uses pytest with fixture-based setup for integration tests.",
		Level: LevelProject, Scope: "fact", Status: StatusConfirmed, Confidence: 0.9, Embedding: vec,
	}
	miss := &MemoryItem{
		ProjectID: p.ID, Key: "vec/miss", Content: "Unrelated database migration notes for operators here.",
		Level: LevelProject, Scope: "fact", Status: StatusConfirmed, Confidence: 0.9,
		Embedding: func() []float32 {
			v := make([]float32, EmbeddingDim)
			v[100] = 1
			return v
		}(),
	}
	proposed := &MemoryItem{
		ProjectID: p.ID, Key: "vec/proposed", Content: "Proposed memory must not appear in vector search results.",
		Level: LevelProject, Scope: "fact", Status: StatusProposed, Confidence: 0.9, Embedding: vec,
	}
	for _, m := range []*MemoryItem{hit, miss, proposed} {
		if err := s.CreateMemoryItem(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	// CreateMemoryItem may force PROPOSED; force confirmed state for hit/miss.
	s.mu.Lock()
	for _, id := range []string{hit.ID, miss.ID} {
		if row, ok := s.memories[id]; ok {
			row.Status = StatusConfirmed
			row.Confidence = 0.9
		}
	}
	s.mu.Unlock()

	query := make([]float32, EmbeddingDim)
	query[0] = 1
	got, err := s.SearchMemoryVector(ctx, p.ID, query, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("expected at least the matching confirmed row")
	}
	if got[0].Key != "vec/hit" {
		t.Fatalf("top hit = %q, want vec/hit", got[0].Key)
	}
	for _, m := range got {
		if m.Key == "vec/proposed" {
			t.Fatal("PROPOSED rows must not appear in vector search")
		}
	}
}
