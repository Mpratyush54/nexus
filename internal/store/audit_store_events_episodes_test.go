package store

// Audit: events fan-out/drop contract + episode validation and status
// machine + level/session-NULL invariant (MemStore).
//
// Migration 001: episode_type CHECK includes onboarding
// (bug_fix|feature|refactor|incident|investigation|onboarding); episode
// status CHECK is OPEN|INVESTIGATING|RESOLVED|WONT_FIX. MemStore validates
// neither, and ResolveEpisode unconditionally overwrites to RESOLVED.
// Migration 003 intent: session memories are level='session' WITH session_id
// set; project/org rows carry session_id NULL.

import (
	"context"
	"testing"
	"time"
)

func TestAuditAppendEventIDsMonotonic(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	var prev int64
	for _, typ := range []string{"A", "B", "C"} {
		ev := &Event{ProjectID: "p1", EventType: typ}
		if err := s.AppendEvent(ctx, ev); err != nil {
			t.Fatal(err)
		}
		if ev.ID <= prev {
			t.Fatalf("event IDs not monotonic: %d after %d", ev.ID, prev)
		}
		if ev.CreatedAt.IsZero() {
			t.Error("CreatedAt should be stamped")
		}
		prev = ev.ID
	}
}

func TestAuditAppendEventSessionEmptyRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	// "" is the MemStore encoding of SQL NULL session_id (cf. nullText).
	if err := s.AppendEvent(ctx, &Event{ProjectID: "p1", EventType: "X"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendEvent(ctx, &Event{ProjectID: "p1", SessionID: "sess-1", EventType: "Y"}); err != nil {
		t.Fatal(err)
	}
	res, err := s.ListEvents(ctx, "p1", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 {
		t.Fatalf("got %d events, want 2", len(res))
	}
	if res[0].SessionID != "" || res[1].SessionID != "sess-1" {
		t.Errorf("session NULL handling broken: %+v", res)
	}
}

// BUG(#110): MemStore preserves nil Payload; Postgres marshals nil to
// '{}' (column NOT NULL). Regression documents the current MemStore
// behavior (nil round-trips as nil).
func TestAuditAppendEventNilPayloadDivergence(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	if err := s.AppendEvent(ctx, &Event{ProjectID: "p1", EventType: "X"}); err != nil {
		t.Fatal(err)
	}
	res, err := s.ListEvents(ctx, "p1", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Payload != nil {
		t.Errorf("MemStore should preserve nil Payload, got %+v", res[0].Payload)
	}
}

func TestAuditSubscribeFanOutDropContract(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := NewMemStore()
	ch, unsub, err := s.Subscribe(ctx, "p1")
	if err != nil {
		t.Fatal(err)
	}
	defer unsub()
	// No reader: if AppendEvent blocked, this deadlocks. Completion proves
	// the non-blocking send; the 64-buffer caps what the lagging reader kept.
	for i := 0; i < 200; i++ {
		if err := s.AppendEvent(ctx, &Event{ProjectID: "p1", EventType: "X"}); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(ch); n > 64 {
		t.Errorf("subscriber buffer holds %d events, want <= 64 (drop contract)", n)
	}
	all, err := s.ListEvents(ctx, "p1", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 200 {
		t.Errorf("backfill via ListEvents = %d, want all 200 appended", len(all))
	}
}

func TestAuditSubscribeProjectIsolationMulti(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := NewMemStore()
	cha, unsubA, err := s.Subscribe(ctx, "pa")
	if err != nil {
		t.Fatal(err)
	}
	defer unsubA()
	chb, unsubB, err := s.Subscribe(ctx, "pb")
	if err != nil {
		t.Fatal(err)
	}
	defer unsubB()
	if err := s.AppendEvent(ctx, &Event{ProjectID: "pa", EventType: "FOR-A"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendEvent(ctx, &Event{ProjectID: "pb", EventType: "FOR-B"}); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-cha:
		if ev.EventType != "FOR-A" {
			t.Errorf("sub A got %q", ev.EventType)
		}
	case <-time.After(2 * time.Second):
		t.Error("sub A timed out")
	}
	select {
	case ev := <-chb:
		if ev.EventType != "FOR-B" {
			t.Errorf("sub B got %q", ev.EventType)
		}
	case <-time.After(2 * time.Second):
		t.Error("sub B timed out")
	}
}

func TestAuditSubscribeDoubleCancelSafe(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := NewMemStore()
	ch, unsub, err := s.Subscribe(ctx, "p1")
	if err != nil {
		t.Fatal(err)
	}
	unsub()
	unsub() // sync.Once: second cancel must be a no-op, not a panic/close-of-closed
	if _, ok := <-ch; ok {
		t.Error("expected closed channel after unsubscribe")
	}
}

func TestAuditCreateEpisodeDefaults(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	ep := &Episode{ProjectID: "p1", Title: "crash on boot", EpisodeType: "bug_fix"}
	if err := s.CreateEpisode(ctx, ep); err != nil {
		t.Fatal(err)
	}
	if ep.ID == "" {
		t.Error("expected minted ID")
	}
	if ep.Status != "OPEN" {
		t.Errorf("Status = %q, want OPEN default", ep.Status)
	}
	if ep.OpenedAt.IsZero() {
		t.Error("expected OpenedAt timestamp")
	}
}

func TestAuditCreateEpisodeOnboardingAccepted(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	ep := &Episode{ProjectID: "p1", Title: "onboard flow", EpisodeType: "onboarding"}
	if err := s.CreateEpisode(ctx, ep); err != nil {
		t.Fatalf("onboarding is in the episode_type CHECK and must be accepted: %v", err)
	}
}

// BUG(#103): episode_type CHECK unenforced — garbage accepted. Regression
// documents current MemStore behavior (no validation).
func TestAuditCreateEpisodeBogusTypeAccepted(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	ep := &Episode{ProjectID: "p1", Title: "weird", EpisodeType: "teleport"}
	if err := s.CreateEpisode(ctx, ep); err != nil {
		t.Fatal(err)
	}
	if ep.EpisodeType != "teleport" {
		t.Errorf("EpisodeType = %q, want preserved %q (MemStore does no CHECK validation)", ep.EpisodeType, "teleport")
	}
}

// BUG(#103): empty episode_type violates NOT NULL/CHECK intent yet accepted.
// Regression documents current MemStore behavior (no validation).
func TestAuditCreateEpisodeEmptyTypeAccepted(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	ep := &Episode{ProjectID: "p1", Title: "typeless"}
	if err := s.CreateEpisode(ctx, ep); err != nil {
		t.Fatal(err)
	}
	if ep.EpisodeType != "" {
		t.Errorf("EpisodeType = %q, want preserved empty (MemStore does no CHECK validation)", ep.EpisodeType)
	}
}

func TestAuditGetEpisodeNotFound(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	if _, err := s.GetEpisode(ctx, "ep_missing"); err != ErrNotFound {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}

func TestAuditResolveEpisodeHappyPath(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	ep := &Episode{ProjectID: "p1", Title: "fix me", EpisodeType: "bug_fix"}
	if err := s.CreateEpisode(ctx, ep); err != nil {
		t.Fatal(err)
	}
	if err := s.ResolveEpisode(ctx, ep.ID, "patched nil deref", "tests green", "u1"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetEpisode(ctx, ep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "RESOLVED" {
		t.Errorf("Status = %q, want RESOLVED", got.Status)
	}
	if got.Resolution != "patched nil deref" || got.Verification != "tests green" || got.ResolvedBy != "u1" {
		t.Errorf("resolution fields not recorded: %+v", got)
	}
	if got.ResolvedAt.IsZero() {
		t.Error("expected ResolvedAt timestamp")
	}
}

func TestAuditResolveEpisodeNotFound(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	if err := s.ResolveEpisode(ctx, "ep_missing", "r", "v", "u"); err != ErrNotFound {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}

// BUG(#103): RESOLVED is terminal but re-resolve silently overwrites
// resolution/verification (last-write-wins, no guard). Regression documents
// current MemStore behavior.
func TestAuditResolveEpisodeTerminalGuard(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	ep := &Episode{ProjectID: "p1", Title: "fix once", EpisodeType: "bug_fix"}
	if err := s.CreateEpisode(ctx, ep); err != nil {
		t.Fatal(err)
	}
	if err := s.ResolveEpisode(ctx, ep.ID, "first fix", "v1", "u1"); err != nil {
		t.Fatal(err)
	}
	if err := s.ResolveEpisode(ctx, ep.ID, "second fix", "v2", "u2"); err != nil {
		t.Fatalf("MemStore re-resolve succeeds (last-write-wins), got err %v", err)
	}
	got, _ := s.GetEpisode(ctx, ep.ID)
	if got.Resolution != "second fix" {
		t.Errorf("re-resolve should overwrite resolution to %q (last-write-wins), got %q", "second fix", got.Resolution)
	}
}

// BUG(#103): episodes in a non-OPEN status (e.g. INVESTIGATING) resolve
// without any status-machine check; WONT_FIX is unreachable via any API.
// Regression documents current MemStore behavior (no from-state check).
func TestAuditResolveEpisodeFromInvestigating(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	ep := &Episode{ProjectID: "p1", Title: "under investigation", EpisodeType: "incident"}
	if err := s.CreateEpisode(ctx, ep); err != nil {
		t.Fatal(err)
	}
	stored, _ := s.GetEpisode(ctx, ep.ID)
	stored.Status = "INVESTIGATING"
	if err := s.ResolveEpisode(ctx, ep.ID, "r", "v", "u"); err != nil {
		t.Fatalf("MemStore ResolveEpisode ignores the status machine, got err %v", err)
	}
	got, _ := s.GetEpisode(ctx, ep.ID)
	if got.Status != "RESOLVED" {
		t.Errorf("Status = %q, want RESOLVED (MemStore overwrites unconditionally)", got.Status)
	}
}

// BUG(#102): level='session' with NULL session_id accepted — migration 003
// intent is session-scoped memories carry session_id. Regression documents
// current MemStore behavior (invariant unenforced).
func TestAuditMemoryLevelSessionNullInvariant(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	item := &MemoryItem{ProjectID: "p1", Key: "k-sess", Content: "content with enough length here", Level: "session"}
	if err := s.CreateMemoryItem(ctx, item); err != nil {
		t.Fatalf("MemStore accepts level=session with empty session_id, got err %v", err)
	}
	if item.Level != "session" || item.SessionID != "" {
		t.Errorf("stored row = level %q session %q, want session/empty (accepted as-is)", item.Level, item.SessionID)
	}
}

// BUG(#102): project-level memory carrying a session_id accepted — scope
// leak: it is invisible to ListSessionVisibleMemories inheritance (which
// requires session_id NULL) yet claims project scope. Regression documents
// current MemStore behavior (invariant unenforced).
func TestAuditMemoryLevelProjectWithSession(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	item := &MemoryItem{ProjectID: "p1", Key: "k-leak", Content: "content with enough length here", Level: "project", SessionID: "sess-1"}
	if err := s.CreateMemoryItem(ctx, item); err != nil {
		t.Fatalf("MemStore accepts level=project with session_id set, got err %v", err)
	}
	if item.Level != "project" || item.SessionID != "sess-1" {
		t.Errorf("stored row = level %q session %q, want project/sess-1 (accepted as-is)", item.Level, item.SessionID)
	}
}

func TestAuditCreateSessionMemoryRequiresSessionAndForcesLevel(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	proj, err := s.ResolveProject(ctx, "", "", "sess-mem-proj")
	if err != nil {
		t.Fatal(err)
	}
	sess := &Session{ProjectID: proj.ID, CreatedBy: "u1"}
	if err := s.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	bad := &MemoryItem{ProjectID: proj.ID, Key: "k", Content: "content with enough length here"}
	if err := s.CreateSessionMemory(ctx, bad); err == nil {
		t.Error("session memory without session_id should fail")
	}
	item := &MemoryItem{ProjectID: proj.ID, SessionID: sess.ID, Key: "k2", Content: "content with enough length here", Level: "project"}
	if err := s.CreateSessionMemory(ctx, item); err != nil {
		t.Fatal(err)
	}
	if item.Level != "session" {
		t.Errorf("Level = %q, want forced session", item.Level)
	}
}
