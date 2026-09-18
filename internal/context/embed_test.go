package context

// Embedding pipeline + agent budget tests (nexus issues #76, #41).

import (
	"context"
	"math"
	"testing"

	"central-memory/internal/store"
)

func TestHashEmbedContract(t *testing.T) {
	a := HashEmbed("The team uses pytest with fixture-based setup")
	b := HashEmbed("The team uses pytest with fixture-based setup")
	if len(a) != EmbedDims {
		t.Fatalf("dims = %d, want %d", len(a), EmbedDims)
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatal("HashEmbed must be deterministic")
		}
	}
	var sum float64
	for _, v := range a {
		sum += float64(v) * float64(v)
	}
	if math.Abs(sum-1) > 1e-5 {
		t.Fatalf("norm² = %v, want 1", sum)
	}
	if got := HashEmbed("completely different words about databases"); EmbedFingerprint(got) == EmbedFingerprint(a) {
		t.Fatal("distinct texts must fingerprint differently")
	}
	if len(HashEmbed("")) != EmbedDims {
		t.Fatal("empty text must still return full-width vector")
	}
}

func TestBackfillEmbeddingsFillsMissing(t *testing.T) {
	ctx := context.Background()
	keep := HashEmbed("kept vector content here")
	items := []*store.MemoryItem{
		{Key: "missing/one", Content: "First memory needing an embedding vector here."},
		{Key: "has/one", Content: "Second memory already embedded content.", Embedding: keep},
		{Key: "short/one", Content: "Third memory with a short stale vector.", Embedding: []float32{1, 2}},
		nil,
	}
	n, err := BackfillEmbeddings(ctx, AsEmbedder(HashEmbed), items)
	if err != nil {
		t.Fatalf("BackfillEmbeddings: %v", err)
	}
	if n != 2 {
		t.Fatalf("filled = %d, want 2 (missing + short)", n)
	}
	if len(items[0].Embedding) != EmbedDims {
		t.Fatal("missing vector not filled")
	}
	if &items[1].Embedding[0] != &keep[0] {
		t.Fatal("existing full-width vector must be kept untouched")
	}
	if len(items[2].Embedding) != EmbedDims {
		t.Fatal("short vector must be regenerated")
	}

	// Canceled context aborts with progress so far.
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	fresh := []*store.MemoryItem{{Key: "fresh/one", Content: "Fresh memory needing an embedding vector here."}}
	if n, err := BackfillEmbeddings(cancelled, AsEmbedder(HashEmbed), fresh); err == nil || n != 0 {
		t.Fatalf("canceled backfill = (%d, %v), want (0, ctx err)", n, err)
	}
	if len(fresh[0].Embedding) != 0 {
		t.Fatal("canceled backfill must not fill")
	}
}

func TestAgentBudgetResolution(t *testing.T) {
	if got := AgentBudget("claude", 0); got != 10000 {
		t.Fatalf("claude budget = %d, want 10000", got)
	}
	if got := AgentBudget("copilot", 0); got != 8000 {
		t.Fatalf("copilot budget = %d, want 8000", got)
	}
	if got := AgentBudget("cursor", 0); got != 6000 {
		t.Fatalf("cursor budget = %d, want 6000", got)
	}
	if got := AgentBudget("unknown-agent", 1234); got != 1234 {
		t.Fatalf("unknown agent budget = %d, want fallback 1234", got)
	}
	if got := AgentBudget("", 0); got != DefaultBudget {
		t.Fatalf("blank agent budget = %d, want DefaultBudget %d", got, DefaultBudget)
	}
	// Resolved budgets feed the builder: a claude-sized budget keeps more.
	big := AssembleXML(ContextInput{ProjectName: "p", Budget: AgentBudget("claude", 0)})
	small := AssembleXML(ContextInput{ProjectName: "p", Budget: DefaultBudget})
	if big.BudgetRemaining < small.BudgetRemaining {
		t.Fatal("larger agent budget must leave at least as much headroom")
	}
}
