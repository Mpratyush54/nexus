package cloudclient

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMemorySearch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/agent/memory/search" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "auth", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"project_id": "p1",
			"count":      1,
			"items": []map[string]any{
				{"key": "cache/redis", "content": "Use Redis for pub/sub.", "level": "project"},
			},
		})
	}))
	defer srv.Close()

	c := &Client{ServerURL: srv.URL, Token: "tok", HTTP: srv.Client()}
	items, pid, err := c.MemorySearch("redis", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if pid != "p1" || len(items) != 1 || items[0].Key != "cache/redis" {
		t.Fatalf("%q %+v", pid, items)
	}
}

func TestMemorySearchRequiresSignIn(t *testing.T) {
	c := New("", "")
	if _, _, err := c.MemorySearch("x", "", 5); err == nil {
		t.Fatal("expected error")
	}
}
