// Package server implements issue #8 (plan §1.8): the central REST API.
//
// Endpoints (all JSON, stdlib net/http only):
//
//	POST /auth/login                  username/password → JWT (open)
//	POST /projects/resolve            upsert by origin/root-commit/folder
//	POST /workspaces/register         upsert on (machine_id, path)
//	POST /workspaces/heartbeat        refresh last_seen + git state
//	GET  /workspaces/{projectID}/active  online workspaces of a project
//	POST /memory                      create a memory item
//	GET  /memory/search                filter memories (text/tag/level)
//	POST /episodes                     open an episode
//	GET  /episodes/search              filter episodes
//	GET  /healthz                      liveness probe (open)
//
// All routes except /auth/login and /healthz require
// `Authorization: Bearer <JWT>` (HS256, github.com/golang-jwt/jwt/v5).
//
// Storage seam: the narrow Store interface below. The server never touches
// Postgres directly and never edits internal/store — a Postgres-backed
// adapter (wiring ProjectStore/WorkspaceStore/memory SQL) is a follow-up;
// tests substitute an in-memory fake.
//
// Presence: a workspace is active while its last_seen is within
// store.OfflineAfter (90s) of the server clock. The server filters with
// store.IsOnlineAtPtr itself, so reads stay correct even when the DB sweeper
// (store.MarkStaleOffline) has not run yet — same dual-predicate reasoning
// as store.WorkspaceStore.ListActive.
package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"central-memory/internal/store"

	"github.com/golang-jwt/jwt/v5"
)

// ErrUnauthorized is returned by Store.Authenticate when credentials are bad.
// Handlers map it to 401; any other store error is a 500.
var ErrUnauthorized = errors.New("server: unauthorized")

// OfflineAfter re-exports the single source of truth for heartbeat silence.
// It is an alias, not a copy: drift is impossible by construction.
const OfflineAfter = store.OfflineAfter

// IsOnlineAt reports whether a workspace last seen at lastSeen is online at
// now. It delegates to store.IsOnlineAtPtr (nil last_seen = offline) so the
// server and the store package share one definition of "stale".
func IsOnlineAt(lastSeen *time.Time, now time.Time) bool {
	return store.IsOnlineAtPtr(lastSeen, now)
}

// ---------------------------------------------------------------------------
// API types (server-owned JSON shapes; mapped to store types at the seam)
// ---------------------------------------------------------------------------

// Memory is a memory item as exposed by the REST API. Field validation
// mirrors the memory_items CHECK constraints (plan §1.1): content 20–2000
// chars, level/scope/status from fixed vocabularies.
type Memory struct {
	ID             string   `json:"id"`
	ProjectID      string   `json:"project_id"`
	Key            string   `json:"key"`
	Content        string   `json:"content"`
	ContextSnippet string   `json:"context_snippet,omitempty"`
	Level          string   `json:"level"`
	Scope          string   `json:"scope"`
	Tags           []string `json:"tags,omitempty"`
	Confidence     float64  `json:"confidence"`
	Status         string   `json:"status"`
	Source         string   `json:"source,omitempty"`
	CreatedAt      string   `json:"created_at"`
}

// MemoryFilter scopes GET /memory/search. Empty fields are ignored except
// ProjectID, which is required.
type MemoryFilter struct {
	ProjectID string
	Query     string // substring over key + content + context_snippet
	Tags      []string
	Key       string
	Level     string
	Limit     int
}

// Episode is a bug/incident/feature arc as exposed by the REST API,
// mirroring the episodes table (plan §1.1).
type Episode struct {
	ID            string   `json:"id"`
	ProjectID     string   `json:"project_id"`
	Title         string   `json:"title"`
	EpisodeType   string   `json:"episode_type"`
	Trigger       string   `json:"trigger,omitempty"`
	Investigation string   `json:"investigation,omitempty"`
	RootCause     string   `json:"root_cause,omitempty"`
	Resolution    string   `json:"resolution,omitempty"`
	Verification  string   `json:"verification,omitempty"`
	Tags          []string `json:"tags,omitempty"`
	FilesInvolved []string `json:"files_involved,omitempty"`
	ErrorPatterns []string `json:"error_patterns,omitempty"`
	Status        string   `json:"status"`
	OpenedAt      string   `json:"opened_at"`
}

