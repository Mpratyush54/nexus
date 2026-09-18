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
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	memctx "central-memory/internal/context"
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

	// Accounts is the full user store for signup/profile/usage (issue #161).
	Accounts *store.UserStore

	// Tokens is the API token store for mint/list/revoke (issue #161).
	Tokens *store.APITokenStore

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

	// Embedder generates vectors for POST /memory and GET /memory/search
	// when the client omits a precomputed embedding (issue #165). Nil
	// selects EmbedderFromEnv at first use (hash fallback by default).
	Embedder memctx.Embedder
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

// resolveEmbedder returns the server's embedding backend (issue #165).
// Nil Embedder lazily binds EmbedderFromEnv (hash by default).
func (s *Server) resolveEmbedder() memctx.Embedder {
	if s != nil && s.Embedder != nil {
		return s.Embedder
	}
	return memctx.EmbedderFromEnv()
}

// embedText generates a 1536-dim vector for text; provider failures fall
// back to HashEmbed inside NewEmbedder.
func (s *Server) embedText(ctx context.Context, text string) []float32 {
	vec, err := s.resolveEmbedder()(ctx, text)
	if err != nil || len(vec) != memctx.EmbedDims {
		return memctx.HashEmbed(text)
	}
	return vec
}

// authorizeProject enforces the project-membership boundary (issue #141):
// every project-scoped route must pass through here. Empty project is a
// 400; store errors fail closed (500, never fail-open); non-members get
// 403. Object-ID routes resolve object → project first, then call this.
func (s *Server) authorizeProject(w http.ResponseWriter, r *http.Request, projectID string) bool {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		writeError(w, http.StatusBadRequest, "project_id is required")
		return false
	}
	ok, err := s.Store.IsProjectMember(r.Context(), authSubject(r), projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not check project membership")
		return false
	}
	if !ok {
		writeError(w, http.StatusForbidden, "not a member of this project")
		return false
	}
	return true
}

// Handler returns the CORS + request-logging middleware chain around the mux.
func (s *Server) Handler() http.Handler {
	return s.withCORS(s.withLogging(s.Mux))
}

// ServeHTTP implements http.Handler so *Server can be passed to http.Serve directly.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.Handler().ServeHTTP(w, r)
}

// WorkspaceIsOnline reports whether ws counts as online at time now.
// A workspace is online only if the store flagged it online AND its last
// heartbeat is within OfflineThreshold (inclusive: exact 90s silence is
// still online, matching store.IsOnlineAt — issue #131). The store's
// GetActiveWorkspace applies the same 90s rule; this helper lets handlers
// defend in depth and is unit tested directly.
func WorkspaceIsOnline(ws *store.Workspace, now time.Time) bool {
	if ws == nil || !ws.IsOnline {
		return false
	}
	return now.UTC().Sub(ws.LastSeen.UTC()) <= OfflineThreshold
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

// maxRequestBodyBytes caps JSON request bodies (issue #154): the daemon
// already caps at 2MB via MaxBytesReader; the server had no cap, letting a
// client stream gigabytes into /auth/login or /memory.
const maxRequestBodyBytes = 2 << 20

// decodeJSON decodes a JSON request body, disallowing trailing garbage.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if r.Body == nil {
		writeError(w, http.StatusBadRequest, "request body is required")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	// Trailing garbage after the first value is rejected (issue #154):
	// Decode stops at the first complete value, so verify EOF follows.
	var extra any
	if err := dec.Decode(&extra); err == nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: trailing data after JSON value")
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

// requireAuth enforces JWT or API-token auth on protected routes. /auth/login,
// /auth/signup, and /healthz are registered without this wrapper.
//
// Identity (issue #140): X-Auth-Subject carries the canonical user UUID
// (login mints sub=users.id) for Postgres UUID columns; X-Auth-User
// carries the display username for logs and UI. X-Auth-Token-ID is set for
// opaque API tokens so WS clients can be dropped on revoke (issue #161).
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := s.resolveBearer(r)
		if err != nil {
			if err == ErrExpiredToken {
				writeError(w, http.StatusUnauthorized, "token expired")
				return
			}
			writeError(w, http.StatusUnauthorized, "missing or invalid bearer token")
			return
		}
		r.Header.Set("X-Auth-Subject", id.Sub)
		r.Header.Set("X-Auth-User", id.Username)
		if id.TokenID != "" {
			r.Header.Set("X-Auth-Token-ID", id.TokenID)
		}
		next(w, r)
	}
}

type resolvedAuth struct {
	Sub      string
	Username string
	TokenID  string
}

func (s *Server) resolveBearer(r *http.Request) (resolvedAuth, error) {
	header := r.Header.Get("Authorization")
	if header == "" {
		return resolvedAuth{}, ErrInvalidToken
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return resolvedAuth{}, ErrInvalidToken
	}
	token := strings.TrimSpace(strings.TrimPrefix(header, prefix))
	if token == "" {
		return resolvedAuth{}, ErrInvalidToken
	}
	if strings.HasPrefix(token, "nxs_") {
		return s.resolveAPIToken(r, token)
	}
	sub, username, err := s.Auth.ValidateClaims(token)
	if err != nil {
		return resolvedAuth{}, err
	}
	return resolvedAuth{Sub: sub, Username: username}, nil
}

func (s *Server) resolveAPIToken(r *http.Request, raw string) (resolvedAuth, error) {
	if s.Tokens == nil {
		return resolvedAuth{}, ErrInvalidToken
	}
	tok, err := s.Tokens.LookupActiveByHash(r.Context(), hashAPITokenSecret(raw))
	if err != nil {
		return resolvedAuth{}, ErrInvalidToken
	}
	username := ""
	if s.Accounts != nil {
		if u, uerr := s.Accounts.GetByID(r.Context(), tok.UserID); uerr == nil && u != nil {
			username = u.Username
		}
	}
	_ = s.Tokens.TouchLastUsed(r.Context(), tok.ID)
	return resolvedAuth{Sub: tok.UserID, Username: username, TokenID: tok.ID}, nil
}

// corsAllowedOrigins lists browser origins allowed to call the API.
func corsAllowedOrigins() map[string]bool {
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

func (s *Server) withCORS(next http.Handler) http.Handler {
	allowed := corsAllowedOrigins()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && allowed[origin] {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Max-Age", "86400")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
