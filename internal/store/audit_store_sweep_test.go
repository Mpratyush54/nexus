package store

// Scheduled-sweep tests (issue #119 box 4): ConfirmDue tiers, SweepLifecycle
// report, and per-project branch auto-archive. All against MemStore (the
// Postgres variants share SQL builders covered by migration tests).

import (
	"context"
	"testing"
	"time"
)

func backdateMemory(s *MemStore, id string, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m, ok := s.memories[id]; ok {
		m.CreatedAt = at
	}
}

func mkProposed(t *testing.T, ctx context.Context, s *MemStore, projectID, key, source string) *MemoryItem {
	t.Helper()
	it := &MemoryItem{
		ProjectID: projectID, Key: key, Level: LevelProject,
		Content: "sweep fixture content that clears the twenty rune floor",
		Source:  source,
	}
	if err := s.CreateMemoryItem(ctx, it); err != nil {
		t.Fatal(err)
	}
	return it
}

func TestAuditMemConfirmDueTiers(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	p, err := s.ResolveProject(ctx, "", "", "sweepconf")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	// Default tier (24h): 25h old is due, 1h old is not.
	due := mkProposed(t, ctx, s, p.ID, "k/due", "processor")
	backdateMemory(s, due.ID, now.Add(-25*time.Hour))
	fresh := mkProposed(t, ctx, s, p.ID, "k/fresh", "processor")
	backdateMemory(s, fresh.ID, now.Add(-time.Hour))
	// Fast tier (1h): 2h old is due.
	fast := mkProposed(t, ctx, s, p.ID, "k/fast", FormatConfirmSource(time.Hour))
	backdateMemory(s, fast.ID, now.Add(-2*time.Hour))
	// Slow explicit tier (48h): 25h old is not due.
	slow := mkProposed(t, ctx, s, p.ID, "k/slow", FormatConfirmSource(48*time.Hour))
	backdateMemory(s, slow.ID, now.Add(-25*time.Hour))

	n, err := s.ConfirmDue(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("confirmed = %d, want 2 (due + fast)", n)
	}
	for id, want := range map[string]string{
		due.ID: StatusConfirmed, fast.ID: StatusConfirmed,
		fresh.ID: StatusProposed, slow.ID: StatusProposed,
	} {
		got, err := s.GetMemoryItem(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != want {
			t.Errorf("%s status = %s, want %s", id, got.Status, want)
		}
	}
	// Idempotent: second sweep confirms nothing.
	if n, _ := s.ConfirmDue(ctx, now); n != 0 {
		t.Errorf("second sweep confirmed %d, want 0", n)
	}
}

func TestAuditSweepLifecycle(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	p, err := s.ResolveProject(ctx, "", "", "sweepfull")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	due := mkProposed(t, ctx, s, p.ID, "k/due", "processor")
	backdateMemory(s, due.ID, now.Add(-25*time.Hour))
	// Cold CONFIRMED row: low confidence, never used → stale candidate.
	cold := &MemoryItem{
		ProjectID: p.ID, Key: "k/cold", Level: LevelProject,
		Content: "cold fixture content that clears the twenty rune floor",
		Status:  StatusConfirmed, Confidence: 0.1,
	}
	if err := s.CreateMemoryItem(ctx, cold); err != nil {
		t.Fatal(err)
	}

	rep, err := SweepLifecycle(ctx, now, s, s, s)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Confirmed != 1 {
		t.Errorf("Confirmed = %d, want 1", rep.Confirmed)
	}
	if rep.StaleCandidates != 1 {
		t.Errorf("StaleCandidates = %d, want 1 (the cold row)", rep.StaleCandidates)
	}
	// Nil seams are skipped, never fatal.
	rep, err = SweepLifecycle(ctx, now, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Confirmed != 0 || rep.ExpiredSessions != 0 || rep.StaleCandidates != 0 {
		t.Errorf("nil-seam sweep = %+v, want zeros", rep)
	}
}

func TestAuditSweepBranchesForProject(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	p, err := s.ResolveProject(ctx, "", "", "sweepbr")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	// Old branch (31d) → archived; fresh branch → kept.
	old, err := s.CreateBranch(ctx, p.ID, "stale-feature", "u1", "private")
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := s.CreateBranch(ctx, p.ID, "active-feature", "u1", "private")
	if err != nil {
		t.Fatal(err)
	}
	b := branchBucket(s)
	b.mu.Lock()
	b.branches[old.ID].CreatedAt = now.Add(-31 * 24 * time.Hour)
	b.mu.Unlock()

	n, err := SweepBranchesForProject(ctx, now, p.ID, s)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("archived = %d, want 1", n)
	}
	got, err := s.GetBranch(ctx, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsArchived() {
		t.Error("31-day branch should be archived")
	}
	got, err = s.GetBranch(ctx, fresh.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.IsArchived() {
		t.Error("fresh branch must stay active")
	}
	// Empty project / nil sweeper are no-ops.
	if n, err := SweepBranchesForProject(ctx, now, "", s); err != nil || n != 0 {
		t.Errorf("empty project = (%d, %v), want (0, nil)", n, err)
	}
	if n, err := SweepBranchesForProject(ctx, now, p.ID, nil); err != nil || n != 0 {
		t.Errorf("nil sweeper = (%d, %v), want (0, nil)", n, err)
	}
}