// EpisodeFilter scopes GET /episodes/search. Empty fields are ignored except
// ProjectID, which is required.
type EpisodeFilter struct {
	ProjectID    string
	Query        string // substring over title + trigger + root_cause + resolution
	ErrorPattern string // exact member of error_patterns
	File         string // exact member of files_involved
	Status       string
	EpisodeType  string
	Limit        int
}

// ---------------------------------------------------------------------------
// Store seam (narrow interface; fakes substitute without Postgres)
// ---------------------------------------------------------------------------

// Store is the persistence surface the REST handlers need. Method set is
// deliberately one-per-endpoint so fakes stay trivial and the future
// Postgres adapter maps each method to exactly one store call:
//
//	Authenticate         → users table lookup (issue follow-up; no users.go yet)
//	ResolveProject       → ProjectStore.Resolve
//	RegisterWorkspace    → WorkspaceStore.Register
//	HeartbeatWorkspace   → WorkspaceStore.Heartbeat
//	ListWorkspaces       → raw per-project list; the SERVER applies the
//	                       IsOnlineAt filter, so no MarkStaleOffline dependency
//	CreateMemory/ListMemories → memory_items insert / filtered select
//	 (no store method yet; vector search in SearchMemory is a follow-up)
//	CreateEpisode/ListEpisodes → episodes insert / filtered select
type Store interface {
	Authenticate(ctx context.Context, username, password string) (userID string, err error)
	ResolveProject(ctx context.Context, params store.ProjectParams) (*store.Project, error)
	RegisterWorkspace(ctx context.Context, params store.WorkspaceParams) (*store.Workspace, error)
	HeartbeatWorkspace(ctx context.Context, id string, hb store.HeartbeatParams) (*store.Workspace, error)
	ListWorkspaces(ctx context.Context, projectID string) ([]store.Workspace, error)
	CreateMemory(ctx context.Context, m Memory) (*Memory, error)
	SearchMemory(ctx context.Context, q MemoryFilter) ([]Memory, error)
	CreateEpisode(ctx context.Context, e Episode) (*Episode, error)
	SearchEpisodes(ctx context.Context, q EpisodeFilter) ([]Episode, error)
}

// ---------------------------------------------------------------------------
// Server
// ---------------------------------------------------------------------------

// Options tunes the Server. JWTSecret is required (HMAC-HS256 key, ≥32 bytes
// recommended). TokenTTL defaults to 24h. Now defaults to time.Now and
// exists so tests drive the 90s offline transition with a fixed clock.
type Options struct {
	JWTSecret []byte
	TokenTTL  time.Duration
	Now       func() time.Time
}

// Server is the central REST API. It is safe for concurrent use; Store must
// also be safe for concurrent use.
type Server struct {
	store  Store
	mux    *http.ServeMux
	secret []byte
	ttl    time.Duration
	now    func() time.Time
}

// DefaultTokenTTL is the JWT lifetime when Options.TokenTTL is unset.
const DefaultTokenTTL = 24 * time.Hour

// maxBodyBytes caps JSON request bodies (DoS guard; largest legit payload is
// a heartbeat or memory write, both small).
const maxBodyBytes = 1 << 20 // 1 MiB

