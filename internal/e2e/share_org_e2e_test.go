// Package e2e drives the desktop core's nx_call methods against a real HTTP
// server, as several signed-in people, to check the sharing and organization
// journeys end to end: the same requests the app sends, over the wire, through
// the real routes and authorization.
package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"central-memory/internal/core"
	"central-memory/internal/server"
	"central-memory/internal/store"
)

type world struct {
	t      *testing.T
	srv    *server.Server
	http   *httptest.Server
	tokens map[string]string
}

func newWorld(t *testing.T, people ...string) *world {
	t.Helper()
	s := server.NewServer(store.NewMemStore())
	s.Log.SetOutput(io.Discard)
	s.Auth = server.NewAuthenticator([]byte("e2e-only-key-0123456789abcdef0123"))
	w := &world{t: t, srv: s, http: httptest.NewServer(s), tokens: map[string]string{}}
	t.Cleanup(w.http.Close)
	for _, p := range people {
		tok, err := s.Auth.Generate(p, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		w.tokens[p] = tok
	}
	return w
}

func (w *world) mem() *store.MemStore { return w.srv.Store.(*store.MemStore) }

// call runs one nx method as person, exactly as the desktop app would.
func (w *world) call(person, method string, args any) (map[string]any, error) {
	w.t.Helper()
	raw, _ := json.Marshal(args)
	deps := core.Deps{Online: true, Cloud: &core.HTTPCloud{BaseURL: w.http.URL, Token: w.tokens[person]}, ContinuationDir: w.t.TempDir()}
	out, err := core.Call(context.Background(), method, raw, deps)
	if err != nil {
		return nil, err
	}
	m, _ := out.(map[string]any)
	return m, nil
}

func (w *world) must(person, method string, args any) map[string]any {
	w.t.Helper()
	out, err := w.call(person, method, args)
	if err != nil {
		w.t.Fatalf("%s %s: %v", person, method, err)
	}
	return out
}

// status returns the HTTP status of a failed call (0 when it succeeded).
func (w *world) status(person, method string, args any) int {
	w.t.Helper()
	_, err := w.call(person, method, args)
	if err == nil {
		return 0
	}
	var ce *core.CloudError
	if errors.As(err, &ce) {
		return ce.Status
	}
	w.t.Fatalf("%s %s: not an HTTP error: %v", person, method, err)
	return -1
}

func str(m map[string]any, key string) string { s, _ := m[key].(string); return s }

func TestOrganizationJourney(t *testing.T) {
	w := newWorld(t, "alice", "bob", "carol", "mallory")

	org := w.must("alice", "orgs.create", map[string]any{"name": "Acme"})
	orgID := str(org, "id")
	if orgID == "" {
		t.Fatalf("orgs.create = %v", org)
	}

	// Invite Bob; he joins with the code.
	inv := w.must("alice", "orgs.invite_create", map[string]any{"org_id": orgID, "email": "bob@example.test", "role": "MEMBER"})
	invites := w.must("alice", "orgs.invites", map[string]any{"org_id": orgID})
	if !strings.Contains(mustJSON(invites), str(inv, "id")) {
		t.Fatalf("pending invites do not list the new one: %v", invites)
	}
	if got := w.status("mallory", "orgs.invites", map[string]any{"org_id": orgID}); got != http.StatusForbidden && got != http.StatusNotFound {
		t.Fatalf("outsider listed invites: %d", got)
	}
	w.must("bob", "orgs.invite_accept", map[string]any{"token": str(inv, "token")})
	if got := w.status("bob", "orgs.invite_accept", map[string]any{"token": str(inv, "token")}); got != http.StatusConflict && got != http.StatusNotFound {
		t.Fatalf("a used invitation must not work twice: %d", got)
	}

	// A revoked invitation cannot be used.
	inv2 := w.must("alice", "orgs.invite_create", map[string]any{"org_id": orgID, "email": "carol@example.test", "role": "MEMBER"})
	w.must("alice", "orgs.invite_revoke", map[string]any{"org_id": orgID, "invite_id": str(inv2, "id")})
	if got := w.status("carol", "orgs.invite_accept", map[string]any{"token": str(inv2, "token")}); got == 0 {
		t.Fatal("revoked invitation was accepted")
	}

	got := w.must("bob", "orgs.get", map[string]any{"org_id": orgID})
	if n := memberCount(got); n != 2 {
		t.Fatalf("members after join = %d (%v)", n, got)
	}
	if !strings.Contains(mustJSON(w.must("bob", "orgs.list", nil)), orgID) {
		t.Fatal("bob's org list lacks the org he joined")
	}

	// Roles: a member cannot promote himself; the owner can.
	if got := w.status("bob", "orgs.member_role", map[string]any{"org_id": orgID, "user_id": "bob", "role": "OWNER"}); got != http.StatusForbidden {
		t.Fatalf("member self-promotion = %d", got)
	}
	w.must("alice", "orgs.member_role", map[string]any{"org_id": orgID, "user_id": "bob", "role": "ADMIN"})

	// The only owner cannot step down or leave, and is told why in words.
	_, err := w.call("alice", "orgs.member_role", map[string]any{"org_id": orgID, "user_id": "alice", "role": "MEMBER"})
	var ce *core.CloudError
	if !errors.As(err, &ce) || ce.Status != http.StatusConflict || !strings.Contains(err.Error(), "at least one owner") || strings.Contains(err.Error(), "{") {
		t.Fatalf("last owner demotion: %v", err)
	}
	if got := w.status("alice", "orgs.member_remove", map[string]any{"org_id": orgID, "user_id": "alice"}); got != http.StatusConflict {
		t.Fatalf("last owner removal = %d", got)
	}

	// Removing Bob takes the org away from him.
	w.must("alice", "orgs.member_remove", map[string]any{"org_id": orgID, "user_id": "bob"})
	if strings.Contains(mustJSON(w.must("bob", "orgs.list", nil)), orgID) {
		t.Fatal("removed member still sees the org")
	}
	if got := w.status("bob", "orgs.get", map[string]any{"org_id": orgID}); got != http.StatusForbidden && got != http.StatusNotFound {
		t.Fatalf("removed member read the org: %d", got)
	}
}

func TestSharingJourney(t *testing.T) {
	w := newWorld(t, "alice", "bob", "carol", "dave", "mallory")
	ctx := context.Background()
	m := w.mem()
	// Acme: alice (owner), bob, carol, dave. Project "app": alice + bob.
	org, err := m.CreateOrganization(ctx, "Acme", "acme-e2e", "alice")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"bob", "carol", "dave"} {
		if _, err := m.AddOrgMember(ctx, org.ID, p, store.OrgRoleMember, "alice"); err != nil {
			t.Fatal(err)
		}
	}
	project, err := m.CreateOrgProject(ctx, org.ID, "app", "App", "", "", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.GrantMember(ctx, project.ID, "bob", "alice"); err != nil {
		t.Fatal(err)
	}

	// Alice's chat, with a message in it.
	sess := w.must("alice", "sessions.create", map[string]any{"project_id": project.ID, "harness": "claude", "native_id": "e2e-chat", "title": "Payment retries"})
	sid := str(sess, "id")
	cloud := &core.HTTPCloud{BaseURL: w.http.URL, Token: w.tokens["alice"]}
	batch, _ := json.Marshal(map[string]any{"turns": []map[string]any{
		{"idx": 1, "role": "user", "text_preview": "How should payment retries back off?", "tool_calls": []any{}},
		{"idx": 2, "role": "assistant", "text_preview": "Exponential backoff with jitter, capped at 5 tries.", "tool_calls": []any{}},
	}})
	if _, err := cloud.Do(ctx, http.MethodPost, "/v1/agent-sessions/"+sid+"/turns/batch", batch); err != nil {
		t.Fatal(err)
	}

	canRead := func(person string) bool {
		t.Helper()
		_, err := w.call(person, "sessions.turns", map[string]any{"session_id": sid})
		return err == nil
	}
	sharedWith := func() map[string]any {
		t.Helper()
		return w.must("alice", "sessions.shared_with", map[string]any{"session_id": sid})
	}
	people := func(m map[string]any) []string {
		var out []string
		list, _ := m["people"].([]any)
		for _, p := range list {
			out = append(out, str(p.(map[string]any), "user_id"))
		}
		return out
	}

	// Private by default: nobody else, not even project members, can read it.
	for _, p := range []string{"bob", "carol", "mallory"} {
		if canRead(p) {
			t.Fatalf("%s read a private chat", p)
		}
	}
	if got := people(sharedWith()); len(got) != 0 {
		t.Fatalf("new chat shared with %v", got)
	}

	// The share dialog's people list: project and org members, never outsiders.
	dir := mustJSON(w.must("alice", "sharing.recipients", map[string]any{"project_id": project.ID}))
	for _, p := range []string{"bob", "carol", "dave"} {
		if !strings.Contains(dir, `"`+p+`"`) {
			t.Fatalf("recipients lack %s: %s", p, dir)
		}
	}
	if strings.Contains(dir, "mallory") {
		t.Fatal("recipients list an outsider")
	}

	// 1. One person (Carol, not in the project).
	w.must("alice", "sessions.share", map[string]any{"session_id": sid, "user_id": "carol", "live": true})
	if !canRead("carol") || canRead("bob") || canRead("dave") {
		t.Fatal("person share reached the wrong people")
	}
	turns := mustJSON(w.must("carol", "sessions.turns", map[string]any{"session_id": sid}))
	if !strings.Contains(turns, "Exponential backoff") {
		t.Fatalf("carol cannot see the conversation: %s", turns)
	}
	if got := people(sharedWith()); len(got) != 1 || got[0] != "carol" {
		t.Fatalf("shared_with = %v", got)
	}
	// Only the owner may share or see who it is shared with.
	if got := w.status("carol", "sessions.share", map[string]any{"session_id": sid, "user_id": "mallory"}); got != http.StatusForbidden && got != http.StatusNotFound {
		t.Fatalf("grantee re-shared: %d", got)
	}
	if got := w.status("carol", "sessions.shared_with", map[string]any{"session_id": sid}); got != http.StatusForbidden && got != http.StatusNotFound {
		t.Fatalf("grantee listed shares: %d", got)
	}
	// Live: a later message reaches the person it is shared with.
	more, _ := json.Marshal(map[string]any{"turns": []map[string]any{{"idx": 3, "role": "user", "text_preview": "And for webhooks?", "tool_calls": []any{}}}})
	if _, err := cloud.Do(ctx, http.MethodPost, "/v1/agent-sessions/"+sid+"/turns/batch", more); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(mustJSON(w.must("carol", "sessions.turns", map[string]any{"session_id": sid})), "webhooks") {
		t.Fatal("live share did not show the newer message")
	}
	// Stop sharing with Carol.
	w.must("alice", "sessions.unshare", map[string]any{"session_id": sid, "user_id": "carol"})
	if canRead("carol") {
		t.Fatal("carol still reads after unshare")
	}

	// 2. The project team (Bob), live.
	w.must("alice", "sessions.share", map[string]any{"session_id": sid, "team": true, "confirm": true, "live": true})
	if !canRead("bob") || canRead("carol") {
		t.Fatal("project share reached the wrong people")
	}
	if team, _ := sharedWith()["team"].(bool); !team {
		t.Fatal("shared_with does not report the project team")
	}
	w.must("alice", "sessions.unshare", map[string]any{"session_id": sid, "team": true})
	if canRead("bob") {
		t.Fatal("bob still reads after project unshare")
	}

	// 3. The whole organization: everyone in Acme, nobody outside.
	if _, err := w.call("alice", "sessions.share", map[string]any{"session_id": sid, "organization_id": org.ID}); err == nil {
		t.Fatal("organization share without confirmation went through")
	}
	w.must("alice", "sessions.share", map[string]any{"session_id": sid, "organization_id": org.ID, "confirm": true})
	for _, p := range []string{"bob", "carol", "dave"} {
		if !canRead(p) {
			t.Fatalf("org share: %s cannot read", p)
		}
	}
	if canRead("mallory") {
		t.Fatal("org share reached an outsider")
	}
	if got := people(sharedWith()); len(got) != 3 {
		t.Fatalf("org share recorded %v", got)
	}

	// Stop sharing with everyone at once, then share again.
	w.must("alice", "sessions.share", map[string]any{"session_id": sid, "team": true, "confirm": true, "live": true})
	w.must("alice", "sessions.unshare", map[string]any{"session_id": sid})
	for _, p := range []string{"bob", "carol", "dave"} {
		if canRead(p) {
			t.Fatalf("%s still reads after stop sharing", p)
		}
	}
	if s := sharedWith(); len(people(s)) != 0 || s["team"] == true {
		t.Fatalf("after stop sharing: %v", s)
	}
	w.must("alice", "sessions.share", map[string]any{"session_id": sid, "user_id": "dave", "live": true})
	if !canRead("dave") {
		t.Fatal("re-sharing after a revoke does not work")
	}

	// Strangers cannot share, unshare or see the chat at all.
	if got := w.status("mallory", "sessions.unshare", map[string]any{"session_id": sid}); got != http.StatusForbidden && got != http.StatusNotFound {
		t.Fatalf("outsider unshare = %d", got)
	}
	// The shared chat shows in the recipient's chat list.
	if !strings.Contains(mustJSON(w.must("dave", "timeline.list", map[string]any{"limit": 50})), sid) {
		t.Fatal("shared chat missing from the recipient's chat list")
	}
}

func memberCount(org map[string]any) int {
	if list, ok := org["members"].([]any); ok {
		return len(list)
	}
	return -1
}

func mustJSON(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		log.Fatal(err)
	}
	return string(raw)
}

