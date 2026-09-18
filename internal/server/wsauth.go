package server

// wsauth.go — WebSocket project/session authorization (issue #90).
//
// The hub previously trusted client-supplied project_id/session_id. Now
// every subscribe and action is authorized through a WSAuthorizer resolved
// from the authenticated user. The default authorizer verifies the project
// exists and, when a session is given, that the session belongs to the
// project and the user is a participant (or the session is open).

import (
	"context"
	"strings"

	"central-memory/internal/store"
)

// WSAuthorizer authorizes userID for projectID/sessionID. Nil session means
// project-only scope. Return nil to allow.
type WSAuthorizer func(userID, projectID, sessionID string) error

// AuthorizeHub sets the hub authorizer. Nil restores the permissive default
// (project non-empty) for backward compatibility in tests.
func (h *Hub) AuthorizeHub(fn WSAuthorizer) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.authz = fn
}

// authorize checks the hub authorizer when set.
func (h *Hub) authorize(userID, projectID, sessionID string) error {
	h.mu.RLock()
	fn := h.authz
	h.mu.RUnlock()
	if fn == nil {
		if strings.TrimSpace(projectID) == "" {
			return errWSProjectRequired
		}
		return nil
	}
	return fn(userID, projectID, sessionID)
}

// NewStoreAuthorizer builds an authorizer over the store: project must
// exist AND the user must be a project member (issue #141 — existence
// alone is selection, not authorization); session (when given) must
// exist, belong to the project, and either be active with the user as
// participant or have been created by the user.
// Unknown users/sessions fail closed.
func NewStoreAuthorizer(st store.Store) WSAuthorizer {
	return func(userID, projectID, sessionID string) error {
		if strings.TrimSpace(projectID) == "" {
			return errWSProjectRequired
		}
		ctx := context.Background()
		if _, err := st.GetProject(ctx, projectID); err != nil {
			return errWSForbidden
		}
		if member, err := st.IsProjectMember(ctx, userID, projectID); err != nil || !member {
			return errWSForbidden
		}
		if strings.TrimSpace(sessionID) == "" {
			return nil
		}
		ss, ok := st.(store.SessionStore)
		if !ok {
			return errWSForbidden
		}
		sess, err := ss.GetSession(ctx, sessionID)
		if err != nil {
			return errWSForbidden
		}
		if sess.ProjectID != projectID {
			return errWSForbidden
		}
		if strings.TrimSpace(userID) == "" {
			return errWSForbidden
		}
		// Creator always allowed.
		if sess.CreatedBy == userID {
			return nil
		}
		parts, err := ss.ListSessionParticipants(ctx, sessionID, true)
		if err != nil {
			return errWSForbidden
		}
		for _, p := range parts {
			if p.UserID == userID {
				return nil
			}
		}
		// Open sessions without participants yet: allow join-via-subscribe
		// so the first participant can enter; subsequent actions require
		// membership established above.
		if len(parts) == 0 {
			return nil
		}
		return errWSForbidden
	}
}
