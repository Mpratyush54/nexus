package store

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestSessionACLOwnerGrantAndAuditChain(t *testing.T) {
	m := NewMemStore()
	ctx := t.Context()
	sid := "11111111-1111-1111-1111-111111111111"
	pid := "22222222-2222-2222-2222-222222222222"

	if err := m.UpsertSessionSnapshot(ctx, &SessionSnapshot{
		SessionID: sid, ProjectID: pid, Harness: "antigravity",
		ConversationID: "c", TranscriptPayload: []byte("gz"),
		OwnerUserID: "owner",
	}); err != nil {
		t.Fatal(err)
	}
	owner, projectID, found, err := m.GetSessionContentOwner(ctx, sid)
	if err != nil || !found || owner != "owner" || projectID != pid {
		t.Fatalf("owner=%q project=%q found=%v err=%v", owner, projectID, found, err)
	}
	// A second version cannot replace the owner.
	if err := m.UpsertSessionSnapshot(ctx, &SessionSnapshot{
		SessionID: sid, ProjectID: pid, Harness: "antigravity",
		ConversationID: "c", TranscriptPayload: []byte("gz2"),
		OwnerUserID: "thief",
	}); err != nil {
		t.Fatal(err)
	}
	latest, err := m.GetLatestSnapshot(ctx, sid)
	if err != nil || latest.OwnerUserID != "owner" || latest.SnapshotVersion != 2 {
		t.Fatalf("latest=%+v err=%v", latest, err)
	}
	ok, err := m.HasSessionContentGrant(ctx, sid, "bob")
	if err != nil || ok {
		t.Fatalf("grant before = %v err=%v", ok, err)
	}
	if err := m.GrantSessionContent(ctx, sid, pid, "bob", "owner"); err != nil {
		t.Fatal(err)
	}
	ok, err = m.HasSessionContentGrant(ctx, sid, "bob")
	if err != nil || !ok {
		t.Fatalf("grant after = %v err=%v", ok, err)
	}

	orphan := "33333333-3333-3333-3333-333333333333"
	if err := m.UpsertSessionSnapshot(ctx, &SessionSnapshot{
		SessionID: orphan, ProjectID: pid, Harness: "antigravity",
		ConversationID: "o", TranscriptPayload: []byte("gz"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := m.AssignSessionOwner(ctx, orphan, "assigned"); err != nil {
		t.Fatal(err)
	}
	if err := m.AssignSessionOwner(ctx, orphan, "other"); err == nil {
		t.Fatal("second assign should conflict")
	}
	if err := m.DeleteOwnerlessSessionContent(ctx, orphan); err == nil {
		t.Fatal("delete of owned session should conflict")
	}

	orphan2 := "44444444-4444-4444-4444-444444444444"
	if err := m.UpsertSessionSnapshot(ctx, &SessionSnapshot{
		SessionID: orphan2, ProjectID: pid, Harness: "antigravity",
		ConversationID: "o2", TranscriptPayload: []byte("gz"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := m.DeleteOwnerlessSessionContent(ctx, orphan2); err != nil {
		t.Fatal(err)
	}
	if _, err := m.GetLatestSnapshot(ctx, orphan2); err != ErrNotFound {
		t.Fatalf("deleted snapshot err=%v", err)
	}

	if err := m.AppendAudit(ctx, AuditEvent{Action: "member.invited", ProjectID: pid, ActorUserID: "owner", ResourceID: "bob"}); err != nil {
		t.Fatal(err)
	}
	if err := m.AppendAudit(ctx, AuditEvent{Action: "member.removed", ProjectID: pid, ActorUserID: "owner", ResourceID: "bob"}); err != nil {
		t.Fatal(err)
	}
	evs, err := m.ListAuditEvents(ctx, pid, "", 10)
	if err != nil || len(evs) != 2 {
		t.Fatalf("events=%v err=%v", evs, err)
	}
	if evs[1].PrevHash != auditChainHash("", evs[1], "{}") {
		t.Fatalf("first hash mismatch %s", evs[1].PrevHash)
	}
	if evs[0].PrevHash != auditChainHash(evs[1].PrevHash, evs[0], "{}") {
		t.Fatalf("second hash mismatch %s", evs[0].PrevHash)
	}
}

func TestSessionACLPostgres(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := t.Context()
	s, err := NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	var ownerID, otherID, projectID string
	err = s.pool.QueryRow(ctx, `SELECT id::text FROM users ORDER BY created_at LIMIT 1`).Scan(&ownerID)
	if errors.Is(err, pgx.ErrNoRows) {
		t.Skip("no users")
	}
	if err != nil {
		t.Fatal(err)
	}
	err = s.pool.QueryRow(ctx, `SELECT id::text FROM users WHERE id <> $1::uuid ORDER BY created_at LIMIT 1`, ownerID).Scan(&otherID)
	if errors.Is(err, pgx.ErrNoRows) {
		t.Skip("need two users")
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := s.pool.QueryRow(ctx, `SELECT id::text FROM projects LIMIT 1`).Scan(&projectID); err != nil {
		t.Fatal(err)
	}

	sid := "0199aaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	cleanup := func() {
		// t.Context() is canceled before Cleanup runs.
		bg := context.Background()
		for _, q := range []string{
			`DELETE FROM session_snapshots WHERE session_id = $1::uuid`,
			`DELETE FROM session_content_grants WHERE session_id = $1::uuid`,
			`DELETE FROM session_content_owners WHERE session_id = $1::uuid`,
			`DELETE FROM audit_events WHERE request_id = $1`,
		} {
			_, _ = s.pool.Exec(bg, q, sid)
		}
	}
	cleanup()
	t.Cleanup(cleanup)

	if err := s.UpsertSessionSnapshot(ctx, &SessionSnapshot{
		SessionID: sid, ProjectID: projectID, Harness: "antigravity",
		ConversationID: "c", TranscriptPayload: []byte("gz"), OwnerUserID: ownerID,
	}); err != nil {
		t.Fatal(err)
	}
	got, proj, found, err := s.GetSessionContentOwner(ctx, sid)
	if err != nil || !found || got != ownerID || proj != projectID {
		t.Fatalf("owner=%s project=%s found=%v err=%v", got, proj, found, err)
	}
	if err := s.UpsertSessionSnapshot(ctx, &SessionSnapshot{
		SessionID: sid, ProjectID: projectID, Harness: "antigravity",
		ConversationID: "c", TranscriptPayload: []byte("gz2"), OwnerUserID: otherID,
	}); err != nil {
		t.Fatal(err)
	}
	latest, err := s.GetLatestSnapshot(ctx, sid)
	if err != nil || latest.OwnerUserID != ownerID || latest.SnapshotVersion != 2 {
		t.Fatalf("latest owner=%s ver=%d err=%v", latest.OwnerUserID, latest.SnapshotVersion, err)
	}
	if err := s.GrantSessionContent(ctx, sid, projectID, otherID, ownerID); err != nil {
		t.Fatal(err)
	}
	ok, err := s.HasSessionContentGrant(ctx, sid, otherID)
	if err != nil || !ok {
		t.Fatalf("grant=%v err=%v", ok, err)
	}
	if err := s.AppendAudit(ctx, AuditEvent{
		Action: "member.invited", ProjectID: projectID, ActorUserID: ownerID,
		ResourceKind: "session", ResourceID: sid, RequestID: sid,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendAudit(ctx, AuditEvent{
		Action: "member.removed", ProjectID: projectID, ActorUserID: ownerID,
		ResourceKind: "session", ResourceID: sid, RequestID: sid,
	}); err != nil {
		t.Fatal(err)
	}
	evs, err := s.ListAuditEvents(ctx, projectID, "", 20)
	if err != nil {
		t.Fatal(err)
	}
	var chain []AuditEvent
	for _, ev := range evs {
		if ev.RequestID == sid {
			chain = append(chain, ev)
		}
	}
	if len(chain) != 2 {
		t.Fatalf("audit rows=%d", len(chain))
	}
	if chain[0].PrevHash == "" || chain[1].PrevHash == "" {
		t.Fatalf("missing chain hash %+v", chain)
	}
	// Newest links to the previous event's hash, using the timestamp that
	// round-tripped through timestamptz.
	if chain[0].PrevHash != auditChainHash(chain[1].PrevHash, chain[0], "{}") {
		t.Fatalf("postgres chain mismatch %s", chain[0].PrevHash)
	}
}