func TestTeleportJourney(t *testing.T) {
	w := newWorld(t, "alice", "bob", "mallory")
	ctx := context.Background()
	m := w.mem()
	project, err := m.ResolveProject(ctx, "", "", "teleport-e2e")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.ClaimProject(ctx, project.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	sess := w.must("alice", "sessions.create", map[string]any{"project_id": project.ID, "harness": "codex", "native_id": "tele-e2e"})
	sid := str(sess, "id")
	// A teleport sends a complete, immutable version of the chat.
	body := []byte("transcript")
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	if err := m.PutBlob(ctx, project.ID, hash, "plain", "transcript", body); err != nil {
		t.Fatal(err)
	}
	ver, err := m.CreateSessionVersion(ctx, sid, "alice")
	if err != nil {
		t.Fatal(err)
	}
	manifest, _ := json.Marshal(map[string]any{"transcript": map[string]string{"blob": "sha256:" + hash}})
	if _, err := m.CompleteSessionVersion(ctx, sid, ver.Version, manifest); err != nil {
		t.Fatal(err)
	}

	sent := w.must("alice", "teleport.send", map[string]any{"session_id": sid, "to_user_id": "bob"})
	tid := str(sent, "id")
	if tid == "" {
		t.Fatalf("teleport.send = %v", sent)
	}
	if !strings.Contains(mustJSON(w.must("bob", "teleport.inbox", nil)), tid) {
		t.Fatal("teleport missing from the recipient's inbox")
	}
	if strings.Contains(mustJSON(w.must("mallory", "teleport.inbox", nil)), tid) {
		t.Fatal("teleport visible to someone it was not sent to")
	}
	if !strings.Contains(mustJSON(w.must("alice", "teleport.sent", nil)), tid) {
		t.Fatal("teleport missing from the sender's sent list")
	}
	if got := w.status("mallory", "teleport.apply", map[string]any{"id": tid}); got == 0 {
		t.Fatal("someone else accepted the teleport")
	}
	w.must("bob", "teleport.apply", map[string]any{"id": tid})

	// A second teleport the sender takes back before it is opened.
	again := w.must("alice", "teleport.send", map[string]any{"session_id": sid, "to_user_id": "bob"})
	if got := w.status("mallory", "teleport.revoke", map[string]any{"id": str(again, "id")}); got == 0 {
		t.Fatal("someone else revoked the teleport")
	}
	w.must("alice", "teleport.revoke", map[string]any{"id": str(again, "id")})
	if got := w.status("bob", "teleport.apply", map[string]any{"id": str(again, "id")}); got == 0 {
		t.Fatal("a revoked teleport was accepted")
	}
}

// Taking back a chat the recipient already opened ends their access.
func TestTeleportRevokeAfterAcceptEndsAccess(t *testing.T) {
	w := newWorld(t, "alice", "bob")
	ctx := context.Background()
	m := w.mem()
	project, _ := m.ResolveProject(ctx, "", "", "teleport-revoke")
	if _, err := m.ClaimProject(ctx, project.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	sid := str(w.must("alice", "sessions.create", map[string]any{"project_id": project.ID, "harness": "codex", "native_id": "tele-revoke"}), "id")
	body := []byte("t")
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	_ = m.PutBlob(ctx, project.ID, hash, "plain", "transcript", body)
	ver, _ := m.CreateSessionVersion(ctx, sid, "alice")
	manifest, _ := json.Marshal(map[string]any{"transcript": map[string]string{"blob": "sha256:" + hash}})
	if _, err := m.CompleteSessionVersion(ctx, sid, ver.Version, manifest); err != nil {
		t.Fatal(err)
	}
	tid := str(w.must("alice", "teleport.send", map[string]any{"session_id": sid, "to_user_id": "bob"}), "id")
	w.must("bob", "teleport.apply", map[string]any{"id": tid})
	if _, err := w.call("bob", "sessions.get", map[string]any{"session_id": sid}); err != nil {
		t.Fatalf("accepted teleport gives no access: %v", err)
	}
	w.must("alice", "teleport.revoke", map[string]any{"id": tid})
	if _, err := w.call("bob", "sessions.get", map[string]any{"session_id": sid}); err == nil {
		t.Fatal("recipient keeps access after the sender took the chat back")
	}
}
