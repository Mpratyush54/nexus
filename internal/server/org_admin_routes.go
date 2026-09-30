package server

import (
	"context"
	"encoding/csv"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"central-memory/internal/store"
)

func (s *Server) orgAdminStore() (store.OrgAdminStore, bool) {
	as, ok := s.Store.(store.OrgAdminStore)
	return as, ok
}

func (s *Server) registerOrgAdminRoutes() {
	s.Mux.HandleFunc("GET /orgs/{id}/audit", s.requireAuth(s.handleOrgAudit))
	s.Mux.HandleFunc("GET /orgs/{id}/storage", s.requireAuth(s.handleOrgStorage))
	s.Mux.HandleFunc("POST /orgs/{id}/invites", s.requireAuth(s.handleOrgInviteCreate))
	s.Mux.HandleFunc("GET /orgs/{id}/invites", s.requireAuth(s.handleOrgInviteList))
	s.Mux.HandleFunc("DELETE /orgs/{id}/invites/{inviteId}", s.requireAuth(s.handleOrgInviteRevoke))
	s.Mux.HandleFunc("POST /orgs/invites/accept", s.requireAuth(s.handleOrgInviteAccept))
	s.Mux.HandleFunc("GET /projects/{id}/capture", s.requireAuth(s.handleProjectCaptureGet))
	s.Mux.HandleFunc("PUT /projects/{id}/capture", s.requireAuth(s.handleProjectCaptureSet))
}

func (s *Server) authorizeOrgOwner(w http.ResponseWriter, r *http.Request, os orgStore, orgID string) bool {
	if !s.authorizeOrgMember(w, r, os, orgID) {
		return false
	}
	role, err := os.GetOrgMemberRole(r.Context(), orgID, authSubject(r))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusForbidden, "not a member of this organization")
			return false
		}
		writeError(w, http.StatusInternalServerError, "could not check org role")
		return false
	}
	if role != store.OrgRoleOwner {
		writeError(w, http.StatusForbidden, "org owner role required")
		return false
	}
	return true
}

// ownerMayChangeRole reports whether the caller may add, change, or remove a
// membership that involves the Owner role. Only an Owner can manage Owners.
func (s *Server) ownerMayChangeRole(w http.ResponseWriter, r *http.Request, os orgStore, orgID, targetUserID, newRole string, removing bool) bool {
	caller, err := os.GetOrgMemberRole(r.Context(), orgID, authSubject(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not check org role")
		return false
	}
	target := ""
	if targetUserID != "" {
		target, err = os.GetOrgMemberRole(r.Context(), orgID, targetUserID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusInternalServerError, "could not check member role")
			return false
		}
		if errors.Is(err, store.ErrNotFound) {
			target = ""
		}
	}
	touchesOwner := target == store.OrgRoleOwner
	if !removing {
		norm, nerr := store.NormalizeOrgRole(newRole)
		if nerr == nil && norm == store.OrgRoleOwner {
			touchesOwner = true
		}
	}
	if touchesOwner && caller != store.OrgRoleOwner {
		writeError(w, http.StatusForbidden, "only an org owner can manage owners")
		return false
	}
	return true
}

func (s *Server) rejectIfCaptureOff(w http.ResponseWriter, r *http.Request, projectID string) bool {
	as, ok := s.orgAdminStore()
	if !ok {
		return false
	}
	enabled, err := as.CaptureEnabled(r.Context(), projectID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "project not found")
			return true
		}
		writeError(w, http.StatusInternalServerError, "could not check capture")
		return true
	}
	if !enabled {
		writeError(w, http.StatusForbidden, "capture is off for this project")
		return true
	}
	return false
}

type projectRoleStore interface {
	GetMemberRole(ctx context.Context, userID, projectID string) (string, error)
}

func (s *Server) authorizeCaptureChange(w http.ResponseWriter, r *http.Request, projectID string) bool {
	p, err := s.Store.GetProject(r.Context(), projectID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "project not found")
			return false
		}
		writeError(w, http.StatusInternalServerError, "could not load project")
		return false
	}
	if rs, ok := s.Store.(projectRoleStore); ok {
		role, rerr := rs.GetMemberRole(r.Context(), authSubject(r), projectID)
		if rerr == nil && role == store.RoleOwner {
			return true
		}
	}
	if p != nil && p.OrgID != "" {
		if os, ok := s.orgStore(); ok {
			role, rerr := os.GetOrgMemberRole(r.Context(), p.OrgID, authSubject(r))
			if rerr == nil && store.OrgManages(role) {
				return true
			}
		}
	}
	writeError(w, http.StatusForbidden, "org admin or project owner required")
	return false
}

func (s *Server) handleProjectCaptureGet(w http.ResponseWriter, r *http.Request) {
	as, ok := s.orgAdminStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "capture settings unavailable")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if !s.authorizeProject(w, r, id) {
		return
	}
	enabled, err := as.CaptureEnabled(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project_id": id, "enabled": enabled})
}

func (s *Server) handleProjectCaptureSet(w http.ResponseWriter, r *http.Request) {
	as, ok := s.orgAdminStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "capture settings unavailable")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if !s.authorizeCaptureChange(w, r, id) {
		return
	}
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := as.SetCaptureEnabled(r.Context(), id, body.Enabled, authSubject(r)); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.recordAudit(r, store.AuditEvent{
		Action: "project.capture_toggled", ResourceKind: "project", ResourceID: id, ProjectID: id,
		Metadata: map[string]any{"enabled": body.Enabled},
	})
	writeJSON(w, http.StatusOK, map[string]any{"project_id": id, "enabled": body.Enabled})
}