// New wires the server: routes + auth middleware. It panics on an empty
// JWTSecret — fail fast rather than minting unverifiable tokens.
func New(st Store, opts Options) *Server {
	if st == nil {
		panic("server: nil Store")
	}
	if len(opts.JWTSecret) == 0 {
		panic("server: Options.JWTSecret is required")
	}
	ttl := opts.TokenTTL
	if ttl <= 0 {
		ttl = DefaultTokenTTL
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	s := &Server{store: st, secret: opts.JWTSecret, ttl: ttl, now: now}
	s.mux = http.NewServeMux()
	s.registerRoutes(s.mux)
	s.mux.HandleFunc("POST /memory/{id}/confirm", s.handleConfirmMemory)
	s.mux.HandleFunc("POST /memory/{id}/reject", s.handleRejectMemory)
	s.mux.HandleFunc("POST /memory/{id}/promote", s.handlePromoteMemory)
	return s
}

// Handler exposes the full stack (auth middleware + routes) as an
// http.Handler for net/http.Serve or httptest.
func (s *Server) Handler() http.Handler {
	return s.withAuth(s.mux)
}

// ---------------------------------------------------------------------------
// Auth (JWT HS256 via github.com/golang-jwt/jwt/v5 — the single allowed dep)
// ---------------------------------------------------------------------------

// contextKey is the request-context key for the authenticated user ID.
type contextKey struct{}

// UserID returns the authenticated user ID for r ("" when unauthenticated,
// e.g. inside /auth/login or /healthz).
func UserID(r *http.Request) string {
	id, _ := r.Context().Value(contextKey{}).(string)
	return id
}

// openPaths skip auth: login mints tokens, healthz is the LB probe.
var openPaths = map[string]bool{
	"POST /auth/login": true,
	"GET /healthz":     true,
}

// withAuth enforces Bearer JWT on every route except openPaths. Failures are
// 401 with a JSON envelope (never 403: the caller is unauthenticated, not
// forbidden).
func (s *Server) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if openPaths[r.Method+" "+routePattern(r)] {
			next.ServeHTTP(w, r)
			return
		}
		id, ok := s.verifyToken(tokenFromHeader(r.Header.Get("Authorization")))
		if !ok {
			writeError(w, http.StatusUnauthorized, "missing or invalid bearer token")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), contextKey{}, id)))
	})
}

// routePattern recovers the mux pattern ("METHOD /path-template") for the
// open-path check without hardcoding full paths in the middleware.
func routePattern(r *http.Request) string {
	p := r.URL.Path
	switch {
	case p == "/auth/login":
		return "/auth/login"
	case p == "/healthz":
		return "/healthz"
	case strings.HasPrefix(p, "/projects/"):
		return "/projects/*"
	case strings.HasPrefix(p, "/workspaces/"):
		return "/workspaces/*"
	case strings.HasPrefix(p, "/memory"):
		return "/memory*"
	case strings.HasPrefix(p, "/episodes"):
		return "/episodes*"
	}
	return p
}

// Claims is the JWT payload: Subject = user ID, plus username for display.
type Claims struct {
	Username string `json:"username"`
	jwt.RegisteredClaims
}

// mintToken issues a signed JWT for userID/username, expiring now+ttl.
func (s *Server) mintToken(userID, username string) (token string, expiresAt time.Time, err error) {
	now := s.now()
	expiresAt = now.Add(s.ttl)
	claims := Claims{
		Username: username,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString(s.secret)
	if err != nil {
		return "", time.Time{}, err
	}
	return signed, expiresAt, nil
}

// verifyToken parses and validates tok (signature + HMAC method + expiry,
// evaluated against the server clock so tests control time). It returns the
// subject on success.
func (s *Server) verifyToken(tok string) (string, bool) {
	if tok == "" {
		return "", false
	}
	claims := &Claims{}
	t, err := jwt.ParseWithClaims(tok, claims,
		func(t *jwt.Token) (any, error) {
			if t.Method != jwt.SigningMethodHS256 {
				return nil, errors.New("server: unexpected signing method")
			}
			return s.secret, nil
		},
		jwt.WithTimeFunc(s.now),
	)
	if err != nil || !t.Valid || claims.Subject == "" {
		return "", false
	}
	return claims.Subject, true
}

// tokenFromHeader strips the "Bearer " scheme; "" when absent/malformed.
func tokenFromHeader(h string) string {
	if h == "" {
		return ""
	}
	scheme, token, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(scheme, "bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}
