package server

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"central-memory/internal/store"

	"github.com/jackc/pgx/v5/pgconn"
)

// registerAuthExtraRoutes wires Phase 1 auth/profile/token endpoints (issue #161).
func (s *Server) registerAuthExtraRoutes() {
	s.Mux.HandleFunc("POST /auth/signup", s.handleSignup)
	s.Mux.HandleFunc("GET /users/me", s.requireAuth(s.handleUsersMeGet))
	s.Mux.HandleFunc("PUT /users/me", s.requireAuth(s.handleUsersMePut))
	s.Mux.HandleFunc("PUT /users/me/password", s.requireAuth(s.handleUsersMePassword))
	s.Mux.HandleFunc("GET /users/me/usage", s.requireAuth(s.handleUsersMeUsage))
	s.Mux.HandleFunc("GET /auth/tokens", s.requireAuth(s.handleAuthTokensList))
	s.Mux.HandleFunc("POST /auth/tokens", s.requireAuth(s.handleAuthTokensCreate))
	s.Mux.HandleFunc("DELETE /auth/tokens/{tokenId}", s.requireAuth(s.handleAuthTokensRevoke))
}

type signupRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Email    string `json:"email"`
}

func (s *Server) handleSignup(w http.ResponseWriter, r *http.Request) {
	var req signupRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	username := strings.TrimSpace(req.Username)
	password := req.Password
	email := strings.TrimSpace(req.Email)
	if username == "" || strings.TrimSpace(password) == "" {
		writeError(w, http.StatusBadRequest, "username and password are required")
		return
	}
	if email == "" || !looksLikeEmail(email) {
		writeError(w, http.StatusBadRequest, "a valid email is required")
		return
	}
	if len(password) < 8 {
		writeError(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}
	if s.login == nil {
		s.login = newRateGate(1, 5)
	}
	if !s.login.allow("signup:" + clientIP(r)) {
		writeError(w, http.StatusTooManyRequests, "too many signup attempts")
		return
	}
	if s.Accounts == nil {
		writeError(w, http.StatusServiceUnavailable, "user authentication not configured")
		return
	}

	hash, err := HashPassword(password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not hash password")
		return
	}
	u, err := s.Accounts.Create(r.Context(), store.UserParams{
		Username: username,
		Email:    email,
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "username or email already taken")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not create user: "+err.Error())
		return
	}
	if err := s.Accounts.SetPasswordHash(r.Context(), u.ID, hash); err != nil {
		writeError(w, http.StatusInternalServerError, "could not set password")
		return
	}
	token, err := s.Auth.GenerateUser(u.ID, u.Username, DefaultTokenTTL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not issue token")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"token":    token,
		"username": u.Username,
		"user_id":  u.ID,
	})
}

func (s *Server) handleUsersMeGet(w http.ResponseWriter, r *http.Request) {
	if s.Accounts == nil {
		writeError(w, http.StatusServiceUnavailable, "user authentication not configured")
		return
	}
	u, err := s.Accounts.GetByID(r.Context(), authSubject(r))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "user not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not load user")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":                u.ID,
		"username":          u.Username,
		"email":             u.Email,
		"settings":          u.Settings,
		"created_at":        u.CreatedAt,
		"is_platform_admin": s.userIsPlatformAdmin(r.Context(), u.ID, u.Username),
	})
}

type usersMePutRequest struct {
	Email    *string         `json:"email"`
	Settings json.RawMessage `json:"settings"`
}

func (s *Server) handleUsersMePut(w http.ResponseWriter, r *http.Request) {
	if s.Accounts == nil {
		writeError(w, http.StatusServiceUnavailable, "user authentication not configured")
		return
	}
	var req usersMePutRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	var settingsPtr *string
	if req.Settings != nil {
		raw := strings.TrimSpace(string(req.Settings))
		if raw != "" && raw != "null" {
			settingsPtr = &raw
		}
	}
	u, err := s.Accounts.UpdateProfile(r.Context(), authSubject(r), req.Email, settingsPtr)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "user not found")
			return
		}
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "email already taken")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not update user")
		return
	}
	writeJSON(w, http.StatusOK, u)
}

type passwordChangeRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

