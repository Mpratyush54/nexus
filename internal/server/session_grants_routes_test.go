package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"testing"

	"central-memory/internal/store"
)

func sharedSession(t *testing.T, s *Server, owner string) (projectID, sessionID string) {
	t.Helper()
	mem := s.Store.(*store.MemStore)
	ctx := t.Context()
	project, err := mem.ResolveProject(ctx, "", "", "grants-"+owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ClaimProject(ctx, project.ID, owner); err != nil {
		t.Fatal(err)
	}
	rec := doJSON(t, s, http.MethodPost, "/v1/agent-sessions", loginAs(t, s, owner), map[string]any{
		"project_id": project.ID, "harness": "codex", "native_id": "grants-chat",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("session: %d %s", rec.Code, rec.Body.String())
	}
	var sess store.AgentSession
	decodeBody(t, rec, &sess)
	return project.ID, sess.ID
}

// The owner can see who a chat is shared with and stop sharing it.
func TestSessionGrantsListAndStopSharing(t *testing.T) {
	s := newTestServer()
	_, sid := sharedSession(t, s, "alice")
	alice, bob, carol := loginAs(t, s, "alice"), loginAs(t, s, "bob"), loginAs(t, s, "carol")
	base := "/v1/agent-sessions/" + sid
	for _, who := range []string{"bob", "carol"} {
		if rec := doJSON(t, s, http.MethodPut, base+"/grants/"+who, alice, map[string]any{"live": true}); rec.Code != http.StatusOK {
			t.Fatalf("grant %s: %d %s", who, rec.Code, rec.Body.String())
		}
	}
	if rec := doJSON(t, s, http.MethodPut, base+"/grants/team", alice, map[string]any{"confirm": true, "live": true}); rec.Code != http.StatusOK {
		t.Fatalf("team: %d %s", rec.Code, rec.Body.String())
	}
	var list struct {
		Team   bool `json:"team"`
		People []struct {
			UserID string `json:"user_id"`
			Name   string `json:"name"`
			Live   bool   `json:"live"`
		} `json:"people"`
	}
	rec := doJSON(t, s, http.MethodGet, base+"/grants", alice, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	decodeBody(t, rec, &list)
	if !list.Team || len(list.People) != 2 || list.People[0].UserID != "bob" || !list.People[0].Live || list.People[0].Name == "" {
		t.Fatalf("list = %+v", list)
	}
	// Only the owner sees the list or can stop sharing.
	if rec := doJSON(t, s, http.MethodGet, base+"/grants", bob, nil); rec.Code == http.StatusOK {
		t.Fatal("a grantee listed the shares")
	}
	if rec := doJSON(t, s, http.MethodDelete, base+"/grants", carol, nil); rec.Code == http.StatusOK {
		t.Fatal("a grantee stopped the sharing")
	}
	if rec := doJSON(t, s, http.MethodDelete, base+"/grants", alice, nil); rec.Code != http.StatusOK {
		t.Fatalf("stop sharing: %d %s", rec.Code, rec.Body.String())
	}
	for _, tok := range []string{bob, carol} {
		if rec := doJSON(t, s, http.MethodGet, base, tok, nil); rec.Code == http.StatusOK {
			t.Fatal("access survived stop sharing")
		}
	}
	rec = doJSON(t, s, http.MethodGet, base+"/grants", alice, nil)
	list.People, list.Team = nil, false
	decodeBody(t, rec, &list)
	if list.Team || len(list.People) != 0 {
		t.Fatalf("after stop sharing: %+v", list)
	}
	// Sharing again works.
	if rec := doJSON(t, s, http.MethodPut, base+"/grants/bob", alice, map[string]any{"live": true}); rec.Code != http.StatusOK {
		t.Fatalf("re-share: %d %s", rec.Code, rec.Body.String())
	}
	if rec := doJSON(t, s, http.MethodGet, base, bob, nil); rec.Code != http.StatusOK {
		t.Fatalf("re-shared read: %d %s", rec.Code, rec.Body.String())
	}
}

func completeVersion(t *testing.T, s *Server, projectID, sessionID, owner string) {
	t.Helper()
	mem := s.Store.(*store.MemStore)
	ctx := t.Context()
	body := []byte("t")
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	if err := mem.PutBlob(ctx, projectID, hash, "plain", "transcript", body); err != nil {
		t.Fatal(err)
	}
	ver, err := mem.CreateSessionVersion(ctx, sessionID, owner)
	if err != nil {
		t.Fatal(err)
	}
	manifest, _ := json.Marshal(map[string]any{"transcript": map[string]string{"blob": "sha256:" + hash}})
	if _, err := mem.CompleteSessionVersion(ctx, sessionID, ver.Version, manifest); err != nil {
		t.Fatal(err)
	}
}

// A teleport the sender took back cannot be accepted, and taking back one
// that was already accepted ends the access it gave.
func TestTeleportRevokeIsFinal(t *testing.T) {
	s := newTestServer()
	pid, sid := sharedSession(t, s, "alice")
	completeVersion(t, s, pid, sid, "alice")
	alice, bob := loginAs(t, s, "alice"), loginAs(t, s, "bob")
	send := func() string {
		rec := doJSON(t, s, http.MethodPost, "/v1/teleports", alice, map[string]any{"session_id": sid, "to_user_id": "bob"})
		if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
			t.Fatalf("send: %d %s", rec.Code, rec.Body.String())
		}
		var tp store.Teleport
		decodeBody(t, rec, &tp)
		return tp.ID
	}

	first := send()
	if rec := doJSON(t, s, http.MethodPost, "/v1/teleports/"+first+"/revoke", alice, nil); rec.Code != http.StatusOK {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body.String())
	}
	if rec := doJSON(t, s, http.MethodPost, "/v1/teleports/"+first+"/accept", bob, nil); rec.Code != http.StatusGone {
		t.Fatalf("accept after revoke = %d %s, want 410", rec.Code, rec.Body.String())
	}
	if rec := doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+sid, bob, nil); rec.Code == http.StatusOK {
		t.Fatal("revoked teleport gave access")
	}

	second := send()
	if rec := doJSON(t, s, http.MethodPost, "/v1/teleports/"+second+"/accept", bob, nil); rec.Code != http.StatusOK {
		t.Fatalf("accept: %d %s", rec.Code, rec.Body.String())
	}
	if rec := doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+sid, bob, nil); rec.Code != http.StatusOK {
		t.Fatalf("accepted read: %d %s", rec.Code, rec.Body.String())
	}
	if rec := doJSON(t, s, http.MethodPost, "/v1/teleports/"+second+"/revoke", alice, nil); rec.Code != http.StatusOK {
		t.Fatalf("revoke accepted: %d %s", rec.Code, rec.Body.String())
	}
	if rec := doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+sid, bob, nil); rec.Code == http.StatusOK {
		t.Fatal("access survived taking the teleport back")
	}
}
