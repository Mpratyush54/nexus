package daemon

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPortalLevelCoercesSession(t *testing.T) {
	if got := portalLevel(LevelSession); got != "project" {
		t.Fatalf("session -> %q, want project", got)
	}
	if got := portalLevel(LevelEphemeral); got != "project" {
		t.Fatalf("ephemeral -> %q, want project", got)
	}
	if got := portalLevel(""); got != "project" {
		t.Fatalf("empty -> %q, want project", got)
	}
	if got := portalLevel(LevelPersonal); got != "personal" {
		t.Fatalf("personal -> %q, want personal", got)
	}
	if got := portalLevel(LevelProject); got != "project" {
		t.Fatalf("project -> %q, want project", got)
	}
}

func TestHTTPMemoryStoreSaveCoercesSessionLevel(t *testing.T) {
	var gotLevel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/memory" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("json: %v", err)
			http.Error(w, "bad json", 400)
			return
		}
		gotLevel, _ = body["level"].(string)
		if gotLevel == "session" {
			http.Error(w, `{"error":"session requires session_id"}`, 500)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	store := NewHTTPMemoryStore(srv.URL, "tok", "proj-uuid")
	err := store.Save(Proposal{
		Key:     "harvest/cursor-decision",
		Content: "We decided Nexus must auto-sync Cursor chats to the portal without MCP.",
		Level:   LevelSession, // heuristic default — must not 500 the portal
		Scope:   ScopeDecision,
		Source:  "processor:heuristic",
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if gotLevel != "project" {
		t.Fatalf("posted level=%q, want project", gotLevel)
	}
	if len(store.Existing()) != 1 {
		t.Fatalf("local cache len=%d, want 1", len(store.Existing()))
	}
}

func TestHTTPMemoryStoreSaveSurfacesErrorBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"could not create memory item: boom"}`, 500)
	}))
	defer srv.Close()
	store := NewHTTPMemoryStore(srv.URL, "tok", "proj-uuid")
	err := store.Save(Proposal{
		Key:     "k",
		Content: "long enough content for the memory validator gates here",
		Level:   LevelProject,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error should include body, got %v", err)
	}
}
