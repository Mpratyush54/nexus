// Browser-safe CORS proxy for the workspace daemon (issue #169 / Phase 9).
//
// Listens on 127.0.0.1:7272 and exposes an allowlisted, read-only surface
// under /local/* so the PWA can inspect git/file state without the daemon
// bearer token. Dangerous write/exec paths are registered only to return
// 403 — they are never proxied.
package daemon

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DefaultProxyAddr is the localhost CORS bridge listen address.
// Override with DAEMON_PROXY env (host:port). Empty disables the proxy.
const DefaultProxyAddr = "127.0.0.1:7272"

// ResolveProxyAddr returns the proxy listen address: DAEMON_PROXY env wins,
// else DefaultProxyAddr. Explicit empty env disables the proxy.
func ResolveProxyAddr() string {
	if v, ok := os.LookupEnv("DAEMON_PROXY"); ok {
		return strings.TrimSpace(v)
	}
	return DefaultProxyAddr
}

// CORSProxy is a read-only HTTP front for browser clients. It is bound to
// loopback only and never forwards /file/write or /command/run.
type CORSProxy struct {
	// Daemon is the workspace agent whose read-only handlers are exposed.
	Daemon *Daemon
	// Addr defaults to DefaultProxyAddr when empty.
	Addr string

	mu  sync.Mutex
	srv *http.Server
}

// NewCORSProxy builds a proxy bound to d. Addr defaults to ResolveProxyAddr().
func NewCORSProxy(d *Daemon) *CORSProxy {
	return &CORSProxy{Daemon: d, Addr: ResolveProxyAddr()}
}

// Handler returns the CORS-wrapped allowlist mux (for tests and embedding).
func (p *CORSProxy) Handler() http.Handler {
	mux := http.NewServeMux()
	d := p.Daemon
	if d == nil {
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			writeErr(w, http.StatusServiceUnavailable, "daemon not configured")
		})
		return withProxyCORS(mux)
	}

	// Allowlisted read-only local endpoints.
	// Status/healthz stay open so the portal can detect Desktop identity.
	// Privileged harvest/git/file require matching portal user (desktop guard).
	mux.HandleFunc("/", d.handleStatusPage)
	mux.HandleFunc("/local/healthz", handleLocalHealthz)
	mux.HandleFunc("/local/status", d.handleLocalStatus)
	mux.HandleFunc("/local/browser-login", d.handleLocalBrowserLogin)
	mux.HandleFunc("/local/login", d.handleLocalLogin) // legacy password form
	mux.HandleFunc("/local/logout", d.handleLocalLogout)

	mux.HandleFunc("/local/git/status", d.withPortalIdentityGate(d.handleGitStatus))
	mux.HandleFunc("/local/git/diff", d.withPortalIdentityGate(d.handleGitDiff))
	mux.HandleFunc("/local/git/log", d.withPortalIdentityGate(d.handleGitLog))
	mux.HandleFunc("/local/file/read", d.withPortalIdentityGate(d.handleFileRead))
	mux.HandleFunc("/local/workspace", d.withPortalIdentityGate(d.handleLocalWorkspace))
	mux.HandleFunc("/local/harvest", d.withPortalIdentityGate(d.handleLocalHarvest))

	// Explicitly block dangerous surfaces if a client probes them.
	block := func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusForbidden, "endpoint blocked on CORS proxy (read-only)")
	}
	mux.HandleFunc("/local/file/write", block)
	mux.HandleFunc("/local/command/run", block)
	mux.HandleFunc("/file/write", block)
	mux.HandleFunc("/command/run", block)

	return withProxyCORS(mux)
}

// Start serves the CORS proxy on Addr. It blocks until the server stops.
// On listen, the bound URL is recorded on Daemon.ProxyURL for heartbeat relay.
func (p *CORSProxy) Start() error {
	addr := strings.TrimSpace(p.Addr)
	if addr == "" {
		addr = DefaultProxyAddr
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	bound := ln.Addr().String()
	p.mu.Lock()
	p.Addr = bound
	p.srv = &http.Server{Addr: bound, Handler: p.Handler()}
	srv := p.srv
	p.mu.Unlock()
	if p.Daemon != nil {
		p.Daemon.SetProxyURL("http://" + bound)
	}
	return srv.Serve(ln)
}

// Close gracefully stops a started proxy.
func (p *CORSProxy) Close() error {
	p.mu.Lock()
	srv := p.srv
	p.mu.Unlock()
	if srv == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(ctx)
}

// handleLocalHealthz is the PWA auto-detect probe (GET /local/healthz).
func handleLocalHealthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleLocalWorkspace returns a browser-safe workspace snapshot.
func (d *Daemon) handleLocalWorkspace(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	branch, commit, dirty, _, err := GitStatus(d.Root)
	if err != nil {
		// Non-git workspaces still report path/machine; leave git fields empty.
		branch, commit, dirty = "", "", false
	}
	name := filepath.Base(d.Root)
	writeJSON(w, http.StatusOK, map[string]any{
		"project":      name,
		"path":         d.Root,
		"branch":       branch,
		"commit":       commit,
		"is_dirty":     dirty,
		"machine_id":   d.MachineID,
		"workspace_id": d.getWorkspaceID(),
	})
}

// proxyCORSOrigins lists browser origins allowed to call the localhost
// bridge. Matches the central-server allowlist plus optional CORS_ORIGINS.
func proxyCORSOrigins() map[string]bool {
	allowed := map[string]bool{
		"https://nexus.pratyushes.dev": true,
		"http://localhost:5173":        true,
		"http://127.0.0.1:5173":        true,
		"http://localhost:4173":        true,
		"http://127.0.0.1:4173":        true,
	}
	if extra := strings.TrimSpace(os.Getenv("CORS_ORIGINS")); extra != "" {
		for _, o := range strings.Split(extra, ",") {
			o = strings.TrimSpace(o)
			if o != "" {
				allowed[o] = true
			}
		}
	}
	return allowed
}

func withProxyCORS(next http.Handler) http.Handler {
	allowed := proxyCORSOrigins()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && allowed[origin] {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Nexus-Portal-User, Access-Control-Request-Private-Network")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Max-Age", "86400")
			// Chrome Private Network Access: HTTPS public sites (nexus.pratyushes.dev)
			// probing http://127.0.0.1 require this on the preflight response.
			w.Header().Set("Access-Control-Allow-Private-Network", "true")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
