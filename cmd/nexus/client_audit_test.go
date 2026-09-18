package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Audit coverage for cmd/nexus/client.go request construction. All traffic
// goes to local httptest servers (stdlib only, no external network).

// auditRoundTripper captures one outgoing request for inspection.
type auditCaptured struct {
	Method string
	Path   string
	Query  url.Values
	Header http.Header
	Body   []byte
}

func auditServer(t *testing.T, status int, respBody string, cap *auditCaptured) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if cap != nil {
			*cap = auditCaptured{Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(), Header: r.Header.Clone(), Body: b}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(respBody))
	}))
}

func auditClientTo(t *testing.T, srv *httptest.Server, token string) *APIClient {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse test URL: %v", err)
	}
	_ = u
	c := newAPIClient(Config{ServerURL: srv.URL, Token: token})
	c.HTTP.Timeout = 5 * time.Second
	return c
}

func TestAuditClientGetSendsAuthAndQuery(t *testing.T) {
	var cap auditCaptured
	srv := auditServer(t, 200, `{"ok":true}`, &cap)
	defer srv.Close()
	c := auditClientTo(t, srv, "tok123")
	ctx := context.Background()

	var out map[string]any
	q := url.Values{}
	q.Set("project_id", "p1")
	q.Set("q", "hello world")
	if err := c.getJSON(ctx, "/memory/search", q, &out); err != nil {
		t.Fatalf("getJSON: %v", err)
	}
	if cap.Method != http.MethodGet || cap.Path != "/memory/search" {
		t.Fatalf("request line wrong: %s %s", cap.Method, cap.Path)
	}
	if got := cap.Header.Get("Authorization"); got != "Bearer tok123" {
		t.Fatalf("Authorization = %q, want Bearer tok123", got)
	}
	if cap.Query.Get("project_id") != "p1" || cap.Query.Get("q") != "hello world" {
		t.Fatalf("query not forwarded: %v", cap.Query)
	}
	if !out["ok"].(bool) {
		t.Fatalf("response not decoded: %v", out)
	}
}

func TestAuditClientPostSetsContentTypeAndBody(t *testing.T) {
	var cap auditCaptured
	srv := auditServer(t, 201, `{"id":"mem_9","status":"PROPOSED"}`, &cap)
	defer srv.Close()
	c := auditClientTo(t, srv, "")
	var out map[string]any
	err := c.postJSON(context.Background(), "/memory",
		map[string]any{"key": "k", "content": "The team uses pytest with fixtures for all integration tests here."}, &out)
	if err != nil {
		t.Fatalf("postJSON: %v", err)
	}
	if ct := cap.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q", ct)
	}
	if got := cap.Header.Get("Authorization"); got != "" {
		t.Fatalf("empty token must send no Authorization, got %q", got)
	}
	var body map[string]any
	if err := json.Unmarshal(cap.Body, &body); err != nil || body["key"] != "k" {
		t.Fatalf("body not JSON-forwarded: %s", cap.Body)
	}
	if out["id"] != "mem_9" {
		t.Fatalf("response not decoded: %v", out)
	}
}

func TestAuditClientPostNilDstSkipsDecode(t *testing.T) {
	srv := auditServer(t, 200, `not-json{{{`, nil)
	defer srv.Close()
	c := auditClientTo(t, srv, "")
	// nil dst must not attempt to decode even a malformed body.
	if err := c.postJSON(context.Background(), "/x", map[string]any{}, nil); err != nil {
		t.Fatalf("postJSON nil dst: %v", err)
	}
}

func TestAuditClientHTTPErrorEnvelope(t *testing.T) {
	srv := auditServer(t, 404, `{"error":{"code":404,"message":"no active workspace for project"}}`, nil)
	defer srv.Close()
	c := auditClientTo(t, srv, "")
	err := c.postJSON(context.Background(), "/sessions/nope/join", map[string]any{}, nil)
	var he *httpError
	if !errors.As(err, &he) {
		t.Fatalf("err type = %T (%v), want *httpError", err, err)
	}
	if he.Status != 404 || he.Message != "no active workspace for project" {
		t.Fatalf("httpError = %+v", he)
	}
	if !isNotFound(err) || isMethodNotAllowed(err) {
		t.Fatal("isNotFound/isMethodNotAllowed mismatch")
	}
}

