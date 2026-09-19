package server

import (
	"context"
	"net/http"
	"os"
	"strings"

	"central-memory/internal/buildinfo"
	"central-memory/internal/store"
)

func (s *Server) registerAdminRoutes() {
	s.Mux.HandleFunc("GET /version", s.handlePlatformVersion)
	s.Mux.HandleFunc("GET /platform/version", s.handlePlatformVersion)
	s.Mux.HandleFunc("GET /platform/releases", s.handlePublicReleaseList)
	s.Mux.HandleFunc("GET /platform/releases/latest", s.handlePublicReleaseLatest)

	s.Mux.HandleFunc("GET /admin/overview", s.requireAuth(s.requirePlatformAdmin(s.handleAdminOverview)))
	s.Mux.HandleFunc("GET /admin/users", s.requireAuth(s.requirePlatformAdmin(s.handleAdminUsers)))
	s.Mux.HandleFunc("PUT /admin/users/{id}", s.requireAuth(s.requirePlatformAdmin(s.handleAdminUserPut)))
	s.Mux.HandleFunc("GET /admin/releases", s.requireAuth(s.requirePlatformAdmin(s.handleAdminReleaseList)))
	s.Mux.HandleFunc("POST /admin/releases", s.requireAuth(s.requirePlatformAdmin(s.handleAdminReleasePublish)))
	s.Mux.HandleFunc("POST /admin/releases/{app}/{version}/yank", s.requireAuth(s.requirePlatformAdmin(s.handleAdminReleaseYank)))
}

func (s *Server) platformStore() (store.PlatformStore, bool) {
	ps, ok := s.Store.(store.PlatformStore)
	return ps, ok
}

func envPlatformAdmins() []string {
	raw := strings.TrimSpace(os.Getenv("PLATFORM_ADMIN_USERNAMES"))
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv("PLATFORM_ADMIN_IDS"))
	}
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func matchEnvAdmin(userID, username string) bool {
	userID = strings.TrimSpace(userID)
	username = strings.TrimSpace(username)
	for _, name := range envPlatformAdmins() {
		if strings.EqualFold(name, userID) || (username != "" && strings.EqualFold(name, username)) {
			return true
		}
	}
	return false
}

func (s *Server) userIsPlatformAdmin(ctx context.Context, userID, username string) bool {
	if matchEnvAdmin(userID, username) {
		return true
	}
	ps, ok := s.platformStore()
	if !ok {
		return false
	}
	yes, err := ps.IsPlatformAdmin(ctx, userID)
	return err == nil && yes
}

func (s *Server) requirePlatformAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.userIsPlatformAdmin(r.Context(), authSubject(r), authUsername(r)) {
			writeError(w, http.StatusForbidden, "super admin role required")
			return
		}
		next(w, r)
	}
}

func (s *Server) handlePlatformVersion(w http.ResponseWriter, r *http.Request) {
	apps := []buildinfo.Info{buildinfo.Current(buildinfo.AppAPI)}
	if v := strings.TrimSpace(os.Getenv("PWA_VERSION")); v != "" {
		apps = append(apps, buildinfo.Info{App: buildinfo.AppPWA, Version: v, Commit: os.Getenv("PWA_COMMIT")})
	}
	ps, ok := s.platformStore()
	if ok {
		for _, app := range []string{buildinfo.AppPWA, buildinfo.AppCLI, buildinfo.AppDaemon} {
			rel, err := ps.LatestRelease(r.Context(), app, "stable")
			if err != nil || rel == nil {
				continue
			}
			apps = append(apps, buildinfo.Info{App: app, Version: rel.Version, Commit: rel.GitSHA})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"api":  buildinfo.Current(buildinfo.AppAPI),
		"apps": apps,
	})
}

