package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// Audit coverage for cmd/nexus/memory.go: parse edges + request building
// against httptest servers (no network).

func TestAuditMemoryParseLimitShorthand(t *testing.T) {
	o, err := parseMemoryArgs([]string{"search", "-n", "7", "redis caching query here"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if o.Limit != 7 {
		t.Fatalf("Limit = %d, want 7", o.Limit)
	}
}

func TestAuditMemoryParseBadLimit(t *testing.T) {
	for _, args := range [][]string{
		{"search", "--limit", "0", "some query words here"},
		{"search", "--limit", "-3", "some query words here"},
	} {
		if _, err := parseMemoryArgs(args); err == nil {
			t.Errorf("parse(%v): expected --limit error", args)
		}
	}
}

func TestAuditMemoryParseProposeBoundary(t *testing.T) {
	content20 := strings.Repeat("a", 20)
	o, err := parseMemoryArgs([]string{"propose", "-k", "k", content20})
	if err != nil {
		t.Fatalf("20-char content must pass: %v", err)
	}
	if o.Key != "k" || o.Content != content20 {
		t.Fatalf("propose parse wrong: %+v", o)
	}
	if _, err := parseMemoryArgs([]string{"propose", "-k", "k", strings.Repeat("a", 19)}); err == nil {
		t.Error("19-char content must fail")
	}
	if _, err := parseMemoryArgs([]string{"propose", "-k", "k", strings.Repeat("a", 2001)}); err == nil {
		t.Error("2001-char content must fail")
	}
}

func TestAuditMemorySearchRequestShape(t *testing.T) {
	var cap auditCaptured
	srv := auditServer(t, 200, `{"items":[{"id":"m1","key":"k","level":"project","status":"CONFIRMED","confidence":0.9,"content":"c"}],"count":1}`, &cap)
	defer srv.Close()
	c := auditClientTo(t, srv, "")
	cfg := Config{ServerURL: srv.URL, ProjectID: "proj9"}
	_ = c

	var out bytes.Buffer
	err := runMemory(context.Background(), cfg,
		[]string{"search", "--level", "project", "--limit", "5", "--tags", "a,b", "redis caching"}, &out)
	if err != nil {
		t.Fatalf("runMemory search: %v", err)
	}
	if cap.Query.Get("project_id") != "proj9" {
		t.Errorf("project_id = %q", cap.Query.Get("project_id"))
	}
	if cap.Query.Get("q") != "redis caching" {
		t.Errorf("q = %q", cap.Query.Get("q"))
	}
	if cap.Query.Get("tags") != "a,b" || cap.Query.Get("limit") != "5" {
		t.Errorf("tags/limit wrong: %v", cap.Query)
	}
	if cap.Query.Get("level") != "project" {
		t.Errorf("level must be forwarded for server filtering, got %v", cap.Query)
	}
	if !strings.Contains(out.String(), "m1") {
		t.Errorf("table output must contain id, got:\n%s", out.String())
	}
}

func TestAuditMemorySearchLevelFilterClientSide(t *testing.T) {
	body := `{"items":[
		{"id":"m1","key":"k1","level":"project","status":"CONFIRMED","confidence":0.9,"content":"c1"},
		{"id":"m2","key":"k2","level":"session","status":"CONFIRMED","confidence":0.9,"content":"c2"}],
		"count":2}`
	srv := auditServer(t, 200, body, nil)
	defer srv.Close()
	cfg := Config{ServerURL: srv.URL, ProjectID: "p", JSON: true}
	var out bytes.Buffer
	if err := runMemory(context.Background(), cfg, []string{"search", "--level", "session", "query words here"}, &out); err != nil {
		t.Fatalf("runMemory: %v", err)
	}
	var decoded struct {
		Items []map[string]any `json:"items"`
		Count int              `json:"count"`
	}
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("JSON output invalid: %v\n%s", err, out.String())
	}
	if decoded.Count != 1 || decoded.Items[0]["id"] != "m2" {
		t.Fatalf("client-side level filter wrong: %+v", decoded)
	}
}

func TestAuditMemorySearchRequiresProject(t *testing.T) {
	var out bytes.Buffer
	err := runMemory(context.Background(), Config{ServerURL: "http://localhost:1"},
		[]string{"search", "some query words"}, &out)
	if err == nil || !strings.Contains(err.Error(), "requires a project") {
		t.Fatalf("expected project error, got %v", err)
	}
}

func TestAuditMemoryProposeRequestShape(t *testing.T) {
	var cap auditCaptured
	srv := auditServer(t, 201, `{"id":"mem_1","key":"testing/framework","level":"project","status":"PROPOSED","content":"The team uses pytest with fixtures."}`, &cap)
	defer srv.Close()
	cfg := Config{ServerURL: srv.URL, ProjectID: "proj1"}
	var out bytes.Buffer
	content := "The team uses pytest with fixtures for testing here ok."
	if err := runMemory(context.Background(), cfg,
		[]string{"propose", "-k", "testing/framework", "-p", "proj1", content}, &out); err != nil {
		t.Fatalf("runMemory propose: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(cap.Body, &body); err != nil {
		t.Fatalf("propose body not JSON: %v", err)
	}
	for k, want := range map[string]string{"key": "testing/framework", "project_id": "proj1", "level": "project", "source": "cli:nexus"} {
		if body[k] != want {
			t.Errorf("body[%q] = %v, want %q", k, body[k], want)
		}
	}
	if !strings.Contains(out.String(), "mem_1") {
		t.Errorf("output must mention id, got:\n%s", out.String())
	}
}

func TestAuditMemoryConfirmReject404Maps(t *testing.T) {
	srv := auditServer(t, 404, `{"error":{"code":404,"message":"not found"}}`, nil)
	defer srv.Close()
	cfg := Config{ServerURL: srv.URL}
	var out bytes.Buffer
	for _, sub := range []string{"confirm", "reject"} {
		err := runMemory(context.Background(), cfg, []string{sub, "mem_1"}, &out)
		var ni *errNotImplemented
		if !errors.As(err, &ni) {
			t.Errorf("%s 404: want *errNotImplemented, got %T (%v)", sub, err, err)
		}
	}
}

func TestAuditMemoryConfirmSuccess(t *testing.T) {
	var cap auditCaptured
	srv := auditServer(t, 200, `{"id":"mem_1","status":"CONFIRMED"}`, &cap)
	defer srv.Close()
	cfg := Config{ServerURL: srv.URL}
	var out bytes.Buffer
	if err := runMemory(context.Background(), cfg, []string{"confirm", "mem_1"}, &out); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if cap.Path != "/memory/mem_1/confirm" || cap.Method != "POST" {
		t.Fatalf("confirm request = %s %s", cap.Method, cap.Path)
	}
	if !strings.Contains(out.String(), "confirmed mem_1") {
		t.Fatalf("output = %q", out.String())
	}
}
