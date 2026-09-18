package store

// Regression tests for issue #131 residuals: branch aliasing, overlay
// validation, tombstone hiding, level-scoped fallback.

import (
	"context"
	"testing"
)

func TestBranchReadsReturnClones(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	pid := testProject(t, s)
	main, err := s.EnsureMainBranch(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	main.Name = "MUTATED"
	got, err := s.GetBranch(ctx, main.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != MainBranchName {
		t.Fatalf("stored row mutated through return pointer: %q", got.Name)
	}
	list, err := s.ListBranches(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	list[0].Name = "MUTATED"
	again, err := s.GetBranch(ctx, main.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.Name != MainBranchName {
		t.Fatalf("stored row mutated through list pointer: %q", again.Name)
	}
}

func TestWriteToBranchValidates(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	pid := testProject(t, s)
	main, _ := s.EnsureMainBranch(ctx, pid)
	for _, tc := range []struct {
		name string
		item *MemoryItem
	}{
		{"short content", &MemoryItem{Key: "k", Content: "too short"}},
		{"bad level", &MemoryItem{Key: "k", Content: "valid content length here!!", Level: "nope"}},
		{"bad scope", &MemoryItem{Key: "k", Content: "valid content length here!!", Scope: "nope"}},
		{"bad confidence", &MemoryItem{Key: "k", Content: "valid content length here!!", Confidence: 9}},
		{"bad embedding", &MemoryItem{Key: "k", Content: "valid content length here!!", Embedding: make([]float32, 7)}},
	} {
		if err := s.WriteToBranch(ctx, main.ID, tc.item); err == nil {
			t.Errorf("%s: WriteToBranch should reject", tc.name)
		}
	}
	// Tombstone marker content passes (merge deletions must write).
	if err := s.WriteToBranch(ctx, main.ID, &MemoryItem{Key: "gone", Content: TombstoneContent, Status: StatusSuperseded}); err != nil {
		t.Fatalf("tombstone write: %v", err)
	}
}

func TestTombstoneHidesKey(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	pid := testProject(t, s)
	main, _ := s.EnsureMainBranch(ctx, pid)
	child, err := s.ForkBranch(ctx, main.ID, "exp", "u", "private", 0)
	if err != nil {
		t.Fatal(err)
	}
	// Parent holds the key; child tombstones it: reads must hide it.
	if err := s.WriteToBranch(ctx, main.ID, &MemoryItem{Key: "cfg", Content: "parent version of the config!!", Status: StatusConfirmed}); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteToBranch(ctx, child.ID, &MemoryItem{Key: "cfg", Content: TombstoneContent, Status: StatusSuperseded}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveRead(ctx, child.ID, "cfg"); err != ErrNotFound {
		t.Fatalf("tombstoned key should hide, got err=%v", err)
	}
	// Parent still reads its own version.
	got, err := s.ResolveRead(ctx, main.ID, "cfg")
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != "parent version of the config!!" {
		t.Fatalf("parent read = %q", got.Content)
	}
}
