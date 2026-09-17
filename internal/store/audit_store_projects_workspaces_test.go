package store

// Audit: ResolveProject priority/ambiguity + workspace registration and
// liveness boundary (MemStore).
//
// Postgres folder fallback is deterministic (ORDER BY created_at ASC LIMIT
// 1: first-registered wins). MemStore iterates a Go map — ambiguous folder
// matches resolve nondeterministically. Postgres RegisterWorkspace upserts
// on (machine_id, path); MemStore always inserts.

import (
	"context"
	"testing"
	"time"
)

func TestAuditResolveProjectURLNormalization(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	a, err := s.ResolveProject(ctx, "git@github.com:org/proj.git", "R1", "F1")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.ResolveProject(ctx, "https://github.com/org/proj", "OTHER", "other")
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != b.ID {
		t.Errorf("URL variants should converge, got %s vs %s", a.ID, b.ID)
	}
	if got := NormalizeGitURL("git@github.com:org/proj.git"); got != "github.com/org/proj" {
		t.Errorf("NormalizeGitURL = %q", got)
	}
}

func TestAuditResolveProjectRootBeatsFolder(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	a, err := s.ResolveProject(ctx, "git@github.com:org/a.git", "R-ROOT", "Fa")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.ResolveProject(ctx, "", "R-ROOT", "unrelated-folder")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != a.ID {
		t.Errorf("root_commit should beat folder_name, got %s want %s", got.ID, a.ID)
	}
}

// BUG(#102): two projects share folder "dup"; Postgres returns the
// first-registered deterministically, MemStore returns whichever the map
// yields first — nondeterministic across calls. Regression documents the
// current MemStore property weakly but deterministically: both projects
// exist and resolution returns one of them.
func TestAuditResolveProjectFolderAmbiguity(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	first, err := s.ResolveProject(ctx, "git@github.com:org/one.git", "R-AAA-1", "dup-folder")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.ResolveProject(ctx, "git@github.com:org/two.git", "R-BBB-2", "other-folder")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID {
		t.Fatal("setup: distinct identities should create distinct projects")
	}
	// Build the ambiguous state (two rows, one folder): reachable in
	// production via legacy rows / direct writes, since ResolveProject
	// itself folder-matches and would never create the second row.
	second.FolderName = "dup-folder"
	seen := map[string]int{}
	for i := 0; i < 50; i++ {
		got, err := s.ResolveProject(ctx, "", "", "dup-folder")
		if err != nil {
			t.Fatal(err)
		}
		if got.ID != first.ID && got.ID != second.ID {
			t.Fatalf("ambiguous folder resolved to unknown project %s", got.ID)
		}
		seen[got.ID]++
	}
	if len(seen) == 0 {
		t.Fatal("expected at least one resolution")
	}
	// Deterministic weak property: resolution returns one of the two owners.
	if _, ok := seen[first.ID]; !ok {
		t.Logf("note: first-registered %s never won over 50 calls (Postgres parity is first-registered-wins)", first.ID)
	}
	if _, ok := seen[second.ID]; !ok {
		t.Logf("note: second project %s never won over 50 calls", second.ID)
	}
}

