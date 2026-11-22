package server

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"central-memory/internal/store"
)

func sendTeleport(t *testing.T, s *Server, fromTok, sessionID, to string) store.Teleport {
	t.Helper()
	rec := doJSON(t, s, http.MethodPost, "/v1/teleports", fromTok, map[string]any{"session_id": sessionID, "to_user_id": to})
	if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("send: %d %s", rec.Code, rec.Body.String())
	}
	var tp store.Teleport
	decodeBody(t, rec, &tp)
	return tp
}

// Whatever order revoke and accept arrive in, a revoked teleport never
// leaves the recipient able to read the session, and a repeated revoke
// still converges.
func TestTeleportRevokeAndAcceptInEitherOrderEndWithoutAccess(t *testing.T) {
	s := newTestServer()
	pid, sid := sharedSession(t, s, "alice")
	completeVersion(t, s, pid, sid, "alice")
	alice, bob := loginAs(t, s, "alice"), loginAs(t, s, "bob")
	noAccess := func(step string) {
		t.Helper()
		if rec := doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+sid, bob, nil); rec.Code == http.StatusOK {
			t.Fatalf("%s: recipient can still read the session", step)
		}
	}

	// Revoke, then accept.
	tp := sendTeleport(t, s, alice, sid, "bob")
	if rec := doJSON(t, s, http.MethodPost, "/v1/teleports/"+tp.ID+"/revoke", alice, nil); rec.Code != http.StatusOK {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body.String())
	}
	if rec := doJSON(t, s, http.MethodPost, "/v1/teleports/"+tp.ID+"/accept", bob, nil); rec.Code != http.StatusGone {
		t.Fatalf("accept after revoke = %d %s, want 410", rec.Code, rec.Body.String())
	}
	noAccess("revoke then accept")

	// Accept, then revoke, then revoke again (a retry).
	tp = sendTeleport(t, s, alice, sid, "bob")
	if rec := doJSON(t, s, http.MethodPost, "/v1/teleports/"+tp.ID+"/accept", bob, nil); rec.Code != http.StatusOK {
		t.Fatalf("accept: %d %s", rec.Code, rec.Body.String())
	}
	if rec := doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+sid, bob, nil); rec.Code != http.StatusOK {
		t.Fatalf("accepted read: %d %s", rec.Code, rec.Body.String())
	}
	for i := 0; i < 2; i++ {
		if rec := doJSON(t, s, http.MethodPost, "/v1/teleports/"+tp.ID+"/revoke", alice, nil); rec.Code != http.StatusOK {
			t.Fatalf("revoke #%d: %d %s", i+1, rec.Code, rec.Body.String())
		}
	}
	noAccess("accept then revoke")
	if rec := doJSON(t, s, http.MethodPost, "/v1/teleports/"+tp.ID+"/accept", bob, nil); rec.Code != http.StatusGone {
		t.Fatalf("re-accept after revoke = %d %s, want 410", rec.Code, rec.Body.String())
	}
	noAccess("re-accept after revoke")

	// Revoke while the teleport is still pending but a grant already exists
	// (an accept that granted after revoke read the status): revoke must
	// remove it even though the status it read was not "accepted".
	tp = sendTeleport(t, s, alice, sid, "bob")
	mem := s.Store.(*store.MemStore)
	if err := mem.GrantAgentSession(t.Context(), sid, "bob", "alice"); err != nil {
		t.Fatal(err)
	}
	if rec := doJSON(t, s, http.MethodPost, "/v1/teleports/"+tp.ID+"/revoke", alice, nil); rec.Code != http.StatusOK {
		t.Fatalf("revoke pending: %d %s", rec.Code, rec.Body.String())
	}
	noAccess("revoke of a pending teleport with a racing grant")
}

// The accept path re-reads the teleport after granting. A revoke that ran
// between accept's status check and its grant (so it had no grant to remove)
// is caught there and the grant is taken back.
func TestTeleportAcceptRecheckUndoesGrantAfterConcurrentRevoke(t *testing.T) {
	s := newTestServer()
	pid, sid := sharedSession(t, s, "alice")
	completeVersion(t, s, pid, sid, "alice")
	alice, bob := loginAs(t, s, "alice"), loginAs(t, s, "bob")
	mem := s.Store.(*store.MemStore)
	ctx := t.Context()
	req := httptest.NewRequest(http.MethodPost, "/v1/teleports/x/accept", nil).WithContext(ctx)

	// Not revoked: the grant stays.
	tp := sendTeleport(t, s, alice, sid, "bob")
	if err := mem.GrantAgentSession(ctx, sid, "bob", "alice"); err != nil {
		t.Fatal(err)
	}
	revoked, err := s.undoGrantIfTeleportRevoked(req, &tp)
	if err != nil || revoked {
		t.Fatalf("live teleport: revoked=%v err=%v", revoked, err)
	}
	if ok, _ := mem.CanReadAgentSession(ctx, "bob", sid); !ok {
		t.Fatal("re-check removed the grant of a live teleport")
	}

	// Interleaving: accept checked status and granted; the sender's revoke
	// set the status in between and found no grant yet.
	if err := mem.SetTeleportStatus(ctx, tp.ID, "revoked", true); err != nil {
		t.Fatal(err)
	}
	revoked, err = s.undoGrantIfTeleportRevoked(req, &tp)
	if err != nil || !revoked {
		t.Fatalf("revoked teleport: revoked=%v err=%v", revoked, err)
	}
	if ok, _ := mem.CanReadAgentSession(ctx, "bob", sid); ok {
		t.Fatal("grant survived a revoke that raced the accept")
	}
	if rec := doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+sid, bob, nil); rec.Code == http.StatusOK {
		t.Fatal("recipient can read the session of a revoked teleport")
	}
}