func TestAuditClientRawErrorFallback(t *testing.T) {
	srv := auditServer(t, 500, `boom`, nil)
	defer srv.Close()
	c := auditClientTo(t, srv, "")
	err := c.postJSON(context.Background(), "/memory", map[string]any{}, nil)
	var he *httpError
	if !errors.As(err, &he) || he.Message != "boom" {
		t.Fatalf("raw fallback wrong: %v", err)
	}
}

func TestAuditClientNotImplementedMapping(t *testing.T) {
	for _, status := range []int{404, 405} {
		err := &httpError{Status: status, Message: "x"}
		mapped := notImplementedFor("session list", err)
		var ni *errNotImplemented
		if !errors.As(mapped, &ni) {
			t.Fatalf("status %d: want *errNotImplemented, got %T", status, mapped)
		}
		if !strings.Contains(ni.Error(), "Phase 3 sessions") {
			t.Fatalf("phase wrong: %s", ni.Error())
		}
	}
	// Non-404/405 passes through untouched.
	orig := &httpError{Status: 500, Message: "x"}
	if mapped := notImplementedFor("session list", orig); mapped != orig {
		t.Fatal("500 must pass through unmapped")
	}
}

func TestAuditClientPhaseFor(t *testing.T) {
	cases := map[string]string{
		"memory confirm x": "Phase 2 confirmation flow",
		"memory reject x":  "Phase 2 confirmation flow",
		"session list":     "Phase 3 sessions",
		"branch fork":      "Phase 5 memory branching",
		"something else":   "a later phase",
	}
	for feat, want := range cases {
		if got := phaseFor(feat); got != want {
			t.Errorf("phaseFor(%q) = %q, want %q", feat, got, want)
		}
	}
}

func TestAuditClientNewClientTrimsBase(t *testing.T) {
	c := newAPIClient(Config{ServerURL: "http://h:8080/"})
	if c.Base != "http://h:8080" {
		t.Fatalf("Base = %q", c.Base)
	}
	if c.HTTP == nil || c.HTTP.Timeout <= 0 {
		t.Fatal("HTTP client must have a positive timeout")
	}
	// BUG(client): TrimSuffix removes only ONE trailing slash, so a
	// ServerURL ending in "//" or more keeps a stray slash and every
	// request path gains a "//" prefix segment (e.g. Base+"/memory" ->
	// "http://h:8080//memory"). Most servers normalize this, but the
	// client should TrimRight "/" instead. Locked in as current behavior.
	c2 := newAPIClient(Config{ServerURL: "http://h:8080///"})
	if c2.Base != "http://h:8080//" {
		t.Fatalf("multi-slash Base = %q, want documented current behavior %q", c2.Base, "http://h:8080//")
	}
}

func TestAuditClientFirstNonEmpty(t *testing.T) {
	if got := firstNonEmpty("", "  ", "a", "b"); got != "a" {
		t.Fatalf("firstNonEmpty = %q", got)
	}
	if got := firstNonEmpty("", " "); got != "" {
		t.Fatalf("all-blank = %q, want empty", got)
	}
}

func TestAuditClientTruncateEdges(t *testing.T) {
	if got := truncate("abc", 0); got != "" {
		t.Fatalf("truncate n=0 = %q", got)
	}
	if got := truncate("abc", 1); got != "a" {
		t.Fatalf("truncate n=1 = %q", got)
	}
	if got := truncate("abcdef", 5); len([]rune(got)) != 5 || !strings.HasSuffix(got, "…") {
		t.Fatalf("truncate = %q", got)
	}
}

func TestAuditClientStrFieldNumbers(t *testing.T) {
	m := map[string]any{"n": 2.5, "b": false}
	if strField(m, "n") != "2.5" {
		t.Fatalf("float field = %q", strField(m, "n"))
	}
	if strField(m, "b") != "false" {
		t.Fatalf("bool field = %q", strField(m, "b"))
	}
}
