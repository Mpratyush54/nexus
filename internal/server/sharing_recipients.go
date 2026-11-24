package server

import (
	"net/http"
	"sort"
	"strconv"
	"strings"

	"central-memory/internal/store"
)

// handleSharingRecipients exposes only people who share the requested project
// or its organization with the caller. Account profile fields are deliberately
// limited to a display label; email and settings never leave this route.
func (s *Server) handleSharingRecipients(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
	if projectID == "" {
		writeError(w, http.StatusBadRequest, "project_id is required")
		return
	}
	if !s.authorizeProject(w, r, projectID) {
		return
	}
	project, err := s.Store.GetProject(r.Context(), projectID)
	if err != nil || project == nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	type recipient struct {
		UserID           string `json:"user_id"`
		Name             string `json:"name"`
		ProjectRole      string `json:"project_role,omitempty"`
		OrganizationRole string `json:"organization_role,omitempty"`
	}
	people := map[string]*recipient{}
	add := func(id string) *recipient {
		id = strings.TrimSpace(id)
		if id == "" || id == authSubject(r) {
			return nil
		}
		if person := people[id]; person != nil {
			return person
		}
		name := id
		if s.Accounts != nil {
			if account, err := s.Accounts.GetByID(r.Context(), id); err == nil && account != nil {
				if label, ok := account.Settings["display_name"].(string); ok && strings.TrimSpace(label) != "" {
					name = strings.TrimSpace(label)
				} else if account.Username != "" {
					name = account.Username
				}
			}
		}
		person := &recipient{UserID: id, Name: name}
		people[id] = person
		return person
	}
	if project.CreatedBy != "" {
		if person := add(project.CreatedBy); person != nil {
			person.ProjectRole = store.RoleOwner
		}
	}
	members, err := s.Store.ListMembers(r.Context(), projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list project members")
		return
	}
	for _, id := range members {
		if person := add(id); person != nil {
			if person.ProjectRole == "" {
				person.ProjectRole = "MEMBER"
			}
		}
	}
	var orgInfo any
	if project.OrgID != "" {
		if os, ok := s.orgStore(); ok {
			member, err := os.IsOrgMember(r.Context(), authSubject(r), project.OrgID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "could not check organization membership")
				return
			}
			if member {
				org, err := os.GetOrganization(r.Context(), project.OrgID)
				if err != nil {
					writeError(w, http.StatusInternalServerError, "could not load organization")
					return
				}
				orgInfo = map[string]string{"id": org.ID, "name": org.Name}
				orgMembers, err := os.ListOrgMembers(r.Context(), project.OrgID)
				if err != nil {
					writeError(w, http.StatusInternalServerError, "could not list organization members")
					return
				}
				for _, member := range orgMembers {
					if person := add(member.UserID); person != nil {
						person.OrganizationRole = member.Role
					}
				}
			}
		}
	}
	items := make([]*recipient, 0, len(people))
	for _, person := range people {
		items = append(items, person)
	}
	sort.Slice(items, func(i, j int) bool { return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name) })
	writeJSON(w, http.StatusOK, map[string]any{"project_id": projectID, "organization": orgInfo, "people": items})
}

// Organization sharing grants the current members of the session's own
// organization. It is deliberately a snapshot, not an implicit grant to
// future members, and requires an explicit confirmation from the client.
func (s *Server) handleShareWithOrganization(w http.ResponseWriter, r *http.Request) {
	row, cs, ok := s.requireSessionOwner(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	var body struct {
		OrganizationID string `json:"organization_id"`
		Confirm        bool   `json:"confirm"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if !body.Confirm {
		writeError(w, http.StatusBadRequest, "organization share needs explicit confirmation")
		return
	}
	project, err := s.Store.GetProject(r.Context(), row.ProjectID)
	if err != nil || project == nil || project.OrgID == "" || project.OrgID != strings.TrimSpace(body.OrganizationID) {
		writeError(w, http.StatusBadRequest, "session project is not in that organization")
		return
	}
	os, ok := s.orgStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "organizations unavailable")
		return
	}
	if !s.authorizeOrgMember(w, r, os, project.OrgID) {
		return
	}
	members, err := os.ListOrgMembers(r.Context(), project.OrgID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list organization members")
		return
	}
	if len(members) > 500 {
		writeError(w, http.StatusRequestEntityTooLarge, "organization has too many members for one share")
		return
	}
	shared := 0
	for _, member := range members {
		if member.UserID == row.OwnerUserID {
			continue
		}
		if err := cs.GrantAgentSessionOpts(r.Context(), row.ID, member.UserID, authSubject(r), store.SessionGrantOpts{Live: true}); err != nil {
			writeError(w, http.StatusInternalServerError, "organization share stopped after "+strconv.Itoa(shared)+" grants: "+err.Error())
			return
		}
		shared++
	}
	s.recordAudit(r, store.AuditEvent{
		Action: "session.shared_organization", ResourceKind: "session", ResourceID: row.ID,
		ProjectID: row.ProjectID, Metadata: map[string]any{"organization_id": project.OrgID, "recipient_count": shared},
	})
	writeJSON(w, http.StatusOK, map[string]any{"session_id": row.ID, "organization_id": project.OrgID, "shared_count": shared, "live": true})
}
