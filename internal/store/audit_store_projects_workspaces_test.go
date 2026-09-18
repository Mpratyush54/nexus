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
	"errors"
	"sync"
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

// FIXED(#110, was BUG(#102)): ambiguous folders resolve deterministically —
// first-registered wins (earliest CreatedAt), matching the Postgres
// ORDER BY created_at ASC LIMIT 1 contract.
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
	s.mu.Lock()
	secondRow := s.projects[second.ID]
	secondRow.FolderName = "dup-folder"
	// Keep the age order unambiguous: first-registered stays oldest.
	secondRow.CreatedAt = first.CreatedAt.Add(time.Second)
	s.mu.Unlock()
	for i := 0; i < 50; i++ {
		got, err := s.ResolveProject(ctx, "", "", "dup-folder")
		if err != nil {
			t.Fatal(err)
		}
		if got.ID != first.ID {
			t.Fatalf("call %d: ambiguous folder resolved to %s, want first-registered %s", i, got.ID, first.ID)
		}
	}
}

func TestAuditGetProjectNotFound(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	if _, err := s.GetProject(ctx, "proj_missing"); err != ErrNotFound {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}

// FIXED(#110): GetProject/ResolveProject return defensive copies — caller
// mutations no longer corrupt the store.
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
	if again.FolderName == "mutated" {
		t.Error("store corrupted via ResolveProject aliasing")
	}
}

// FIXED(#110 + #87): re-registering the same (machine_id, path) upserts
// like Postgres ON CONFLICT — one row, same ID, only liveness/git fields
// refreshed. Identity (project/user) is preserved, not rebound.
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
	ws2.Branch = "dev"
	if err := s.RegisterWorkspace(ctx, ws2); err != nil {
		t.Fatal(err)
	}
	if ws1.ID != ws2.ID {
		t.Errorf("upsert must reuse the row ID: got %s vs %s", ws1.ID, ws2.ID)
	}
	s.mu.RLock()
	n := len(s.workspaces)
	stored := s.workspaces[ws1.ID]
	s.mu.RUnlock()
	if n != 1 {
		t.Errorf("upsert must keep one row per (machine_id,path): %d rows", n)
	}
	if stored.Branch != "dev" {
		t.Errorf("upsert must refresh git state, branch = %q", stored.Branch)
	}
	// Identity is sticky: a re-registration naming another project does
	// not rebind the row (issue #87).
	other, err := s.ResolveProject(ctx, "", "", "ws-proj-other")
	if err != nil {
		t.Fatal(err)
	}
	hijack := &Workspace{ProjectID: other.ID, UserID: "u2", MachineID: "m1", Path: "/repo"}
	if err := s.RegisterWorkspace(ctx, hijack); err != nil {
		t.Fatal(err)
	}
	if hijack.ID != ws1.ID {
		t.Errorf("same (machine_id,path) must map to the same row, got %s", hijack.ID)
	}
	s.mu.RLock()
	kept := s.workspaces[ws1.ID]
	s.mu.RUnlock()
	if kept.ProjectID != proj.ID || kept.UserID != "u1" {
		t.Errorf("upsert rebound identity to %s/%s", kept.ProjectID, kept.UserID)
	}
	// Unknown projects fail instead of creating orphan workspaces.
	ghost := &Workspace{ProjectID: "proj_missing", UserID: "u1", MachineID: "m9", Path: "/ghost"}
	if err := s.RegisterWorkspace(ctx, ghost); !errors.Is(err, ErrNotFound) {
		t.Errorf("orphan workspace: got %v, want ErrNotFound", err)
	}
}

// FIXED(#86): concurrent first registration of one identity converges on a
// single project row — every goroutine gets the same ID.
func TestAuditResolveProjectConcurrentFirstRegistration(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	const n = 32
	ids := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p, err := s.ResolveProject(ctx, "git@github.com:org/race.git", "R-RACE", "race-folder")
			if err != nil {
				errs[i] = err
				return
			}
			ids[i] = p.ID
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}
	for i := 1; i < n; i++ {
		if ids[i] != ids[0] {
			t.Fatalf("divergent IDs: %s vs %s", ids[0], ids[i])
		}
	}
	s.mu.RLock()
	nrows := len(s.projects)
	s.mu.RUnlock()
	if nrows != 1 {
		t.Fatalf("concurrent first registration created %d rows, want 1", nrows)
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

// FIXED (#131): GetActiveWorkspace now uses the inclusive boundary —
// silence == 90s is still online, matching IsOnlineAt/IsStaleAt and the
// Postgres >= predicate. The strict-After behavior this test used to pin
// is gone on both backends.
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
	if _, err := s.GetActiveWorkspace(ctx, proj.ID); err != nil {
		t.Errorf("workspace silent exactly 90s should read as online (inclusive boundary), got %v", err)
	}
	s.mu.Lock()
	s.workspaces[ws.ID].LastSeen = time.Now().UTC().Add(-OfflineThreshold - time.Second)
	s.mu.Unlock()
	if _, err := s.GetActiveWorkspace(ctx, proj.ID); err != ErrNotFound {
		t.Errorf("workspace silent 91s should read as offline, got %v", err)
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
