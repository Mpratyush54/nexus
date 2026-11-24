package server

import (
	"context"
	"net/http"
	"strings"

	"central-memory/internal/store"
)

// A person who shared a chat must be able to see who has it and take it
// back. Owners only: the list names people.

type grantLister interface {
	ListAgentSessionGrants(ctx context.Context, sessionID string) ([]store.SessionGrantInfo, error)
}

// handleListGrants: GET /v1/agent-sessions/{id}/grants ->
// {session_id, visibility, team, people:[{user_id, name, live, granted_at}]}.
func (s *Server) handleListGrants(w http.ResponseWriter, r *http.Request) {
	row, _, ok := s.requireSessionOwner(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	lister, ok := s.Store.(grantLister)
	if !ok {
		writeError(w, http.StatusNotImplemented, "sharing list unavailable")
		return
	}
	grants, err := lister.ListAgentSessionGrants(r.Context(), row.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list who this chat is shared with")
		return
	}
	type person struct {
		store.SessionGrantInfo
		Name string `json:"name"`
	}
	people := make([]person, 0, len(grants))
	for _, g := range grants {
		people = append(people, person{SessionGrantInfo: g, Name: s.displayName(r.Context(), g.UserID)})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id": row.ID, "visibility": row.Visibility, "team": row.Visibility == "team", "people": people,
	})
}

// handleUnshareAll: DELETE /v1/agent-sessions/{id}/grants stops sharing the
// chat with everyone (each person, and the project team).
func (s *Server) handleUnshareAll(w http.ResponseWriter, r *http.Request) {
	row, _, ok := s.requireSessionOwner(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	lister, ok := s.Store.(grantLister)
	if !ok {
		writeError(w, http.StatusNotImplemented, "sharing list unavailable")
		return
	}
	grants, err := lister.ListAgentSessionGrants(r.Context(), row.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list who this chat is shared with")
		return
	}
	revoked := 0
	for _, g := range grants {
		if err := revokeGrant(r, s, row.ID, g.UserID); err == nil {
			revoked++
		}
	}
	if row.Visibility == "team" {
		if err := setVisibility(r, s, row.ID, "private"); err != nil {
			writeError(w, http.StatusInternalServerError, "could not stop sharing with the project team")
			return
		}
	}
	s.recordAudit(r, store.AuditEvent{
		Action: "session.unshared_all", ResourceKind: "session", ResourceID: row.ID, ProjectID: row.ProjectID,
		Metadata: map[string]any{"revoked_count": revoked, "was_team": row.Visibility == "team"},
	})
	writeJSON(w, http.StatusOK, map[string]any{"session_id": row.ID, "revoked": revoked, "visibility": "private"})
}

// displayName is the label people see for an account: its display name,
// else its username, else the id. Email never leaves the server here.
func (s *Server) displayName(ctx context.Context, userID string) string {
	if s.Accounts != nil {
		if account, err := s.Accounts.GetByID(ctx, userID); err == nil && account != nil {
			if label, ok := account.Settings["display_name"].(string); ok && strings.TrimSpace(label) != "" {
				return strings.TrimSpace(label)
			}
			if account.Username != "" {
				return account.Username
			}
		}
	}
	return userID
}
