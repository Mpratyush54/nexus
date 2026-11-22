package store

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

func timelineCounts(t *testing.T, s AgentCloudStore, q TimelineQuery) map[string]int {
	t.Helper()
	items, _, err := s.ListTimeline(t.Context(), q)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int{}
	for _, item := range items {
		out[item.SessionID] = item.TurnCount
	}
	return out
}

func TestMemTimelineTurnCountAndHideEmpty(t *testing.T) {
	m := NewMemStore()
	ctx := t.Context()
	full, err := m.UpsertAgentSession(ctx, &AgentSession{ProjectID: "p1", OwnerUserID: "owner", Harness: "codex", NativeID: "real"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 2; i++ {
		if err := m.AppendSessionTurn(ctx, SessionTurn{SessionID: full.ID, Idx: i, Role: "user", TextPreview: "hi"}); err != nil {
			t.Fatal(err)
		}
	}
	empty, err := m.UpsertAgentSession(ctx, &AgentSession{ProjectID: "p1", OwnerUserID: "owner", Harness: "cursor", NativeID: "state"})
	if err != nil {
		t.Fatal(err)
	}
	all := timelineCounts(t, m, TimelineQuery{UserID: "owner"})
	if len(all) != 2 || all[full.ID] != 2 || all[empty.ID] != 0 {
		t.Fatalf("turn counts: %+v", all)
	}
	shown := timelineCounts(t, m, TimelineQuery{UserID: "owner", HideEmpty: true})
	if len(shown) != 1 || shown[full.ID] != 2 {
		t.Fatalf("hide empty: %+v", shown)
	}
}

// Postgres: turn_count counts session_turns, hide_empty filters before the
// page limit, and migration 046 removes only empty file-name sessions.
func TestPostgresTimelineTurnCountHideEmptyAndCleanup(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("no isolated TEST_POSTGRES_DSN")
	}
	ctx := t.Context()
	s, err := NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.RunMigrations(ctx, "../../migrations"); err != nil {
		t.Fatal(err)
	}
	var owner string
	name := fmt.Sprintf("empty-chats-%d", time.Now().UnixNano())
	if err := s.pool.QueryRow(ctx, `INSERT INTO users(username) VALUES ($1) RETURNING id::text`, name).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = s.pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1::uuid`, owner) }()
	project, err := s.ResolveProject(ctx, "", "", name)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = s.pool.Exec(context.Background(), `DELETE FROM projects WHERE id=$1::uuid`, project.ID) }()
	upsert := func(harness, native string) *AgentSession {
		t.Helper()
		row, err := s.UpsertAgentSession(ctx, &AgentSession{ProjectID: project.ID, OwnerUserID: owner, Harness: harness, NativeID: native, OriginMachineID: name})
		if err != nil {
			t.Fatal(err)
		}
		return row
	}
	real := upsert("codex", "real-chat")
	for i := 1; i <= 3; i++ {
		if err := s.AppendSessionTurn(ctx, SessionTurn{SessionID: real.ID, Idx: i, Role: "user", TextPreview: "hello"}); err != nil {
			t.Fatal(err)
		}
	}
	emptyState := upsert("cursor", "state")           // removed by 046
	emptyTranscript := upsert("gemini", "transcript") // removed by 046
	emptyOther := upsert("claude", "some-real-uuid")  // empty but not a file name: kept
	withTurn := upsert("opencode", "opencode")        // file name with a turn: kept
	granted := upsert("copilot", "state")             // empty file name but shared: kept
	if err := s.AppendSessionTurn(ctx, SessionTurn{SessionID: withTurn.ID, Idx: 1, Role: "user", TextPreview: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_grants (session_id, grantee_user_id, granted_by) VALUES ($1::uuid, $2::uuid, $2::uuid)`, granted.ID, owner); err != nil {
		t.Fatal(err)
	}

	q := TimelineQuery{UserID: owner, ProjectID: project.ID, Limit: 100}
	all := timelineCounts(t, s, q)
	if len(all) != 6 || all[real.ID] != 3 || all[withTurn.ID] != 1 || all[emptyState.ID] != 0 {
		t.Fatalf("turn counts: %+v", all)
	}
	q.HideEmpty = true
	q.Limit = 2
	shown := timelineCounts(t, s, q)
	if len(shown) != 2 || shown[real.ID] != 3 || shown[withTurn.ID] != 1 {
		t.Fatalf("hide empty: %+v", shown)
	}

	sqlBytes, err := os.ReadFile("../../migrations/046_empty_file_name_sessions.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ { // idempotent
		if _, err := s.pool.Exec(ctx, string(sqlBytes)); err != nil {
			t.Fatalf("apply 046 (run %d): %v", i, err)
		}
	}
	exists := func(id string) bool {
		var n int
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM agent_sessions WHERE id=$1::uuid`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n == 1
	}
	for _, id := range []string{emptyState.ID, emptyTranscript.ID} {
		if exists(id) {
			t.Fatalf("empty file-name session %s not removed", id)
		}
		var native string
		if err := s.pool.QueryRow(ctx, `SELECT row_data->>'native_id' FROM agent_sessions_cleanup_046 WHERE id=$1::uuid`, id).Scan(&native); err != nil || native == "" {
			t.Fatalf("session %s not archived: %q %v", id, native, err)
		}
	}
	for _, id := range []string{real.ID, emptyOther.ID, withTurn.ID, granted.ID} {
		if !exists(id) {
			t.Fatalf("session %s removed by 046", id)
		}
	}
	_, _ = s.pool.Exec(context.Background(), `DELETE FROM agent_sessions_cleanup_046 WHERE id = ANY($1::uuid[])`, []string{emptyState.ID, emptyTranscript.ID})
}
