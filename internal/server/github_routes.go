// github_routes.go — GitHub connect / import / status / disconnect (issue #167).
package server

import (
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"central-memory/internal/github"
	"central-memory/internal/store"
)

func (s *Server) githubStore() (store.GitHubStore, bool) {
	gs, ok := s.Store.(store.GitHubStore)
	return gs, ok
}

func (s *Server) registerGitHubRoutes() {
	s.Mux.HandleFunc("POST /projects/{id}/github/connect", s.requireAuth(s.handleGitHubConnect))
	s.Mux.HandleFunc("POST /projects/{id}/github/import", s.requireAuth(s.handleGitHubImport))
	s.Mux.HandleFunc("GET /projects/{id}/github/status", s.requireAuth(s.handleGitHubStatus))
	s.Mux.HandleFunc("DELETE /projects/{id}/github/disconnect", s.requireAuth(s.handleGitHubDisconnect))
	s.registerGitHubOAuthRoutes()
}

type githubConnectRequest struct {
	Owner       string `json:"owner"`
	Repo        string `json:"repo"`
	AccessToken string `json:"access_token"`
	SyncMode    string `json:"sync_mode"` // manual | auto | one_time
}

type githubImportResult struct {
	Login      string `json:"login"`
	GitHubPerm string `json:"github_permission"`
	MappedRole string `json:"mapped_role"`
	UserID     string `json:"user_id,omitempty"`
	Status     string `json:"status"` // granted | updated | invite | skipped
	InviteHint string `json:"invite_hint,omitempty"`
	MatchedBy  string `json:"matched_by,omitempty"`
}

func normalizeSyncMode(m string) string {
	switch strings.ToLower(strings.TrimSpace(m)) {
	case "auto":
		return "auto"
	case "one_time", "onetime", "one-time":
		return "one_time"
	default:
		return "manual"
	}
}

func (s *Server) handleGitHubConnect(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if !s.authorizePermission(w, r, id, store.PermMemberManage) {
		return
	}
	gs, ok := s.githubStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "github integration not supported by configured store")
		return
	}
	var req githubConnectRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	owner := strings.TrimSpace(req.Owner)
	repo := strings.TrimSpace(req.Repo)
	if owner == "" || repo == "" {
		if inferredOwner, inferredRepo := s.githubOwnerRepoFromProject(r, id); inferredOwner != "" && inferredRepo != "" {
			if owner == "" {
				owner = inferredOwner
			}
			if repo == "" {
				repo = inferredRepo
			}
		}
	}
	if owner == "" || repo == "" {
		writeError(w, http.StatusBadRequest, "owner and repo are required")
		return
	}
	token := strings.TrimSpace(req.AccessToken)
	if token == "" {
		token = s.oauthTokenFor(authSubject(r))
	}
	if token == "" {
		token = strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
	}
	link := &store.GitHubLink{
		ProjectID:   id,
		Owner:       owner,
		Repo:        repo,
		AccessToken: token,
		SyncMode:    normalizeSyncMode(req.SyncMode),
		ConnectedBy: authSubject(r),
		ConnectedAt: time.Now().UTC(),
	}
	if err := gs.UpsertGitHubLink(r.Context(), link); err != nil {
		writeError(w, http.StatusInternalServerError, "could not connect github: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project_id": id,
		"owner":      owner,
		"repo":       repo,
		"sync_mode":  link.SyncMode,
		"connected":  true,
	})
}

func (s *Server) handleGitHubStatus(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if !s.authorizeProject(w, r, id) {
		return
	}
	gs, ok := s.githubStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "github integration not supported by configured store")
		return
	}
	suggestedOwner, suggestedRepo := s.githubOwnerRepoFromProject(r, id)
	cfg := s.githubOAuthConfig()
	oauthLinked := s.oauthTokenFor(authSubject(r)) != ""
	link, err := gs.GetGitHubLink(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusOK, map[string]any{
				"connected":        false,
				"project_id":       id,
				"suggested_owner":  suggestedOwner,
				"suggested_repo":   suggestedRepo,
				"has_token":        oauthLinked || strings.TrimSpace(os.Getenv("GITHUB_TOKEN")) != "",
				"oauth_configured": cfg.Configured(),
				"oauth_linked":     oauthLinked,
			})
			return
		}
		writeError(w, http.StatusInternalServerError, "could not load github status: "+err.Error())
		return
	}
	htmlURL := "https://github.com/" + link.Owner + "/" + link.Repo
	writeJSON(w, http.StatusOK, map[string]any{
		"connected":        true,
		"project_id":       link.ProjectID,
		"owner":            link.Owner,
		"repo":             link.Repo,
		"html_url":         htmlURL,
		"sync_mode":        link.SyncMode,
		"connected_by":     link.ConnectedBy,
		"connected_at":     link.ConnectedAt,
		"last_import_at":   link.LastImportAt,
		"suggested_owner":  suggestedOwner,
		"suggested_repo":   suggestedRepo,
		"has_token":        strings.TrimSpace(link.AccessToken) != "" || oauthLinked || strings.TrimSpace(os.Getenv("GITHUB_TOKEN")) != "",
		"oauth_configured": cfg.Configured(),
		"oauth_linked":     oauthLinked,
	})
}