func TestAuditGetProjectNotFound(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	if _, err := s.GetProject(ctx, "proj_missing"); err != ErrNotFound {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}

// BUG(#110): GetProject/ResolveProject return internal pointers (aliasing).
// Regression documents current MemStore behavior (no defensive copy).
func TestAuditGetProjectAliasing(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	p, err := s.ResolveProject(ctx, "git@github.com:org/a.git", "R1", "folder-a")
	if err != nil {
		t.Fatal(err)
	}
	p.FolderName = "mutated"
	again, err := s.GetProject(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.FolderName != "mutated" {
		t.Errorf("mutating a ResolveProject result should mutate the store (no copy), got %q", again.FolderName)
	}
}

// BUG(#110): re-registering the same (machine_id, path) must upsert
// (Postgres ON CONFLICT); MemStore inserts a duplicate row with a new ID.
// Regression documents current MemStore behavior.
func TestAuditRegisterWorkspaceUpsertDivergence(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	proj, err := s.ResolveProject(ctx, "", "", "ws-proj")
	if err != nil {
		t.Fatal(err)
	}
	mk := func() *Workspace {
		return &Workspace{ProjectID: proj.ID, UserID: "u1", MachineID: "m1", Path: "/repo"}
	}
	ws1 := mk()
	if err := s.RegisterWorkspace(ctx, ws1); err != nil {
		t.Fatal(err)
	}
	ws2 := mk()
	if err := s.RegisterWorkspace(ctx, ws2); err != nil {
		t.Fatal(err)
	}
	if ws1.ID == ws2.ID {
		t.Errorf("MemStore mints a new ID per RegisterWorkspace (%s); Postgres upserts one row", ws1.ID)
	}
	s.mu.RLock()
	n := len(s.workspaces)
	s.mu.RUnlock()
	if n != 2 {
		t.Errorf("MemStore keeps duplicate workspace rows for one (machine_id,path): %d rows, want 2", n)
	}
}

func TestAuditHeartbeatUnknown(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	if err := s.Heartbeat(ctx, "ws_missing", "main", "x", false); err != ErrNotFound {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}

func TestAuditHeartbeatRevives(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	proj, _ := s.ResolveProject(ctx, "", "", "revive-proj")
	ws := &Workspace{ProjectID: proj.ID, UserID: "u1", MachineID: "m1", Path: "/r"}
	if err := s.RegisterWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}
	if err := s.Heartbeat(ctx, ws.ID, "dev", "sha1", true); err != nil {
		t.Fatal(err)
	}
	active, err := s.GetActiveWorkspace(ctx, proj.ID)
	if err != nil {
		t.Fatal(err)
	}
	if active.Branch != "dev" || !active.IsDirty {
		t.Errorf("heartbeat state not persisted: %+v", active)
	}
}

// BUG(#87): the header contract says "silence == 90s is still online" and
// IsOnlineAt implements an inclusive boundary, but GetActiveWorkspace uses
// a strict `After(threshold)` predicate — a workspace silent exactly 90s
// is reported offline. Regression documents current MemStore behavior
// (strict boundary, consistent with the Postgres SQL predicate).
func TestAuditActiveWorkspaceExactBoundary(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	proj, _ := s.ResolveProject(ctx, "", "", "boundary-proj")
	ws := &Workspace{ProjectID: proj.ID, UserID: "u1", MachineID: "m1", Path: "/r"}
	if err := s.RegisterWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.workspaces[ws.ID].LastSeen = time.Now().UTC().Add(-OfflineThreshold)
	s.mu.Unlock()
	if _, err := s.GetActiveWorkspace(ctx, proj.ID); err != ErrNotFound {
		t.Errorf("workspace silent exactly 90s should read as offline under the strict After predicate, got %v", err)
	}
}

func TestAuditIsOnlineAtExactBoundary(t *testing.T) {
	base := time.Now().UTC()
	if !IsOnlineAt(base, base.Add(OfflineThreshold)) {
		t.Error("exactly 90s silence should still count as online")
	}
	if IsOnlineAt(base, base.Add(OfflineThreshold).Add(time.Nanosecond)) {
		t.Error("90s+1ns silence should count as offline")
	}
	if IsStaleAt(base, base.Add(OfflineThreshold)) {
		t.Error("IsStaleAt must negate IsOnlineAt at the boundary")
	}
	if got := ExpiryAt(base); !got.Equal(base.Add(OfflineThreshold)) {
		t.Errorf("ExpiryAt = %v", got)
	}
}

func TestAuditFilterOnlineDropsZeroTime(t *testing.T) {
	now := time.Now().UTC()
	ws := []Workspace{
		{ID: "fresh", LastSeen: now},
		{ID: "zero"},
		{ID: "stale", LastSeen: now.Add(-OfflineThreshold - time.Second)},
	}
	out := FilterOnline(ws, now)
	if len(out) != 1 || out[0].ID != "fresh" {
		t.Errorf("FilterOnline = %+v, want only fresh", out)
	}
}

func TestAuditOfflineThresholdValue(t *testing.T) {
	if OfflineThreshold != 90*time.Second {
		t.Errorf("OfflineThreshold = %v, want 90s", OfflineThreshold)
	}
}
