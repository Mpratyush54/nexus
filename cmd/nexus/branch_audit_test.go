package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// Audit coverage for cmd/nexus/branch.go: parse edges + request wiring.

func TestAuditBranchParseEdges(t *testing.T) {
	o, err := parseBranchArgs([]string{"fork", "-p", "proj1", "feat"})
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	if o.From != "main" {
		t.Fatalf("fork default From = %q, want main", o.From)
	}
	if _, err := parseBranchArgs([]string{"merge"}); err == nil {
		t.Error("merge without source must fail")
	}
	if _, err := parseBranchArgs([]string{"merge", "a", "b", "c"}); err == nil {
		t.Error("merge with 3 positionals must fail")
	}
	if _, err := parseBranchArgs([]string{"diff", "a", "b"}); err == nil {
		t.Error("diff with 2 positionals must fail")
	}
	if _, err := parseBranchArgs([]string{"list", "extra"}); err == nil {
		t.Error("list with positional must fail")
	}
	if _, err := parseBranchArgs(nil); err == nil {
		t.Error("empty branch args must fail")
	}
}

func TestAuditBranchListRequestShape(t *testing.T) {
	var cap auditCaptured
	srv := auditServer(t, 200, `{"items":[{"name":"main","visibility":"shared"}],"count":1}`, &cap)
	defer srv.Close()
	cfg := Config{ServerURL: srv.URL, ProjectID: "proj1"}
	var out bytes.Buffer
	if err := runBranch(context.Background(), cfg, []string{"list"}, &out); err != nil {
		t.Fatalf("list: %v", err)
	}
	if cap.Path != "/branches" || cap.Query.Get("project_id") != "proj1" {
		t.Fatalf("list request wrong: %s %v", cap.Path, cap.Query)
	}
	if !strings.Contains(out.String(), "main") {
		t.Fatalf("output must list main, got:\n%s", out.String())
	}
}

func TestAuditBranchForkBody(t *testing.T) {
	var cap auditCaptured
	srv := auditServer(t, 201, `{"name":"bob-exp","project_id":"proj1"}`, &cap)
	defer srv.Close()
	cfg := Config{ServerURL: srv.URL, ProjectID: "proj1"}
	var out bytes.Buffer
	if err := runBranch(context.Background(), cfg, []string{"fork", "--from", "main", "bob-exp"}, &out); err != nil {
		t.Fatalf("fork: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(cap.Body, &body); err != nil {
		t.Fatalf("fork body: %v", err)
	}
	if body["name"] != "bob-exp" || body["project_id"] != "proj1" || body["from"] != "main" {
		t.Fatalf("fork body wrong: %v", body)
	}
	if !strings.Contains(out.String(), "forked branch bob-exp from main") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestAuditBranchForkRequiresProject(t *testing.T) {
	var out bytes.Buffer
	err := runBranch(context.Background(), Config{ServerURL: "http://localhost:1"}, []string{"fork", "x"}, &out)
	if err == nil || !strings.Contains(err.Error(), "requires a project") {
		t.Fatalf("expected project error, got %v", err)
	}
}

func TestAuditBranchCheckoutPath(t *testing.T) {
	var cap auditCaptured
	srv := auditServer(t, 200, `{"name":"main"}`, &cap)
	defer srv.Close()
	cfg := Config{ServerURL: srv.URL}
	var out bytes.Buffer
	if err := runBranch(context.Background(), cfg, []string{"checkout", "main"}, &out); err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if cap.Path != "/branches/main/checkout" {
		t.Fatalf("checkout path = %s", cap.Path)
	}
}

func TestAuditBranchDiffQuery(t *testing.T) {
	var cap auditCaptured
	srv := auditServer(t, 200, `{"source":"main","target":"bob-exp"}`, &cap)
	defer srv.Close()
	cfg := Config{ServerURL: srv.URL, ProjectID: "proj1"}
	var out bytes.Buffer
	if err := runBranch(context.Background(), cfg, []string{"diff", "bob-exp"}, &out); err != nil {
		t.Fatalf("diff: %v", err)
	}
	if cap.Path != "/branches/diff" {
		t.Fatalf("diff path = %s", cap.Path)
	}
	if cap.Query.Get("target") != "bob-exp" || cap.Query.Get("project_id") != "proj1" {
		t.Fatalf("diff query wrong: %v", cap.Query)
	}
}

func TestAuditBranchMergeDefaultTarget(t *testing.T) {
	var cap auditCaptured
	srv := auditServer(t, 200, `{"merged":true,"conflicts":[]}`, &cap)
	defer srv.Close()
	cfg := Config{ServerURL: srv.URL, ProjectID: "proj1"}
	var out bytes.Buffer
	if err := runBranch(context.Background(), cfg, []string{"merge", "bob-exp"}, &out); err != nil {
		t.Fatalf("merge: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(cap.Body, &body); err != nil {
		t.Fatalf("merge body: %v", err)
	}
	if body["source"] != "bob-exp" || body["target"] != "main" {
		t.Fatalf("merge defaults wrong: %v", body)
	}
	if !strings.Contains(out.String(), "merged bob-exp into main") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestAuditBranch404Maps(t *testing.T) {
	srv := auditServer(t, 404, `{"error":{"code":404,"message":"nope"}}`, nil)
	defer srv.Close()
	cfg := Config{ServerURL: srv.URL, ProjectID: "p"}
	for _, args := range [][]string{{"list"}, {"fork", "x"}, {"checkout", "x"}, {"diff"}, {"merge", "x"}} {
		var out bytes.Buffer
		err := runBranch(context.Background(), cfg, args, &out)
		var ni *errNotImplemented
		if !errors.As(err, &ni) {
			t.Errorf("branch %v 404: want *errNotImplemented, got %T (%v)", args, err, err)
		}
	}
}
