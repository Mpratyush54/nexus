package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"
)

// exerciseFindMemoryByKey: exact key wins over rows that only mention the
// key in their content, private rows stay hidden from viewer-less reads,
// and WithoutEmbeddings drops the vector (Postgres) without changing rows.
func exerciseFindMemoryByKey(t *testing.T, st interface {
	Store
	FindMemoryByKey(ctx context.Context, projectID, key string) (*MemoryItem, error)
}, projectID string, dim int, wantLean bool) {
	t.Helper()
	ctx := t.Context()
	vec := make([]float32, dim)
	vec[0] = 1
	noise := &MemoryItem{ProjectID: projectID, Key: "notes/brief-mention", Content: "Mentions project/brief in passing: rebuilt by the worker.",
		Level: LevelProject, Scope: "fact", Status: StatusConfirmed, Confidence: 1, Embedding: vec}
	brief := &MemoryItem{ProjectID: projectID, Key: ProjectBriefKey, Content: "Project brief: a Go server with a Postgres store.",
		Level: LevelProject, Scope: "fact", Status: StatusConfirmed, Confidence: 0.5, Embedding: vec}
	for _, m := range []*MemoryItem{noise, brief} {
		if err := st.CreateMemoryItem(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.FindMemoryByKey(ctx, projectID, ProjectBriefKey)
	if err != nil || got.ID != brief.ID {
		t.Fatalf("find brief: %+v %v", got, err)
	}
	if len(got.Embedding) != dim {
		t.Fatalf("default read keeps the embedding: %d", len(got.Embedding))
	}
	lean, err := st.FindMemoryByKey(WithoutEmbeddings(ctx), projectID, ProjectBriefKey)
	if err != nil || lean.ID != brief.ID || lean.Content != brief.Content {
		t.Fatalf("lean find: %+v %v", lean, err)
	}
	if wantLean && lean.Embedding != nil {
		t.Fatalf("WithoutEmbeddings must skip the vector: %d", len(lean.Embedding))
	}
	list, err := st.SearchMemory(WithoutEmbeddings(ctx), projectID, "", nil, 10)
	if err != nil || len(list) != 2 {
		t.Fatalf("lean list: %d %v", len(list), err)
	}
	if wantLean && (list[0].Embedding != nil || list[1].Embedding != nil) {
		t.Fatal("lean list kept embeddings")
	}
	if _, err := st.FindMemoryByKey(ctx, projectID, "missing/key"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing key: %v", err)
	}
	if _, err := st.FindMemoryByKey(ctx, projectID, "project/"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("prefix must not match: %v", err)
	}
}

func TestFindMemoryByKeyMem(t *testing.T) {
	m := NewMemStore()
	p, err := m.ResolveProject(t.Context(), "", "", "find-by-key")
	if err != nil {
		t.Fatal(err)
	}
	exerciseFindMemoryByKey(t, m, p.ID, 1536, false)
}

func TestFindMemoryByKeyPostgres(t *testing.T) {
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
	p, err := s.ResolveProject(t.Context(), "", "", fmt.Sprintf("find-by-key-%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM memory_items WHERE project_id=$1::uuid`, p.ID)
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM projects WHERE id=$1::uuid`, p.ID)
	}()
	var idx bool
	if err := s.pool.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE indexname = 'idx_memory_project_key')`).Scan(&idx); err != nil || !idx {
		t.Fatalf("migration 049 index missing: %v", err)
	}
	exerciseFindMemoryByKey(t, s, p.ID, 1536, true)
}
