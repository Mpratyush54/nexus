package store

import (
	"strings"
	"testing"
)

func boolPtr(b bool) *bool    { return &b }
func strPtr(s string) *string { return &s }

func TestCleanSessionTitle(t *testing.T) {
	cases := map[string]string{
		"  hello  ":              "hello",
		"a\nb\tc\r\nd":           "a b c d",
		"bell\x07 and ‮evil":     "bell and evil",
		"":                       "",
		"\x00\x01":               "",
		strings.Repeat("é", 250): strings.Repeat("é", SessionUserStateTitleMax),
	}
	for in, want := range cases {
		if got := CleanSessionTitle(in); got != want {
			t.Fatalf("CleanSessionTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

// sessionUserStateContract runs the same checks on any store.
func sessionUserStateContract(t *testing.T, st SessionUserStateStore, user, other, sid string) {
	t.Helper()
	ctx := t.Context()
	got, err := st.SetSessionUserState(ctx, user, sid, SessionUserStatePatch{Title: strPtr(" Renamed "), Pinned: boolPtr(true)})
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Renamed" || got.PinnedAt == nil || got.ArchivedAt != nil || got.SessionID != sid {
		t.Fatalf("rename+pin = %+v", got)
	}
	pinnedAt := *got.PinnedAt
	// Pinning again keeps the original time.
	got, err = st.SetSessionUserState(ctx, user, sid, SessionUserStatePatch{Pinned: boolPtr(true)})
	if err != nil || got.PinnedAt == nil || !got.PinnedAt.Equal(pinnedAt) || got.Title != "Renamed" {
		t.Fatalf("repin = %+v %v", got, err)
	}
	// Archiving unpins.
	got, err = st.SetSessionUserState(ctx, user, sid, SessionUserStatePatch{Archived: boolPtr(true)})
	if err != nil || got.PinnedAt != nil || got.ArchivedAt == nil {
		t.Fatalf("archive = %+v %v", got, err)
	}
	// Pinning un-archives.
	got, err = st.SetSessionUserState(ctx, user, sid, SessionUserStatePatch{Pinned: boolPtr(true)})
	if err != nil || got.PinnedAt == nil || got.ArchivedAt != nil {
		t.Fatalf("pin archived = %+v %v", got, err)
	}
	states, err := st.ListSessionUserStates(ctx, user)
	if err != nil || len(states) != 1 || states[sid].Title != "Renamed" || states[sid].PinnedAt == nil {
		t.Fatalf("list = %+v %v", states, err)
	}
	if others, err := st.ListSessionUserStates(ctx, other); err != nil || len(others) != 0 {
		t.Fatalf("other's states = %+v %v", others, err)
	}
	// Clearing everything removes the row.
	got, err = st.SetSessionUserState(ctx, user, sid, SessionUserStatePatch{Title: strPtr(""), Pinned: boolPtr(false)})
	if err != nil || !got.Empty() {
		t.Fatalf("clear = %+v %v", got, err)
	}
	if states, err := st.ListSessionUserStates(ctx, user); err != nil || len(states) != 0 {
		t.Fatalf("after clear = %+v %v", states, err)
	}
	if _, err := st.SetSessionUserState(ctx, user, " ", SessionUserStatePatch{Pinned: boolPtr(true)}); err == nil {
		t.Fatal("empty session id accepted")
	}
}

func TestMemStoreSessionUserState(t *testing.T) {
	m := NewMemStore()
	sessionUserStateContract(t, m, "alice", "bob", "chat-1")

	// Deleting the chat drops every person's state; turn counts are read.
	ctx := t.Context()
	p, err := m.ResolveProject(ctx, "", "", "state-mem")
	if err != nil {
		t.Fatal(err)
	}
	row, err := m.UpsertAgentSession(ctx, &AgentSession{ProjectID: p.ID, OwnerUserID: "alice", Harness: "codex", NativeID: "n1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.AppendSessionTurn(ctx, SessionTurn{SessionID: row.ID, Idx: 1, Role: "user", TextPreview: "hi"}); err != nil {
		t.Fatal(err)
	}
	counts, err := m.SessionTurnCounts(ctx, []string{row.ID, "missing"})
	if err != nil || len(counts) != 1 || counts[row.ID] != 1 {
		t.Fatalf("counts = %v %v", counts, err)
	}
	for _, u := range []string{"alice", "bob"} {
		if _, err := m.SetSessionUserState(ctx, u, row.ID, SessionUserStatePatch{Archived: boolPtr(true)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.DeleteAgentSession(ctx, row.ID); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"alice", "bob"} {
		if states, _ := m.ListSessionUserStates(ctx, u); len(states) != 0 {
			t.Fatalf("%s states after delete = %+v", u, states)
		}
	}
}

func TestPostgresSessionUserState(t *testing.T) {
	s, owner, projectID := deleteTestPG(t)
	ctx := t.Context()
	var other string
	if err := s.pool.QueryRow(ctx, `INSERT INTO users(username) VALUES ($1) RETURNING id::text`, "state-other-"+owner).Scan(&other); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = s.pool.Exec(ctx, `DELETE FROM users WHERE id=$1::uuid`, other) })
	row, err := s.UpsertAgentSession(ctx, &AgentSession{ProjectID: projectID, OwnerUserID: owner, Harness: "codex", NativeID: "state-pg"})
	if err != nil {
		t.Fatal(err)
	}
	sessionUserStateContract(t, s, owner, other, row.ID)
	// Archived snapshot chats have text ids without an agent session.
	if _, err := s.SetSessionUserState(ctx, owner, "legacy-snapshot-id", SessionUserStatePatch{Pinned: boolPtr(true)}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendSessionTurn(ctx, SessionTurn{SessionID: row.ID, Idx: 1, Role: "user", TextPreview: "hi"}); err != nil {
		t.Fatal(err)
	}
	counts, err := s.SessionTurnCounts(ctx, []string{row.ID, "legacy-snapshot-id"})
	if err != nil || len(counts) != 1 || counts[row.ID] != 1 {
		t.Fatalf("counts = %v %v", counts, err)
	}
	for _, u := range []string{owner, other} {
		if _, err := s.SetSessionUserState(ctx, u, row.ID, SessionUserStatePatch{Archived: boolPtr(true)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.DeleteAgentSession(ctx, row.ID); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{owner, other} {
		states, err := s.ListSessionUserStates(ctx, u)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := states[row.ID]; ok {
			t.Fatalf("state for deleted chat kept: %+v", states)
		}
	}
	states, _ := s.ListSessionUserStates(ctx, owner)
	if _, ok := states["legacy-snapshot-id"]; !ok {
		t.Fatalf("snapshot pin lost: %+v", states)
	}
}