func (s *Server) handleOrgAudit(w http.ResponseWriter, r *http.Request) {
	os, ok := s.requireOrgStore(w)
	if !ok {
		return
	}
	as, ok := s.orgAdminStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "audit log unavailable")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if !s.authorizeOrgMember(w, r, os, id) {
		return
	}
	role, err := os.GetOrgMemberRole(r.Context(), id, authSubject(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not check org role")
		return
	}
	actor := ""
	if !store.OrgManages(role) {
		actor = authSubject(r)
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	events, err := as.ListOrgAudit(r.Context(), id, actor, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if r.URL.Query().Get("format") == "csv" {
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", `attachment; filename="audit.csv"`)
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"at", "actor_user_id", "actor_kind", "action", "resource_kind", "resource_id", "project_id", "outcome"})
		for _, ev := range events {
			_ = cw.Write([]string{
				ev.At.UTC().Format(time.RFC3339Nano), ev.ActorUserID, ev.ActorKind, ev.Action,
				ev.ResourceKind, ev.ResourceID, ev.ProjectID, ev.Outcome,
			})
		}
		cw.Flush()
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": events, "count": len(events)})
}

func (s *Server) handleOrgStorage(w http.ResponseWriter, r *http.Request) {
	os, ok := s.requireOrgStore(w)
	if !ok {
		return
	}
	as, ok := s.orgAdminStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "storage view unavailable")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if !s.authorizeOrgMember(w, r, os, id) {
		return
	}
	role, err := os.GetOrgMemberRole(r.Context(), id, authSubject(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not check org role")
		return
	}
	report, err := as.OrgStorageAggregates(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !store.OrgManages(role) {
		var mine []*store.MemberStorageAggregate
		for _, row := range report.Members {
			if row != nil && row.UserID == authSubject(r) {
				mine = append(mine, row)
			}
		}
		if mine == nil {
			mine = []*store.MemberStorageAggregate{}
		}
		report.Members = mine
		report.Projects = []*store.ProjectStorageAggregate{}
	}
	writeJSON(w, http.StatusOK, report)
}

func (s *Server) handleOrgInviteCreate(w http.ResponseWriter, r *http.Request) {
	os, ok := s.requireOrgStore(w)
	if !ok {
		return
	}
	as, ok := s.orgAdminStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "invites unavailable")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if !s.authorizeOrgAdmin(w, r, os, id) {
		return
	}
	var body struct {
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if !s.ownerMayChangeRole(w, r, os, id, "", body.Role, false) {
		return
	}
	inv, err := as.CreateOrgInvite(r.Context(), id, body.Email, body.Role, authSubject(r), time.Now().Add(14*24*time.Hour))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "organization not found")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	emailSent := false
	if s.Mail != nil && s.Mail.Configured() && strings.TrimSpace(inv.Email) != "" {
		body := "You have been invited to a Nexus organization.\n\nAccept with this token:\n" + inv.Token + "\n"
		if err := s.Mail.Send(inv.Email, "Nexus organization invite", body); err != nil {
			s.Log.Printf("invite mail: %v", err)
		} else {
			emailSent = true
		}
	}
	s.recordAudit(r, store.AuditEvent{
		TenantID: id, Action: "member.invited", ResourceKind: "org_invite", ResourceID: inv.ID,
		Metadata: map[string]any{"email": inv.Email, "role": inv.Role, "email_sent": emailSent},
	})
	writeJSON(w, http.StatusCreated, inv)
}

func (s *Server) handleOrgInviteList(w http.ResponseWriter, r *http.Request) {
	os, ok := s.requireOrgStore(w)
	if !ok {
		return
	}
	as, ok := s.orgAdminStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "invites unavailable")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if !s.authorizeOrgAdmin(w, r, os, id) {
		return
	}
	items, err := as.ListOrgInvites(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

func (s *Server) handleOrgInviteRevoke(w http.ResponseWriter, r *http.Request) {
	os, ok := s.requireOrgStore(w)
	if !ok {
		return
	}
	as, ok := s.orgAdminStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "invites unavailable")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if !s.authorizeOrgAdmin(w, r, os, id) {
		return
	}
	if err := as.RevokeOrgInvite(r.Context(), id, r.PathValue("inviteId")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "invite not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revoked": true})
}

func (s *Server) handleOrgInviteAccept(w http.ResponseWriter, r *http.Request) {
	as, ok := s.orgAdminStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "invites unavailable")
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	member, err := as.AcceptOrgInvite(r.Context(), body.Token, authSubject(r))
	if err != nil {
		if errors.Is(err, store.ErrInviteExpired) {
			writeError(w, http.StatusGone, "invite expired")
			return
		}
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "invite not found")
			return
		}
		if errors.Is(err, store.ErrConflict) {
			writeError(w, http.StatusConflict, "invite already accepted")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.recordAudit(r, store.AuditEvent{
		TenantID: member.OrgID, Action: "member.joined", ResourceKind: "member", ResourceID: member.UserID,
		Metadata: map[string]any{"role": member.Role},
	})
	writeJSON(w, http.StatusOK, member)
}
