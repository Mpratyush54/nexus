package server

import (
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"central-memory/internal/store"
)

var guestOffboardRoutes sync.Map

const guestLinkDefaultHours = 7 * 24

func (s *Server) guestOffboardStore() (store.GuestOffboardStore, bool) {
	gs, ok := s.Store.(store.GuestOffboardStore)
	return gs, ok
}

func (s *Server) registerGuestOffboardRoutes() {
	if s == nil || s.Mux == nil {
		return
	}
	if _, loaded := guestOffboardRoutes.LoadOrStore(s.Mux, struct{}{}); loaded {
		return
	}
	s.Mux.HandleFunc("POST /v1/agent-sessions/{id}/guest-links", s.requireAuth(s.handleGuestLinkCreate))
	s.Mux.HandleFunc("POST /v1/guest-links/accept", s.requireAuth(s.handleGuestLinkAccept))
	s.Mux.HandleFunc("GET /orgs/{id}/offboard/preview", s.requireAuth(s.handleOrgOffboardPreview))
	s.Mux.HandleFunc("POST /orgs/{id}/offboard", s.requireAuth(s.handleOrgOffboard))
}

func (s *Server) handleGuestLinkCreate(w http.ResponseWriter, r *http.Request) {
	cs, ok := s.agentCloud()
	if !ok {
		writeError(w, http.StatusNotImplemented, "agent sessions unavailable")
		return
	}
	gs, ok := s.guestOffboardStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "guest links unavailable")
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("id"))
	row, err := cs.GetAgentSession(r.Context(), sessionID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if row.OwnerUserID != authSubject(r) {
		writeError(w, http.StatusForbidden, "only the session owner can create a guest link")
		return
	}
	var body struct {
		ExpiresInHours int   `json:"expires_in_hours"`
		SingleUse      *bool `json:"single_use"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.ExpiresInHours < 0 {
		writeError(w, http.StatusBadRequest, "expires_in_hours must be positive")
		return
	}
	hours := body.ExpiresInHours
	if hours == 0 {
		hours = guestLinkDefaultHours
	}
	singleUse := true
	if body.SingleUse != nil {
		singleUse = *body.SingleUse
	}
	link, err := gs.CreateGuestLink(r.Context(), sessionID, authSubject(r), time.Now().UTC().Add(time.Duration(hours)*time.Hour), singleUse)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		if errors.Is(err, store.ErrForbidden) {
			writeError(w, http.StatusForbidden, "only the session owner can create a guest link")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, link)
}

func (s *Server) handleGuestLinkAccept(w http.ResponseWriter, r *http.Request) {
	gs, ok := s.guestOffboardStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "guest links unavailable")
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Token) == "" {
		writeError(w, http.StatusBadRequest, "token is required")
		return
	}
	link, err := gs.AcceptGuestLink(r.Context(), body.Token, authSubject(r))
	if err != nil {
		if errors.Is(err, store.ErrGuestLinkExpired) {
			writeError(w, http.StatusGone, "guest link expired")
			return
		}
		if errors.Is(err, store.ErrConflict) {
			writeError(w, http.StatusConflict, "guest link already used")
			return
		}
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "guest link not found")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id":      link.SessionID,
		"grantee_user_id": link.GranteeUserID,
	})
}

func (s *Server) handleOrgOffboardPreview(w http.ResponseWriter, r *http.Request) {
	os, ok := s.requireOrgStore(w)
	if !ok {
		return
	}
	gs, ok := s.guestOffboardStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "offboarding unavailable")
		return
	}
	orgID := strings.TrimSpace(r.PathValue("id"))
	if !s.authorizeOrgAdmin(w, r, os, orgID) {
		return
	}
	fromUser := strings.TrimSpace(r.URL.Query().Get("user"))
	if fromUser == "" {
		writeError(w, http.StatusBadRequest, "user query parameter is required")
		return
	}
	prev, err := gs.PreviewOffboard(r.Context(), orgID, fromUser)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "organization not found")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.Tokens != nil {
		if n, err := s.Tokens.CountActiveForUser(r.Context(), fromUser); err == nil {
			prev.Tokens = n
		}
	}
	writeJSON(w, http.StatusOK, prev)
}

func (s *Server) handleOrgOffboard(w http.ResponseWriter, r *http.Request) {
	os, ok := s.requireOrgStore(w)
	if !ok {
		return
	}
	gs, ok := s.guestOffboardStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "offboarding unavailable")
		return
	}
	orgID := strings.TrimSpace(r.PathValue("id"))
	if !s.authorizeOrgAdmin(w, r, os, orgID) {
		return
	}
	var body struct {
		FromUserID string `json:"from_user_id"`
		ToUserID   string `json:"to_user_id"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	fromUser := strings.TrimSpace(body.FromUserID)
	toUser := strings.TrimSpace(body.ToUserID)
	if fromUser == "" || toUser == "" {
		writeError(w, http.StatusBadRequest, "from_user_id and to_user_id are required")
		return
	}
	if fromUser == toUser {
		writeError(w, http.StatusBadRequest, "receiver must be a different user")
		return
	}
	res, err := gs.Offboard(r.Context(), orgID, fromUser, toUser)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "organization not found")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.Tokens != nil {
		if n, err := s.Tokens.RevokeAllForUser(r.Context(), fromUser); err == nil {
			res.TokensRevoked = n
		}
	}
	s.recordAudit(r, store.AuditEvent{
		Action:       "member.offboarded",
		ResourceKind: "user",
		ResourceID:   fromUser,
		TenantID:     orgID,
		ActorKind:    "admin",
		Metadata: map[string]any{
			"to_user_id":                toUser,
			"sessions_transferred":      res.SessionsTransferred,
			"grants_kept":               res.GrantsKept,
			"secret_grants_transferred": res.SecretGrantsTransferred,
			"tokens_revoked":            res.TokensRevoked,
		},
	})
	writeJSON(w, http.StatusOK, res)
}
