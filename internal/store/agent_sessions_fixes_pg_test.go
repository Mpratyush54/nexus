package store

import (
	"context"
	"testing"
	"time"
)

// Postgres paths of the chat-list and sharing fixes.
func TestPostgresSessionActivityKeysAndGrants(t *testing.T) {
	dsn := testPostgresDSN(t)
	ctx := t.Context()
	s, err := NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	var users []string
	rows, err := s.pool.Query(ctx, `SELECT id::text FROM users ORDER BY created_at LIMIT 2`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		users = append(users, id)
	}
	rows.Close()
	if len(users) == 0 {
		t.Skip("no users")
	}
	var projectID string
	if err := s.pool.QueryRow(ctx, `SELECT id::text FROM projects LIMIT 1`).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	owner := users[0]
	native := "ses_pg_activity_fix"
	cleanup := func() {
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM agent_sessions WHERE project_id = $1::uuid AND native_id = $2`, projectID, native)
	}
	cleanup()
	t.Cleanup(cleanup)

	sess, err := s.UpsertAgentSession(ctx, &AgentSession{ProjectID: projectID, OwnerUserID: owner, Harness: "claude", NativeID: native, OriginMachineID: "m"})
	if err != nil {
		t.Fatal(err)
	}
	// Importing history: the newest message is from September.
	old := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	for i, at := range []time.Time{old.Add(-time.Hour), old, old.Add(-2 * time.Hour)} {
		src := at
		if err := s.AppendSessionTurn(ctx, SessionTurn{SessionID: sess.ID, Idx: i + 1, Role: "user", TextPreview: "x", SourceAt: &src}); err != nil {
			t.Fatal(err)
		}
	}
	again, err := s.UpsertAgentSession(ctx, &AgentSession{ProjectID: projectID, OwnerUserID: owner, Harness: "claude", NativeID: native, OriginMachineID: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if !again.LastActiveAt.Equal(old) {
		t.Fatalf("last_active_at = %v, want %v (newest source time; re-register is not activity)", again.LastActiveAt, old)
	}

	grants, err := s.ListAgentSessionGrants(ctx, sess.ID)
	if err != nil || len(grants) != 0 {
		t.Fatalf("new session grants = %v %v", grants, err)
	}
	if len(users) < 2 {
		t.Log("one user in the test database: grant checks skipped")
		return
	}
	if err := s.GrantAgentSessionOpts(ctx, sess.ID, users[1], owner, SessionGrantOpts{Live: true}); err != nil {
		t.Fatal(err)
	}
	grants, err = s.ListAgentSessionGrants(ctx, sess.ID)
	if err != nil || len(grants) != 1 || grants[0].UserID != users[1] || !grants[0].Live {
		t.Fatalf("grants = %+v %v", grants, err)
	}
	if err := s.RevokeAgentSessionGrant(ctx, sess.ID, users[1]); err != nil {
		t.Fatal(err)
	}
	if grants, _ = s.ListAgentSessionGrants(ctx, sess.ID); len(grants) != 0 {
		t.Fatalf("revoked grant still listed: %+v", grants)
	}
	// Sharing again after a revoke creates a fresh grant.
	if err := s.GrantAgentSessionOpts(ctx, sess.ID, users[1], owner, SessionGrantOpts{Live: true}); err != nil {
		t.Fatal(err)
	}
	if grants, _ = s.ListAgentSessionGrants(ctx, sess.ID); len(grants) != 1 {
		t.Fatalf("re-share grants = %+v", grants)
	}
}

// Re-sending an imported history with source times re-dates the chat.
func TestResentHistoryRedatesChat(t *testing.T) {
	check := func(t *testing.T, st interface {
		UpsertAgentSession(context.Context, *AgentSession) (*AgentSession, error)
		AppendSessionTurn(context.Context, SessionTurn) error
	}, projectID, owner string) {
		ctx := t.Context()
		sess, err := st.UpsertAgentSession(ctx, &AgentSession{ProjectID: projectID, OwnerUserID: owner, Harness: "cursor", NativeID: "redate-chat", OriginMachineID: "m"})
		if err != nil {
			t.Fatal(err)
		}
		// First import: no source times.
		for i := 1; i <= 2; i++ {
			if err := st.AppendSessionTurn(ctx, SessionTurn{SessionID: sess.ID, Idx: i, Role: "user", TextPreview: "x"}); err != nil {
				t.Fatal(err)
			}
		}
		// Re-sent with source times: 409s that fill the dates.
		old := time.Date(2026, 9, 30, 19, 41, 0, 0, time.UTC)
		for i, at := range []time.Time{old.Add(-time.Hour), old} {
			src := at
			_ = st.AppendSessionTurn(ctx, SessionTurn{SessionID: sess.ID, Idx: i + 1, Role: "user", TextPreview: "x", SourceAt: &src})
		}
		got, _ := st.UpsertAgentSession(ctx, &AgentSession{ProjectID: projectID, OwnerUserID: owner, Harness: "cursor", NativeID: "redate-chat", OriginMachineID: "m"})
		if !got.LastActiveAt.Equal(old) {
			t.Fatalf("last_active_at = %v, want %v", got.LastActiveAt, old)
		}
	}
	t.Run("mem", func(t *testing.T) { check(t, NewMemStore(), "p", "u") })
	t.Run("postgres", func(t *testing.T) {
		dsn := testPostgresDSN(t)
		s, err := NewPostgresStore(t.Context(), dsn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		var owner, projectID string
		if err := s.pool.QueryRow(t.Context(), `SELECT id::text FROM users ORDER BY created_at LIMIT 1`).Scan(&owner); err != nil {
			t.Skip("no users")
		}
		_ = s.pool.QueryRow(t.Context(), `SELECT id::text FROM projects LIMIT 1`).Scan(&projectID)
		clean := func() {
			_, _ = s.pool.Exec(context.Background(), `DELETE FROM agent_sessions WHERE project_id = $1::uuid AND native_id = 'redate-chat'`, projectID)
		}
		clean()
		t.Cleanup(clean)
		check(t, s, projectID, owner)
	})
}
