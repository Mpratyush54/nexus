package server

import (
	"net/http"
	"testing"
)

func TestWorkspaceLocalViaHeartbeatChannel(t *testing.T) {
	s := newTestServer()
	alice := loginAs(t, s, "alice")
	proj := resolveTestProject(t, s, alice, "local-bridge")

	// Use the workspace resolveTestProject already registered.
	rec := doJSON(t, s, http.MethodGet, "/workspaces/"+proj+"/active", alice, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("active = %d body=%s", rec.Code, rec.Body.String())
	}
	var ws struct {
		ID string `json:"id"`
	}
	decodeBody(t, rec, &ws)
	if ws.ID == "" {
		t.Fatal("empty workspace id")
	}

	rec = doJSON(t, s, http.MethodPost, "/workspaces/heartbeat", alice, map[string]any{
		"workspace_id": ws.ID,
		"branch":       "feat/x",
		"commit_sha":   "def456",
		"is_dirty":     true,
		"proxy_url":    "http://127.0.0.1:19001",
		"git_log":      "def456 feat",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("heartbeat = %d body=%s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodGet, "/workspaces/"+proj+"/local", alice, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("local after hb = %d body=%s", rec.Code, rec.Body.String())
	}
	var after map[string]any
	decodeBody(t, rec, &after)
	if after["online"] != true {
		t.Fatalf("expected online: %+v", after)
	}
	if after["branch"] != "feat/x" {
		t.Fatalf("branch = %v", after["branch"])
	}
	if after["proxy_url"] != "http://127.0.0.1:19001" {
		t.Fatalf("proxy_url = %v (want advertised bridge, not hardcoded)", after["proxy_url"])
	}
	if after["git_log"] != "def456 feat" {
		t.Fatalf("git_log = %v", after["git_log"])
	}
	if after["source"] != "server" {
		t.Fatalf("source = %v, want server channel", after["source"])
	}
}
