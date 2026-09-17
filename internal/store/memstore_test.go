package store

// Unit tests for the MemStore foundation (issues #1/#2/#9):
//   - NormalizeGitURL canonicalization
//   - ResolveProject priority: canonical_url > root_commit > folder_name
//   - Heartbeat liveness + OfflineThreshold expiry
//   - AppendEvent/ListEvents ordering + Subscribe fan-out

import (
	"context"
	"testing"
	"time"
)

func TestNormalizeGitURL(t *testing.T) {
	cases := map[string]string{
		"git@github.com:Mpratyush54/nexus.git":     "github.com/mpratyush54/nexus",
		"https://github.com/Mpratyush54/nexus.git": "github.com/mpratyush54/nexus",
		"https://github.com/Mpratyush54/nexus":     "github.com/mpratyush54/nexus",
		"http://github.com/x/y.git":                "github.com/x/y",
		"github.com/x/y":                           "github.com/x/y",
		"ssh://git@github.com/x/y.git":             "github.com/x/y",
		"  git@github.com:x/y.git  ":               "github.com/x/y",
		"":                                         "",
	}
	for raw, want := range cases {
		if got := NormalizeGitURL(raw); got != want {
			t.Errorf("NormalizeGitURL(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestResolveProjectPriority(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()

	// Seed: URL U1 + root R1 + folder F1.
	a, err := s.ResolveProject(ctx, "git@github.com:org/proj.git", "R1", "F1")
	if err != nil {
		t.Fatal(err)
	}

	// URL wins even when root + folder point elsewhere.
	got, _ := s.ResolveProject(ctx, "https://github.com/org/proj.git", "OTHER-ROOT", "other-folder")
	if got.ID != a.ID {
		t.Errorf("URL match: got %s, want %s", got.ID, a.ID)
	}

	// Root wins over folder: same root, different URL/folder.
	got, _ = s.ResolveProject(ctx, "", "R1", "other-folder")
	if got.ID != a.ID {
		t.Errorf("root match: got %s, want %s", got.ID, a.ID)
	}

	// URL beats a competing root claim: project B shares root R2 with C,
	// but an explicit URL match must return the URL owner.
	b, _ := s.ResolveProject(ctx, "git@github.com:org/b.git", "R2", "Fb")
	c, _ := s.ResolveProject(ctx, "git@github.com:org/c.git", "R2", "Fc")
	_ = c // shares root R2 with b; first registrant owns the root fallback
	got, _ = s.ResolveProject(ctx, "https://github.com/org/b", "R2", "Fc")
	if got.ID != b.ID {
		t.Errorf("URL-over-root: got %s (%s), want %s", got.ID, got.FolderName, b.ID)
	}

	// Folder fallback: no URL/root -> match on folder name.
	d, _ := s.ResolveProject(ctx, "", "", "lonely-folder")
	got, _ = s.ResolveProject(ctx, "", "", "lonely-folder")
	if got.ID != d.ID {
		t.Errorf("folder match: got %s, want %s", got.ID, d.ID)
	}

	// Nothing matches -> new project with a distinct ID.
	e, _ := s.ResolveProject(ctx, "git@github.com:org/brand-new.git", "BRAND-NEW", "brand-new")
	for _, p := range []*Project{a, b, d} {
		if e.ID == p.ID {
			t.Errorf("new identity collided with %s", p.ID)
		}
	}
}

func TestHeartbeatOfflineThreshold(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()

	proj, _ := s.ResolveProject(ctx, "", "", "hb-proj")
	ws := &Workspace{ProjectID: proj.ID, UserID: "u1", MachineID: "m1", Path: `D:\w`}
	if err := s.RegisterWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}

	// Fresh heartbeat -> active.
	if err := s.Heartbeat(ctx, ws.ID, "main", "abc123", false); err != nil {
		t.Fatal(err)
	}
	active, err := s.GetActiveWorkspace(ctx, proj.ID)
	if err != nil {
		t.Fatalf("expected active workspace, got %v", err)
	}
	if active.Branch != "main" || active.CommitSHA != "abc123" {
		t.Errorf("heartbeat did not persist git state: %+v", active)
	}
	if !active.IsOnline {
		t.Error("heartbeat should mark workspace online")
	}

	// Silence past the threshold -> offline (ErrNotFound).
	s.mu.Lock()
	s.workspaces[ws.ID].LastSeen = time.Now().UTC().Add(-OfflineThreshold - time.Second)
	s.mu.Unlock()
	if _, err := s.GetActiveWorkspace(ctx, proj.ID); err != ErrNotFound {
		t.Errorf("stale workspace: got %v, want ErrNotFound", err)
	}

	// A new heartbeat revives it.
	if err := s.Heartbeat(ctx, ws.ID, "dev", "def456", true); err != nil {
		t.Fatal(err)
	}
	active, err = s.GetActiveWorkspace(ctx, proj.ID)
	if err != nil {
		t.Fatalf("revived workspace should be active: %v", err)
	}
	if active.Branch != "dev" || !active.IsDirty {
		t.Errorf("revive did not update state: %+v", active)
	}

	// Unknown workspace -> ErrNotFound.
	if err := s.Heartbeat(ctx, "ws_missing", "main", "x", false); err != ErrNotFound {
		t.Errorf("unknown heartbeat: got %v, want ErrNotFound", err)
	}
}

func TestAppendListEvents(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()

	for i, typ := range []string{"SESSION_STARTED", "FILE_MODIFIED", "COMMAND_EXECUTED"} {
		if err := s.AppendEvent(ctx, &Event{ProjectID: "p1", EventType: typ,
			Payload: map[string]any{"i": i}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AppendEvent(ctx, &Event{ProjectID: "other", EventType: "X"}); err != nil {
		t.Fatal(err)
	}

	all, err := s.ListEvents(ctx, "p1", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("got %d events, want 3", len(all))
	}
	for i := 1; i < len(all); i++ {
		if all[i].ID <= all[i-1].ID {
			t.Fatalf("events out of order: %+v", all)
		}
	}

	// sinceID filters, limit caps.
	part, _ := s.ListEvents(ctx, "p1", all[0].ID, 1)
	if len(part) != 1 || part[0].ID != all[1].ID {
		t.Errorf("since/limit wrong: %+v", part)
	}

	// Other projects are isolated.
	other, _ := s.ListEvents(ctx, "other", 0, 0)
	if len(other) != 1 {
		t.Errorf("project isolation broken: %+v", other)
	}
}

func TestSubscribeReceivesAppends(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := NewMemStore()

	ch, unsub, err := s.Subscribe(ctx, "p1")
	if err != nil {
		t.Fatal(err)
	}
	defer unsub()

	if err := s.AppendEvent(ctx, &Event{ProjectID: "p1", EventType: "MEMORY_PROPOSED"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendEvent(ctx, &Event{ProjectID: "p2", EventType: "NOISE"}); err != nil {
		t.Fatal(err)
	}

	select {
	case ev := <-ch:
		if ev.EventType != "MEMORY_PROPOSED" {
			t.Errorf("got %s, want MEMORY_PROPOSED", ev.EventType)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for subscribed event")
	}

	// Unsubscribe closes the channel.
	unsub()
	if _, ok := <-ch; ok {
		t.Error("expected closed channel after unsubscribe")
	}
}
