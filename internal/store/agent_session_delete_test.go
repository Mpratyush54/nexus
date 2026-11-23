package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"central-memory/internal/usage"
)

func deleteTestPG(t *testing.T) (*PostgresStore, string, string) {
	t.Helper()
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("no isolated TEST_POSTGRES_DSN")
	}
	s, err := NewPostgresStore(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.RunMigrations(t.Context(), "../../migrations"); err != nil {
		t.Fatal(err)
	}
	var owner string
	name := fmt.Sprintf("session-delete-%d", time.Now().UnixNano())
	if err := s.pool.QueryRow(t.Context(), `INSERT INTO users(username) VALUES ($1) RETURNING id::text`, name).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = s.pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1::uuid`, owner) })
	project, err := s.ResolveProject(t.Context(), "", "", name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = s.pool.Exec(context.Background(), `DELETE FROM projects WHERE id=$1::uuid`, project.ID) })
	return s, owner, project.ID
}

func TestDeleteAgentSessionPostgres(t *testing.T) {
	s, owner, projectID := deleteTestPG(t)
	ctx := t.Context()
	var grantee string
	if err := s.pool.QueryRow(ctx, `INSERT INTO users(username) VALUES ($1) RETURNING id::text`, fmt.Sprintf("grantee-%d", time.Now().UnixNano())).Scan(&grantee); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = s.pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1::uuid`, grantee) }()
	row, err := s.UpsertAgentSession(ctx, &AgentSession{ProjectID: projectID, OwnerUserID: owner, Harness: "claude", NativeID: "doomed"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		if err := s.AppendSessionTurn(ctx, SessionTurn{SessionID: row.ID, Idx: i, Role: "user", TextPreview: fmt.Sprint("turn ", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.CreateSessionVersion(ctx, row.ID, owner); err != nil {
		t.Fatal(err)
	}
	if err := s.GrantAgentSession(ctx, row.ID, grantee, owner); err != nil {
		t.Fatal(err)
	}
	idx := int64(1)
	if err := s.RecordSessionUsage(ctx, []SessionUsage{{SessionID: row.ID, Key: "call-1", TurnIdx: &idx, Entry: usage.Entry{Model: "m", Input: 5}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_snapshots (session_id, project_id, harness, conversation_id, transcript_payload)
		VALUES ($1::uuid, $2::uuid, 'claude', 'doomed', '\x00'::bytea)`, row.ID, projectID); err != nil {
		t.Fatal(err)
	}
	fork, err := s.ForkAgentSession(ctx, row.ID, owner)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM agent_sessions WHERE id=$1::uuid`, fork.ID)
	}()

	res, err := s.DeleteAgentSession(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Turns != 3 || res.Versions != 1 || res.Grants != 1 || res.Usage != 1 || res.Snapshots != 1 {
		t.Fatalf("result = %+v", res)
	}
	if _, err := s.GetAgentSession(ctx, row.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("session survived: %v", err)
	}
	for _, table := range []string{"session_turns", "session_versions", "session_grants", "session_usage", "session_snapshots"} {
		var n int
		if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM `+table+` WHERE session_id = $1::uuid`, row.ID).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s left %d rows (%v)", table, n, err)
		}
	}
	child, err := s.GetAgentSession(ctx, fork.ID)
	if err != nil || child.ParentSessionID != "" {
		t.Fatalf("fork after delete: %+v %v", child, err)
	}
	if _, err := s.DeleteAgentSession(ctx, row.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	if _, err := s.DeleteAgentSession(ctx, "not-a-uuid"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bad id: %v", err)
	}
}

func TestDedupeSessionTurnsPostgres(t *testing.T) {
	s, owner, projectID := deleteTestPG(t)
	ctx := t.Context()
	row, err := s.UpsertAgentSession(ctx, &AgentSession{ProjectID: projectID, OwnerUserID: owner, Harness: "claude", NativeID: "dupes"})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	at2 := at.Add(time.Minute)
	calls := json.RawMessage(`[{"name":"Read","input":{"path":"a.go"}}]`)
	for _, turn := range []SessionTurn{
		{Idx: 1, Role: "user", TextPreview: "fix it", SourceAt: &at},
		{Idx: 2, Role: "assistant", TextPreview: "done", ToolCalls: calls, SourceAt: &at2},
		{Idx: 3, Role: "user", TextPreview: "fix it"},
		{Idx: 4, Role: "assistant", TextPreview: "done", ToolCalls: json.RawMessage(`[{"input":{"path":"a.go"},"name":"Read"}]`)},
		{Idx: 5, Role: "assistant", TextPreview: "done"},
	} {
		turn.SessionID = row.ID
		if err := s.AppendSessionTurn(ctx, turn); err != nil {
			t.Fatal(err)
		}
	}
	dry, err := s.DedupeSessionTurns(ctx, row.ID, true)
	if err != nil || dry.Removed != 2 || dry.Kept != 3 || len(dry.RemovedIdx) != 2 || dry.RemovedIdx[0] != 3 || dry.RemovedIdx[1] != 4 {
		t.Fatalf("dry run = %+v %v", dry, err)
	}
	if turns, _ := s.ListSessionTurns(ctx, row.ID); len(turns) != 5 {
		t.Fatalf("dry run deleted: %d", len(turns))
	}
	res, err := s.DedupeSessionTurns(ctx, row.ID, false)
	if err != nil || res.Removed != 2 || res.Kept != 3 {
		t.Fatalf("dedupe = %+v %v", res, err)
	}
	turns, err := s.ListSessionTurns(ctx, row.ID)
	if err != nil || len(turns) != 3 || turns[0].Idx != 1 || turns[1].Idx != 2 || turns[2].Idx != 5 {
		t.Fatalf("turns = %+v %v", turns, err)
	}
	// Turn 5 is undated, so the chat's activity is not rewritten.
	again, err := s.DedupeSessionTurns(ctx, row.ID, false)
	if err != nil || again.Removed != 0 || again.Kept != 3 {
		t.Fatalf("second dedupe = %+v %v", again, err)
	}
	if _, err := s.DedupeSessionTurns(ctx, "00000000-0000-0000-0000-000000000000", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
}

func TestDedupeSessionTurnsPostgresDatesChat(t *testing.T) {
	s, owner, projectID := deleteTestPG(t)
	ctx := t.Context()
	row, err := s.UpsertAgentSession(ctx, &AgentSession{ProjectID: projectID, OwnerUserID: owner, Harness: "claude", NativeID: "dated"})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	for _, turn := range []SessionTurn{
		{Idx: 1, Role: "user", TextPreview: "hi", SourceAt: &at},
		{Idx: 2, Role: "user", TextPreview: "hi"},
	} {
		turn.SessionID = row.ID
		if err := s.AppendSessionTurn(ctx, turn); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.DedupeSessionTurns(ctx, row.ID, false); err != nil {
		t.Fatal(err)
	}
	after, err := s.GetAgentSession(ctx, row.ID)
	if err != nil || !after.LastActiveAt.Equal(at) {
		t.Fatalf("last_active_at = %v %v", after.LastActiveAt, err)
	}
}

func TestMemStoreDeleteAgentSession(t *testing.T) {
	m := NewMemStore()
	ctx := t.Context()
	project, err := m.ResolveProject(ctx, "", "", "mem-delete")
	if err != nil {
		t.Fatal(err)
	}
	row, err := m.UpsertAgentSession(ctx, &AgentSession{ProjectID: project.ID, OwnerUserID: "alice", Harness: "codex", NativeID: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.AppendSessionTurn(ctx, SessionTurn{SessionID: row.ID, Idx: 1, Role: "user", TextPreview: "hi"}); err != nil {
		t.Fatal(err)
	}
	fork, err := m.ForkAgentSession(ctx, row.ID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	link, err := m.CreateGuestLink(ctx, row.ID, "alice", time.Now().Add(time.Hour), true)
	if err != nil {
		t.Fatal(err)
	}
	res, err := m.DeleteAgentSession(ctx, row.ID)
	if err != nil || res.Turns != 1 {
		t.Fatalf("delete = %+v %v", res, err)
	}
	if _, err := m.GetAgentSession(ctx, row.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("session survived: %v", err)
	}
	if _, err := m.ListSessionTurns(ctx, row.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("turns survived: %v", err)
	}
	if child, err := m.GetAgentSession(ctx, fork.ID); err != nil || child.ParentSessionID != "" {
		t.Fatalf("fork = %+v %v", child, err)
	}
	if _, err := m.GetGuestLinkByToken(ctx, link.Token); err == nil {
		t.Fatal("guest link survived")
	}
	if _, err := m.DeleteAgentSession(ctx, row.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}
