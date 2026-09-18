package server

import (
	"net/http"
	"testing"
)

func TestGitHubConnectStatusDisconnect(t *testing.T) {
	s := newTestServer()
	alice := loginAs(t, s, "alice")
	proj := resolveTestProject(t, s, alice, "github-demo")

	rec := doJSON(t, s, http.MethodPost, "/projects/"+proj+"/github/connect", alice, map[string]any{
		"owner":        "acme",
		"repo":         "nexus",
		"access_token": "gho_x",
		"sync_mode":    "manual",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("connect = %d body=%s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodGet, "/projects/"+proj+"/github/status", alice, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var status map[string]any
	decodeBody(t, rec, &status)
	if status["connected"] != true {
		t.Fatalf("expected connected: %+v", status)
	}
	if status["owner"] != "acme" || status["repo"] != "nexus" {
		t.Fatalf("unexpected status: %+v", status)
	}

	rec = doJSON(t, s, http.MethodDelete, "/projects/"+proj+"/github/disconnect", alice, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("disconnect = %d body=%s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodGet, "/projects/"+proj+"/github/status", alice, nil)
	decodeBody(t, rec, &status)
	if status["connected"] != false {
		t.Fatalf("expected disconnected: %+v", status)
	}
}

func TestGitHubConnectRequiresFields(t *testing.T) {
	s := newTestServer()
	alice := loginAs(t, s, "alice")
	proj := resolveTestProject(t, s, alice, "github-bad")

	rec := doJSON(t, s, http.MethodPost, "/projects/"+proj+"/github/connect", alice, map[string]any{
		"owner": "acme",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestGitHubImportRequiresConnect(t *testing.T) {
	s := newTestServer()
	alice := loginAs(t, s, "alice")
	proj := resolveTestProject(t, s, alice, "github-import")

	rec := doJSON(t, s, http.MethodPost, "/projects/"+proj+"/github/import", alice, map[string]any{})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d %s", rec.Code, rec.Body.String())
	}
}
