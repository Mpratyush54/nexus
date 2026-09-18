// rbac_routes.go — project role CRUD + member role assignment (issue #163).
package server

import (
	"errors"
	"net/http"
	"strings"

	"central-memory/internal/store"
)

func (s *Server) registerRBACRoutes() {
	s.Mux.HandleFunc("GET /projects/{id}/roles", s.requireAuth(s.handleRoleList))
	s.Mux.HandleFunc("POST /projects/{id}/roles", s.requireAuth(s.handleRoleCreate))
	s.Mux.HandleFunc("PUT /projects/{id}/roles/{roleId}", s.requireAuth(s.handleRoleUpdate))
	s.Mux.HandleFunc("DELETE /projects/{id}/roles/{roleId}", s.requireAuth(s.handleRoleDelete))
	s.Mux.HandleFunc("PUT /projects/{id}/members/{userId}/role", s.requireAuth(s.handleMemberSetRole))
}

// roleStore returns the RBAC surface when the backing store implements it.
func (s *Server) roleStore() (store.RoleStore, bool) {
	rs, ok := s.Store.(store.RoleStore)
	return rs, ok
}

// authorizePermission enforces membership then a granular permission
// (issue #163). When the store does not implement RoleStore, membership
// alone is sufficient (fail-open for exotic test stores) so existing
// membership-only paths keep working.
func (s *Server) authorizePermission(w http.ResponseWriter, r *http.Request, projectID, permission string) bool {
	if !s.authorizeProject(w, r, projectID) {
		return false
	}
	rs, ok := s.roleStore()
	if !ok {
		return true
	}
	allowed, err := rs.HasPermission(r.Context(), authSubject(r), projectID, permission)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not check permission")
		return false
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "missing permission: "+permission)
		return false
	}
	return true
}

// authorizeMemoryPermission resolves a memory then checks permission on its project.
func (s *Server) authorizeMemoryPermission(w http.ResponseWriter, r *http.Request, id, permission string) bool {
	item, err := s.Store.GetMemoryItem(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "memory not found")
			return false
		}
		writeError(w, http.StatusInternalServerError, "could not load memory: "+err.Error())
		return false
	}
	return s.authorizePermission(w, r, item.ProjectID, permission)
}

type roleWriteRequest struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Permissions []string `json:"permissions"`
}

type memberRoleRequest struct {
	Role string `json:"role"`
}

func (s *Server) handleRoleList(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if !s.authorizeProject(w, r, id) {
		return
	}
	rs, ok := s.roleStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "roles not supported by configured store")
		return
	}
	roles, err := rs.ListProjectRoles(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not list roles: "+err.Error())
		return
	}
	if roles == nil {
		roles = []*store.ProjectRole{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": roles, "count": len(roles)})
}

func (s *Server) handleRoleCreate(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if !s.authorizePermission(w, r, id, store.PermMemberManage) {
		return
	}
	rs, ok := s.roleStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "roles not supported by configured store")
		return
	}
	var req roleWriteRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	role := &store.ProjectRole{
		ProjectID:   id,
		Name:        req.Name,
		Description: req.Description,
		Permissions: req.Permissions,
	}
	if err := rs.CreateProjectRole(r.Context(), role); err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		if isRoleInputError(err) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "could not create role: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, role)
}

func (s *Server) handleRoleUpdate(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	roleID := strings.TrimSpace(r.PathValue("roleId"))
	if roleID == "" {
		writeError(w, http.StatusBadRequest, "role id is required")
		return
	}
	if !s.authorizePermission(w, r, id, store.PermMemberManage) {
		return
	}
	rs, ok := s.roleStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "roles not supported by configured store")
		return
	}
	var req roleWriteRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	role := &store.ProjectRole{
		ID:          roleID,
		ProjectID:   id,
		Name:        req.Name,
		Description: req.Description,
		Permissions: req.Permissions,
	}
	if err := rs.UpdateProjectRole(r.Context(), role); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "role not found")
			return
		}
		if errors.Is(err, store.ErrConflict) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		if isRoleInputError(err) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "could not update role: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, role)
}

func (s *Server) handleRoleDelete(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	roleID := strings.TrimSpace(r.PathValue("roleId"))
	if roleID == "" {
		writeError(w, http.StatusBadRequest, "role id is required")
		return
	}
	if !s.authorizePermission(w, r, id, store.PermMemberManage) {
		return
	}
	rs, ok := s.roleStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "roles not supported by configured store")
		return
	}
	if err := rs.DeleteProjectRole(r.Context(), id, roleID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "role not found")
			return
		}
		if errors.Is(err, store.ErrConflict) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "could not delete role: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "id": roleID})
}

func (s *Server) handleMemberSetRole(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	userID := strings.TrimSpace(r.PathValue("userId"))
	if userID == "" {
		writeError(w, http.StatusBadRequest, "user id is required")
		return
	}
	if !s.authorizePermission(w, r, id, store.PermMemberManage) {
		return
	}
	rs, ok := s.roleStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "roles not supported by configured store")
		return
	}
	var req memberRoleRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Role) == "" {
		writeError(w, http.StatusBadRequest, "role is required")
		return
	}
	if err := rs.SetMemberRole(r.Context(), id, userID, req.Role); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		if errors.Is(err, store.ErrConflict) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		if isRoleInputError(err) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "could not set member role: "+err.Error())
		return
	}
	role, _ := rs.GetMemberRole(r.Context(), userID, id)
	writeJSON(w, http.StatusOK, map[string]any{
		"project_id": id,
		"user_id":    userID,
		"role":       role,
	})
}

func isRoleInputError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "is required") ||
		strings.Contains(msg, "invalid ") ||
		strings.Contains(msg, "unknown permission") ||
		strings.Contains(msg, "too long")
}
