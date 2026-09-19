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

func TestHTTPMemoryStoreExtractRemote(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"provider":"openrouter","count":1,"items":[{"key":"cache/redis","content":"We decided to use Redis for pub/sub.","level":"project","scope":"decision","confidence":0.9,"source":"processor:openrouter"}]}`))
	}))
	defer srv.Close()

	store := NewHTTPMemoryStore(srv.URL, "tok", "proj-uuid")
	provider, props, err := store.ExtractRemote(t.Context(), []map[string]string{
		{"speaker": "user", "content": "We decided to use Redis for pub/sub between services."},
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/memory/extract" {
		t.Fatalf("path = %q", gotPath)
	}
	if provider != "openrouter" || len(props) != 1 {
		t.Fatalf("provider=%q props=%+v", provider, props)
	}
	if len(store.Existing()) != 1 {
		t.Fatalf("local cache = %d", len(store.Existing()))
	}
}

func TestProcessorConnectedUsesExtractRemote(t *testing.T) {
	var memoryPosts, extractPosts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/memory/extract":
			extractPosts++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"provider":"heuristic","count":1,"items":[{"key":"a/b","content":"We decided to use Redis for pub/sub between daemon and portal.","level":"project","scope":"decision","confidence":0.9,"source":"processor:heuristic"}]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/memory":
			memoryPosts++
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	store := NewHTTPMemoryStore(srv.URL, "tok", "proj-uuid")
	p := NewProcessor(store, 0, 0, true, HeuristicProvider{})
	ev := Event{Type: EventConversationTurn, Payload: map[string]any{
		"speaker": "user",
		"content": "Use when the user asks anything about pull requests and also random skill noise.",
	}}
	got, err := p.ProcessEvents(t.Context(), "proj", []Event{ev})
	if err != nil {
		t.Fatal(err)
	}
	if extractPosts != 1 {
		t.Fatalf("extract posts = %d", extractPosts)
	}
	if memoryPosts != 0 {
		t.Fatalf("direct /memory posts = %d want 0", memoryPosts)
	}
	if p.LastExtractProvider() != "heuristic" {
		t.Fatalf("provider = %q", p.LastExtractProvider())
	}
	if len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
}
