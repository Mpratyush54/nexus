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
	var sawProposed bool
	for _, m := range got {
		if m.Key == "vec/proposed" {
			sawProposed = true
		}
		if m.Key == "vec/miss" {
			t.Fatal("orthogonal vector must not outrank the query")
		}
	}
	if !sawProposed {
		t.Fatal("PROPOSED rows are active on write and must appear in vector search")
	}
}

func TestMemStoreSearchMemoryVectorPagination(t *testing.T) {
	s := NewMemStore()
	ctx := context.Background()
	p, err := s.ResolveProject(ctx, "https://example.com/page.git", "abc", "embed-page")
	if err != nil {
		t.Fatal(err)
	}
	scores := []float32{1, 0.8, 0.6}
	for i, score := range scores {
		vec := make([]float32, EmbeddingDim)
		vec[0] = score
		vec[1] = 1 - score
		item := &MemoryItem{
			ProjectID: p.ID, Key: "page/" + string(rune('0'+i)), Content: "pagination fixture content for vector ordering checks",
			Level: LevelProject, Scope: "fact", Status: StatusConfirmed, Confidence: 0.9, Embedding: vec,
		}
		if err := s.CreateMemoryItem(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	query := make([]float32, EmbeddingDim)
	query[0] = 1
	for offset, want := range []string{"page/0", "page/1", "page/2"} {
		got, err := s.SearchMemoryVectorPage(ctx, p.ID, query, 1, offset, "")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Key != want {
			t.Fatalf("offset %d = %v, want %s", offset, keys(got), want)
		}
	}
	got, err := s.SearchMemoryVectorPage(ctx, p.ID, query, 1, len(scores), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("offset past end returned %v", keys(got))
	}
}

func keys(items []*MemoryItem) []string {
	out := make([]string, len(items))
	for i, item := range items {
		out[i] = item.Key
	}
	return out
}
