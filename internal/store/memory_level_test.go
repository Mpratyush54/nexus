package store

import (
	"context"
	"testing"
)

func TestMemStoreSearchMemoryPageFiltersLevel(t *testing.T) {
	ctx := WithViewer(context.Background(), "alice")
	s := NewMemStore()
	p, err := s.ResolveProject(ctx, "", "", "level-filter")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.GrantMember(ctx, p.ID, "alice", "alice"); err != nil {
		t.Fatal(err)
	}
	levels := []string{LevelOrganization, LevelProject, LevelPersonal}
	for _, level := range levels {
		item := &MemoryItem{
			ProjectID:  p.ID,
			Key:        "level/" + level,
			Content:    "level filter fixture content",
			Level:      level,
			UserID:     "alice",
			Scope:      "fact",
			Status:     StatusConfirmed,
			Confidence: 0.9,
		}
		if err := s.CreateMemoryItem(ctx, item); err != nil {
			t.Fatal(err)
		}
	}

	all, total, err := s.SearchMemoryPage(ctx, p.ID, "level filter fixture", nil, 10, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != len(levels) || total != len(levels) {
		t.Fatalf("unfiltered page = %d items, total %d; want %d", len(all), total, len(levels))
	}

	items, total, err := s.SearchMemoryPage(ctx, p.ID, "level filter fixture", nil, 10, 0, LevelPersonal)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(items) != 1 || items[0].Level != LevelPersonal {
		t.Fatalf("personal page = %v, total %d; want one personal item", keys(items), total)
	}
}

func TestMemStoreSearchMemoryVectorPageFiltersLevel(t *testing.T) {
	ctx := WithViewer(context.Background(), "alice")
	s := NewMemStore()
	p, err := s.ResolveProject(ctx, "", "", "level-vector-filter")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.GrantMember(ctx, p.ID, "alice", "alice"); err != nil {
		t.Fatal(err)
	}
	levels := []string{LevelProject, LevelPersonal}
	for i, level := range levels {
		vec := make([]float32, EmbeddingDim)
		vec[0] = 1
		vec[1] = float32(i) / 10
		item := &MemoryItem{
			ProjectID:  p.ID,
			Key:        "level/vector/" + level,
			Content:    "vector level fixture content",
			Level:      level,
			UserID:     "alice",
			Scope:      "fact",
			Status:     StatusConfirmed,
			Confidence: 0.9,
			Embedding:  vec,
		}
		if err := s.CreateMemoryItem(ctx, item); err != nil {
			t.Fatal(err)
		}
	}

	query := make([]float32, EmbeddingDim)
	query[0] = 1
	items, err := s.SearchMemoryVectorPage(ctx, p.ID, query, 10, 0, LevelPersonal)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Level != LevelPersonal {
		t.Fatalf("personal vector page = %v; want one personal item", keys(items))
	}
}
