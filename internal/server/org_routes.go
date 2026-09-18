// org_routes.go — organization CRUD + membership + org projects (issue #168).
//
// Routes:
//
//	POST/GET /orgs
//	GET      /orgs/{id}
//	POST     /orgs/{id}/members
//	PUT      /orgs/{id}/members/{userId}/role
//	DELETE   /orgs/{id}/members/{userId}
//	POST     /orgs/{id}/projects
//
// Store methods live on both MemStore and PostgresStore; the orgStore seam
// keeps the core Store interface unchanged (same pattern as memoryEditStore).
package server

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"central-memory/internal/store"
)

// orgStore is the Phase 8 organizations surface (issue #168).
type orgStore interface {
	CreateOrganization(ctx context.Context, name, slug, createdBy string) (*store.Organization, error)
	ListOrganizations(ctx context.Context, userID string) ([]*store.Organization, error)
	GetOrganization(ctx context.Context, id string) (*store.Organization, error)
	IsOrgMember(ctx context.Context, userID, orgID string) (bool, error)
	GetOrgMemberRole(ctx context.Context, orgID, userID string) (string, error)
	ListOrgMembers(ctx context.Context, orgID string) ([]*store.OrganizationMember, error)
	ListOrgProjects(ctx context.Context, orgID string) ([]*store.Project, error)
	AddOrgMember(ctx context.Context, orgID, userID, role, grantedBy string) (*store.OrganizationMember, error)
	SetOrgMemberRole(ctx context.Context, orgID, userID, role string) (*store.OrganizationMember, error)
	RemoveOrgMember(ctx context.Context, orgID, userID string) error
	CreateOrgProject(ctx context.Context, orgID, folderName, displayName, canonicalURL, rootCommit, createdBy string) (*store.Project, error)
}

func (s *Server) orgStore() (orgStore, bool) {
	os, ok := s.Store.(orgStore)
	return os, ok
}

func (s *Server) registerOrgRoutes() {
	s.Mux.HandleFunc("POST /orgs", s.requireAuth(s.handleOrgCreate))
	s.Mux.HandleFunc("GET /orgs", s.requireAuth(s.handleOrgList))
	s.Mux.HandleFunc("GET /orgs/{id}", s.requireAuth(s.handleOrgGet))
	s.Mux.HandleFunc("POST /orgs/{id}/members", s.requireAuth(s.handleOrgMemberAdd))
	s.Mux.HandleFunc("PUT /orgs/{id}/members/{userId}/role", s.requireAuth(s.handleOrgMemberSetRole))
	s.Mux.HandleFunc("DELETE /orgs/{id}/members/{userId}", s.requireAuth(s.handleOrgMemberRemove))
	s.Mux.HandleFunc("POST /orgs/{id}/projects", s.requireAuth(s.handleOrgProjectCreate))
}

type orgCreateRequest struct {
	Name string `json:"name"`
	Slug string `json:"slug"`
}

type orgMemberAddRequest struct {
	UserID string `json:"user_id"`
	Role   string `json:"role"`
}

type orgMemberRoleRequest struct {
	Role string `json:"role"`
}

type orgProjectCreateRequest struct {
	FolderName   string `json:"folder_name"`
	DisplayName  string `json:"display_name"`
	CanonicalURL string `json:"canonical_url"`
	RootCommit   string `json:"root_commit"`
}

func (s *Server) requireOrgStore(w http.ResponseWriter) (orgStore, bool) {
	os, ok := s.orgStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "organizations not supported by configured store")
		return nil, false
	}
	return os, true
}

// authorizeOrgMember requires org membership (any role).
func (s *Server) authorizeOrgMember(w http.ResponseWriter, r *http.Request, os orgStore, orgID string) bool {
	orgID = strings.TrimSpace(orgID)
	if orgID == "" {
		writeError(w, http.StatusBadRequest, "org id is required")
		return false
	}
	ok, err := os.IsOrgMember(r.Context(), authSubject(r), orgID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not check org membership")
		return false
	}
	if !ok {
		writeError(w, http.StatusForbidden, "not a member of this organization")
		return false
	}
	return true
}

// authorizeOrgAdmin requires ADMIN role on the org.
func (s *Server) authorizeOrgAdmin(w http.ResponseWriter, r *http.Request, os orgStore, orgID string) bool {
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
	if role != store.OrgRoleAdmin {
		writeError(w, http.StatusForbidden, "org admin role required")
		return false
	}
	return true
}

