// Tests for the static web-dashboard handler (issue #47, plan Phase 3).
//
// web.go shipped with zero tests. These pin, DB-free via httptest: the exact
// allowlist (only "/" + the two assets serve; everything else 404s —
// notably "/index.html", which is NOT an allowlisted URL path), the pinned
// MIME types (never host-mime-dependent), the security headers, traversal
// rejection (non-allowlisted paths 404 even with ".." segments), and that
// RegisterWebRoutes cannot shadow API routes (exact patterns, no catch-all).
package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// writeWebFixtures creates a dashboard asset dir with known bodies.
func writeWebFixtures(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	fixtures := map[string]string{
		"index.html": "<!doctype html><title>nexus</title>\n",
		"app.js":     "console.log('nexus');\n",
		"styles.css": "body { color: red; }\n",
	}
	for name, body := range fixtures {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestWebMimeTypesAndBodies(t *testing.T) {
	dir := writeWebFixtures(t)
	h := NewWebHandler(dir)
	cases := []struct {
		path string
		mime string
		body string
	}{
		{"/", "text/html; charset=utf-8", "<!doctype html><title>nexus</title>\n"},
		{"/app.js", "text/javascript; charset=utf-8", "console.log('nexus');\n"},
		{"/styles.css", "text/css; charset=utf-8", "body { color: red; }\n"},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s = %d, want 200", tc.path, rec.Code)
			}
			if got := rec.Header().Get("Content-Type"); got != tc.mime {
				t.Errorf("Content-Type = %q, want %q", got, tc.mime)
			}
			if got := rec.Body.String(); got != tc.body {
				t.Errorf("body = %q, want %q", got, tc.body)
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store", got)
			}
			if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
			}
		})
	}
}

func TestWebAllowlistRejectsUnknown(t *testing.T) {
	dir := writeWebFixtures(t)
	h := NewWebHandler(dir)
	// No SPA fallback: unknown paths must 404, never serve index.html.
	// "/index.html" is deliberately included — the allowlist keys on "/",
	// not the file name, so it must also 404.
	for _, p := range []string{
		"/nope", "/index.html", "/api/memory", "/healthz",
		"/APP.JS", "/app.js.map", "/",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if p == "/" {
			if rec.Code != http.StatusOK {
				t.Errorf("GET / = %d, want 200", rec.Code)
			}
			continue
		}
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", p, rec.Code)
		}
	}
}

func TestWebTraversalRejected(t *testing.T) {
	dir := writeWebFixtures(t)
	h := NewWebHandler(dir)
	// ".." segments clean to non-allowlisted paths, which must 404 —
	// directory escape can never serve outside the allowlist.
	for _, p := range []string{
		"/../secret", "/app.js/../secret", "/%2e%2e/secret",
		"/..%2fsecret", "/styles.css/%2e%2e/%2e%2e/secret",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", p, rec.Code)
		}
	}
}

func TestWebMissingFileIs404(t *testing.T) {
	// Allowlisted URL but no file on disk: 404 (ServeFile), never 500.
	h := NewWebHandler(t.TempDir())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/app.js", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /app.js with missing file = %d, want 404", rec.Code)
	}
}

func TestWebRegisterDoesNotShadowAPI(t *testing.T) {
	dir := writeWebFixtures(t)
	old := WebDir
	WebDir = dir
	defer func() { WebDir = old }()

	// RegisterWebRoutes must use exact patterns only ("GET /{$}" for root,
	// never a catch-all), so API routes and unknown paths are unaffected.
	mux := http.NewServeMux()
	srv := &Server{}
	srv.RegisterWebRoutes(mux)
	mux.HandleFunc("GET /api/ping", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("pong"))
	})

	for _, tc := range []struct {
		path string
		code int
		body string
	}{
		{"/", 200, "<!doctype html><title>nexus</title>\n"},
		{"/app.js", 200, "console.log('nexus');\n"},
		{"/api/ping", 200, "pong"},
		{"/api/unknown", 404, ""},
		{"/unknown", 404, ""},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if rec.Code != tc.code {
			t.Errorf("GET %s = %d, want %d", tc.path, rec.Code, tc.code)
		}
		if tc.body != "" && rec.Body.String() != tc.body {
			t.Errorf("GET %s body = %q, want %q", tc.path, rec.Body.String(), tc.body)
		}
	}
}
