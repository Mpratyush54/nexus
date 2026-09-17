package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Audit coverage for cmd/nexus/main.go globals: global-flag parsing,
// status args, runStatus wiring, and usage text.

func TestAuditGlobalArgsShorthand(t *testing.T) {
	cfg := defaultConfig()
	cfg.ServerURL = "http://default:8080"
	rest, err := parseGlobalArgs([]string{"-p", "proj9", "status"}, &cfg)
	if err != nil {
		t.Fatalf("parseGlobalArgs: %v", err)
	}
	if cfg.ProjectID != "proj9" || len(rest) != 1 || rest[0] != "status" {
		t.Fatalf("shorthand -p wrong: %+v %v", cfg, rest)
	}
}

func TestAuditStatusParseRejectsPositionals(t *testing.T) {
	if _, err := parseStatusArgs([]string{"extra"}); err == nil {
		t.Error("status with positional must fail")
	}
}

func TestAuditStatusAgainstHttptest(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("/workspaces/proj1/active", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"ws1","machine_id":"m1","branch":"main","is_dirty":false,"last_seen":"2026-01-01T00:00:00Z"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	cfg := Config{ServerURL: srv.URL, ProjectID: "proj1"}
	var out bytes.Buffer
	if err := runStatus(context.Background(), cfg, nil, &out); err != nil {
		t.Fatalf("runStatus: %v", err)
	}
	if !strings.Contains(out.String(), "ws1") {
		t.Fatalf("status must show workspace, got:\n%s", out.String())
	}
}

func TestAuditNexusUsageMentionsCommands(t *testing.T) {
	var out bytes.Buffer
	usage(&out)
	for _, want := range []string{"memory search", "session list", "branch list", "episode list", "doctor"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("usage missing %q", want)
		}
	}
}
