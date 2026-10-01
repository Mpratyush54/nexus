package server

import (
	"errors"
	"net/http"
	"strings"

	"central-memory/internal/store"
)

func (s *Server) sessionContent() (store.SessionContentStore, bool) {
	acl, ok := s.Store.(store.SessionContentStore)
	return acl, ok
}

// isProjectAdmin is a project role of OWNER or ADMIN. Org ADMIN and
// project session:read do not qualify (product spec 4.5 / 10.5).
func (s *Server) isProjectAdmin(r *http.Request, projectID string) bool {
	rs, ok := s.roleStore()
	if !ok {
		return false
	}
	role, err := rs.GetMemberRole(r.Context(), authSubject(r), projectID)
	if err != nil {
		return false
	}
	return role == store.RoleOwner || role == store.RoleAdmin
}

// authorizeSessionContent allows the owner, an active grantee, or — only
// when the owner is unresolved — a project admin. Org role never passes.
func (s *Server) authorizeSessionContent(w http.ResponseWriter, r *http.Request, projectID, sessionID, ownerUserID string) bool {
	user := authSubject(r)
	ownerUserID = strings.TrimSpace(ownerUserID)
	if ownerUserID != "" && user == ownerUserID {
		return true
	}
	acl, ok := s.sessionContent()
	if !ok {
		writeError(w, http.StatusForbidden, "session is private")
		return false
	}
	if ownerUserID != "" {
		granted, err := acl.HasSessionContentGrant(r.Context(), sessionID, user)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not check session grant")
			return false
		}
		if granted {
			return true
		}
		writeError(w, http.StatusForbidden, "session is private")
		return false
	}
	if s.isProjectAdmin(r, projectID) {
		return true
	}
	writeError(w, http.StatusForbidden, "session is private")
	return false
}

// claimSessionWrite lets the owner or a grantee upload another version.
// The first uploader of a session with no content becomes the owner.
// Ownerless existing content stays ownerless until a project admin assigns it.
func (s *Server) claimSessionWrite(w http.ResponseWriter, r *http.Request, projectID, sessionID string) (string, bool) {
	acl, ok := s.sessionContent()
	if !ok {
		writeError(w, http.StatusForbidden, "session is private")
		return "", false
	}
	owner, ownerProject, found, err := acl.GetSessionContentOwner(r.Context(), sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not check session owner")
		return "", false
	}
	if found && ownerProject != "" && ownerProject != projectID {
		writeError(w, http.StatusBadRequest, "session belongs to another project")
		return "", false
	}
	user := authSubject(r)
	if !found {
		exists, err := acl.SessionHasContent(r.Context(), sessionID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not check session owner")
			return "", false
		}
		if exists {
			writeError(w, http.StatusForbidden, "session owner must be assigned")
			return "", false
		}
		if err := acl.SetSessionContentOwner(r.Context(), sessionID, projectID, user); err != nil {
			if errors.Is(err, store.ErrConflict) {
				owner, _, found, err = acl.GetSessionContentOwner(r.Context(), sessionID)
				if err != nil || !found || owner != user {
					writeError(w, http.StatusForbidden, "session is private")
					return "", false
				}
				return owner, true
			}
			writeError(w, http.StatusInternalServerError, "could not record session owner")
			return "", false
		}
		return user, true
	}
	if strings.TrimSpace(owner) == "" {
		writeError(w, http.StatusForbidden, "session owner must be assigned")
		return "", false
	}
	if user == owner {
		return owner, true
	}
	granted, err := acl.HasSessionContentGrant(r.Context(), sessionID, user)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not check session grant")
		return "", false
	}
	if granted {
		return owner, true
	}
	writeError(w, http.StatusForbidden, "session is private")
	return "", false
}

// authorizeProvenanceSession gates file and tool reads. Missing content is
// 404. Existing content the caller cannot see is 403.
func (s *Server) authorizeProvenanceSession(w http.ResponseWriter, r *http.Request, sessionID string) bool {
	acl, ok := s.sessionContent()
	if !ok {
		writeError(w, http.StatusForbidden, "session is private")
		return false
	}
	owner, projectID, found, err := acl.GetSessionContentOwner(r.Context(), sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not check session owner")
		return false
	}
	ps := provenanceStore(s.Store)
	if ps == nil {
		writeError(w, http.StatusNotImplemented, "provenance store unavailable")
		return false
	}
	if !found {
		snap, err := ps.GetLatestSnapshot(r.Context(), sessionID)
		if err == nil {
			found = true
			owner = snap.OwnerUserID
			projectID = snap.ProjectID
		} else if !errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusInternalServerError, err.Error())
			return false
		}
	}
	if !found {
		ops, err := ps.ListFileOperations(r.Context(), sessionID, store.FileOpListOpts{Limit: 1})
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return false
		}
		if len(ops) > 0 {
			found = true
			projectID = ops[0].ProjectID
		}
	}
	if !found {
		tex, err := ps.ListToolExecutions(r.Context(), sessionID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return false
		}
		if len(tex) > 0 {
			found = true
			projectID = tex[0].ProjectID
		}
	}
	if !found {
		writeError(w, http.StatusNotFound, "session content not found")
		return false
	}
	return s.authorizeSessionContent(w, r, projectID, sessionID, owner)
}

func (s *Server) recordAudit(r *http.Request, ev store.AuditEvent) {
	acl, ok := s.sessionContent()
	if !ok {
		return
	}
	if ev.ActorUserID == "" {
		ev.ActorUserID = authSubject(r)
	}
	if ev.ActorKind == "" {
		ev.ActorKind = "user"
	}
	if ev.Outcome == "" {
		ev.Outcome = "ok"
	}
	if ev.IP == "" {
		ev.IP = clientIP(r)
	}
	if ev.UserAgent == "" && r != nil {
		ev.UserAgent = r.UserAgent()
	}
	if ev.RequestID == "" && r != nil {
		ev.RequestID = r.Header.Get("X-Request-Id")
	}
	if ev.TenantID == "" && ev.ProjectID != "" {
		if p, err := s.Store.GetProject(r.Context(), ev.ProjectID); err == nil && p != nil {
			ev.TenantID = p.OrgID
		}
	}
	_ = acl.AppendAudit(r.Context(), ev)
}
