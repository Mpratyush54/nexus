package daemon

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"

	"central-memory/internal/config"
)

type workspaceSwitchRequest struct {
	Path string `json:"path"`
}

// handleWorkspaceSwitch updates config.WorkspaceRoot so the next daemon
// restart picks up the folder. It does not hot-swap Root at runtime.
func (d *Daemon) handleWorkspaceSwitch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req workspaceSwitchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	path := strings.TrimSpace(req.Path)
	if path == "" {
		writeErr(w, http.StatusBadRequest, "path is required")
		return
	}
	st, err := os.Stat(path)
	if err != nil || !st.IsDir() {
		writeErr(w, http.StatusBadRequest, "path must be an existing directory")
		return
	}
	if err := config.SaveFile(config.File{WorkspaceRoot: path}); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not save workspace: "+err.Error())
		return
	}
	normalized := config.NormalizeWorkspacePath(path)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"message": "Workspace switched. Daemon restart required.",
		"path":    normalized,
	})
}

// handleWorkspaceRecent returns the MRU workspace list from local config.
func (d *Daemon) handleWorkspaceRecent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	cfg, err := config.LoadFile()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	list := cfg.RecentWorkspaces
	if list == nil {
		list = []string{}
	}
	writeJSON(w, http.StatusOK, list)
}
