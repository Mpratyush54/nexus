package store

// Branches CoW tests (nexus issue #17, plan §5.2):
//   - Forking copies zero rows (branch row only, parent pointer set).
//   - Diverged child reads resolve child-first, then parent, then main.
//   - Writes to a child leave parent/main (and siblings) unchanged.
//   - Private branches deny non-owners; shared branches allow anyone.

import (
	"context"
	"testing"
)

func testProject(t *testing.T, s *MemStore) string {
	t.Helper()
	p, err := s.ResolveProject(context.Background(), "", "", "proj-branches")
	if err != nil {
		t.Fatal(err)
	}
	return p.ID
}

func TestBranchForkCopiesZeroRows(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	pid := testProject(t, s)

	main, err := s.EnsureMainBranch(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	if main.Name != MainBranchName || main.Visibility != BranchVisibilityShared {
		t.Fatalf("main = %+v, want name=main visibility=shared", main)
	}
	// EnsureMainBranch is idempotent: same row back.
	again, err := s.EnsureMainBranch(ctx, pid)
	if err != nil || again.ID != main.ID {
		t.Fatalf("EnsureMainBranch not idempotent: %v / %v vs %v", again, err, main.ID)
	}

	// Seed two main-line memories, then count rows.
	if err := s.CreateMemoryItem(ctx, &MemoryItem{ProjectID: pid, Key: "a", Content: "01234567890123456789+ a"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateMemoryItem(ctx, &MemoryItem{ProjectID: pid, Key: "b", Content: "01234567890123456789+ b"}); err != nil {
		t.Fatal(err)
	}
	s.mu.RLock()
	before := len(s.memories)
	s.mu.RUnlock()

	child, err := s.ForkBranch(ctx, main.ID, "bob-exp", "bob", "private", 0)
	if err != nil {
		t.Fatal(err)
	}
	if child.ParentBranchID != main.ID {
		t.Fatalf("child parent = %q, want %q", child.ParentBranchID, main.ID)
	}

	s.mu.RLock()
	after := len(s.memories)
	s.mu.RUnlock()
	if after != before {
		t.Fatalf("fork copied rows: memories %d -> %d, want unchanged", before, after)
	}
}

func TestBranchCoWResolution(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	pid := testProject(t, s)

	if err := s.CreateMemoryItem(ctx, &MemoryItem{
		ProjectID: pid, Key: "db/cache", Content: "01234567890123456789: team uses Redis",
		Status: "CONFIRMED",
	}); err != nil {
		t.Fatal(err)
	}

	child, err := s.CreateBranch(ctx, pid, "bob-exp", "bob", "private")
	if err != nil {
		t.Fatal(err)
	}

	// Unmodified key resolves through the child to main.
	got, err := s.ResolveRead(ctx, child.ID, "db/cache")
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != "01234567890123456789: team uses Redis" {
		t.Fatalf("read-through = %q", got.Content)
	}

	// Diverge on the child: child sees its own value, main is untouched.
	if err := s.WriteToBranch(ctx, child.ID, &MemoryItem{
		Key: "db/cache", Content: "01234567890123456789: child uses Memcached",
		Status: "CONFIRMED",
	}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.ResolveRead(ctx, child.ID, "db/cache")
	if got.Content != "01234567890123456789: child uses Memcached" {
		t.Fatalf("child read = %q, want diverged value", got.Content)
	}
	main, _ := s.EnsureMainBranch(ctx, pid)
	got, _ = s.ResolveRead(ctx, main.ID, "db/cache")
	if got.Content != "01234567890123456789: team uses Redis" {
		t.Fatalf("main read = %q, want original value", got.Content)
	}

	// Child-only key is invisible from main.
	if err := s.WriteToBranch(ctx, child.ID, &MemoryItem{Key: "exp/note", Content: "01234567890123456789: scratch idea here"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveRead(ctx, main.ID, "exp/note"); err != ErrNotFound {
		t.Fatalf("main sees child-only key: err = %v, want ErrNotFound", err)
	}
	if _, err := s.ResolveRead(ctx, child.ID, "exp/note"); err != nil {
		t.Fatalf("child cannot read own key: %v", err)
	}
}

func TestBranchWriteIsolation(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	pid := testProject(t, s)

	a, err := s.CreateBranch(ctx, pid, "alice-exp", "alice", "private")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBranch(ctx, pid, "bob-exp", "bob", "private")
	if err != nil {
		t.Fatal(err)
	}

	if err := s.WriteToBranch(ctx, a.ID, &MemoryItem{Key: "k", Content: "01234567890123456789: alice value"}); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteToBranch(ctx, b.ID, &MemoryItem{Key: "k", Content: "01234567890123456789: bob value!!!"}); err != nil {
		t.Fatal(err)
	}

	ga, _ := s.ResolveRead(ctx, a.ID, "k")
	gb, _ := s.ResolveRead(ctx, b.ID, "k")
	if ga.Content == gb.Content {
		t.Fatalf("siblings share value %q, want isolation", ga.Content)
	}
	main, _ := s.EnsureMainBranch(ctx, pid)
	if _, err := s.ResolveRead(ctx, main.ID, "k"); err != ErrNotFound {
		t.Fatalf("main sees branch key: err = %v, want ErrNotFound", err)
	}

	// Duplicate fork name on the same project conflicts.
	if _, err := s.CreateBranch(ctx, pid, "alice-exp", "alice", "private"); err != ErrConflict {
		t.Fatalf("dup name err = %v, want ErrConflict", err)
	}
}

func TestBranchVisibility(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	pid := testProject(t, s)

	priv, err := s.CreateBranch(ctx, pid, "priv", "alice", "private")
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckBranchAccess(priv, "alice"); err != nil {
		t.Fatalf("owner denied on private branch: %v", err)
	}
	if err := CheckBranchAccess(priv, "bob"); err != ErrForbidden {
		t.Fatalf("non-owner on private branch: err = %v, want ErrForbidden", err)
	}
	if err := CheckBranchAccess(priv, ""); err != nil {
		t.Fatalf("system denied on private branch: %v", err)
	}

	shared, err := s.CreateBranch(ctx, pid, "shared-exp", "alice", "shared")
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckBranchAccess(shared, "bob"); err != nil {
		t.Fatalf("anyone denied on shared branch: %v", err)
	}
	if err := CheckBranchAccess(nil, "bob"); err != ErrNotFound {
		t.Fatalf("nil branch err = %v, want ErrNotFound", err)
	}

	// Visibility is listed per project.
	branches, err := s.ListBranches(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	if len(branches) != 3 { // main + priv + shared-exp
		t.Fatalf("branches = %d, want 3 (main + 2)", len(branches))
	}
}

func TestBranchMaxDepth(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	pid := testProject(t, s)

	parent, err := s.EnsureMainBranch(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	// main is level 1: four more forks reach the 5-level cap.
	for i := 0; i < MaxBranchDepth-1; i++ {
		child, err := s.ForkBranch(ctx, parent.ID, string(rune('a'+i))+"-lvl", "u", "private", 0)
		if err != nil {
			t.Fatalf("level %d fork: %v", i+2, err)
		}
		parent = child
	}
	if _, err := s.ForkBranch(ctx, parent.ID, "too-deep", "u", "private", 0); err == nil {
		t.Fatal("fork past max depth succeeded, want error")
	}
}
