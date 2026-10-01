package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	"central-memory/internal/store"
)

// opsRoutesOnce guards registerOpsRoutes (same pattern as guestOffboardRoutes).
var opsRoutesOnce sync.Map

// opsSuspended holds tenant suspend reasons (P7 scaffolding; not durable yet).
var opsSuspended sync.Map // orgID -> reason string

// registerOpsRoutes wires P7 /ops/v1 expansions. Existing status/tenants/queues/health
// stay in registerCloudContractRoutes; this registrar adds users/plans/flags/releases/suspend.
func (s *Server) registerOpsRoutes() {
	if s == nil || s.Mux == nil {
		return
	}
	if _, loaded := opsRoutesOnce.LoadOrStore(s.Mux, struct{}{}); loaded {
		return
	}
	s.Mux.HandleFunc("GET /ops/v1/users", s.requireAuth(s.requirePlatformAdmin(s.handleOpsUsers)))
	s.Mux.HandleFunc("GET /ops/v1/plans", s.requireAuth(s.requirePlatformAdmin(s.handleOpsPlans)))
	s.Mux.HandleFunc("GET /ops/v1/flags", s.requireAuth(s.requirePlatformAdmin(s.handleOpsFlags)))
	s.Mux.HandleFunc("GET /ops/v1/releases", s.requireAuth(s.requirePlatformAdmin(s.handleOpsReleases)))
	s.Mux.HandleFunc("POST /ops/v1/tenants/{id}/suspend", s.requireAuth(s.requirePlatformAdmin(s.handleOpsTenantSuspend)))
}

func opsEmailHash(email string) string {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(email))
	return hex.EncodeToString(sum[:])
}

// handleOpsUsers lists ids + usernames / email hashes only (D20/D22). No session content.
func (s *Server) handleOpsUsers(w http.ResponseWriter, r *http.Request) {
	orgID := strings.TrimSpace(r.URL.Query().Get("org"))
	type row struct {
		ID        string `json:"id"`
		Username  string `json:"username,omitempty"`
		EmailHash string `json:"email_hash,omitempty"`
	}
	items := []row{}
	seen := map[string]struct{}{}

	add := func(id, username, email string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		items = append(items, row{
			ID:        id,
			Username:  strings.TrimSpace(username),
			EmailHash: opsEmailHash(email),
		})
	}

	if orgID != "" {
		os, ok := s.orgStore()
		if !ok {
			writeError(w, http.StatusNotImplemented, "organizations unavailable")
			return
		}
		members, err := os.ListOrgMembers(r.Context(), orgID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		for _, m := range members {
			if m == nil {
				continue
			}
			username, email := "", ""
			if s.Accounts != nil {
				if u, uerr := s.Accounts.GetByID(r.Context(), m.UserID); uerr == nil && u != nil {
					username, email = u.Username, u.Email
				}
			}
			if username == "" {
				username = m.UserID
			}
			add(m.UserID, username, email)
		}
	} else if s.Accounts != nil {
		users, err := s.Accounts.List(r.Context(), 200, 0)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not list users: "+err.Error())
			return
		}
		for _, u := range users {
			add(u.ID, u.Username, u.Email)
		}
	} else if ps, ok := s.platformStore(); ok {
		ids, _ := ps.ListPlatformAdmins(r.Context())
		for _, id := range ids {
			add(id, id, "")
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"users":           items,
		"count":           len(items),
		"session_content": false,
	})
}

func (s *Server) handleOpsPlans(w http.ResponseWriter, r *http.Request) {
	type planRow struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		PriceCents int    `json:"price_cents"`
		Currency   string `json:"currency,omitempty"`
		Interval   string `json:"interval,omitempty"`
		Public     bool   `json:"public"`
	}
	items := []planRow{}
	if bs, ok := s.billingStore(); ok {
		plans, err := bs.ListPlans(r.Context(), false)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not list plans: "+err.Error())
			return
		}
		for _, p := range plans {
			if p == nil {
				continue
			}
			items = append(items, planRow{
				ID: p.ID, Name: p.Name, PriceCents: p.PriceCents,
				Currency: p.Currency, Interval: p.Interval, Public: p.Public,
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"plans":           items,
		"count":           len(items),
		"session_content": false,
	})
}

func (s *Server) handleOpsFlags(w http.ResponseWriter, r *http.Request) {
	// Feature-flag store lands later; empty list is OK for P7 scaffolding.
	writeJSON(w, http.StatusOK, map[string]any{
		"flags":           []any{},
		"count":           0,
		"session_content": false,
	})
}

func (s *Server) handleOpsReleases(w http.ResponseWriter, r *http.Request) {
	type relRow struct {
		App     string `json:"app"`
		Version string `json:"version"`
		Channel string `json:"channel"`
		Yanked  bool   `json:"yanked,omitempty"`
		GitSHA  string `json:"git_sha,omitempty"`
	}
	items := []relRow{}
	if ps, ok := s.platformStore(); ok {
		list, err := ps.ListReleases(r.Context(), r.URL.Query().Get("app"), r.URL.Query().Get("channel"), true)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not list releases: "+err.Error())
			return
		}
		for _, rel := range list {
			if rel == nil {
				continue
			}
			// Deliberately omit notes / free-text — ops never carries session or release prose content keys.
			items = append(items, relRow{
				App: rel.App, Version: rel.Version, Channel: rel.Channel,
				Yanked: rel.Yanked, GitSHA: rel.GitSHA,
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"releases":        items,
		"count":           len(items),
		"session_content": false,
	})
}

func (s *Server) handleOpsTenantSuspend(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "tenant id is required")
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	reason := strings.TrimSpace(body.Reason)
	if reason == "" {
		writeError(w, http.StatusBadRequest, "reason is required")
		return
	}
	// Confirm tenant exists when org store is available.
	if os, ok := s.orgStore(); ok {
		if _, err := os.GetOrganization(r.Context(), id); err != nil {
			writeError(w, http.StatusNotFound, "tenant not found")
			return
		}
	}
	opsSuspended.Store(id, reason)
	s.recordAudit(r, store.AuditEvent{
		ActorKind:    "super_admin",
		Action:       "ops.tenant_suspended",
		ResourceKind: "organization",
		ResourceID:   id,
		Metadata:     map[string]any{"reason": reason},
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"id":              id,
		"suspended":       true,
		"reason":          reason,
		"session_content": false,
	})
}

// tenantSuspended reports whether an org id was suspended via /ops.
func tenantSuspended(id string) (reason string, ok bool) {
	v, loaded := opsSuspended.Load(strings.TrimSpace(id))
	if !loaded {
		return "", false
	}
	reason, _ = v.(string)
	return reason, true
}

// assertOpsNoContentKeys walks a JSON value and rejects forbidden content keys.
func assertOpsNoContentKeys(raw []byte) error {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	return walkForbidKeys(v, map[string]struct{}{
		"title": {}, "summary": {}, "content": {}, "transcript": {},
	})
}

func walkForbidKeys(v any, forbid map[string]struct{}) error {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			lk := strings.ToLower(k)
			if _, bad := forbid[lk]; bad {
				return errForbiddenOpsKey(k)
			}
			if err := walkForbidKeys(child, forbid); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range t {
			if err := walkForbidKeys(child, forbid); err != nil {
				return err
			}
		}
	}
	return nil
}

type forbiddenOpsKey string

func (e forbiddenOpsKey) Error() string { return "ops response contains forbidden key: " + string(e) }

func errForbiddenOpsKey(k string) error { return forbiddenOpsKey(k) }
