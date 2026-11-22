package store

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestFilePathMatchesStore(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"internal/mcp/tools.go", `D:\repo\internal\mcp\tools.go`, true},
		{"mcp/tools.go", "internal/mcp/tools.go", true},
		{"Internal/MCP/Tools.go", "internal/mcp/tools.go", true},
		{"ools.go", "internal/mcp/tools.go", false},
		{"internal/mcp/oldtools.go", "tools.go", false},
		{"  ", "tools.go", false},
	}
	for _, c := range cases {
		if got := FilePathMatches(c.a, c.b); got != c.want {
			t.Errorf("FilePathMatches(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// staleFixture creates: match (CONFIRMED, old, file matches), young
// (CONFIRMED, created after at), proposed (PROPOSED, old), other (CONFIRMED,
// old, different file), foreign (other project).
func staleFixture(t *testing.T, s *MemStore) (projectID string, at time.Time, ids map[string]string) {
	t.Helper()
	ctx := context.Background()
	p, err := s.ResolveProject(ctx, "", "", "stale-proj")
	if err != nil {
		t.Fatal(err)
	}
	q, err := s.ResolveProject(ctx, "", "", "stale-other")
	if err != nil {
		t.Fatal(err)
	}
	at = time.Now().UTC()
	ids = map[string]string{}
	mk := func(name, project, status string, files []string, created time.Time) {
		item := &MemoryItem{ProjectID: project, Key: "k/" + name, Content: "Memory about " + name + " and its files.", Status: status, FilesAffected: files}
		if err := s.CreateMemoryItem(ctx, item); err != nil {
			t.Fatal(err)
		}
		s.memories[item.ID].CreatedAt = created
		ids[name] = item.ID
	}
	old := at.Add(-time.Hour)
	mk("match", p.ID, StatusConfirmed, []string{`internal\server\routes.go`}, old)
	mk("young", p.ID, StatusConfirmed, []string{"internal/server/routes.go"}, at.Add(time.Minute))
	mk("proposed", p.ID, StatusProposed, []string{"internal/server/routes.go"}, old)
	mk("other", p.ID, StatusConfirmed, []string{"internal/server/oldroutes.go"}, old)
	mk("foreign", q.ID, StatusConfirmed, []string{"internal/server/routes.go"}, old)
	return p.ID, at, ids
}

func TestMemStoreMarkStaleOnlyMatchingOlderConfirmed(t *testing.T) {
	s := NewMemStore()
	ctx := context.Background()
	projectID, at, ids := staleFixture(t, s)

	marked, err := s.MarkMemoriesStaleForFiles(ctx, projectID, []string{"D:/repo/internal/server/routes.go", ""}, at)
	if err != nil {
		t.Fatal(err)
	}
	if len(marked) != 1 || marked[0] != ids["match"] {
		t.Fatalf("marked = %v, want only %s", marked, ids["match"])
	}
	for name, id := range ids {
		got, _ := s.GetMemoryItem(ctx, id)
		if (name == "match") != (got.StaleSince != nil) {
			t.Fatalf("%s stale = %v", name, got.StaleSince)
		}
	}
	// A second report keeps the first stamp.
	again, _ := s.MarkMemoriesStaleForFiles(ctx, projectID, []string{"routes.go"}, at)
	if len(again) != 0 {
		t.Fatalf("re-mark = %v", again)
	}

	// Editing clears it.
	content := "Routes are registered in routes.go and project_brief.go."
	if _, err := s.UpdateMemory(ctx, ids["match"], MemoryPatch{Content: &content}, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetMemoryItem(ctx, ids["match"]); got.StaleSince != nil {
		t.Fatal("edit must clear stale_since")
	}

	// Re-confirming clears it too.
	if _, err := s.MarkMemoriesStaleForFiles(ctx, projectID, []string{"internal/server/routes.go"}, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetMemoryItem(ctx, ids["match"]); got.StaleSince == nil {
		t.Fatal("expected stale again")
	}
	if err := s.ConfirmMemory(ctx, ids["match"], "reviewer"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetMemoryItem(ctx, ids["match"]); got.StaleSince != nil {
		t.Fatal("confirm must clear stale_since")
	}
}

func TestMemStoreSearchMemoryByFile(t *testing.T) {
	s := NewMemStore()
	ctx := context.Background()
	projectID, _, ids := staleFixture(t, s)
	got, err := s.SearchMemoryByFile(ctx, projectID, "server/routes.go", 10)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, m := range got {
		seen[m.ID] = true
	}
	if len(got) != 3 || !seen[ids["match"]] || !seen[ids["young"]] || !seen[ids["proposed"]] {
		t.Fatalf("by file = %d rows %v", len(got), seen)
	}
}

func TestPostgresMarkStaleAndFileSearch(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("no isolated TEST_POSTGRES_DSN")
	}
	ctx := context.Background()
	s, err := NewPostgresStore(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.RunMigrations(t.Context(), "../../migrations"); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("stale-%d", time.Now().UnixNano())
	var owner string
	if err := s.pool.QueryRow(ctx, `INSERT INTO users(username) VALUES ($1) RETURNING id::text`, name).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = s.pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1::uuid`, owner) }()
	project, err := s.ResolveProject(ctx, "", "", name)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM memory_versions WHERE memory_id IN (SELECT id FROM memory_items WHERE project_id=$1::uuid)`, project.ID)
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM memory_items WHERE project_id=$1::uuid`, project.ID)
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM projects WHERE id=$1::uuid`, project.ID)
	}()
	at := time.Now().UTC()
	ids := map[string]string{}
	mk := func(key, status string, files []string, created time.Time) {
		item := &MemoryItem{ProjectID: project.ID, Key: key, Content: "Memory about " + key + " and its files.", Status: status, FilesAffected: files}
		if err := s.CreateMemoryItem(ctx, item); err != nil {
			t.Fatal(err)
		}
		if _, err := s.pool.Exec(ctx, `UPDATE memory_items SET created_at=$2 WHERE id=$1::uuid`, item.ID, created); err != nil {
			t.Fatal(err)
		}
		ids[key] = item.ID
	}
	old := at.Add(-time.Hour)
	mk("match", StatusConfirmed, []string{`internal\server\routes.go`}, old)
	mk("young", StatusConfirmed, []string{"internal/server/routes.go"}, at.Add(time.Minute))
	mk("proposed", StatusProposed, []string{"internal/server/routes.go"}, old)
	mk("other", StatusConfirmed, []string{"internal/server/oldroutes.go"}, old)

	marked, err := s.MarkMemoriesStaleForFiles(ctx, project.ID, []string{"D:/repo/internal/server/routes.go"}, at)
	if err != nil {
		t.Fatal(err)
	}
	if len(marked) != 1 || marked[0] != ids["match"] {
		t.Fatalf("marked = %v, want %s", marked, ids["match"])
	}
	got, err := s.GetMemoryItem(ctx, ids["match"])
	if err != nil || got.StaleSince == nil || !got.StaleSince.Equal(at.Truncate(time.Microsecond)) {
		t.Fatalf("stale_since = %v %v", got.StaleSince, err)
	}
	for _, k := range []string{"young", "proposed", "other"} {
		if m, _ := s.GetMemoryItem(ctx, ids[k]); m.StaleSince != nil {
			t.Fatalf("%s wrongly stale", k)
		}
	}

	byFile, err := s.SearchMemoryByFile(ctx, project.ID, "server/routes.go", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(byFile) != 3 {
		t.Fatalf("by file = %d rows", len(byFile))
	}

	content := "Routes are registered in routes.go and project_brief.go."
	updated, err := s.UpdateMemory(ctx, ids["match"], MemoryPatch{Content: &content}, owner)
	if err != nil {
		t.Fatal(err)
	}
	if updated.StaleSince != nil {
		t.Fatal("edit must clear stale_since")
	}
	if _, err := s.MarkMemoriesStaleForFiles(ctx, project.ID, []string{"routes.go"}, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.ConfirmMemory(ctx, ids["match"], owner); err != nil {
		t.Fatal(err)
	}
	if m, _ := s.GetMemoryItem(ctx, ids["match"]); m.StaleSince != nil {
		t.Fatal("confirm must clear stale_since")
	}
}
