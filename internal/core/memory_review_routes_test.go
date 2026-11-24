package core

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestMemoryReviewAndBriefRoutes pins the verbs, paths and bodies of the
// Memory page's review, history and project brief methods. The server
// decodes bodies strictly (DisallowUnknownFields), so bodies must be exact.
func TestMemoryReviewAndBriefRoutes(t *testing.T) {
	cases := []struct {
		method string
		args   string
		verb   string
		path   string
		body   string
	}{
		{"memory.update", `{"id":"m/1","content":"Use LF endings everywhere","scope":"decision"}`, http.MethodPut, "/memory/m%2F1", `{"content":"Use LF endings everywhere","scope":"decision"}`},
		{"memory.history", `{"id":"m/1"}`, http.MethodGet, "/memory/m%2F1/history", ""},
		{"memory.revert", `{"id":"m1","version":3}`, http.MethodPost, "/memory/m1/revert", `{"version":3}`},
		{"memory.confirm", `{"id":"m1","session_id":"s1"}`, http.MethodPost, "/memory/m1/confirm", `{}`},
		{"memory.reject", `{"memory_id":"m1"}`, http.MethodPost, "/memory/m1/reject", `{}`},
		{"memory.pin", `{"id":"m1"}`, http.MethodPost, "/memory/m1/pin", `{}`},
		{"memory.forget", `{"id":"m1"}`, http.MethodPost, "/memory/m1/forget", `{}`},
		{"projects.brief", `{"project_id":"p 1"}`, http.MethodGet, "/v1/projects/p%201/brief", ""},
		{"projects.brief_refresh", `{"project_id":"p1"}`, http.MethodPost, "/v1/projects/p1/brief/refresh", ""},
		{"projects.graph", `{"project_id":"p 1","limit":300}`, http.MethodGet, "/v1/projects/p%201/graph?limit=300", ""},
		{"projects.graph", `{"project_id":"p1"}`, http.MethodGet, "/v1/projects/p1/graph", ""},
	}
	for _, tc := range cases {
		verb, path, body, err := cloudRequest(tc.method, json.RawMessage(tc.args))
		if err != nil {
			t.Fatalf("%s: %v", tc.method, err)
		}
		if verb != tc.verb || path != tc.path || string(body) != tc.body {
			t.Fatalf("%s = %s %s %q, want %s %s %q", tc.method, verb, path, body, tc.verb, tc.path, tc.body)
		}
		if !known(tc.method) {
			t.Fatalf("%s missing from Methods()", tc.method)
		}
	}
}

func TestMemoryReviewRoutesRequireIDs(t *testing.T) {
	for _, tc := range []struct{ method, args string }{
		{"memory.history", `{}`},
		{"memory.confirm", `{"session_id":"s1"}`},
		{"memory.reject", `{}`},
		{"memory.revert", `{"id":"m1"}`},
		{"memory.revert", `{"id":"m1","version":0}`},
		{"projects.brief", `{}`},
		{"projects.brief_refresh", `{"id":"p1"}`},
		{"projects.graph", `{}`},
	} {
		if _, _, _, err := cloudRequest(tc.method, json.RawMessage(tc.args)); err == nil {
			t.Fatalf("%s %s: want error", tc.method, tc.args)
		}
	}
}

func TestAuthTokenRoutes(t *testing.T) {
	if m, p, _, err := cloudRequest("auth.tokens.list", nil); err != nil || m != "GET" || p != "/auth/tokens" {
		t.Fatalf("list = %s %s %v", m, p, err)
	}
	if m, p, _, err := cloudRequest("auth.tokens.revoke", []byte(`{"token_id":"tok 1"}`)); err != nil || m != "DELETE" || p != "/auth/tokens/tok%201" {
		t.Fatalf("revoke = %s %s %v", m, p, err)
	}
	if _, _, _, err := cloudRequest("auth.tokens.revoke", []byte(`{}`)); err == nil {
		t.Fatal("revoke without id must fail")
	}
}
