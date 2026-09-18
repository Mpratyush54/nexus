package store

// Audit: session visibility/promotion + ConfirmDue tier handling (MemStore +
// pure helpers).
//
// PromotionCandidates divergence: MemStore includes org-level (ProjectID "")
// session memories in EVERY project's candidates; the Postgres query
// (`WHERE project_id = $1::uuid`) excludes NULL-project rows.
// ConfirmDue tier note: any source tag carrying `confirm_after=<duration>`
// is honored verbatim — there is no allowlist of tiers, so any writer that
// can set source (or ConfirmAfter) can fast-track auto-confirm.

import (
	"context"
	"strings"
	"testing"
	"time"
)

func auditProject(t *testing.T, ctx context.Context, s *MemStore, folder string) *Project {
	t.Helper()
	p, err := s.ResolveProject(ctx, "", "", folder)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func auditSession(t *testing.T, ctx context.Context, s *MemStore, p *Project) *Session {
	t.Helper()
	sess := &Session{ProjectID: p.ID, CreatedBy: "u1"}
	if err := s.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	return sess
}

func auditSessionMem(t *testing.T, ctx context.Context, s *MemStore, p *Project, sessID, key, status string) *MemoryItem {
	t.Helper()
	m := &MemoryItem{ProjectID: p.ID, SessionID: sessID, Key: key, Content: "session content with enough length"}
	if status != "" {
		m.Status = status
	}
	if err := s.CreateSessionMemory(ctx, m); err != nil {
		t.Fatal(err)
	}
	return m
}

// FIXED(#110): org-level (NULL-project) session rows are excluded from
// project promotion candidates, matching the Postgres
// WHERE project_id = $1 scoping.
func TestAuditPromotionCandidatesOrgNullDivergence(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	p := auditProject(t, ctx, s, "promo-org-proj")
	org := &MemoryItem{Key: "orgkey", Content: "org session content long enough", Level: "session", SessionID: "s-org-1", Status: StatusConfirmed}
	if err := s.CreateMemoryItem(ctx, org); err != nil {
		t.Fatal(err)
	}
	cands, err := s.PromotionCandidates(ctx, p.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cands {
		if c.Key == "orgkey" {
			t.Errorf("org-level (NULL project) key %q must be excluded from project %s candidates", "orgkey", p.ID)
		}
	}
}

func TestAuditPromotionCandidatesThresholdAndRejected(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	p := auditProject(t, ctx, s, "promo-threshold-proj")
	for i := 0; i < 3; i++ {
		sess := &Session{ProjectID: p.ID, CreatedBy: "u1"}
		if err := s.CreateSession(ctx, sess); err != nil {
			t.Fatal(err)
		}
		auditSessionMem(t, ctx, s, p, sess.ID, "repeat-key", StatusConfirmed)
	}
	bad := auditSession(t, ctx, s, p)
	auditSessionMem(t, ctx, s, p, bad.ID, "rejected-key", StatusRejected)
	// REJECTED-only key must stay out even at threshold 1.
	cands, err := s.PromotionCandidates(ctx, p.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]int{}
	for _, c := range cands {
		byKey[c.Key] = c.SessionCount
	}
	if byKey["repeat-key"] != 3 {
		t.Errorf("repeat-key sessions = %d, want 3", byKey["repeat-key"])
	}
	if _, ok := byKey["rejected-key"]; ok {
		t.Error("REJECTED-only key must be excluded from candidates")
	}
	// Default threshold is 3.
	cands, err = s.PromotionCandidates(ctx, p.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].Key != "repeat-key" {
		t.Errorf("default threshold candidates = %+v, want only repeat-key", cands)
	}
}

func TestAuditNewSessionInherits(t *testing.T) {
	cases := []struct {
		name string
		mem  *MemoryItem
		want bool
	}{
		{"project confirmed", &MemoryItem{Level: "project", Status: "CONFIRMED"}, true},
		{"org confirmed", &MemoryItem{Level: "organization", Status: "CONFIRMED"}, true},
		{"proposed not inherited", &MemoryItem{Level: "project", Status: "PROPOSED"}, false},
		{"session never inherited", &MemoryItem{Level: "session", Status: "CONFIRMED", SessionID: "s"}, false},
		{"project with session id", &MemoryItem{Level: "project", Status: "CONFIRMED", SessionID: "s"}, false},
		{"personal not inherited", &MemoryItem{Level: "personal", Status: "CONFIRMED"}, false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		if got := NewSessionInherits(tc.mem); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestAuditSessionOverrideAndSiblingIsolation(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	p := auditProject(t, ctx, s, "override-proj")
	projMem := &MemoryItem{ProjectID: p.ID, Key: "shared-key", Content: "project version content long enough", Level: "project", Status: StatusConfirmed}
	if err := s.CreateMemoryItem(ctx, projMem); err != nil {
		t.Fatal(err)
	}
	s1 := auditSession(t, ctx, s, p)
	s2 := auditSession(t, ctx, s, p)
	auditSessionMem(t, ctx, s, p, s1.ID, "shared-key", StatusConfirmed)
	auditSessionMem(t, ctx, s, p, s2.ID, "sibling-only", StatusConfirmed)
	vis, err := s.ListSessionVisibleMemories(ctx, s1.ID)
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]*MemoryItem{}
	for _, m := range vis {
		byKey[m.Key] = m
	}
	winner, ok := byKey["shared-key"]
	if !ok {
		t.Fatal("shared-key missing from visible memories")
	}
	if winner.SessionID != s1.ID {
		t.Errorf("session key should override project key; winner session = %q", winner.SessionID)
	}
	if _, ok := byKey["sibling-only"]; ok {
		t.Error("sibling session memory leaked into s1's visible set")
	}
	if _, err := s.ListSessionVisibleMemories(ctx, "sess_missing"); err != ErrNotFound {
		t.Errorf("unknown session: got %v, want ErrNotFound", err)
	}
}

func TestAuditPromoteSessionMemoryGuards(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	p := auditProject(t, ctx, s, "promote-guard-proj")
	sess := auditSession(t, ctx, s, p)
	m := auditSessionMem(t, ctx, s, p, sess.ID, "promote-me", StatusProposed)
	if err := s.PromoteSessionMemory(ctx, m.ID, "u1"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetMemoryItem(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Level != "project" || got.SessionID != "" || got.Status != StatusConfirmed {
		t.Errorf("promotion incomplete: %+v", got)
	}
	// Non-session memory must not promote.
	plain := &MemoryItem{ProjectID: p.ID, Key: "plain", Content: "plain project content long enough"}
	if err := s.CreateMemoryItem(ctx, plain); err != nil {
		t.Fatal(err)
	}
	if err := s.PromoteSessionMemory(ctx, plain.ID, "u1"); err == nil {
		t.Error("promoting a non-session memory should fail")
	}
	if err := s.PromoteSessionMemory(ctx, "mem_missing", "u1"); err != ErrNotFound {
		t.Errorf("unknown memory: got %v, want ErrNotFound", err)
	}
}

func TestAuditJoinLeaveEdges(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	p := auditProject(t, ctx, s, "join-leave-proj")
	sess := auditSession(t, ctx, s, p)
	if _, err := s.JoinSession(ctx, sess.ID, "u1", "", "BOGUS"); err == nil {
		t.Error("bogus role should fail")
	}
	if _, err := s.JoinSession(ctx, sess.ID, "", "", ""); err == nil {
		t.Error("join without user or agent should fail")
	}
	if _, err := s.JoinSession(ctx, "sess_missing", "u1", "", ""); err != ErrNotFound {
		t.Errorf("unknown session join: got %v, want ErrNotFound", err)
	}
	if _, err := s.JoinSession(ctx, sess.ID, "u1", "", "owner"); err != nil {
		t.Fatal(err)
	}
	if err := s.LeaveSession(ctx, sess.ID, "u1", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.LeaveSession(ctx, sess.ID, "u1", ""); err != ErrNotFound {
		t.Errorf("double leave: got %v, want ErrNotFound", err)
	}
	if err := s.EndSession(ctx, sess.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.EndSession(ctx, sess.ID); err != nil {
		t.Errorf("EndSession should be idempotent: %v", err)
	}
	if _, err := s.JoinSession(ctx, sess.ID, "u2", "", ""); err == nil {
		t.Error("join on ended session should fail")
	}
}

func TestAuditConfirmTierRoundTripAndSpoof(t *testing.T) {
	// Tier encoding round-trips through the source tag.
	src := FormatConfirmSource(time.Hour)
	if !strings.Contains(src, "confirm_after=") {
		t.Fatalf("FormatConfirmSource = %q", src)
	}
	d, ok := ParseConfirmAfter(src)
	if !ok || d != time.Hour {
		t.Errorf("ParseConfirmAfter(%q) = %v,%v", src, d, ok)
	}
	if got := FormatConfirmSource(0); got != "processor" {
		t.Errorf("non-positive tier should record plain source, got %q", got)
	}
	// SPOOF NOTE (passes by design, no allowlist): any duration string in
	// the tag is honored, including absurd fast-tracks. ConfirmDue cannot
	// distinguish a processor-written tier from a hand-crafted one.
	if d, ok := ParseConfirmAfter("processor:confirm_after=1ns"); !ok || d != time.Nanosecond {
		t.Errorf("spoof tier not honored: %v,%v", d, ok)
	}
	in := ProposedInput{
		ProjectID: "p1", Key: "k", Content: "content with enough length here",
		Level: "project", Scope: "fact", Confidence: 0.9, ConfirmAfter: time.Nanosecond,
	}
	if err := in.Validate(); err != nil {
		t.Fatalf("1ns ConfirmAfter should validate (no tier allowlist): %v", err)
	}
	t.Log("SPOOF NOTE: ConfirmAfter has no allowlist; any writer can fast-track auto-confirm via a 1ns tier")
	if _, ok := ParseConfirmAfter("processor"); ok {
		t.Error("plain source must fall back to default tier")
	}
	if _, ok := ParseConfirmAfter("processor:confirm_after=banana"); ok {
		t.Error("unparseable tier must fall back to default tier")
	}
}

func TestAuditIsConfirmDueEdges(t *testing.T) {
	now := time.Now().UTC()
	if IsConfirmDue(time.Time{}, time.Hour, now) {
		t.Error("zero proposal time must never read as due")
	}
	if !IsConfirmDue(now.Add(-time.Hour), time.Hour, now) {
		t.Error("exact due instant should read as due")
	}
	if IsConfirmDue(now.Add(-time.Minute), time.Hour, now) {
		t.Error("fresh item should not be due")
	}
	// Non-positive tier falls back to the 24h default.
	if !IsConfirmDue(now.Add(-25*time.Hour), 0, now) {
		t.Error("25h-old item with default tier should be due")
	}
	if IsConfirmDue(now.Add(-time.Hour), 0, now) {
		t.Error("1h-old item with default 24h tier should not be due")
	}
}

func TestAuditSaveProposedSQLGuards(t *testing.T) {
	in := ProposedInput{
		ProjectID: "p1", Key: "k", Content: "content with enough length here",
		Level: "project", Scope: "fact", Confidence: 0.9, ConfirmAfter: time.Hour,
	}
	query, args := BuildSaveProposedSQL(in)
	if !strings.Contains(query, "'PROPOSED'") {
		t.Errorf("save SQL must hard-code PROPOSED status, got: %s", query)
	}
	if args[1] != nil {
		t.Errorf("empty session must bind NULL, got %v", args[1])
	}
	if src, _ := args[len(args)-1].(string); !strings.Contains(src, "confirm_after=") {
		t.Errorf("confirm tier must ride in source tag, got %q", src)
	}
	if !strings.Contains(BuildConfirmIDsSQL(), "status = 'PROPOSED'") {
		t.Error("confirm sweep write must stay idempotent via status predicate")
	}
	if !strings.Contains(BuildSetStatusSQL(), "AND status = $3") {
		t.Error("SetStatus SQL must predicate on expected current status (compare-and-set)")
	}
	if err := (ProposedInput{}).Validate(); err == nil {
		t.Error("empty ProposedInput must fail validation")
	}
}

// FIXED(#119): session TTL sweeper ends stale active sessions (idempotent),
// honoring explicit ExpiresAt over the default TTL.
func TestAuditExpireStaleSessions(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	p := auditProject(t, ctx, s, "expiry-proj")
	now := time.Now().UTC()

	old := &Session{ProjectID: p.ID, CreatedBy: "u1"}
	if err := s.CreateSession(ctx, old); err != nil {
		t.Fatal(err)
	}
	b := memSessionsOf(s)
	b.mu.Lock()
	b.sessions[old.ID].CreatedAt = now.Add(-8 * 24 * time.Hour)
	b.mu.Unlock()

	pinned := &Session{ProjectID: p.ID, CreatedBy: "u1", ExpiresAt: now.Add(30 * 24 * time.Hour)}
	if err := s.CreateSession(ctx, pinned); err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	b.sessions[pinned.ID].CreatedAt = now.Add(-8 * 24 * time.Hour)
	b.mu.Unlock()

	n, err := s.ExpireStaleSessions(ctx, now, 0)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("swept = %d, want 1 (only the TTL-lapsed session)", n)
	}
	got, _ := s.GetSession(ctx, old.ID)
	if got.IsActive || got.EndedAt.IsZero() {
		t.Errorf("lapsed session not ended: %+v", got)
	}
	kept, _ := s.GetSession(ctx, pinned.ID)
	if !kept.IsActive {
		t.Error("session with future ExpiresAt must survive the sweep")
	}
	// Idempotent rerun sweeps nothing.
	n, err = s.ExpireStaleSessions(ctx, now, 0)
	if err != nil || n != 0 {
		t.Errorf("rerun: swept = %d, err = %v; want 0, nil", n, err)
	}
}
