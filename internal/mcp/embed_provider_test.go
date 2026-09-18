package mcp

// Provider wiring + vector memory_search (nexus issue #165).

import (
	"context"
	"testing"

	memctx "central-memory/internal/context"
)

// vectorFakeStore adds SearchMemoryVector on top of fakeStore.
type vectorFakeStore struct {
	*fakeStore
	vectorCalls int
}

func (v *vectorFakeStore) SearchMemoryVector(_ context.Context, projectID string, queryVec []float32, limit int) ([]*MemoryItem, error) {
	v.vectorCalls++
	if len(queryVec) != EmbedDims {
		return nil, nil
	}
	var out []*MemoryItem
	for _, m := range v.memories {
		if m.ProjectID != projectID || len(m.Embedding) != EmbedDims {
			continue
		}
		if m.Status != "CONFIRMED" || m.Confidence <= 0.3 {
			continue
		}
		out = append(out, m)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func TestMemoryWriteEmbedderFallback(t *testing.T) {
	s, ms := newTestServerWithT(t)
	// Provider that always fails → HashEmbed fallback.
	s.cfg.Embedder = func(ctx context.Context, text string) ([]float32, error) {
		return nil, context.Canceled
	}
	// Wrap with NewEmbedder-style fallback manually: resolveEmbedder uses
	// Embedder directly; embedForWrite falls back to HashEmbed on error.
	r := callTool(t, s, "memory_write", map[string]any{
		"key": "embed/fallback", "content": "The team uses pytest with fixture-based setup for integration tests.",
	})
	if r.Error != nil {
		t.Fatalf("write: %v", r.Error)
	}
	vec := ms.memories[0].Embedding
	want := HashEmbed(embedText("embed/fallback", "The team uses pytest with fixture-based setup for integration tests."))
	if memctx.EmbedFingerprint(vec) != memctx.EmbedFingerprint(want) {
		t.Fatal("failed Embedder must fall back to HashEmbed")
	}
}

func TestMemorySearchUsesVectorPath(t *testing.T) {
	dir := t.TempDir()
	base := newFakeStore()
	base.workspace = &Workspace{Branch: "main", CommitSHA: "abc", Path: dir}
	base.project = &Project{DisplayName: "p", FolderName: "p"}
	vs := &vectorFakeStore{fakeStore: base}
	s := NewServer(vs, Config{
		ProjectID: "proj_test", ProjectName: "p", WorkspacePath: dir,
		Embedder: memctx.NewEmbedder(memctx.EmbeddingConfig{Provider: memctx.ProviderHash}),
	})
	content := "The team uses pytest with fixture-based setup for integration tests."
	vec := HashEmbed(embedText("vec/hit", content))
	_ = vs.CreateMemoryItem(context.Background(), &MemoryItem{
		ProjectID: "proj_test", Key: "vec/hit", Content: content,
		Level: "project", Scope: "fact", Status: "CONFIRMED", Confidence: 0.9,
		Embedding: vec,
	})
	r := callTool(t, s, "memory_search", map[string]any{"query": "pytest fixtures"})
	if r.Error != nil {
		t.Fatalf("search: %v", r.Error)
	}
	if vs.vectorCalls == 0 {
		t.Fatal("memory_search must call SearchMemoryVector when available")
	}
	m := resultMap(t, r)
	if got := num(t, m, "items_included"); got < 1 {
		t.Fatalf("items_included = %v, want >= 1", got)
	}
}

func TestMemoryWritePreferEmbedderOverEmbed(t *testing.T) {
	s, ms := newTestServerWithT(t)
	s.cfg.Embed = func(text string) []float32 {
		t.Fatal("sync Embed must not run when Embedder is set")
		return nil
	}
	marker := make([]float32, EmbedDims)
	marker[7] = 1
	s.cfg.Embedder = func(ctx context.Context, text string) ([]float32, error) {
		return marker, nil
	}
	r := callTool(t, s, "memory_write", map[string]any{
		"key": "embed/prefer", "content": "The team uses pytest with fixture-based setup for integration tests.",
	})
	if r.Error != nil {
		t.Fatalf("write: %v", r.Error)
	}
	got := ms.memories[0].Embedding
	if len(got) != EmbedDims || got[7] != 1 {
		t.Fatal("Embedder must win over Embed")
	}
}
