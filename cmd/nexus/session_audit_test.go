package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// Audit coverage for cmd/nexus/session.go: parse edges + request wiring.

func TestAuditSessionParseEdges(t *testing.T) {
	if _, err := parseSessionArgs(nil); err == nil {
		t.Error("empty session args must fail")
	}
	if _, err := parseSessionArgs([]string{"list", "extra"}); err == nil {
		t.Error("list with positional must fail")
	}
	if _, err := parseSessionArgs([]string{"create", "-p", "proj1"}); err == nil {
		t.Error("create without title must fail")
	}
	o, err := parseSessionArgs([]string{"create", "-p", "proj1", "Auth", "refactor"})
	if err != nil || o.Title != "Auth refactor" {
		t.Fatalf("multiword title wrong: %+v %v", o, err)
	}
	if _, err := parseSessionArgs([]string{"join", "a", "b"}); err == nil {
		t.Error("join with 2 ids must fail")
	}
}

func TestAuditSessionListShape(t *testing.T) {
	var cap auditCaptured
	srv := auditServer(t, 200, `{"items":[{"id":"s1","title":"Auth refactor","project_id":"p1","is_active":true}],"count":1}`, &cap)
	defer srv.Close()
	cfg := Config{ServerURL: srv.URL, ProjectID: "p1"}
	var out bytes.Buffer
	if err := runSession(context.Background(), cfg, []string{"list"}, &out); err != nil {
		t.Fatalf("list: %v", err)
	}
	if cap.Path != "/sessions" || cap.Query.Get("project_id") != "p1" {
		t.Fatalf("list request wrong: %s %v", cap.Path, cap.Query)
	}
	if !strings.Contains(out.String(), "s1") {
		t.Fatalf("output must contain id, got:\n%s", out.String())
	}
}

func TestAuditSessionListEmpty(t *testing.T) {
	srv := auditServer(t, 200, `{"items":[],"count":0}`, nil)
	defer srv.Close()
	cfg := Config{ServerURL: srv.URL, ProjectID: "p1"}
	var out bytes.Buffer
	if err := runSession(context.Background(), cfg, []string{"list"}, &out); err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out.String(), "no sessions") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestAuditSessionCreateBody(t *testing.T) {
	var cap auditCaptured
	srv := auditServer(t, 201, `{"id":"s9","title":"Auth refactor"}`, &cap)
	defer srv.Close()
	cfg := Config{ServerURL: srv.URL, ProjectID: "p1"}
	var out bytes.Buffer
	if err := runSession(context.Background(), cfg, []string{"create", "Auth refactor"}, &out); err != nil {
		t.Fatalf("create: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(cap.Body, &body); err != nil {
		t.Fatalf("create body: %v", err)
	}
	if body["title"] != "Auth refactor" || body["project_id"] != "p1" {
		t.Fatalf("create body wrong: %v", body)
	}
	if !strings.Contains(out.String(), "created session s9") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestAuditSessionCreateRequiresProject(t *testing.T) {
	var out bytes.Buffer
	err := runSession(context.Background(), Config{ServerURL: "http://localhost:1"}, []string{"create", "Title here"}, &out)
	if err == nil || !strings.Contains(err.Error(), "requires a project") {
		t.Fatalf("expected project error, got %v", err)
	}
}

func TestAuditSessionJoinPath(t *testing.T) {
	var cap auditCaptured
	srv := auditServer(t, 200, `{"session_id":"s1"}`, &cap)
	defer srv.Close()
	cfg := Config{ServerURL: srv.URL}
	var out bytes.Buffer
	if err := runSession(context.Background(), cfg, []string{"join", "s1"}, &out); err != nil {
		t.Fatalf("join: %v", err)
	}
	if cap.Path != "/sessions/s1/join" || cap.Method != "POST" {
		t.Fatalf("join request = %s %s", cap.Method, cap.Path)
	}
	if !strings.Contains(out.String(), "joined session s1") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestAuditSession404Maps(t *testing.T) {
	srv := auditServer(t, 404, `{"error":{"code":404,"message":"nope"}}`, nil)
	defer srv.Close()
	cfg := Config{ServerURL: srv.URL, ProjectID: "p"}
	for _, args := range [][]string{{"list"}, {"create", "Some title here"}, {"join", "s1"}} {
		var out bytes.Buffer
		err := runSession(context.Background(), cfg, args, &out)
		var ni *errNotImplemented
		if !errors.As(err, &ni) {
			t.Errorf("session %v 404: want *errNotImplemented, got %T (%v)", args, err, err)
		}
	}
}
