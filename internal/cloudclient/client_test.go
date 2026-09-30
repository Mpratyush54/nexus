package cloudclient

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMemorySearch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/memory/search" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "auth", http.StatusUnauthorized)
			return
		}
		if r.URL.Query().Get("project_id") != "p1" {
			http.Error(w, "pid", http.StatusBadRequest)
			return
		}
		if r.URL.Query().Get("q") != "redis" {
			http.Error(w, "q", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"count": 1,
			"items": []map[string]any{
				{
					"key": "cache/redis", "content": "Use Redis for pub/sub.", "level": "project",
					"files_affected": []string{"internal/cache.go"}, "created_at": "2026-09-30T12:00:00Z",
				},
			},
		})
	}))
	defer srv.Close()

	c := &Client{ServerURL: srv.URL, Token: "tok", HTTP: srv.Client()}
	items, pid, err := c.MemorySearch("redis", "p1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if pid != "p1" || len(items) != 1 || items[0].Key != "cache/redis" {
		t.Fatalf("%q %+v", pid, items)
	}
	if items[0].CreatedAt == "" || len(items[0].FilesAffected) != 1 {
		t.Fatalf("rich fields: %+v", items[0])
	}
}

func TestMemoryBrowseEmptyQuery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/memory/search" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("q") != "" {
			t.Fatalf("browse should omit q, got %q", r.URL.Query().Get("q"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"count": 1,
			"items": []map[string]any{{"key": "a/b", "content": "hello"}},
		})
	}))
	defer srv.Close()

	c := &Client{ServerURL: srv.URL, Token: "tok", HTTP: srv.Client()}
	items, _, err := c.MemorySearch("", "p1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("%+v", items)
	}
}

func TestMemorySearchFilteredLevelAndTags(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/memory/search" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("level") != "project" {
			http.Error(w, "level", http.StatusBadRequest)
			return
		}
		if r.URL.Query().Get("tags") != "redis,cache" {
			http.Error(w, "tags", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"count": 1,
			"items": []map[string]any{{"key": "cache/redis", "content": "ok", "level": "project"}},
		})
	}))
	defer srv.Close()

	c := &Client{ServerURL: srv.URL, Token: "tok", HTTP: srv.Client()}
	items, _, err := c.MemorySearchFiltered(MemorySearchParams{
		ProjectID: "p1",
		Query:     "",
		Limit:     10,
		Level:     "project",
		Tags:      []string{"redis", "cache"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Key != "cache/redis" {
		t.Fatalf("%+v", items)
	}
}

func TestMemorySearchRequiresSignIn(t *testing.T) {
	c := &Client{ServerURL: "http://example.invalid", Token: ""}
	if _, _, err := c.MemorySearch("x", "p", 5); err == nil {
		t.Fatal("expected error")
	}
}

func TestMemorySearchAgentFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/agent/memory/search" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"project_id": "auto",
			"count":      1,
			"items":      []map[string]any{{"key": "k", "content": "c"}},
		})
	}))
	defer srv.Close()

	c := &Client{ServerURL: srv.URL, Token: "tok", HTTP: srv.Client()}
	items, pid, err := c.MemorySearch("anything", "", 5)
	if err != nil {
		t.Fatal(err)
	}
	if pid != "auto" || len(items) != 1 {
		t.Fatalf("%q %+v", pid, items)
	}
}

func TestSetAuthBearerHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}, "count": 0})
	}))
	defer srv.Close()

	c := &Client{ServerURL: srv.URL, Token: "nxs_session", HTTP: srv.Client()}
	if _, _, err := c.MemoryBrowse("p1", 5); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer nxs_session" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
}

func TestProjectDashboard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/projects/p1/dashboard" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "auth", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"project_id": "p1",
			"metrics": map[string]any{
				"memories": 12, "proposed": 3, "confirmed": 9,
				"members": 2, "online": 1, "events_7d": 5, "agent_calls": 7,
			},
			"heatmap": []map[string]any{
				{"date": "2026-09-29", "count": 2},
				{"date": "2026-09-30", "count": 4},
			},
			"recent": []map[string]any{
				{"id": 1, "event_type": "MEMORY_WRITE", "created_at": "2026-09-30T12:00:00Z"},
			},
		})
	}))
	defer srv.Close()

	c := &Client{ServerURL: srv.URL, Token: "tok", HTTP: srv.Client()}
	dash, err := c.ProjectDashboard("p1")
	if err != nil {
		t.Fatal(err)
	}
	if dash.Metrics.Memories != 12 || len(dash.Heatmap) != 2 || dash.Heatmap[1].Count != 4 {
		t.Fatalf("%+v", dash)
	}
	if len(dash.Recent) != 1 || dash.Recent[0].EventType != "MEMORY_WRITE" {
		t.Fatalf("recent: %+v", dash.Recent)
	}
}
