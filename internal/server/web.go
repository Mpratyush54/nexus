// Static web-dashboard routes for issue #14 (plan Phase 3).
//
// OWNERSHIP: this file ONLY. routes.go, server.go and ws.go are owned by
// issues #8/#13 and are deliberately untouched — this file adds the
// dashboard's static-file route without modifying them.
//
// Serving choice: disk-backed static handler, not go:embed. go:embed
// patterns forbid "..", and the issue requires web/ at the repo root, so
// ../../web cannot be embedded from this package. The issue explicitly
// allows "Go embed or static handler" — this is the static handler.
//
// Auth note: RegisterWebRoutes mounts on whatever mux the caller passes.
// If that is the authenticated mux (Server.Handler), browsers cannot send
// `Authorization: Bearer` on navigation, so the follow-up is either to
// mount NewWebHandler outside withAuth in the server entrypoint (preferred)
// or to extend openPaths in server.go (another owner's file — not done
// here). See ADR-014.
package server

import (
	"net/http"
	"path"
	"path/filepath"
)

// WebDir is the dashboard asset directory, relative to the server process's
// working directory ("web" when run from the repo root). Override it when
// embedding the server elsewhere; NewWebHandler takes an explicit dir.
var WebDir = "web"

// webFileNames is the exact allowlist of servable assets: URL path → file
// on disk. Anything else is 404 — notably there is NO SPA fallback, so
// unknown paths can never swallow API 404s.
var webFileNames = map[string]string{
	"/":           "index.html",
	"/app.js":     "app.js",
	"/styles.css": "styles.css",
}

// webContentTypes pins MIME per asset so rendering never depends on the
// host's mime tables (notably .js, which some platforms mislabel).
var webContentTypes = map[string]string{
	"/":           "text/html; charset=utf-8",
	"/app.js":     "text/javascript; charset=utf-8",
	"/styles.css": "text/css; charset=utf-8",
}

// NewWebHandler returns a standalone handler serving the dashboard's three
// static files from dir (WebDir when dir == ""). Mount it outside the auth
// middleware so browsers can load the SPA without a Bearer token; the SPA
// itself sends the JWT on every REST fetch and WS ?token= query.
func NewWebHandler(dir string) http.Handler {
	if dir == "" {
		dir = WebDir
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveWebFile(w, r, dir)
	})
}

// RegisterWebRoutes mounts the dashboard on mux: exact root plus the two
// static assets. "GET /{$}" matches ONLY "/" (no catch-all), so existing
// API routes are unaffected. Call it next to New(), e.g.:
//
//	srv := New(store, opts)
//	mux := http.NewServeMux()
//	// ... re-register API via srv.Handler() or mount web outside auth:
//	srv.RegisterWebRoutes(mux)
func (s *Server) RegisterWebRoutes(mux *http.ServeMux) {
	h := NewWebHandler("")
	mux.Handle("GET /{$}", h)
	mux.Handle("GET /app.js", h)
	mux.Handle("GET /styles.css", h)
}

// serveWebFile serves one allowlisted file with pinned MIME, no-store (the
// dashboard is a live view; stale index.html would pin stale WS paths), and
// nosniff. Non-allowlisted paths are 404.
func serveWebFile(w http.ResponseWriter, r *http.Request, dir string) {
	clean := path.Clean(r.URL.Path)
	name, ok := webFileNames[clean]
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", webContentTypes[clean])
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeFile(w, r, filepath.Join(dir, name))
}
