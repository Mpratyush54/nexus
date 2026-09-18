package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// Audit coverage for cmd/nexus/episode.go: parse edges + request wiring.

func TestAuditEpisodeParseEdges(t *testing.T) {
	if _, err := parseEpisodeArgs(nil); err == nil {
		t.Error("empty episode args must fail")
	}
	if _, err := parseEpisodeArgs([]string{"list", "extra"}); err == nil {
		t.Error("list with positional must fail")
	}
	if _, err := parseEpisodeArgs([]string{"list", "--limit", "0"}); err == nil {
		t.Error("list limit 0 must fail")
	}
	if _, err := parseEpisodeArgs([]string{"search", "-p", "x"}); err == nil {
		t.Error("search without query must fail")
	}
	// error-pattern defaults to the query text.
	o, err := parseEpisodeArgs([]string{"search", "-p", "proj1", "ConnectionTimeout trouble"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if o.ErrorPattern != "ConnectionTimeout trouble" || o.Query != "ConnectionTimeout trouble" {
		t.Fatalf("default error-pattern wrong: %+v", o)
	}
	// Explicit --error-pattern is preserved.
	o, err = parseEpisodeArgs([]string{"search", "--error-pattern", "pgx.*", "timeout here"})
	if err != nil || o.ErrorPattern != "pgx.*" || o.Query != "timeout here" {
		t.Fatalf("explicit error-pattern wrong: %+v %v", o, err)
	}
	// -n shorthand for limit.
	o, err = parseEpisodeArgs([]string{"list", "-n", "3"})
	if err != nil || o.Limit != 3 {
		t.Fatalf("limit shorthand wrong: %+v %v", o, err)
	}
}

func TestAuditEpisodeListQueryShape(t *testing.T) {
	var cap auditCaptured
	srv := auditServer(t, 200, `{"items":[{"id":"e1","title":"Auth timeout","episode_type":"bug_fix","status":"OPEN"}],"count":1}`, &cap)
	defer srv.Close()
	cfg := Config{ServerURL: srv.URL, ProjectID: "proj1"}
	var out bytes.Buffer
	if err := runEpisode(context.Background(), cfg, []string{"list", "--limit", "10"}, &out); err != nil {
		t.Fatalf("list: %v", err)
	}
	if cap.Path != "/episodes/search" {
		t.Fatalf("episode list path = %s, want /episodes/search", cap.Path)
	}
	if cap.Query.Get("project_id") != "proj1" || cap.Query.Get("limit") != "10" {
		t.Fatalf("episode list query wrong: %v", cap.Query)
	}
	if !strings.Contains(out.String(), "e1") {
		t.Fatalf("output must contain id, got:\n%s", out.String())
	}
}

func TestAuditEpisodeSearchSendsBothParams(t *testing.T) {
	var cap auditCaptured
	srv := auditServer(t, 200, `{"items":[],"count":0}`, &cap)
	defer srv.Close()
	cfg := Config{ServerURL: srv.URL, ProjectID: "proj1"}
	var out bytes.Buffer
	if err := runEpisode(context.Background(), cfg, []string{"search", "ConnectionTimeout"}, &out); err != nil {
		t.Fatalf("search: %v", err)
	}
	// One call covers both match modes: error_pattern + q.
	if cap.Query.Get("q") != "ConnectionTimeout" || cap.Query.Get("error_pattern") != "ConnectionTimeout" {
		t.Fatalf("search query wrong: %v", cap.Query)
	}
	if !strings.Contains(out.String(), "no episodes found") {
		t.Fatalf("empty output = %q", out.String())
	}
}

func TestAuditEpisodeRequiresProject(t *testing.T) {
	var out bytes.Buffer
	err := runEpisode(context.Background(), Config{ServerURL: "http://localhost:1"}, []string{"list"}, &out)
	if err == nil || !strings.Contains(err.Error(), "requires a project") {
		t.Fatalf("expected project error, got %v", err)
	}
}

func TestAuditEpisodeJSONOutput(t *testing.T) {
	srv := auditServer(t, 200, `{"items":[],"count":0}`, nil)
	defer srv.Close()
	cfg := Config{ServerURL: srv.URL, ProjectID: "p", JSON: true}
	var out bytes.Buffer
	if err := runEpisode(context.Background(), cfg, []string{"list"}, &out); err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out.String(), `"count": 0`) && !strings.Contains(out.String(), `"count":0`) {
		t.Fatalf("JSON output must carry count, got:\n%s", out.String())
	}
}