func (s *Server) handleUsersMePassword(w http.ResponseWriter, r *http.Request) {
	if s.Accounts == nil {
		writeError(w, http.StatusServiceUnavailable, "user authentication not configured")
		return
	}
	var req passwordChangeRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.CurrentPassword) == "" || strings.TrimSpace(req.NewPassword) == "" {
		writeError(w, http.StatusBadRequest, "current_password and new_password are required")
		return
	}
	if len(req.NewPassword) < 8 {
		writeError(w, http.StatusBadRequest, "new_password must be at least 8 characters")
		return
	}
	hash, err := s.Accounts.GetPasswordHashByID(r.Context(), authSubject(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load password")
		return
	}
	if hash == "" || !VerifyPassword(req.CurrentPassword, hash) {
		writeError(w, http.StatusUnauthorized, "current password is incorrect")
		return
	}
	next, err := HashPassword(req.NewPassword)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not hash password")
		return
	}
	if err := s.Accounts.SetPasswordHash(r.Context(), authSubject(r), next); err != nil {
		writeError(w, http.StatusInternalServerError, "could not update password")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleUsersMeUsage(w http.ResponseWriter, r *http.Request) {
	if s.Accounts == nil {
		writeError(w, http.StatusServiceUnavailable, "user authentication not configured")
		return
	}
	stats, err := s.Accounts.GetUsage(r.Context(), authSubject(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load usage")
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (s *Server) handleAuthTokensList(w http.ResponseWriter, r *http.Request) {
	if s.Tokens == nil {
		writeError(w, http.StatusServiceUnavailable, "api tokens not configured")
		return
	}
	items, err := s.Tokens.ListByUser(r.Context(), authSubject(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list tokens")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

type createTokenRequest struct {
	Name    string   `json:"name"`
	Scopes  []string `json:"scopes"`
	AgentID string   `json:"agent_id"`
}

func (s *Server) handleAuthTokensCreate(w http.ResponseWriter, r *http.Request) {
	if s.Tokens == nil {
		writeError(w, http.StatusServiceUnavailable, "api tokens not configured")
		return
	}
	var req createTokenRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	agentID := strings.TrimSpace(req.AgentID)
	raw, prefix, hash, err := mintAPITokenSecret()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not mint token")
		return
	}
	scopes := req.Scopes
	if scopes == nil {
		scopes = []string{}
	}
	tok, err := s.Tokens.Create(r.Context(), authSubject(r), name, prefix, hash, scopes, agentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create token")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"token":      raw, // shown once
		"id":         tok.ID,
		"name":       tok.Name,
		"prefix":     tok.Prefix,
		"scopes":     tok.Scopes,
		"agent_id":   tok.AgentID,
		"created_at": tok.CreatedAt.Format(time.RFC3339Nano),
	})
}

func (s *Server) handleAuthTokensRevoke(w http.ResponseWriter, r *http.Request) {
	if s.Tokens == nil {
		writeError(w, http.StatusServiceUnavailable, "api tokens not configured")
		return
	}
	tokenID := strings.TrimSpace(r.PathValue("tokenId"))
	if tokenID == "" {
		writeError(w, http.StatusBadRequest, "tokenId is required")
		return
	}
	if err := s.Tokens.Revoke(r.Context(), authSubject(r), tokenID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "token not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not revoke token")
		return
	}
	if s.hub != nil {
		s.hub.DropByToken(tokenID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": tokenID, "revoked": true})
}

func mintAPITokenSecret() (raw, prefix, hash string, err error) {
	buf := make([]byte, 32)
	if _, err = rand.Read(buf); err != nil {
		return "", "", "", err
	}
	raw = "nxs_" + base64.RawURLEncoding.EncodeToString(buf)
	prefix = raw
	if len(prefix) > 12 {
		prefix = prefix[:12]
	}
	sum := sha256.Sum256([]byte(raw))
	hash = hex.EncodeToString(sum[:])
	return raw, prefix, hash, nil
}

func hashAPITokenSecret(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func looksLikeEmail(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || !strings.Contains(value, "@") {
		return false
	}
	at := strings.LastIndex(value, "@")
	if at <= 0 || at == len(value)-1 {
		return false
	}
	domain := value[at+1:]
	return strings.Contains(domain, ".")
}
