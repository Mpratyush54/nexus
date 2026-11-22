package store

import (
	"context"
	"fmt"
	"math"
	"os"
	"testing"
	"time"
)

const testBGEModel = "local:BAAI/bge-small-en-v1.5"

type vectorModelStore interface {
	CreateMemoryItem(ctx context.Context, item *MemoryItem) error
	SearchMemoryVectorModel(ctx context.Context, projectID string, q VectorQuery, limit int) ([]*MemoryItem, error)
	ListMemoryForReembed(ctx context.Context, model, afterID string, limit int) ([]ReembedCandidate, error)
	SetMemoryEmbedding(ctx context.Context, id string, vec []float32, model string) error
}

func axisCosVec(cosWithAxis0 float64) []float32 {
	v := make([]float32, EmbeddingDim)
	v[0] = float32(cosWithAxis0)
	v[1] = float32(math.Sqrt(1 - cosWithAxis0*cosWithAxis0))
	return v
}

func keysOf(items []*MemoryItem) map[string]bool {
	out := map[string]bool{}
	for _, m := range items {
		out[m.Key] = true
	}
	return out
}

// checkVectorModelFilter is shared by the MemStore and Postgres tests so
// both backends answer identically.
func checkVectorModelFilter(t *testing.T, st vectorModelStore, projectID string) {
	t.Helper()
	ctx := context.Background()
	mk := func(key, model string, vec []float32) *MemoryItem {
		m := &MemoryItem{
			ProjectID: projectID, Key: key,
			Content: "Memory " + key + " describes how deploys and embeddings work here.",
			Level:   LevelProject, Scope: "fact", Status: StatusConfirmed, Confidence: 0.9,
			Embedding: vec, EmbeddingModel: model,
		}
		if err := st.CreateMemoryItem(ctx, m); err != nil {
			t.Fatalf("create %s: %v", key, err)
		}
		return m
	}
	// Identical vectors, different models: only the query's model may match.
	ids := map[string]string{
		"legacy":  mk("legacy", "", axisCosVec(1)).ID,              // NULL tag = legacy hash
		"hashed":  mk("hashed", "hash", axisCosVec(1)).ID,          // explicit hash
		"bge":     mk("bge", testBGEModel, axisCosVec(1)).ID,       // model vector
		"bge-mid": mk("bge-mid", testBGEModel, axisCosVec(0.5)).ID, // cos 0.5: below the bge floor
	}

	q := axisCosVec(1)
	got, err := st.SearchMemoryVectorModel(ctx, projectID, VectorQuery{Vec: q, Model: testBGEModel, MinSimilarity: 0.6}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if k := keysOf(got); len(k) != 1 || !k["bge"] {
		t.Fatalf("bge query at 0.6 = %v, want only bge (no hash rows, bge-mid below floor)", k)
	}
	got, _ = st.SearchMemoryVectorModel(ctx, projectID, VectorQuery{Vec: q, Model: testBGEModel, MinSimilarity: 0.2}, 10)
	if k := keysOf(got); len(k) != 2 || !k["bge"] || !k["bge-mid"] {
		t.Fatalf("bge query at 0.2 = %v, want bge + bge-mid", k)
	}
	got, _ = st.SearchMemoryVectorModel(ctx, projectID, VectorQuery{Vec: q, Model: "hash"}, 10)
	if k := keysOf(got); len(k) != 2 || !k["legacy"] || !k["hashed"] {
		t.Fatalf("hash query = %v, want legacy (NULL) + hashed, never bge", k)
	}
	got, _ = st.SearchMemoryVectorModel(ctx, projectID, VectorQuery{Vec: q}, 10)
	if k := keysOf(got); len(k) != 4 {
		t.Fatalf("unfiltered query = %v, want all 4", k)
	}

	// Reembed seams: legacy + hashed + nothing else are stale for bge;
	// converting one moves it into the bge result set.
	// Page through every stale row (a shared Postgres may hold others).
	stale := map[string]bool{}
	for after := ""; ; {
		page, err := st.ListMemoryForReembed(ctx, testBGEModel, after, 100)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		for _, c := range page {
			stale[c.ID] = true
		}
		after = page[len(page)-1].ID
	}
	if !stale[ids["legacy"]] || !stale[ids["hashed"]] || stale[ids["bge"]] || stale[ids["bge-mid"]] {
		t.Fatalf("stale for bge: legacy=%v hashed=%v bge=%v bge-mid=%v", stale[ids["legacy"]], stale[ids["hashed"]], stale[ids["bge"]], stale[ids["bge-mid"]])
	}
	if err := st.SetMemoryEmbedding(ctx, ids["legacy"], axisCosVec(1), testBGEModel); err != nil {
		t.Fatal(err)
	}
	got, _ = st.SearchMemoryVectorModel(ctx, projectID, VectorQuery{Vec: q, Model: testBGEModel, MinSimilarity: 0.6}, 10)
	if k := keysOf(got); len(k) != 2 || !k["legacy"] {
		t.Fatalf("after re-embed = %v, want bge + legacy", k)
	}
}

func TestMemStoreVectorSearchModelFilterAndFloor(t *testing.T) {
	s := NewMemStore()
	p, err := s.ResolveProject(context.Background(), "https://example.com/model.git", "abc", "model-filter")
	if err != nil {
		t.Fatal(err)
	}
	checkVectorModelFilter(t, s, p.ID)
}

func TestVectorQueryFloorDefaults(t *testing.T) {
	for in, want := range map[float64]float64{0: MinVectorSimilarity, -1: MinVectorSimilarity, 1: MinVectorSimilarity, 0.6: 0.6} {
		if got := (VectorQuery{MinSimilarity: in}).floor(); got != want {
			t.Errorf("floor(%v) = %v, want %v", in, got, want)
		}
	}
}

func TestPostgresVectorSearchModelFilter(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("no isolated TEST_POSTGRES_DSN")
	}
	s, err := NewPostgresStore(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.RunMigrations(t.Context(), "../../migrations"); err != nil {
		t.Fatal(err)
	}
	var owner string
	name := fmt.Sprintf("embed-model-%d", time.Now().UnixNano())
	if err := s.pool.QueryRow(t.Context(), `INSERT INTO users(username) VALUES ($1) RETURNING id::text`, name).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = s.pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1::uuid`, owner) }()
	project, err := s.ResolveProject(t.Context(), "", "", name)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM memory_items WHERE project_id=$1::uuid`, project.ID)
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM projects WHERE id=$1::uuid`, project.ID)
	}()
	checkVectorModelFilter(t, s, project.ID)
}
