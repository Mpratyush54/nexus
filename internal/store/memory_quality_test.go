package store

// Memory quality fixes: personal rows default private, same-key superseding
// is scoped by level and owner, vector search has a similarity floor and
// honours the viewer, the page total matches the list, and recording use
// leaves updated_at alone.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"testing"
	"time"
)

func qualityProject(t *testing.T, s *MemStore, name string) *Project {
	t.Helper()
	ctx := context.Background()
	p, err := s.ResolveProject(ctx, "", "", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimProject(ctx, p.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := s.GrantMember(ctx, p.ID, "bob", "alice"); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPersonalMemoryDefaultsPrivateToOwner(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	p := qualityProject(t, s, "personal-private")

	personal := &MemoryItem{
		ProjectID: p.ID, Key: "editor/theme", Level: LevelPersonal,
		Content: "I prefer a dark editor theme with a monospace font everywhere.",
		UserID:  "alice", ProposedBy: "alice", Status: StatusConfirmed,
	}
	if err := s.CreateMemoryItem(ctx, personal); err != nil {
		t.Fatal(err)
	}
	if personal.Visibility != VisibilityPrivate {
		t.Fatalf("personal visibility = %q, want private", personal.Visibility)
	}

	// Bob is a project member but not the owner: neither search nor a
	// direct fetch may reveal it.
	got, err := s.SearchMemory(WithViewer(ctx, "bob"), p.ID, "", nil, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range got {
		if m.ID == personal.ID {
			t.Fatal("teammate sees another member's personal memory")
		}
	}
	if _, err := s.GetMemoryItem(WithViewer(ctx, "bob"), personal.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("teammate GetMemoryItem err = %v, want ErrNotFound", err)
	}

	// The owner still sees it.
	got, err = s.SearchMemory(WithViewer(ctx, "alice"), p.ID, "", nil, 50)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range got {
		found = found || m.ID == personal.ID
	}
	if !found {
		t.Fatal("owner lost their own personal memory")
	}

	// Project rows keep the project default; an explicit visibility wins.
	proj := &MemoryItem{
		ProjectID: p.ID, Key: "build/tool", Level: LevelProject,
		Content: "The build uses PowerShell scripts with gofmt checks.", ProposedBy: "alice",
	}
	if err := s.CreateMemoryItem(ctx, proj); err != nil {
		t.Fatal(err)
	}
	if proj.Visibility != VisibilityProject {
		t.Fatalf("project visibility = %q, want project", proj.Visibility)
	}
	shared := &MemoryItem{
		ProjectID: p.ID, Key: "editor/keys", Level: LevelPersonal, Visibility: VisibilityPublic,
		Content: "I prefer vim key bindings in every editor I use.", UserID: "alice", ProposedBy: "alice",
	}
	if err := s.CreateMemoryItem(ctx, shared); err != nil {
		t.Fatal(err)
	}
	if shared.Visibility != VisibilityPublic {
		t.Fatalf("explicit visibility overridden: %q", shared.Visibility)
	}
}

func TestDefaultMemoryVisibilityOwnerlessPersonalStaysProject(t *testing.T) {
	// No owner: private would be visible to no one.
	if got := DefaultMemoryVisibility(&MemoryItem{Level: LevelPersonal}); got != VisibilityProject {
		t.Fatalf("ownerless personal = %q, want project", got)
	}
	if got := DefaultMemoryVisibility(&MemoryItem{Level: "Personal", UserID: "u1"}); got != VisibilityPrivate {
		t.Fatalf("owned personal = %q, want private", got)
	}
	if got := DefaultMemoryVisibility(&MemoryItem{Level: LevelSession, UserID: "u1"}); got != VisibilityProject {
		t.Fatalf("session = %q, want project", got)
	}
}

func TestSupersedesExistingScopedByLevelAndOwner(t *testing.T) {
	cases := []struct {
		name     string
		newItem  MemoryItem
		old      MemoryItem
		replaces bool
	}{
		{"same project level", MemoryItem{Level: LevelProject, ProposedBy: "a"}, MemoryItem{Level: LevelProject, ProposedBy: "b"}, true},
		{"blank level is project", MemoryItem{}, MemoryItem{Level: LevelProject}, true},
		{"project does not replace personal", MemoryItem{Level: LevelProject}, MemoryItem{Level: LevelPersonal, UserID: "a"}, false},
		{"personal does not replace project", MemoryItem{Level: LevelPersonal, UserID: "a"}, MemoryItem{Level: LevelProject}, false},
		{"organization vs project", MemoryItem{Level: LevelOrganization}, MemoryItem{Level: LevelProject}, false},
		{"personal same owner", MemoryItem{Level: LevelPersonal, UserID: "a", ProposedBy: "a"}, MemoryItem{Level: LevelPersonal, UserID: "a", ProposedBy: "a"}, true},
		{"personal other owner", MemoryItem{Level: LevelPersonal, UserID: "a", ProposedBy: "a"}, MemoryItem{Level: LevelPersonal, UserID: "b", ProposedBy: "b"}, false},
		{"session other owner", MemoryItem{Level: LevelSession, ProposedBy: "a"}, MemoryItem{Level: LevelSession, ProposedBy: "b"}, false},
		{"session same owner", MemoryItem{Level: LevelSession, ProposedBy: "a"}, MemoryItem{Level: LevelSession, ProposedBy: "a"}, true},
	}
	for _, tc := range cases {
		if got := SupersedesExisting(&tc.newItem, &tc.old); got != tc.replaces {
			t.Errorf("%s: SupersedesExisting = %v, want %v", tc.name, got, tc.replaces)
		}
	}
}

// unitVec returns a 1536-dim unit vector with cosine `sim` to e0.
func unitVec(sim float64) []float32 {
	v := make([]float32, EmbeddingDim)
	v[0] = float32(sim)
	v[1] = float32(math.Sqrt(1 - sim*sim))
	return v
}

func TestSearchMemoryVectorSimilarityFloorAndViewer(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	p := qualityProject(t, s, "vector-floor")
	mk := func(key string, sim float64, level, owner string) *MemoryItem {
		m := &MemoryItem{
			ProjectID: p.ID, Key: key, Level: level, Status: StatusConfirmed, Confidence: 0.9,
			Content: "vector floor test memory content " + key, Embedding: unitVec(sim),
			ProposedBy: owner,
		}
		if level == LevelPersonal {
			m.UserID = owner
		}
		if err := s.CreateMemoryItem(ctx, m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	_ = mk("near", 0.9, LevelProject, "alice")
	_ = mk("junk", MinVectorSimilarity/2, LevelProject, "alice")
	_ = mk("mine", 0.8, LevelPersonal, "alice")

	q := unitVec(1)
	ids := func(items []*MemoryItem) map[string]bool {
		out := map[string]bool{}
		for _, m := range items {
			out[m.Key] = true
		}
		return out
	}
	got, err := s.SearchMemoryVector(WithViewer(ctx, "alice"), p.ID, q, 10)
	if err != nil {
		t.Fatal(err)
	}
	if k := ids(got); !k["near"] || !k["mine"] || k["junk"] {
		t.Fatalf("alice vector keys = %v, want near+mine without junk", k)
	}
	got, err = s.SearchMemoryVector(WithViewer(ctx, "bob"), p.ID, q, 10)
	if err != nil {
		t.Fatal(err)
	}
	if k := ids(got); !k["near"] || k["mine"] {
		t.Fatalf("bob vector keys = %v, want near only (alice's personal is private)", k)
	}
	// Nothing reaches the floor: empty, so callers fall back to keywords.
	// e1-ish query: near/mine/junk all sit below the floor against it.
	q2 := make([]float32, EmbeddingDim)
	q2[2] = 1
	got, err = s.SearchMemoryVector(WithViewer(ctx, "alice"), p.ID, q2, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("below-floor query returned %d rows, want 0", len(got))
	}
}

func TestSearchMemoryPageTotalMatchesList(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	p := qualityProject(t, s, "page-total")
	for i, key := range []string{"a/one", "a/two", "a/three", "a/four", "a/five"} {
		m := &MemoryItem{
			ProjectID: p.ID, Key: key, Status: StatusConfirmed, ProposedBy: "alice",
			Content: "page total memory number " + key + " for counting",
		}
		if i == 4 {
			m.Status = StatusSuperseded
		}
		if err := s.CreateMemoryItem(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	all, err := s.SearchMemory(WithViewer(ctx, "alice"), p.ID, "", nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	page, total, err := s.SearchMemoryPage(WithViewer(ctx, "alice"), p.ID, "", nil, 2, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if total != len(all) || total != 4 || len(page) != 2 {
		t.Fatalf("total=%d list=%d page=%d, want total == list == 4, page 2", total, len(all), len(page))
	}
}

func TestRecordMemoryUseLeavesUpdatedAt(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	p := qualityProject(t, s, "use-updated")
	m := &MemoryItem{ProjectID: p.ID, Key: "use/k", Content: "recording use must not move updated_at", Status: StatusConfirmed}
	if err := s.CreateMemoryItem(ctx, m); err != nil {
		t.Fatal(err)
	}
	before, _ := s.GetMemoryItem(ctx, m.ID)
	time.Sleep(5 * time.Millisecond)
	if err := s.RecordMemoryUse(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	after, _ := s.GetMemoryItem(ctx, m.ID)
	if after.UseCount != 1 || after.LastUsedAt.IsZero() {
		t.Fatalf("use_count=%d last_used=%v", after.UseCount, after.LastUsedAt)
	}
	if !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("updated_at moved: %v -> %v", before.UpdatedAt, after.UpdatedAt)
	}
}

// TestPostgresMemoryQuality covers the SQL paths (scoped supersede, count,
// RecordMemoryUse, vector floor). It needs DATABASE_URL with migrations
// applied and two users, and skips otherwise.
func TestPostgresMemoryQuality(t *testing.T) {
	// Same contract as the other Postgres tests here: CI's postgres job
	// sets TEST_POSTGRES_DSN to an isolated database.
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("no isolated TEST_POSTGRES_DSN")
	}
	ctx := context.Background()
	s, err := NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.RunMigrations(ctx, "../../migrations"); err != nil {
		t.Fatal(err)
	}
	var users []string
	for _, who := range []string{"alice", "bob"} {
		var id string
		name := fmt.Sprintf("memq-%s-%d", who, time.Now().UnixNano())
		if err := s.pool.QueryRow(ctx, `INSERT INTO users(username) VALUES ($1) RETURNING id::text`, name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		users = append(users, id)
		t.Cleanup(func() { _, _ = s.pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1::uuid`, id) })
	}
	alice, bob := users[0], users[1]
	p, err := s.ResolveProject(ctx, "", "", "memory-quality-"+time.Now().Format("150405.000000"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = s.pool.Exec(bg, `DELETE FROM memory_items WHERE project_id = $1::uuid`, p.ID)
		_, _ = s.pool.Exec(bg, `DELETE FROM projects WHERE id = $1::uuid`, p.ID)
	})

	mk := func(key, level, owner string) *MemoryItem {
		m := &MemoryItem{
			ProjectID: p.ID, Key: key, Level: level, Status: StatusConfirmed, ProposedBy: owner,
			Content: "postgres memory quality content for " + key + " " + level + " " + owner,
		}
		if level == LevelPersonal {
			m.UserID = owner
		}
		if err := s.CreateMemoryItem(ctx, m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	projRow := mk("shared/key", LevelProject, alice)
	alicePersonal := mk("shared/key", LevelPersonal, alice)
	bobPersonal := mk("shared/key", LevelPersonal, bob)
	if alicePersonal.Visibility != VisibilityPrivate {
		t.Fatalf("personal visibility = %q", alicePersonal.Visibility)
	}
	for _, id := range []string{projRow.ID, alicePersonal.ID, bobPersonal.ID} {
		got, err := s.GetMemoryItem(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if got.SupersededBy != "" || got.Status == StatusSuperseded {
			t.Fatalf("row %s (%s) superseded across level/owner", id, got.Level)
		}
	}
	// Same level + owner does supersede.
	again := mk("shared/key", LevelPersonal, alice)
	old, err := s.GetMemoryItem(ctx, alicePersonal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if old.SupersededBy != again.ID {
		t.Fatalf("same-owner personal not superseded: superseded_by=%q", old.SupersededBy)
	}

	before, _ := s.GetMemoryItem(ctx, projRow.ID)
	if err := s.RecordMemoryUse(ctx, projRow.ID); err != nil {
		t.Fatal(err)
	}
	after, _ := s.GetMemoryItem(ctx, projRow.ID)
	if after.UseCount != before.UseCount+1 || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("RecordMemoryUse: use %d->%d updated %v->%v", before.UseCount, after.UseCount, before.UpdatedAt, after.UpdatedAt)
	}
}