// A snapshot shared with B stays on B's timeline when the owner's chat is
// later captured as a modern session B cannot read. The owner still sees the
// chat once (the modern session).
func TestTimelineKeepsSharedSnapshotWhenModernCopyIsPrivate(t *testing.T) {
	s := newTestServer()
	mem := s.Store.(*store.MemStore)
	ctx := t.Context()
	project, err := mem.ResolveProject(ctx, "", "", "shared-snapshot")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ClaimProject(ctx, project.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := mem.GrantMember(ctx, project.ID, "bob", "alice"); err != nil {
		t.Fatal(err)
	}
	if err := mem.UpsertSessionSnapshot(ctx, &store.SessionSnapshot{
		SessionID: "native-1", ProjectID: project.ID, OwnerUserID: "alice",
		Harness: "claude", ConversationID: "native-1", TurnCount: 5,
	}); err != nil {
		t.Fatal(err)
	}
	if err := mem.GrantSessionContent(ctx, "native-1", project.ID, "bob", "alice"); err != nil {
		t.Fatal(err)
	}
	modern, err := mem.UpsertAgentSession(ctx, &store.AgentSession{
		ProjectID: project.ID, OwnerUserID: "alice", Harness: "claude", NativeID: "native-1",
		OriginMachineID: "pc", Visibility: "private",
	})
	if err != nil {
		t.Fatal(err)
	}
	list := func(tok, query string) map[string]int {
		t.Helper()
		path := "/v1/timeline?project_id=" + url.QueryEscape(project.ID)
		if query != "" {
			path += "&q=" + url.QueryEscape(query)
		}
		rec := doJSON(t, s, http.MethodGet, path, tok, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("timeline: %d %s", rec.Code, rec.Body.String())
		}
		var body struct {
			Items []store.TimelineItem `json:"items"`
		}
		decodeBody(t, rec, &body)
		ids := map[string]int{}
		for _, it := range body.Items {
			ids[it.SessionID]++
		}
		return ids
	}
	alice, bob := loginAs(t, s, "alice"), loginAs(t, s, "bob")
	if ids := list(bob, ""); ids["native-1"] != 1 || ids[modern.ID] != 0 {
		t.Fatalf("bob's timeline = %v; want the shared snapshot, not the private session", ids)
	}
	// The search path skips "captured" snapshots too; it must use the same rule.
	if ids := list(bob, "native-1"); ids["native-1"] != 1 {
		t.Fatalf("bob's search = %v; want the shared snapshot", ids)
	}
	if ids := list(alice, ""); ids["native-1"] != 0 || ids[modern.ID] != 1 {
		t.Fatalf("alice's timeline = %v; want only the modern session, once", ids)
	}
	// Once bob can read the modern copy, it replaces the snapshot for him too.
	if err := mem.SetAgentSessionVisibility(ctx, modern.ID, "team"); err != nil {
		t.Fatal(err)
	}
	if ids := list(bob, ""); ids["native-1"] != 0 || ids[modern.ID] != 1 {
		t.Fatalf("bob's timeline with a team session = %v; want only the modern session", ids)
	}
}

// unreadableSnapshotStore fails GetLatestSnapshot for one session, as a
// Postgres row with a broken payload or a transient read error would.
type unreadableSnapshotStore struct {
	*store.MemStore
	bad string
}

func (u unreadableSnapshotStore) GetLatestSnapshot(ctx context.Context, sessionID string) (*store.SessionSnapshot, error) {
	if sessionID == u.bad {
		return nil, errors.New("snapshot payload unreadable")
	}
	return u.MemStore.GetLatestSnapshot(ctx, sessionID)
}

// One snapshot whose payload cannot be read drops out of a search; it does
// not turn the whole timeline into a 500.
func TestTimelineSearchSkipsUnreadableSnapshot(t *testing.T) {
	mem := store.NewMemStore()
	s := NewServer(unreadableSnapshotStore{MemStore: mem, bad: "broken"})
	s.Log.SetOutput(&bytes.Buffer{})
	ctx := t.Context()
	project, err := mem.ResolveProject(ctx, "", "", "unreadable-snapshot")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ClaimProject(ctx, project.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	for _, snap := range []*store.SessionSnapshot{
		{SessionID: "broken", ProjectID: project.ID, OwnerUserID: "owner", Harness: "codex", ConversationID: "broken"},
		{SessionID: "good", ProjectID: project.ID, OwnerUserID: "owner", Harness: "codex", ConversationID: "good",
			TranscriptPayload: gzipJSONL(t, `{"timestamp":"2026-01-02T03:04:05Z","type":"response_item","payload":{"role":"user","content":"find the zebra please"}}`)},
	} {
		if err := mem.UpsertSessionSnapshot(ctx, snap); err != nil {
			t.Fatal(err)
		}
	}
	rec := doJSON(t, s, http.MethodGet, "/v1/timeline?q=zebra&project_id="+url.QueryEscape(project.ID), loginAs(t, s, "owner"), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("search with an unreadable snapshot = %d %s, want 200", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"good"`) || strings.Contains(rec.Body.String(), `"broken"`) {
		t.Fatalf("search results = %s; want the readable match only", rec.Body.String())
	}
}