func (s *Server) handlePublicReleaseList(w http.ResponseWriter, r *http.Request) {
	ps, ok := s.platformStore()
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"items": []*store.AppRelease{}, "count": 0})
		return
	}
	app := r.URL.Query().Get("app")
	channel := r.URL.Query().Get("channel")
	items, err := ps.ListReleases(r.Context(), app, channel, false)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list releases: "+err.Error())
		return
	}
	if items == nil {
		items = []*store.AppRelease{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

func (s *Server) handlePublicReleaseLatest(w http.ResponseWriter, r *http.Request) {
	ps, ok := s.platformStore()
	if !ok {
		writeError(w, http.StatusNotFound, "no releases")
		return
	}
	app := store.NormalizeReleaseApp(r.URL.Query().Get("app"))
	if app == "" {
		app = buildinfo.AppCLI
	}
	channel := store.NormalizeReleaseChannel(r.URL.Query().Get("channel"))
	rel, err := ps.LatestRelease(r.Context(), app, channel)
	if err != nil {
		writeError(w, http.StatusNotFound, "no release for "+app)
		return
	}
	writeJSON(w, http.StatusOK, rel)
}

func (s *Server) handleAdminOverview(w http.ResponseWriter, r *http.Request) {
	stats := &store.PlatformStats{}
	if ps, ok := s.platformStore(); ok {
		if st, err := ps.PlatformStats(r.Context()); err == nil && st != nil {
			stats = st
		}
	}
	if s.Accounts != nil {
		if users, err := s.Accounts.List(r.Context(), 1, 0); err == nil {
			// List is capped; prefer SQL stats.users when Postgres filled it.
			if stats.Users == 0 {
				stats.Users = len(users)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"role":  "SUPER_ADMIN",
		"stats": stats,
		"api":   buildinfo.Current(buildinfo.AppAPI),
		"aws": map[string]any{
			"region":          firstNonEmptyEnv("AWS_REGION", "AWS_DEFAULT_REGION"),
			"ecs_cluster":     os.Getenv("ECS_CLUSTER"),
			"ecs_service":     os.Getenv("ECS_SERVICE"),
			"releases_bucket": firstNonEmptyEnv("RELEASES_S3_BUCKET", "NEXUS_RELEASES_BUCKET"),
		},
		"bootstrap_env": "PLATFORM_ADMIN_USERNAMES",
	})
}

func firstNonEmptyEnv(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

func (s *Server) handleAdminUsers(w http.ResponseWriter, r *http.Request) {
	type row struct {
		ID              string `json:"id"`
		Username        string `json:"username"`
		Email           string `json:"email,omitempty"`
		IsPlatformAdmin bool   `json:"is_platform_admin"`
		Source          string `json:"source,omitempty"`
	}
	seen := map[string]int{}
	items := []row{}
	add := func(id, username, email, source string, admin bool) {
		id = strings.TrimSpace(id)
		if id == "" {
			id = username
		}
		if id == "" {
			return
		}
		if i, ok := seen[id]; ok {
			if admin {
				items[i].IsPlatformAdmin = true
			}
			if items[i].Username == "" {
				items[i].Username = username
			}
			return
		}
		seen[id] = len(items)
		items = append(items, row{
			ID: id, Username: username, Email: email,
			IsPlatformAdmin: admin, Source: source,
		})
	}
	if s.Accounts != nil {
		users, err := s.Accounts.List(r.Context(), 200, 0)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not list users: "+err.Error())
			return
		}
		for _, u := range users {
			admin := s.userIsPlatformAdmin(r.Context(), u.ID, u.Username)
			src := ""
			if matchEnvAdmin(u.ID, u.Username) {
				src = "env"
			}
			add(u.ID, u.Username, u.Email, src, admin)
		}
	}
	if ps, ok := s.platformStore(); ok {
		ids, _ := ps.ListPlatformAdmins(r.Context())
		for _, id := range ids {
			add(id, id, "", "grant", true)
		}
	}
	for _, name := range envPlatformAdmins() {
		add(name, name, "", "env", true)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

func (s *Server) handleAdminUserPut(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "user id is required")
		return
	}
	var req struct {
		IsPlatformAdmin *bool `json:"is_platform_admin"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.IsPlatformAdmin == nil {
		writeError(w, http.StatusBadRequest, "is_platform_admin is required")
		return
	}
	if matchEnvAdmin(id, "") {
		writeError(w, http.StatusConflict, "cannot change an env-bootstrap super admin; unset PLATFORM_ADMIN_USERNAMES first")
		return
	}
	ps, ok := s.platformStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "platform admin store not available")
		return
	}
	if !*req.IsPlatformAdmin {
		ids, _ := ps.ListPlatformAdmins(r.Context())
		remaining := 0
		for _, other := range ids {
			if other != id {
				remaining++
			}
		}
		if remaining == 0 && len(envPlatformAdmins()) == 0 {
			writeError(w, http.StatusConflict, "cannot revoke the last super admin")
			return
		}
	}
	if err := ps.SetPlatformAdmin(r.Context(), id, authSubject(r), *req.IsPlatformAdmin); err != nil {
		writeError(w, http.StatusInternalServerError, "could not update super admin: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":                id,
		"is_platform_admin": *req.IsPlatformAdmin,
	})
}

func (s *Server) handleAdminReleaseList(w http.ResponseWriter, r *http.Request) {
	ps, ok := s.platformStore()
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"items": []*store.AppRelease{}, "count": 0})
		return
	}
	items, err := ps.ListReleases(r.Context(), r.URL.Query().Get("app"), r.URL.Query().Get("channel"), true)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list releases: "+err.Error())
		return
	}
	if items == nil {
		items = []*store.AppRelease{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

func (s *Server) handleAdminReleasePublish(w http.ResponseWriter, r *http.Request) {
	ps, ok := s.platformStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "release store not available")
		return
	}
	var rel store.AppRelease
	if !decodeJSON(w, r, &rel) {
		return
	}
	rel.PublishedBy = authSubject(r)
	if store.NormalizeReleaseApp(rel.App) == "" {
		writeError(w, http.StatusBadRequest, "app must be api, pwa, cli, daemon, or desktop")
		return
	}
	if strings.TrimSpace(rel.Version) == "" {
		writeError(w, http.StatusBadRequest, "version is required")
		return
	}
	if err := ps.UpsertRelease(r.Context(), &rel); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, rel)
}

func (s *Server) handleAdminReleaseYank(w http.ResponseWriter, r *http.Request) {
	ps, ok := s.platformStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "release store not available")
		return
	}
	app := r.PathValue("app")
	version := r.PathValue("version")
	if err := ps.YankRelease(r.Context(), app, version); err != nil {
		writeMemoryEditError(w, err, "could not yank release")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"yanked": true, "app": app, "version": version})
}
