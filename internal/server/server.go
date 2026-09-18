// Package server implements the Central Server REST API (Phase 1.8).
//
// Endpoints (see implementation-plan.md §1.8):
//
//	POST /auth/login                  issue JWT stub token (simple login for v1)
//	POST /projects/resolve            return canonical project ID
//	POST /workspaces/register         register a machine workspace
//	POST /workspaces/heartbeat        update branch/commit/dirty + last_seen
//	GET  /workspaces/{projectId}/active  most-recently-seen online workspace
//	POST /memory                      create a proposed memory item
//	GET  /memory/search                keyword-fallback search (pgvector later)
//	POST /episodes                    open a new episode
//	GET  /episodes/search              search episodes by error pattern / text
//	GET  /healthz                      unauthenticated health check
package server

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"central-memory/internal/store"
)

// OfflineThreshold is the silence window after which a workspace is considered
// offline. It mirrors the daemon's 30s heartbeat interval x3 (tolerates two
// consecutive missed beats) and matches MemStore.GetActiveWorkspace's 90s
// filter. See docs/decisions/2026-09-17-central-server.md.
const OfflineThreshold = 90 * time.Second

// Server is the Central API HTTP server. It is intentionally thin: all
// persistence goes through the store.Store interface so the Postgres/RDS
// implementation can replace MemStore without touching handlers.
type Server struct {
	Store store.Store
	Mux   *http.ServeMux
	Auth  *Authenticator
	Log   *log.Logger

	// Ingest rate-limit gates (issues #93/#100). Lazily initialized so
	// zero-value Servers in tests still enforce defaults.
	events *rateGate
	fileop *rateGate
	quota  *quotaGate

	// login throttles /auth/login attempts (issue #133): 1/s sustained,
	// burst 5 per source IP. Lazily initialized like the gates above.
	login *rateGate

	// Users resolves login usernames to password hashes (issue #133).
	// Nil means authentication is unconfigured and /auth/login fails
	// closed with 503. Wire NewUserLookup(store.NewUserStore(db)) in
	// production; tests wire fakes.
	Users UserLookup

	// checkouts tracks active branch per project (issue #104): checkout
	// mutates this map, never just echoes the resolved branch.
	checkoutMu sync.Mutex
	checkouts  map[string]string

	// steer manager + hub for steering routes/WS (issue #42).
	// Set via AttachSteering; nil means steering routes 501.
	steerMu sync.RWMutex
	steer   steerManager
	hub     *Hub

	// handoffs stores in-flight handoff packages (issue #82).
	handoffMu sync.Mutex
	handoffs  map[string]*handoffRecord
}

// NewServer wires routes onto a fresh stdlib ServeMux.
func NewServer(st store.Store) *Server {
	s := &Server{
		Store:     st,
		Mux:       http.NewServeMux(),
		Auth:      NewAuthenticatorFromEnv(),
		Log:       log.New(os.Stderr, "[central-server] ", log.LstdFlags),
		events:    newRateGate(100, 100),
		fileop:    newRateGate(50, 50),
		quota:     newQuotaGate(),
		login:     newRateGate(1, 5),
		checkouts: make(map[string]string),
		handoffs:  make(map[string]*handoffRecord),
	}
	s.registerRoutes()
	return s
}

// eventAllowed enforces the 100 events/s gate before expensive ingest work.
func (s *Server) eventAllowed(scope string) bool {
	if s.events == nil {
		s.events = newRateGate(100, 100)
	}
	if scope == "" {
		scope = "global"
	}
	return s.events.allow(scope)
}

// quotaAllowed enforces governance token quotas on ingest content.
func (s *Server) quotaAllowed(chars int) (bool, string) {
	if s.quota == nil {
		s.quota = newQuotaGate()
	}
	return s.quota.allowN(estimateIngestTokens(chars))
}

// Handler returns the request-logging middleware chain around the mux.
func (s *Server) Handler() http.Handler {
	return s.withLogging(s.Mux)
}

// ServeHTTP implements http.Handler so *Server can be passed to http.Serve directly.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.Handler().ServeHTTP(w, r)
}

// WorkspaceIsOnline reports whether ws counts as online at time now.
// A workspace is online only if the store flagged it online AND its last
// heartbeat is within OfflineThreshold. The store's GetActiveWorkspace applies
// the same 90s rule; this helper lets handlers defend in depth and is unit
// tested directly.
func WorkspaceIsOnline(ws *store.Workspace, now time.Time) bool {
	if ws == nil || !ws.IsOnline {
		return false
	}
	return now.UTC().Sub(ws.LastSeen.UTC()) < OfflineThreshold
}

// --- JSON envelope helpers ---

// errorEnvelope is the single error shape returned by every handler.
type errorEnvelope struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// writeJSON encodes v as JSON with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes the standard {"error":{"code":...,"message":...}} envelope.
func writeError(w http.ResponseWriter, status int, msg string) {
	var env errorEnvelope
	env.Error.Code = status
	env.Error.Message = msg
	writeJSON(w, status, env)
}

// decodeJSON decodes a JSON request body, disallowing trailing garbage.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if r.Body == nil {
		writeError(w, http.StatusBadRequest, "request body is required")
		return false
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

// --- middleware ---

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (rec *statusRecorder) WriteHeader(status int) {
	rec.status = status
	rec.ResponseWriter.WriteHeader(status)
}

// withLogging logs method, path, status, and latency for every request.
func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		s.Log.Printf("%s %s -> %d (%s)", r.Method, r.URL.Path, rec.status, time.Since(start).Round(time.Millisecond))
	})
}

// requireAuth enforces the JWT stub on protected routes. /auth/login and
// /healthz are registered without this wrapper. On failure it returns 401
// with the standard error envelope.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sub, err := s.Auth.bearerSubject(r.Header.Get("Authorization"))
		if err != nil {
			if err == ErrExpiredToken {
				writeError(w, http.StatusUnauthorized, "token expired")
				return
			}
			writeError(w, http.StatusUnauthorized, "missing or invalid bearer token")
			return
		}
		r.Header.Set("X-Auth-Subject", sub)
		next(w, r)
	}
}
