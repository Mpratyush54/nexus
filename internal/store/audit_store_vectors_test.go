package store

// Vector-dimension + Unicode-length regression tests (issue #105).
//
// The schema is vector(1536): wrong-width vectors must fail in Go with a
// clear error, never as a Postgres runtime crash. Content length follows
// rune counts (Postgres char_length), never Go byte counts.

import (
	"context"
	"strings"
	"testing"
)

func vecN(n int) []float32 {
	v := make([]float32, n)
	for i := range v {
		v[i] = 0.01
	}
	return v
}

func TestAuditValidateEmbeddingDim(t *testing.T) {
	if err := ValidateEmbeddingDim(nil); err != nil {
		t.Errorf("nil embedding must pass (NULL path), got %v", err)
	}
	if err := ValidateEmbeddingDim([]float32{}); err != nil {
		t.Errorf("empty embedding must pass (NULL path), got %v", err)
	}
	if err := ValidateEmbeddingDim(vecN(EmbeddingDim)); err != nil {
		t.Errorf("1536-dim must pass, got %v", err)
	}
	for _, n := range []int{1, 2, 3, 128, 1535, 1537, 4096} {
		if err := ValidateEmbeddingDim(vecN(n)); err == nil {
			t.Errorf("%d-dim embedding accepted, want rejection (vector(1536))", n)
		}
	}
}

func TestAuditCreateMemoryRejectsWrongDimEmbedding(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	p, err := s.ResolveProject(ctx, "", "", "vecproj")
	if err != nil {
		t.Fatal(err)
	}
	item := &MemoryItem{
		ProjectID: p.ID, Key: "k/vec", Level: "project",
		Content:   "this content is comfortably inside the twenty rune floor",
		Embedding: vecN(3),
	}
	if err := s.CreateMemoryItem(ctx, item); err == nil {
		t.Error("3-dim embedding accepted on create, want dimension error")
	}
	item.Embedding = vecN(EmbeddingDim)
	if err := s.CreateMemoryItem(ctx, item); err != nil {
		t.Errorf("1536-dim embedding rejected on create: %v", err)
	}
}

func TestAuditCreateEpisodeRejectsWrongDimEmbedding(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	p, err := s.ResolveProject(ctx, "", "", "veceps")
	if err != nil {
		t.Fatal(err)
	}
	ep := &Episode{ProjectID: p.ID, Title: "dim episode", EpisodeType: "bug_fix", Embedding: vecN(128)}
	if err := s.CreateEpisode(ctx, ep); err == nil {
		t.Error("128-dim episode embedding accepted, want dimension error")
	}
	ep.Embedding = nil
	if err := s.CreateEpisode(ctx, ep); err != nil {
		t.Errorf("nil episode embedding rejected: %v", err)
	}
}

func TestAuditSemanticSearchRejectsBadQueryVec(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	p, err := s.ResolveProject(ctx, "", "", "vecq")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SearchEpisodesSemantic(ctx, p.ID, nil, 5); err == nil {
		t.Error("nil query vec accepted, want needs-embedding error")
	}
	if _, err := s.SearchEpisodesSemantic(ctx, p.ID, vecN(7), 5); err == nil {
		t.Error("7-dim query vec accepted, want dimension error")
	}
}

func TestAuditContentLengthIsRuneBased(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	p, err := s.ResolveProject(ctx, "", "", "runeproj")
	if err != nil {
		t.Fatal(err)
	}
	mk := func(content string) *MemoryItem {
		return &MemoryItem{ProjectID: p.ID, Key: "k/" + content[:3], Level: "project", Content: content}
	}
	// 2000 runes x 3 bytes = 6000 bytes: rune-valid, byte-invalid. Must pass.
	wide2000 := strings.Repeat("漢", 2000)
	if err := s.CreateMemoryItem(ctx, mk(wide2000)); err != nil {
		t.Errorf("2000-rune multibyte content rejected: %v (length must be rune-based)", err)
	}
	// 19 runes x 4 bytes = 76 bytes: rune-invalid, byte-valid. Must fail.
	narrow19 := strings.Repeat("𐍈", 19)
	if err := s.CreateMemoryItem(ctx, mk(narrow19)); err == nil {
		t.Error("19-rune content accepted, want rejection (rune floor is 20)")
	}
	// 2001 ASCII runes: over ceiling. Must fail.
	if err := s.CreateMemoryItem(ctx, mk(strings.Repeat("x", 2001))); err == nil {
		t.Error("2001-rune content accepted, want rejection (ceiling 2000)")
	}
}
