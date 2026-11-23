package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"central-memory/internal/store"
)

func TestPointInTimeGrantCannotReadLiveSessionContent(t *testing.T) {
	s := newTestServer()
	mem := s.Store.(*store.MemStore)
	ctx := t.Context()
	proj, err := mem.ResolveProject(ctx, "", "", "pit-live-content")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ClaimProject(ctx, proj.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	owner := loginAs(t, s, "owner")
	grantee := loginAs(t, s, "grantee")

	rec := doJSON(t, s, http.MethodPost, "/v1/agent-sessions", owner, map[string]any{
		"project_id": proj.ID, "harness": "claude", "native_id": "ses_pit_live",
		"origin_machine_id": "lap", "summary": "stored summary",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("session: %d %s", rec.Code, rec.Body.String())
	}
	var sess store.AgentSession
	decodeBody(t, rec, &sess)
	if err := mem.AppendSessionTurn(ctx, store.SessionTurn{
		SessionID: sess.ID, Idx: 1, Role: "user", TextPreview: "live transcript",
	}); err != nil {
		t.Fatal(err)
	}

	raw := []byte("pinned transcript")
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	if err := mem.PutBlob(ctx, proj.ID, hash, "plain", "transcript", raw); err != nil {
		t.Fatal(err)
	}
	ver, err := mem.CreateSessionVersion(ctx, sess.ID, "owner")
	if err != nil {
		t.Fatal(err)
	}
	manifest, _ := json.Marshal(map[string]any{"transcript": map[string]string{"blob": "sha256:" + hash}})
	if _, err := mem.CompleteSessionVersion(ctx, sess.ID, ver.Version, manifest); err != nil {
		t.Fatal(err)
	}
	rec = doJSON(t, s, http.MethodPut, "/v1/agent-sessions/"+sess.ID+"/grants/grantee", owner, map[string]any{
		"live": false, "version_id": ver.ID,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("pit grant: %d %s", rec.Code, rec.Body.String())
	}

	for _, path := range []string{"/turns", "/fetch", "/files", "/operations", "/memories"} {
		rec = doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+sess.ID+path, grantee, nil)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("PIT GET %s = %d %s, want 403", path, rec.Code, rec.Body.String())
		}
	}
	rec = doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+sess.ID+"/versions", grantee, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), ver.ID) {
		t.Fatalf("PIT versions: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+sess.ID+"/summary", grantee, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("PIT summary: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodPut, "/v1/agent-sessions/"+sess.ID+"/grants/grantee", owner, map[string]any{
		"live": true,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("live grant: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+sess.ID+"/turns", grantee, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "live transcript") {
		t.Fatalf("live GET turns: %d %s", rec.Code, rec.Body.String())
	}
}

func TestReadGrantCannotWriteAgentSession(t *testing.T) {
	s := newTestServer()
	mem := s.Store.(*store.MemStore)
	ctx := t.Context()
	proj, err := mem.ResolveProject(ctx, "", "", "read-only-grant")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ClaimProject(ctx, proj.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	owner := loginAs(t, s, "owner")
	grantee := loginAs(t, s, "grantee")

	rec := doJSON(t, s, http.MethodPost, "/v1/agent-sessions", owner, map[string]any{
		"project_id": proj.ID, "harness": "claude", "native_id": "ses_read_only",
		"origin_machine_id": "lap",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("session: %d %s", rec.Code, rec.Body.String())
	}
	var sess store.AgentSession
	decodeBody(t, rec, &sess)
	if err := mem.GrantAgentSession(ctx, sess.ID, "grantee", "owner"); err != nil {
		t.Fatal(err)
	}

	rec = doJSON(t, s, http.MethodPost, "/v1/agent-sessions/"+sess.ID+"/turns", grantee, map[string]any{
		"idx": 1, "role": "user", "text_preview": "forged",
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("grantee turn write: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodPost, "/v1/agent-sessions/"+sess.ID+"/turns/batch", grantee, map[string]any{
		"turns": []map[string]any{{"idx": 1, "role": "user", "text_preview": "forged"}},
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("grantee batch write: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodPost, "/v1/agent-sessions/"+sess.ID+"/versions", grantee, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("grantee version write: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodPost, "/v1/agent-sessions/"+sess.ID+"/fork", grantee, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("grantee fork: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodPost, "/v1/agent-sessions/"+sess.ID+"/merge-code", grantee, map[string]any{
		"from_session_id": sess.ID,
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("grantee merge: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodPost, "/v1/continue", grantee, map[string]any{
		"session_id": sess.ID, "mode": "here", "resume": "native",
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("grantee continue: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+sess.ID+"/forks", grantee, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("grantee forks: %d %s", rec.Code, rec.Body.String())
	}
}

func TestStorageUsageScopeRequiresAccess(t *testing.T) {
	s := newTestServer()
	mem := s.Store.(*store.MemStore)
	owner := loginAs(t, s, "owner")
	grantee := loginAs(t, s, "grantee")
	if err := mem.SetStorageUsage(t.Context(), "user:owner", 5, 10); err != nil {
		t.Fatal(err)
	}
	rec := doJSON(t, s, http.MethodGet, "/v1/storage/usage?scope=user:owner", grantee, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("grantee read another user's usage: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodGet, "/v1/storage/usage?scope=user:grantee", grantee, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("grantee own usage: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodGet, "/v1/storage/usage?scope=user:owner", owner, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"used":5`) {
		t.Fatalf("owner usage: %d %s", rec.Code, rec.Body.String())
	}
}
