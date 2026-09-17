package store

// Pure + MemStore tests for branch upkeep (issue #44): archive eligibility
// and staleness flags. Postgres paths share the same predicates and SQL
// shapes; live-DB coverage runs gated on TEST_POSTGRES_DSN.

import (
	"context"
	"testing"
	"time"
)

func TestIsArchivable(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	old := now.Add(-31 * 24 * time.Hour)
	archivedAt := now.Add(-time.Hour)
	cases := []struct {
		name   string
		branch *MemoryBranch
		want   bool
	}{
		{"old non-main archivable", &MemoryBranch{ID: "b1", Name: "feat", CreatedAt: old}, true},
		{"exactly 30d is eligible (boundary inclusive)", &MemoryBranch{ID: "b2", Name: "feat", CreatedAt: now.Add(-BranchArchiveTTL)}, true},
		{"young not eligible", &MemoryBranch{ID: "b3", Name: "feat", CreatedAt: now.Add(-29 * 24 * time.Hour)}, false},
		{"main never archivable", &MemoryBranch{ID: "b4", Name: MainBranchName, CreatedAt: old}, false},
		{"already archived not re-archivable", &MemoryBranch{ID: "b5", Name: "feat", CreatedAt: old, ArchivedAt: &archivedAt}, false},
		{"nil never archivable", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsArchivable(tc.branch, now); got != tc.want {
				t.Errorf("IsArchivable = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMemStoreArchiveAndStale(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	main, err := s.EnsureMainBranch(ctx, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ArchiveBranch(ctx, main.ID, time.Now().UTC()); err == nil {
		t.Fatal("main must never archive")
	}
	old, err := s.CreateBranch(ctx, "p1", "old-feat", "u1", BranchVisibilityPrivate)
	if err != nil {
		t.Fatal(err)
	}
	// Backdate past the TTL (creation stamps now).
	old.CreatedAt = time.Now().UTC().Add(-31 * 24 * time.Hour)
	got, err := s.ArchiveBranch(ctx, old.ID, time.Now().UTC())
	if err != nil {
		t.Fatalf("ArchiveBranch: %v", err)
	}
	if !got.IsArchived() {
		t.Fatal("archived branch must report IsArchived")
	}
	if _, err := s.ArchiveBranch(ctx, old.ID, time.Now().UTC()); err == nil {
		t.Fatal("re-archive must fail")
	}
	stale, err := s.MarkStale(ctx, old.ID)
	if err != nil {
		t.Fatalf("MarkStale: %v", err)
	}
	if !stale.PotentiallyStale {
		t.Fatal("marked branch must be potentially stale")
	}
	if _, err := s.ArchiveBranch(ctx, "ghost", time.Now().UTC()); err == nil {
		t.Fatal("unknown branch archive must fail")
	}
	if _, err := s.MarkStale(ctx, "ghost"); err == nil {
		t.Fatal("unknown branch mark-stale must fail")
	}
}
