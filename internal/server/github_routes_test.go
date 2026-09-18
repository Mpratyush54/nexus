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

func TestGitHubStatusSuggestsFromCanonicalURL(t *testing.T) {
	s := newTestServer()
	alice := loginAs(t, s, "alice")
	rec := doJSON(t, s, http.MethodPost, "/projects/resolve", alice, map[string]string{
		"folder_name":   "nexus-suggest",
		"canonical_url": "https://github.com/acme/nexus.git",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("resolve = %d %s", rec.Code, rec.Body.String())
	}
	var project struct {
		ID string `json:"id"`
	}
	decodeBody(t, rec, &project)
	rec = doJSON(t, s, http.MethodPost, "/workspaces/register", alice, map[string]string{
		"project_id": project.ID,
		"machine_id": "test-machine-suggest",
		"path":       "/tmp/test-ws-suggest",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("register = %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodGet, "/projects/"+project.ID+"/github/status", alice, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d %s", rec.Code, rec.Body.String())
	}
	var status map[string]any
	decodeBody(t, rec, &status)
	if status["connected"] != false {
		t.Fatalf("expected disconnected: %+v", status)
	}
	if status["suggested_owner"] != "acme" || status["suggested_repo"] != "nexus" {
		t.Fatalf("expected suggested acme/nexus, got %+v", status)
	}

	rec = doJSON(t, s, http.MethodPost, "/projects/"+project.ID+"/github/connect", alice, map[string]any{})
	if rec.Code != http.StatusOK {
		t.Fatalf("connect infer = %d %s", rec.Code, rec.Body.String())
	}
	decodeBody(t, rec, &status)
	if status["owner"] != "acme" || status["repo"] != "nexus" {
		t.Fatalf("inferred connect: %+v", status)
	}
}

func TestProjectPresenceIncludesCaller(t *testing.T) {
	s := newTestServer()
	alice := loginAs(t, s, "alice")
	proj := resolveTestProject(t, s, alice, "presence-nav")

	rec := doJSON(t, s, http.MethodGet, "/projects/"+proj+"/presence", alice, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("presence = %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Count int `json:"count"`
		Items []struct {
			UserID string `json:"user_id"`
			Status string `json:"status"`
		} `json:"items"`
	}
	decodeBody(t, rec, &body)
	if body.Count != 1 || len(body.Items) != 1 || body.Items[0].UserID != "alice" {
		t.Fatalf("expected alice online, got %+v", body)
	}

	h := NewHub()
	s.AttachHub(h)
	h.Add(&Client{ID: "c-bob", UserID: "bob", ProjectID: proj})

	rec = doJSON(t, s, http.MethodGet, "/projects/"+proj+"/presence", alice, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("presence+hub = %d %s", rec.Code, rec.Body.String())
	}
	decodeBody(t, rec, &body)
	got := map[string]bool{}
	for _, it := range body.Items {
		got[it.UserID] = true
	}
	if !got["alice"] || !got["bob"] {
		t.Fatalf("expected alice+bob, got %+v", body.Items)
	}
}
