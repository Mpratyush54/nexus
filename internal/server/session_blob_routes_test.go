package server

import (
	"encoding/base64"
	"net/http"
	"testing"

	"central-memory/internal/store"
)

func TestSessionVersionBlobRequiresVisibleManifestAndGrant(t *testing.T) {
	s := newTestServer()
	mem := s.Store.(*store.MemStore)
	project, err := mem.ResolveProject(t.Context(), "", "", "code-package")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ClaimProject(t.Context(), project.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	owner := loginAs(t, s, "owner")
	other := loginAs(t, s, "other")
	rec := doJSON(t, s, http.MethodPost, "/v1/agent-sessions", owner, map[string]any{
		"project_id": project.ID, "harness": "codex", "native_id": "native-1",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create session: %d %s", rec.Code, rec.Body.String())
	}
	var session store.AgentSession
	decodeBody(t, rec, &session)
	transcript := []byte("transcript")
	file := []byte("package main\n")
	for _, item := range []struct {
		data    []byte
		purpose string
	}{{transcript, "transcript"}, {file, "file"}} {
		rec = doJSON(t, s, http.MethodPut, "/v1/blobs/"+shaHex(item.data), owner, map[string]any{
			"project_id": project.ID, "purpose": item.purpose, "body_b64": base64.StdEncoding.EncodeToString(item.data),
		})
		if rec.Code != http.StatusCreated {
			t.Fatalf("put blob: %d %s", rec.Code, rec.Body.String())
		}
	}
	base := "/v1/agent-sessions/" + session.ID
	rec = doJSON(t, s, http.MethodPost, base+"/versions", owner, map[string]any{})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create version: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodPost, base+"/versions/1/complete", owner, map[string]any{
		"transcript": map[string]any{"blob": "sha256:" + shaHex(transcript)},
		"files":      []map[string]any{{"path": "main.go", "blob": "sha256:" + shaHex(file), "size": len(file)}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("complete version: %d %s", rec.Code, rec.Body.String())
	}
	path := base + "/versions/1/blobs/" + shaHex(file)
	rec = doJSON(t, s, http.MethodGet, path, owner, nil)
	if rec.Code != http.StatusOK || rec.Body.String() != string(file) {
		t.Fatalf("owner download: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodGet, path, other, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("stranger download: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodGet, base+"/versions/1/blobs/"+shaHex(transcript), owner, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("non-file blob: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodPost, base+"/grants", owner, map[string]any{"user_id": "other"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("grant: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodGet, path, other, nil)
	if rec.Code != http.StatusOK || rec.Body.String() != string(file) {
		t.Fatalf("grantee download: %d %s", rec.Code, rec.Body.String())
	}
}
