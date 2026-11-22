package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"
)

// The Postgres read paths behind the chat list and chat page: one paged
// query with the snapshot read rule, and keyset turn pages.
func TestTimelineReadsPostgres(t *testing.T) {
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
	migrations := os.Getenv("TEST_MIGRATIONS_DIR")
	if migrations == "" {
		migrations = "../../migrations"
	}
	if err := s.RunMigrations(ctx, migrations); err != nil {
		t.Fatal(err)
	}
	tag := fmt.Sprintf("tlr-%d", time.Now().UnixNano())
	user := func(name string) string {
		var id string
		if err := s.pool.QueryRow(ctx, `INSERT INTO users(username) VALUES ($1) RETURNING id::text`, tag+"-"+name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = s.pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1::uuid`, id) })
		return id
	}
	alice, bob := user("alice"), user("bob")
	project := func(name, owner string, members ...string) string {
		var id string
		if err := s.pool.QueryRow(ctx, `INSERT INTO projects(folder_name, created_by) VALUES ($1, $2::uuid) RETURNING id::text`, tag+"-"+name, owner).Scan(&id); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = s.pool.Exec(context.Background(), `DELETE FROM projects WHERE id=$1::uuid`, id) })
		for _, m := range members {
			if _, err := s.pool.Exec(ctx, `INSERT INTO project_members(project_id, user_id) VALUES ($1::uuid, $2::uuid)`, id, m); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	shared := project("shared", alice, bob)
	elsewhere := project("elsewhere", alice)
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	n := 0
	snapshot := func(projectID, owner, harness, conv string, version int) string {
		t.Helper()
		n++
		var sid string
		if err := s.pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&sid); err != nil {
			t.Fatal(err)
		}
		at := base.Add(time.Duration(n) * time.Second)
		for v := 1; v <= version; v++ {
			if _, err := s.pool.Exec(ctx, `
				INSERT INTO session_snapshots (session_id, snapshot_version, project_id, harness, conversation_id, turn_count, transcript_payload, owner_user_id, created_at, updated_at)
				VALUES ($1::uuid, $2, $3::uuid, $4, $5, 7, $6, NULLIF($7,'')::uuid, $8, $8)`,
				sid, v, projectID, harness, conv, []byte(fmt.Sprintf("payload-%d", v)), owner, at.Add(time.Duration(v)*time.Millisecond)); err != nil {
				t.Fatal(err)
			}
		}
		return sid
	}
	grant := func(sid, projectID, grantee string, revoked bool) {
		t.Helper()
		if _, err := s.pool.Exec(ctx, `INSERT INTO session_content_grants(session_id, project_id, grantee_user_id, revoked_at)
			VALUES ($1::uuid, $2::uuid, $3::uuid, CASE WHEN $4 THEN now() END)`, sid, projectID, grantee, revoked); err != nil {
			t.Fatal(err)
		}
	}
	bobOwn := snapshot(shared, bob, "codex", tag+"-bob-own", 2)
	bobCaptured := snapshot(shared, bob, "codex", tag+"-captured", 1)
	granted := snapshot(shared, alice, "Cursor", tag+"-granted", 1)
	grant(granted, shared, bob, false)
	revoked := snapshot(shared, alice, "codex", tag+"-revoked", 1)
	grant(revoked, shared, bob, true)
	private := snapshot(shared, alice, "codex", tag+"-private", 1)
	ownerless := snapshot(shared, "", "codex", tag+"-ownerless", 1)
	away := snapshot(elsewhere, alice, "codex", tag+"-away", 1)
	grant(away, elsewhere, bob, false)
	// Bob's readable modern copy of one conversation hides its snapshot.
	if _, err := s.UpsertAgentSession(ctx, &AgentSession{ProjectID: shared, OwnerUserID: bob, Harness: "codex", NativeID: tag + "-captured", OriginMachineID: "pc"}); err != nil {
		t.Fatal(err)
	}
	// Alice's private copy of the granted chat does not hide it from bob.
	if _, err := s.UpsertAgentSession(ctx, &AgentSession{ProjectID: shared, OwnerUserID: alice, Harness: "Cursor", NativeID: tag + "-granted", OriginMachineID: "pc"}); err != nil {
		t.Fatal(err)
	}
	ids := func(rows []SessionSnapshot) []string {
		out := []string{}
		for _, r := range rows {
			out = append(out, r.SessionID)
		}
		return out
	}
	list := func(q LegacyTimelineQuery) []SessionSnapshot {
		t.Helper()
		rows, err := s.ListLegacyTimeline(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		return rows
	}
	got := list(LegacyTimelineQuery{UserID: bob, Limit: 50})
	// Only rows from this test's projects (the database may hold others).
	mine := []SessionSnapshot{}
	for _, r := range got {
		if r.ProjectID == shared || r.ProjectID == elsewhere {
			mine = append(mine, r)
		}
	}
	if fmt.Sprint(ids(mine)) != fmt.Sprint([]string{granted, bobOwn}) {
		t.Fatalf("bob's snapshots = %v; want granted %s then own %s (not %s %s %s %s %s)",
			ids(mine), granted, bobOwn, bobCaptured, revoked, private, ownerless, away)
	}
	if mine[1].SnapshotVersion != 2 || len(mine[1].TranscriptPayload) != 0 {
		t.Fatalf("latest version metadata only: %+v", mine[1])
	}
	if rows := list(LegacyTimelineQuery{UserID: bob, ProjectID: shared, Harness: "cursor", Limit: 50}); fmt.Sprint(ids(rows)) != fmt.Sprint([]string{granted}) {
		t.Fatalf("harness filter = %v", ids(rows))
	}
	page1 := list(LegacyTimelineQuery{UserID: bob, ProjectID: shared, Limit: 1})
	page2 := list(LegacyTimelineQuery{UserID: bob, ProjectID: shared, Limit: 1, CursorAt: page1[0].UpdatedAt, CursorID: page1[0].SessionID})
	page3 := list(LegacyTimelineQuery{UserID: bob, ProjectID: shared, Limit: 1, CursorAt: page2[0].UpdatedAt, CursorID: page2[0].SessionID})
	if len(page1) != 1 || len(page2) != 1 || len(page3) != 0 || page1[0].SessionID != granted || page2[0].SessionID != bobOwn {
		t.Fatalf("cursor pages = %v %v %v", ids(page1), ids(page2), ids(page3))
	}
	aliceRows := list(LegacyTimelineQuery{UserID: alice, ProjectID: shared, Limit: 50})
	if fmt.Sprint(ids(aliceRows)) != fmt.Sprint([]string{private, revoked}) {
		// granted is hidden for alice by her own modern copy.
		t.Fatalf("alice's snapshots = %v; want %s %s", ids(aliceRows), private, revoked)
	}
	payloads, err := s.LatestSnapshotTranscripts(ctx, []string{bobOwn, granted, "not-a-uuid"})
	if err != nil || string(payloads[bobOwn]) != "payload-2" || string(payloads[granted]) != "payload-1" || len(payloads) != 2 {
		t.Fatalf("transcripts = %q %v", payloads, err)
	}
	meta, err := s.GetLatestSnapshotMeta(ctx, bobOwn)
	if err != nil || meta.SnapshotVersion != 2 || meta.OwnerUserID != bob || meta.TranscriptPayload != nil {
		t.Fatalf("meta = %+v %v", meta, err)
	}

	// Turn pages.
	sess, err := s.UpsertAgentSession(ctx, &AgentSession{ProjectID: shared, OwnerUserID: bob, Harness: "claude", NativeID: tag + "-turns", OriginMachineID: "pc"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 9; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		turn := SessionTurn{SessionID: sess.ID, Idx: i, Role: role, TextPreview: fmt.Sprintf("turn %d", i)}
		if i == 0 {
			turn.TextPreview = "  "
		}
		if i == 4 {
			turn.ToolCalls = json.RawMessage(`[{"name":"bash"}]`)
		}
		if i == 6 {
			turn.TextPreview = `{"tool_calls":[]}`
		}
		if err := s.AppendSessionTurn(ctx, turn); err != nil {
			t.Fatal(err)
		}
	}
	after := 2
	page, more, err := s.ListSessionTurnsPage(ctx, sess.ID, &after, 3)
	if err != nil || more != true || len(page) != 3 || page[0].Idx != 3 || page[2].Idx != 5 {
		t.Fatalf("page = %+v more=%v %v", page, more, err)
	}
	after = 5
	page, more, err = s.ListSessionTurnsPage(ctx, sess.ID, &after, 3)
	if err != nil || more || len(page) != 3 || page[2].Idx != 8 {
		t.Fatalf("last page = %+v more=%v %v", page, more, err)
	}
	first, last, err := s.SessionTurnOverview(ctx, sess.ID)
	if err != nil || first.Idx != 2 || last.Idx != 7 {
		t.Fatalf("overview = %d %d %v", first.Idx, last.Idx, err)
	}
	calls, err := s.ListSessionToolCallTurns(ctx, sess.ID)
	if err != nil || len(calls) != 2 || calls[0].Idx != 4 || calls[1].Idx != 6 {
		t.Fatalf("tool call turns = %+v %v", calls, err)
	}

	// Timeline search through the trigram-indexable predicate keeps the
	// substring rule, including LIKE metacharacters.
	if err := s.AppendSessionTurn(ctx, SessionTurn{SessionID: sess.ID, Idx: 9, Role: "user", TextPreview: "50% of a_b DONE"}); err != nil {
		t.Fatal(err)
	}
	for q, want := range map[string]bool{"0% of a_b": true, "% OF A": true, "a_b done": true, "axb": false, "0%x": false} {
		items, _, err := s.ListTimeline(ctx, TimelineQuery{UserID: bob, ProjectID: shared, Query: q, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, it := range items {
			found = found || it.SessionID == sess.ID
		}
		if found != want {
			t.Fatalf("search %q found=%v; want %v", q, found, want)
		}
	}
}
