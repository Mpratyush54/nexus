// local_bridge.go — daemon→server→PWA local-workspace channel (no hardcoded :7272).
//
// The workspace daemon already heartbeats branch/commit/dirty to the central
// server. This file stores an optional browser-bridge URL + recent git log
// from those heartbeats and exposes GET /workspaces/{projectId}/local so the
// PWA reads local state through the API/WS path instead of probing localhost.
package server

import (
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"central-memory/internal/store"
)

// localBridgeSnapshot is the latest local workspace view relayed by a daemon.
type localBridgeSnapshot struct {
	WorkspaceID string    `json:"workspace_id"`
	ProjectID   string    `json:"project_id"`
	UserID      string    `json:"user_id"`
	Path        string    `json:"path,omitempty"`
	Branch      string    `json:"branch,omitempty"`
	CommitSHA   string    `json:"commit_sha,omitempty"`
	IsDirty     bool      `json:"is_dirty"`
	MachineID   string    `json:"machine_id,omitempty"`
	ProxyURL    string    `json:"proxy_url,omitempty"` // optional same-machine browser bridge
	GitLog      string    `json:"git_log,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
}

var (
	bridgeMu   sync.RWMutex
	bridgeByWS = map[string]*localBridgeSnapshot{} // workspaceID -> snap
)

func (s *Server) registerLocalBridgeRoutes() {
	s.Mux.HandleFunc("GET /workspaces/{projectId}/local", s.requireAuth(s.handleWorkspaceLocal))
}

func rememberLocalBridge(ws *store.Workspace, proxyURL, gitLog string) *localBridgeSnapshot {
	if ws == nil || strings.TrimSpace(ws.ID) == "" {
		return nil
	}
	snap := &localBridgeSnapshot{
		WorkspaceID: ws.ID,
		ProjectID:   ws.ProjectID,
		UserID:      ws.UserID,
		Path:        ws.Path,
		Branch:      ws.Branch,
		CommitSHA:   ws.CommitSHA,
		IsDirty:     ws.IsDirty,
		MachineID:   ws.MachineID,
		ProxyURL:    strings.TrimSpace(proxyURL),
		GitLog:      strings.TrimSpace(gitLog),
		UpdatedAt:   time.Now().UTC(),
	}
	bridgeMu.Lock()
	bridgeByWS[ws.ID] = snap
	bridgeMu.Unlock()
	return snap
}

func localBridgeForProject(projectID string) *localBridgeSnapshot {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil
	}
	bridgeMu.RLock()
	defer bridgeMu.RUnlock()
	var best *localBridgeSnapshot
	for _, snap := range bridgeByWS {
		if snap == nil || snap.ProjectID != projectID {
			continue
		}
		if best == nil || snap.UpdatedAt.After(best.UpdatedAt) {
			best = snap
		}
	}
	return best
}

func (s *Server) handleWorkspaceLocal(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.PathValue("projectId"))
	if !s.authorizeProject(w, r, projectID) {
		return
	}

	ws, err := s.Store.GetActiveWorkspace(r.Context(), projectID)
	online := false
	if err == nil && ws != nil {
		online = WorkspaceIsOnline(ws, time.Now())
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, "could not fetch workspace: "+err.Error())
		return
	}

	snap := localBridgeForProject(projectID)
	out := map[string]any{
		"online":     online,
		"project_id": projectID,
		"source":     "server",
	}
	if online && ws != nil {
		out["workspace"] = ws
		out["branch"] = ws.Branch
		out["commit_sha"] = ws.CommitSHA
		out["is_dirty"] = ws.IsDirty
		out["path"] = ws.Path
		out["machine_id"] = ws.MachineID
		out["workspace_id"] = ws.ID
	}
	if snap != nil {
		useSnap := !online || (ws != nil && snap.WorkspaceID == ws.ID)
		if useSnap {
			if snap.Branch != "" {
				out["branch"] = snap.Branch
			}
			if snap.CommitSHA != "" {
				out["commit_sha"] = snap.CommitSHA
			}
			out["is_dirty"] = snap.IsDirty
			if snap.Path != "" {
				out["path"] = snap.Path
			}
			if snap.MachineID != "" {
				out["machine_id"] = snap.MachineID
			}
			out["workspace_id"] = snap.WorkspaceID
		}
		if snap.ProxyURL != "" {
			out["proxy_url"] = snap.ProxyURL
		}
		if snap.GitLog != "" {
			out["git_log"] = snap.GitLog
		}
		out["bridge_updated_at"] = snap.UpdatedAt
		if !online {
			out["stale"] = true
		}
	}

	writeJSON(w, http.StatusOK, out)
}

func (s *Server) publishWorkspaceLocal(projectID string, snap *localBridgeSnapshot) {
	if snap == nil || strings.TrimSpace(projectID) == "" {
		return
	}
	_, hub := s.getSteer()
	if hub == nil {
		return
	}
	hub.PublishEvent(projectID, "", "WORKSPACE_LOCAL", map[string]any{
		"workspace_id": snap.WorkspaceID,
		"branch":       snap.Branch,
		"commit_sha":   snap.CommitSHA,
		"is_dirty":     snap.IsDirty,
		"path":         snap.Path,
		"proxy_url":    snap.ProxyURL,
		"updated_at":   snap.UpdatedAt,
	}, snap.UserID)
}
