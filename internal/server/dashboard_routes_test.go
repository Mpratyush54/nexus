package server

import (
	"net/http"
	"testing"
	"time"

	"central-memory/internal/store"
)

func TestProjectDashboardShape(t *testing.T) {
	s := newTestServer()
	alice := loginAs(t, s, "alice")
	proj := resolveTestProject(t, s, alice, "dash-proj")

	_ = s.Store.AppendEvent(t.Context(), &store.Event{
		ProjectID: proj,
		UserID:    "alice",
		EventType: "MEMORY_PROPOSED",
		Payload:   map[string]any{"key": "a/b"},
		CreatedAt: time.Now().UTC(),
	})
	_ = s.Store.CreateMemoryItem(t.Context(), &store.MemoryItem{
		ProjectID:  proj,
		Key:        "a/b",
		Content:    "hello world content that is long enough for validation",
		Level:      "project",
		Scope:      "fact",
		Status:     "PROPOSED",
		Confidence: 0.8,
	})

	rec := doJSON(t, s, http.MethodGet, "/projects/"+proj+"/dashboard", alice, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("dashboard = %d %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	decodeBody(t, rec, &body)
	metrics, _ := body["metrics"].(map[string]any)
	if metrics == nil {
		t.Fatalf("missing metrics: %+v", body)
	}
	heat, _ := body["heatmap"].([]any)
	if len(heat) < 300 {
		t.Fatalf("heatmap days = %d, want ~365", len(heat))
	}
}

func TestGitHubOAuthStatusWhenUnconfigured(t *testing.T) {
	t.Setenv("GITHUB_CLIENT_ID", "")
	t.Setenv("GITHUB_CLIENT_SECRET", "")
	s := newTestServer()
	alice := loginAs(t, s, "alice")
	proj := resolveTestProject(t, s, alice, "oauth-status")
	rec := doJSON(t, s, http.MethodGet, "/projects/"+proj+"/github/oauth/status", alice, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	decodeBody(t, rec, &body)
	if body["oauth_configured"] != false {
		t.Fatalf("expected oauth_configured false: %+v", body)
	}
}
