package store

// Session scoping tests (nexus issue #12):
// join/leave, isolation of sibling session memories, project+org
// inheritance with session-key override, and the 3-session promotion rule.

import (
	"context"
	"testing"
)

func mustProject(t *testing.T, s *MemStore, folder string) *Project {
	t.Helper()
	p, err := s.ResolveProject(context.Background(), "", "", folder)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func mustSession(t *testing.T, s *MemStore, p *Project, title string) *Session {
	t.Helper()
	sess := &Session{ProjectID: p.ID, Title: title, CreatedBy: "u_alice"}
	if err := s.CreateSession(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	return sess
}

func memContent(key, filler string) string {
	// Memory content has a 20-char minimum in Postgres; keep MemStore data
	// realistic too.
	return key + " — " + filler + " with enough detail to be a real memory."
}

func TestSessionJoinLeave(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	p := mustProject(t, s, "sess-proj")
	sess := mustSession(t, s, p, "Auth refactor")

	if _, err := s.JoinSession(ctx, sess.ID, "u_alice", "", "OWNER"); err != nil {
		t.Fatalf("JoinSession alice: %v", err)
	}
	if _, err := s.JoinSession(ctx, sess.ID, "u_bob", "", ""); err != nil {
		t.Fatalf("JoinSession bob (default role): %v", err)
	}
	got, err := s.ListSessionParticipants(ctx, sess.ID, true)
	if err != nil || len(got) != 2 {
		t.Fatalf("active participants = %v, %v; want 2", got, err)
	}
	if got[0].Role == "" || got[1].Role == "" {
		t.Fatalf("roles must default, got %+v", got)
	}

	if err := s.LeaveSession(ctx, sess.ID, "u_bob", ""); err != nil {
		t.Fatalf("LeaveSession: %v", err)
	}
	active, _ := s.ListSessionParticipants(ctx, sess.ID, true)
	if len(active) != 1 || active[0].UserID != "u_alice" {
		t.Fatalf("after leave, active = %+v; want only alice", active)
	}
	all, _ := s.ListSessionParticipants(ctx, sess.ID, false)
	if len(all) != 2 {
		t.Fatalf("all participants = %d; want 2 (history retained)", len(all))
	}

	// Re-join reactivates the same row.
	if _, err := s.JoinSession(ctx, sess.ID, "u_bob", "", "OBSERVER"); err != nil {
		t.Fatalf("re-join: %v", err)
	}
	active, _ = s.ListSessionParticipants(ctx, sess.ID, true)
	if len(active) != 2 {
		t.Fatalf("after re-join, active = %d; want 2", len(active))
	}

	// Bad role rejected; ended session rejects joins.
	if _, err := s.JoinSession(ctx, sess.ID, "u_x", "", "ADMIN"); err == nil {
		t.Fatal("expected invalid role to fail")
	}
	if err := s.EndSession(ctx, sess.ID); err != nil {
		t.Fatalf("EndSession: %v", err)
	}
	if _, err := s.JoinSession(ctx, sess.ID, "u_z", "", ""); err == nil {
		t.Fatal("expected join on ended session to fail")
	}
	ended, _ := s.GetSession(ctx, sess.ID)
	if ended.IsActive || ended.EndedAt.IsZero() {
		t.Fatalf("ended session not stamped: %+v", ended)
	}
}

func TestSessionMemoryIsolationAndInheritance(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	p := mustProject(t, s, "scope-proj")
	sA := mustSession(t, s, p, "Session A")
	sB := mustSession(t, s, p, "Session B")

	// Project-scoped CONFIRMED memory (inherited by both).
	proj := &MemoryItem{ProjectID: p.ID, Key: "testing/framework",
		Content: memContent("testing/framework", "team uses pytest fixtures"),
		Level:   "project", Status: "CONFIRMED"}
	if err := s.CreateMemoryItem(ctx, proj); err != nil {
		t.Fatal(err)
	}
	// Org-level CONFIRMED memory (inherited by both).
	org := &MemoryItem{Key: "security/auth",
		Content: memContent("security/auth", "all APIs must use JWT auth"),
		Level:   "organization", Status: "CONFIRMED"}
	if err := s.CreateMemoryItem(ctx, org); err != nil {
		t.Fatal(err)
	}
	// Session A memory overriding the project key + one private to A.
	for _, m := range []*MemoryItem{
		{ProjectID: p.ID, SessionID: sA.ID, Key: "testing/framework",
			Content: memContent("testing/framework", "use unittest just for this spike")},
		{ProjectID: p.ID, SessionID: sA.ID, Key: "scope/do_not_touch",
			Content: memContent("scope/do_not_touch", "do not touch payments dir now")},
	} {
		if err := s.CreateSessionMemory(ctx, m); err != nil {
			t.Fatal(err)
		}
		if m.Level != "session" {
			t.Fatalf("CreateSessionMemory must force level=session, got %q", m.Level)
		}
	}

	visA, err := s.ListSessionVisibleMemories(ctx, sA.ID)
	if err != nil {
		t.Fatal(err)
	}
	keysA := map[string]string{}
	for _, m := range visA {
		keysA[m.Key] = m.Level + ":" + m.Content
	}
	if len(visA) != 3 {
		t.Fatalf("session A sees %d memories, want 3 (2 own + 1 org): %v", len(visA), keysA)
	}
	// Override: session key wins over the project key.
	found := false
	for _, m := range visA {
		if m.Key == "testing/framework" {
			found = true
			if m.Level != "session" || m.SessionID != sA.ID {
				t.Fatalf("override failed: %+v", m)
			}
		}
	}
	if !found {
		t.Fatal("testing/framework missing from session A view")
	}

	// Session B inherits project+org but NOT A's session memories.
	visB, err := s.ListSessionVisibleMemories(ctx, sB.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(visB) != 2 {
		t.Fatalf("session B sees %d memories, want 2 (project+org): %+v", len(visB), visB)
	}
	for _, m := range visB {
		if m.SessionID != "" {
			t.Fatalf("sibling session memory leaked: %+v", m)
		}
		if !NewSessionInherits(m) {
			t.Fatalf("non-inheritable memory in view: %+v", m)
		}
	}
	// NewSessionInherits predicate direct checks.
	if NewSessionInherits(&MemoryItem{Level: "session", Status: "CONFIRMED"}) {
		t.Fatal("session-level must not inherit")
	}
	if NewSessionInherits(&MemoryItem{Level: "project", Status: "PROPOSED"}) {
		t.Fatal("PROPOSED must not inherit")
	}
}

func TestSessionPromotionFlow(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	p := mustProject(t, s, "promo-proj")

	// Same key appears in 3 distinct sessions → candidate.
	for i, title := range []string{"S1", "S2", "S3"} {
		sess := mustSession(t, s, p, title)
		m := &MemoryItem{ProjectID: p.ID, SessionID: sess.ID, Key: "db/cache",
			Content: memContent("db/cache", "redis pick number "+string(rune('a'+i))+" for pubsub")}
		if err := s.CreateSessionMemory(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	cands, err := s.PromotionCandidates(ctx, p.ID, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].Key != "db/cache" || cands[0].SessionCount != 3 {
		t.Fatalf("candidates = %+v; want one db/cache x3", cands)
	}

	// Same key twice is not enough.
	sess4 := mustSession(t, s, p, "S4")
	_ = s.CreateSessionMemory(ctx, &MemoryItem{ProjectID: p.ID, SessionID: sess4.ID,
		Key: "solo/fact", Content: memContent("solo/fact", "only seen once ever here")})
	cands, _ = s.PromotionCandidates(ctx, p.ID, 3)
	for _, c := range cands {
		if c.Key == "solo/fact" {
			t.Fatal("solo/fact must not be a candidate")
		}
	}

	// Promote one item: becomes project-scoped, inherited by new sessions.
	if err := s.PromoteSessionMemory(ctx, cands[0].ItemIDs[0], "u_alice"); err != nil {
		t.Fatalf("PromoteSessionMemory: %v", err)
	}
	promoted, _ := s.GetMemoryItem(ctx, cands[0].ItemIDs[0])
	if promoted.Level != "project" || promoted.SessionID != "" || promoted.Status != "CONFIRMED" {
		t.Fatalf("promotion wrong: %+v", promoted)
	}
	fresh := mustSession(t, s, p, "Fresh")
	vis, _ := s.ListSessionVisibleMemories(ctx, fresh.ID)
	seen := false
	for _, m := range vis {
		if m.Key == "db/cache" && m.Level == "project" {
			seen = true
		}
	}
	if !seen {
		t.Fatal("promoted memory not inherited by new session")
	}
}