func (s *Server) handleOrgCreate(w http.ResponseWriter, r *http.Request) {
	os, ok := s.requireOrgStore(w)
	if !ok {
		return
	}
	var req orgCreateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if !s.enforcePlanDimension(w, r, store.OwnerUser, authSubject(r), "orgs") {
		return
	}
	o, err := os.CreateOrganization(r.Context(), req.Name, req.Slug, authSubject(r))
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "could not create organization: "+err.Error())
		return
	}
	if bs, ok := s.billingStore(); ok {
		_, _ = bs.EnsureSubscription(r.Context(), store.OwnerOrg, o.ID)
	}
	writeJSON(w, http.StatusCreated, o)
}

func (s *Server) handleOrgList(w http.ResponseWriter, r *http.Request) {
	os, ok := s.requireOrgStore(w)
	if !ok {
		return
	}
	items, err := os.ListOrganizations(r.Context(), authSubject(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list organizations: "+err.Error())
		return
	}
	if items == nil {
		items = []*store.Organization{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

func (s *Server) handleOrgGet(w http.ResponseWriter, r *http.Request) {
	os, ok := s.requireOrgStore(w)
	if !ok {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if !s.authorizeOrgMember(w, r, os, id) {
		return
	}
	o, err := os.GetOrganization(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "organization not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not load organization: "+err.Error())
		return
	}
	members, err := os.ListOrgMembers(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list members: "+err.Error())
		return
	}
	projects, err := os.ListOrgProjects(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list projects: "+err.Error())
		return
	}
	if members == nil {
		members = []*store.OrganizationMember{}
	}
	if projects == nil {
		projects = []*store.Project{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"organization": o,
		"members":      members,
		"projects":     projects,
	})
}

func (s *Server) handleOrgMemberAdd(w http.ResponseWriter, r *http.Request) {
	os, ok := s.requireOrgStore(w)
	if !ok {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if !s.authorizeOrgAdmin(w, r, os, id) {
		return
	}
	var req orgMemberAddRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.UserID) == "" {
		writeError(w, http.StatusBadRequest, "user_id is required")
		return
	}
	if !s.enforcePlanDimension(w, r, store.OwnerOrg, id, "members") {
		return
	}
	m, err := os.AddOrgMember(r.Context(), id, req.UserID, req.Role, authSubject(r))
	if err != nil {
		if strings.Contains(err.Error(), "invalid org role") {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "could not add member: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, m)
}

func (s *Server) handleOrgMemberSetRole(w http.ResponseWriter, r *http.Request) {
	os, ok := s.requireOrgStore(w)
	if !ok {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	userID := strings.TrimSpace(r.PathValue("userId"))
	if !s.authorizeOrgAdmin(w, r, os, id) {
		return
	}
	if userID == "" {
		writeError(w, http.StatusBadRequest, "userId is required")
		return
	}
	var req orgMemberRoleRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Role) == "" {
		writeError(w, http.StatusBadRequest, "role is required")
		return
	}
	m, err := os.SetOrgMemberRole(r.Context(), id, userID, req.Role)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "member not found")
			return
		}
		if errors.Is(err, store.ErrConflict) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		if strings.Contains(err.Error(), "invalid org role") {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "could not update role: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) handleOrgMemberRemove(w http.ResponseWriter, r *http.Request) {
	os, ok := s.requireOrgStore(w)
	if !ok {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	userID := strings.TrimSpace(r.PathValue("userId"))
	if !s.authorizeOrgAdmin(w, r, os, id) {
		return
	}
	if userID == "" {
		writeError(w, http.StatusBadRequest, "userId is required")
		return
	}
	if err := os.RemoveOrgMember(r.Context(), id, userID); err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "could not remove member: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"org_id": id, "user_id": userID, "revoked": true})
}

func (s *Server) handleOrgProjectCreate(w http.ResponseWriter, r *http.Request) {
	os, ok := s.requireOrgStore(w)
	if !ok {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if !s.authorizeOrgAdmin(w, r, os, id) {
		return
	}
	var req orgProjectCreateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.FolderName) == "" {
		writeError(w, http.StatusBadRequest, "folder_name is required")
		return
	}
	if !s.enforcePlanDimension(w, r, store.OwnerOrg, id, "projects") {
		return
	}
	p, err := os.CreateOrgProject(r.Context(), id, req.FolderName, req.DisplayName, req.CanonicalURL, req.RootCommit, authSubject(r))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "organization not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not create project: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, p)
}
