package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func doProxy(p *CORSProxy, method, target, body, origin string) *httptest.ResponseRecorder {
	var rdr *strings.Reader
	if body == "" {
		rdr = strings.NewReader("")
	} else {
		rdr = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, target, rdr)
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	p.Handler().ServeHTTP(w, r)
	return w
}

func TestCORSProxyHealthz(t *testing.T) {
	d := testDaemon(t)
	p := NewCORSProxy(d)
	w := doProxy(p, http.MethodGet, "/local/healthz", "", "http://localhost:5173")
	if w.Code != http.StatusOK {
		t.Fatalf("healthz status = %d (%s)", w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["ok"] != true {
		t.Fatalf("healthz body = %s, want ok:true", w.Body.String())
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
		t.Fatalf("CORS origin = %q, want localhost:5173", got)
	}
}

func TestCORSProxyPrivateNetworkAccess(t *testing.T) {
	d := testDaemon(t)
	p := NewCORSProxy(d)
	r := httptest.NewRequest(http.MethodOptions, "/local/status", nil)
	r.Header.Set("Origin", "https://nexus.pratyushes.dev")
	r.Header.Set("Access-Control-Request-Method", "GET")
	r.Header.Set("Access-Control-Request-Private-Network", "true")
	w := httptest.NewRecorder()
	p.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("OPTIONS status = %d", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Private-Network"); got != "true" {
		t.Fatalf("Allow-Private-Network = %q, want true (Chrome PNA)", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://nexus.pratyushes.dev" {
		t.Fatalf("CORS origin = %q", got)
	}
}

func TestCORSProxyHealthzNoAuth(t *testing.T) {
	d := testDaemon(t)
	p := NewCORSProxy(d)
	// No Authorization header — browser bridge is auth-free on loopback.
	w := doProxy(p, http.MethodGet, "/local/healthz", "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("unauthenticated healthz = %d, want 200", w.Code)
	}
}

func TestCORSProxyBlocksWrites(t *testing.T) {
	d := testDaemon(t)
	p := NewCORSProxy(d)
	for _, path := range []string{
		"/local/file/write",
		"/local/command/run",
		"/file/write",
		"/command/run",
	} {
		w := doProxy(p, http.MethodPost, path, `{"path":"x","content":"y"}`, "http://localhost:5173")
		if w.Code != http.StatusForbidden {
			t.Errorf("%s: got %d want 403 (%s)", path, w.Code, w.Body.String())
		}
	}
}

func TestCORSProxyFileRead(t *testing.T) {
	d := testDaemon(t)
	p := NewCORSProxy(d)
	w := doProxy(p, http.MethodPost, "/local/file/read", `{"path":"a.txt"}`, "http://localhost:5173")
	if w.Code != http.StatusOK {
		t.Fatalf("file/read = %d (%s)", w.Code, w.Body.String())
	}
	var out struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Content != "alpha content here" {
		t.Fatalf("content = %q", out.Content)
	}
}

func TestCORSProxyWorkspace(t *testing.T) {
	d := testDaemon(t)
	p := NewCORSProxy(d)
	w := doProxy(p, http.MethodGet, "/local/workspace", "", "https://nexus.pratyushes.dev")
	if w.Code != http.StatusOK {
		t.Fatalf("workspace = %d (%s)", w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["path"] != d.Root {
		t.Fatalf("path = %v want %s", out["path"], d.Root)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://nexus.pratyushes.dev" {
		t.Fatalf("CORS origin = %q", got)
	}
}

func TestCORSProxyGitStatus(t *testing.T) {
	d := testDaemon(t)
	p := NewCORSProxy(d)
	w := doProxy(p, http.MethodGet, "/local/git/status", "", "")
	// TempDir is not a git repo — handler still returns 200 with empty git
	// fields or 500 depending on git availability; accept either structured
	// JSON as long as the route is reachable without auth.
	if w.Code != http.StatusOK && w.Code != http.StatusInternalServerError {
		t.Fatalf("git/status = %d (%s)", w.Code, w.Body.String())
	}
}

func TestCORSProxyOPTIONS(t *testing.T) {
	d := testDaemon(t)
	p := NewCORSProxy(d)
	w := doProxy(p, http.MethodOptions, "/local/healthz", "", "http://localhost:5173")
	if w.Code != http.StatusNoContent {
		t.Fatalf("OPTIONS = %d want 204", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
		t.Fatalf("CORS origin = %q", got)
	}
}

func TestCORSProxyDisallowsUnknownOrigin(t *testing.T) {
	d := testDaemon(t)
	p := NewCORSProxy(d)
	w := doProxy(p, http.MethodGet, "/local/healthz", "", "https://evil.example")
	if w.Code != http.StatusOK {
		t.Fatalf("healthz still serves locally: %d", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("unexpected CORS allow for evil origin: %q", got)
	}
}

func TestNewCORSProxyDefaultAddr(t *testing.T) {
	p := NewCORSProxy(testDaemon(t))
	if p.Addr != DefaultProxyAddr {
		t.Fatalf("Addr = %q want %q", p.Addr, DefaultProxyAddr)
	}
}