func (s *Server) githubOwnerRepoFromProject(r *http.Request, projectID string) (owner, repo string) {
	p, err := s.Store.GetProject(r.Context(), projectID)
	if err != nil || p == nil {
		return "", ""
	}
	return github.ParseOwnerRepo(p.CanonicalURL)
}

func (s *Server) handleGitHubDisconnect(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if !s.authorizePermission(w, r, id, store.PermMemberManage) {
		return
	}
	gs, ok := s.githubStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "github integration not supported by configured store")
		return
	}
	if err := gs.DeleteGitHubLink(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "github not connected")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not disconnect github: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project_id": id, "disconnected": true})
}

func (s *Server) handleGitHubImport(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if !s.authorizePermission(w, r, id, store.PermMemberInvite) {
		return
	}
	ownerType, ownerID := s.billingOwnerForProject(r.Context(), id)
	if !s.enforcePlanDimension(w, r, ownerType, ownerID, "github_import") {
		return
	}
	gs, ok := s.githubStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "github integration not supported by configured store")
		return
	}
	link, err := gs.GetGitHubLink(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			owner, repo := s.githubOwnerRepoFromProject(r, id)
			if owner == "" || repo == "" {
				writeError(w, http.StatusBadRequest, "connect a GitHub repository first")
				return
			}
			link = &store.GitHubLink{
				ProjectID:   id,
				Owner:       owner,
				Repo:        repo,
				AccessToken: strings.TrimSpace(s.oauthTokenFor(authSubject(r))),
				SyncMode:    "one_time",
				ConnectedBy: authSubject(r),
				ConnectedAt: time.Now().UTC(),
			}
			if link.AccessToken == "" {
				link.AccessToken = strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
			}
			if uerr := gs.UpsertGitHubLink(r.Context(), link); uerr != nil {
				writeError(w, http.StatusInternalServerError, "could not connect github: "+uerr.Error())
				return
			}
		} else {
			writeError(w, http.StatusInternalServerError, "could not load github link: "+err.Error())
			return
		}
	}
	token := strings.TrimSpace(link.AccessToken)
	if token == "" {
		token = s.oauthTokenFor(authSubject(r))
	}
	if token == "" {
		token = strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
	}
	client := github.NewClient(token)
	collabs, err := client.ListCollaborators(r.Context(), link.Owner, link.Repo)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	results := make([]githubImportResult, 0, len(collabs))
	granter := authSubject(r)
	for _, c := range collabs {
		role := github.MapPermissionToRole(c.Permissions)
		row := githubImportResult{
			Login:      c.Login,
			GitHubPerm: c.Permissions,
			MappedRole: role,
		}
		userID, matchedBy := s.matchGitHubUser(r, gs, c)
		if userID == "" {
			row.Status = "invite"
			row.InviteHint = "No matching Central Memory user — ask @" + c.Login + " to sign up, then re-import"
			_ = gs.UpsertGitHubUserMap(r.Context(), &store.GitHubUserMap{
				GitHubLogin: c.Login,
				GitHubID:    c.ID,
				Email:       c.Email,
			})
			results = append(results, row)
			continue
		}
		row.UserID = userID
		row.MatchedBy = matchedBy
		_ = gs.UpsertGitHubUserMap(r.Context(), &store.GitHubUserMap{
			GitHubLogin: c.Login,
			GitHubID:    c.ID,
			UserID:      userID,
			Email:       c.Email,
		})
		if err := s.Store.GrantMember(r.Context(), id, userID, granter); err != nil {
			row.Status = "skipped"
			results = append(results, row)
			continue
		}
		if rs, ok := s.roleStore(); ok {
			if err := rs.SetMemberRole(r.Context(), id, userID, role); err == nil {
				row.Status = "granted"
			} else {
				row.Status = "updated"
			}
		} else {
			row.Status = "granted"
		}
		results = append(results, row)
	}
	_ = gs.TouchGitHubImport(r.Context(), id)

	invites := 0
	granted := 0
	for _, r := range results {
		if r.Status == "invite" {
			invites++
		}
		if r.Status == "granted" || r.Status == "updated" {
			granted++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project_id": id,
		"owner":      link.Owner,
		"repo":       link.Repo,
		"imported":   granted,
		"invites":    invites,
		"count":      len(results),
		"items":      results,
	})
}

func (s *Server) matchGitHubUser(r *http.Request, gs store.GitHubStore, c github.Collaborator) (userID, matchedBy string) {
	if mapped, err := gs.GetGitHubUserMap(r.Context(), c.Login); err == nil && mapped != nil && mapped.UserID != "" {
		return mapped.UserID, "github_map"
	}
	if s.Accounts == nil {
		return "", ""
	}
	if u, err := s.Accounts.GetByUsername(r.Context(), c.Login); err == nil && u != nil {
		return u.ID, "username"
	}
	email := strings.TrimSpace(c.Email)
	if email != "" {
		if u, err := s.Accounts.GetByEmail(r.Context(), email); err == nil && u != nil {
			return u.ID, "email"
		}
	}
	return "", ""
}
