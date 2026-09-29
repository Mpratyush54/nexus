package daemon

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// DiagnosticResult is the JSON payload for GET /local/diagnostics.
type DiagnosticResult struct {
	ServerReachable   bool            `json:"server_reachable"`
	ServerLatencyMs   int             `json:"server_latency_ms"`
	TokenValid        bool            `json:"token_valid"`
	UserID            string          `json:"user_id,omitempty"`
	WorkspaceLinked   bool            `json:"workspace_linked"`
	WorkspaceID       string          `json:"workspace_id,omitempty"`
	GitAvailable      bool            `json:"git_available"`
	GitBranch         string          `json:"git_branch,omitempty"`
	GitRemote         string          `json:"git_remote,omitempty"`
	HarnessPathsFound map[string]bool `json:"harness_paths_found"`
	Errors            []string        `json:"errors,omitempty"`
}

// RunDiagnostics probes server reachability, auth, workspace, git, and harness paths.
func RunDiagnostics(d *Daemon) DiagnosticResult {
	res := DiagnosticResult{
		HarnessPathsFound: map[string]bool{},
	}
	if d == nil {
		res.Errors = append(res.Errors, "daemon not configured")
		return res
	}

	server := strings.TrimSpace(d.ServerURL)
	if server == "" {
		res.Errors = append(res.Errors, "no server URL configured")
	} else {
		client := &http.Client{Timeout: 5 * time.Second}
		start := time.Now()
		resp, err := client.Get(strings.TrimSuffix(server, "/") + "/healthz")
		res.ServerLatencyMs = int(time.Since(start).Milliseconds())
		if err != nil {
			res.Errors = append(res.Errors, "server unreachable: "+err.Error())
		} else {
			_ = resp.Body.Close()
			res.ServerReachable = resp.StatusCode >= 200 && resp.StatusCode < 500
			if !res.ServerReachable {
				res.Errors = append(res.Errors, "server healthz returned "+resp.Status)
			}
		}
	}

	res.TokenValid = strings.TrimSpace(d.ServerToken) != ""
	if !res.TokenValid {
		res.Errors = append(res.Errors, "not signed in — no server token")
	}
	res.UserID = strings.TrimSpace(d.UserID)
	res.WorkspaceID = d.getWorkspaceID()
	res.WorkspaceLinked = res.WorkspaceID != ""
	if strings.TrimSpace(d.Root) == "" {
		res.Errors = append(res.Errors, "no workspace folder selected")
	} else if !res.WorkspaceLinked {
		res.Errors = append(res.Errors, "workspace folder set but not linked to a server project yet")
	}

	if _, err := exec.LookPath("git"); err == nil {
		res.GitAvailable = true
	} else {
		res.Errors = append(res.Errors, "git binary not found on PATH")
	}
	if d.Root != "" && res.GitAvailable {
		branch, _, _, _, err := GitStatus(d.Root)
		if err != nil {
			res.Errors = append(res.Errors, "git status: "+err.Error())
		} else {
			res.GitBranch = branch
		}
		if remote, err := runGit(d.Root, "remote", "get-url", "origin"); err == nil {
			res.GitRemote = strings.TrimSpace(remote)
		}
	}

	for name, path := range harnessProbePaths() {
		if path == "" {
			res.HarnessPathsFound[name] = false
			continue
		}
		st, err := os.Stat(path)
		ok := err == nil && st.IsDir()
		res.HarnessPathsFound[name] = ok
		if !ok {
			res.Errors = append(res.Errors, "harness path missing: "+name+" ("+path+")")
		}
	}
	return res
}

func harnessProbePaths() map[string]string {
	home, _ := os.UserHomeDir()
	appData := os.Getenv("APPDATA")
	out := map[string]string{
		"antigravity": filepath.Join(home, ".gemini", "antigravity"),
		"claude":      filepath.Join(home, ".claude"),
	}
	if runtime.GOOS == "windows" {
		out["cursor"] = filepath.Join(appData, "Cursor")
		out["copilot"] = filepath.Join(appData, "GitHub Copilot")
	} else {
		out["cursor"] = filepath.Join(home, ".config", "Cursor")
		out["copilot"] = filepath.Join(home, ".config", "GitHub Copilot")
	}
	return out
}

func (d *Daemon) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, RunDiagnostics(d))
}
