package mcp

// Embedding pipeline + budget/dedup tests (nexus issues #76, #41).
//
//   - memory_write generates a 1536-dim L2-normalized vector via the
//     configured Embed backend (default HashEmbed) and carries it on the
//     DTO for the store adapter to persist.
//   - memory_search dedupes same-key collisions (most-specific level wins)
//     and truncates the XML block to the effective agent budget, keeping it
//     well-formed.

import (
	"context"
	"math"
	"strings"
	"testing"
)

func TestMemoryWriteCarriesEmbedding(t *testing.T) {
	s, ms := newTestServerWithT(t)
	r := callTool(t, s, "memory_write", map[string]any{
		"key": "embed/pipeline", "content": "The team uses pytest with fixture-based setup for integration tests.",
	})
	if r.Error != nil {
		t.Fatalf("write: %v", r.Error)
	}
	if len(ms.memories) != 1 {
		t.Fatalf("stored = %d, want 1", len(ms.memories))
	}
	vec := ms.memories[0].Embedding
	if len(vec) != EmbedDims {
		t.Fatalf("embedding dims = %d, want %d", len(vec), EmbedDims)
	}
	var sum float64
	for _, v := range vec {
		sum += float64(v) * float64(v)
	}
	if math.Abs(sum-1) > 1e-5 {
		t.Fatalf("embedding norm² = %v, want 1 (L2-normalized)", sum)
	}
}

func TestMemoryWriteCustomEmbedBackend(t *testing.T) {
	s, ms := newTestServerWithT(t)
	s.cfg.Embed = func(text string) []float32 {
		vec := make([]float32, EmbedDims)
		vec[0] = 1 // marker backend: deterministic, normalized
		return vec
	}
	r := callTool(t, s, "memory_write", map[string]any{
		"key": "embed/custom", "content": "The team uses pytest with fixture-based setup for integration tests.",
	})
	if r.Error != nil {
		t.Fatalf("write: %v", r.Error)
	}
	vec := ms.memories[0].Embedding
	if len(vec) != EmbedDims || vec[0] != 1 {
		t.Fatal("custom Embed backend was not used")
	}
}

func TestMemorySearchDedupesByKey(t *testing.T) {
	s, ms := newTestServerWithT(t)
	ctx := context.Background()
	for _, item := range []*MemoryItem{
		{ProjectID: "proj_test", Key: "dup/key", Content: "Organization level memory content here.", Level: "organization", Scope: "fact"},
		{ProjectID: "proj_test", Key: "dup/key", Content: "Session level memory content here now.", Level: "session", Scope: "fact"},
	} {
		if err := ms.CreateMemoryItem(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	r := callTool(t, s, "memory_search", map[string]any{"query": "memory content"})
	m := resultMap(t, r)
	if got := num(t, m, "items_included"); got != 1 {
		t.Fatalf("items_included = %v, want 1 (deduped)", got)
	}
	ctxXML, _ := m["context"].(string)
	if !strings.Contains(ctxXML, "Session level") || strings.Contains(ctxXML, "Organization level") {
		t.Fatalf("dedup must keep the session override: %s", ctxXML)
	}
}

func TestMemorySearchTruncatesToBudget(t *testing.T) {
	s, ms := newTestServerWithT(t)
	s.cfg.TokenBudget = 300 // tiny budget forces truncation
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		if err := ms.CreateMemoryItem(ctx, &MemoryItem{
			ProjectID: "proj_test", Key: "bulk/key",
			Content: "The team uses pytest with fixture-based setup for integration tests number.",
			Level:   "project", Scope: "fact",
		}); err != nil {
			t.Fatal(err)
		}
		// Distinct keys so dedup keeps all ten.
		ms.memories[len(ms.memories)-1].Key = "bulk/key-" + string(rune('a'+i))
	}
	r := callTool(t, s, "memory_search", map[string]any{"query": "pytest integration"})
	m := resultMap(t, r)
	ctxXML, _ := m["context"].(string)
	if len([]rune(ctxXML)) > 300 {
		t.Fatalf("context len = %d, want <= budget 300", len([]rune(ctxXML)))
	}
	if !strings.HasPrefix(ctxXML, "<project_memory") || !strings.HasSuffix(ctxXML, "</project_memory>") {
		t.Fatalf("truncated context must stay well-formed: %s", ctxXML)
	}
	if got := num(t, m, "budget_remaining"); got != 300-float64(len([]rune(ctxXML))) {
		t.Fatalf("budget_remaining = %v inconsistent with truncated output", got)
	}
}
