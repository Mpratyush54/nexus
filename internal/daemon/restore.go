package daemon

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// RestoreOptions controls session hydration onto a local workspace.
type RestoreOptions struct {
	SessionID string
	Workspace string
	Harness   string
	DryRun    bool
	Force     bool
	ServerURL string
	Token     string
}

// RestoreSession downloads a snapshot and reconstitutes Antigravity brain + git diff.
func RestoreSession(ctx context.Context, opt RestoreOptions) (string, error) {
	opt.SessionID = strings.TrimSpace(opt.SessionID)
	opt.Workspace = strings.TrimSpace(opt.Workspace)
	if opt.SessionID == "" || opt.Workspace == "" {
		return "", fmt.Errorf("session_id and workspace are required")
	}
	st, err := os.Stat(opt.Workspace)
	if err != nil || !st.IsDir() {
		return "", fmt.Errorf("workspace must be an existing directory")
	}
	if _, err := os.Stat(filepath.Join(opt.Workspace, ".git")); err != nil && !opt.Force {
		return "", fmt.Errorf("workspace is not a git repo (pass force to override)")
	}
	if !opt.Force {
		if porcelain, err := runGit(opt.Workspace, "status", "--porcelain"); err == nil && strings.TrimSpace(porcelain) != "" {
			return "", fmt.Errorf("workspace is dirty — commit/stash or pass force")
		}
	}

	base := strings.TrimSuffix(strings.TrimSpace(opt.ServerURL), "/")
	if base == "" {
		return "", fmt.Errorf("server URL required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		base+"/sessions/"+opt.SessionID+"/snapshot?include=transcript,diff,artifacts", nil)
	if err != nil {
		return "", err
	}
	if tok := strings.TrimSpace(opt.Token); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("download snapshot: %s %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	var snap struct {
		Harness            string `json:"harness"`
		ConversationID     string `json:"conversation_id"`
		TurnCount          int    `json:"turn_count"`
		GitBranch          string `json:"git_branch"`
		GitCommit          string `json:"git_commit"`
		TranscriptB64      string `json:"transcript_payload_b64"`
		DiffB64            string `json:"uncommitted_diff_b64"`
		ArtifactsB64       string `json:"artifacts_bundle_b64"`
	}
	if err := json.Unmarshal(raw, &snap); err != nil {
		return "", fmt.Errorf("bad snapshot JSON: %w", err)
	}
	harness := strings.TrimSpace(opt.Harness)
	if harness == "" {
		harness = snap.Harness
	}
	if harness == "" {
		harness = "antigravity"
	}
	if !strings.EqualFold(harness, "antigravity") && !strings.EqualFold(harness, "gemini") {
		return "", fmt.Errorf("harness %q not supported yet (Antigravity only)", harness)
	}

	steps := []string{}
	if snap.GitBranch != "" {
		steps = append(steps, "checkout branch "+snap.GitBranch)
		if !opt.DryRun {
			cur, _, _, _, _ := GitStatus(opt.Workspace)
			if cur != snap.GitBranch {
				if _, err := runGit(opt.Workspace, "checkout", snap.GitBranch); err != nil {
					return "", fmt.Errorf("git checkout %s: %w", snap.GitBranch, err)
				}
			}
		}
	}

	diffGZ, _ := base64.StdEncoding.DecodeString(snap.DiffB64)
	diffRaw, _ := ungzipBytes(diffGZ)
	if len(diffRaw) > 0 {
		steps = append(steps, "apply uncommitted diff")
		if !opt.DryRun {
			if err := gitApplyDiff(opt.Workspace, diffRaw); err != nil {
				return "", err
			}
		}
	}

	convID := strings.TrimSpace(snap.ConversationID)
	if convID == "" {
		convID = opt.SessionID
	}
	_, brainDir := SnapshotHarnessPaths("antigravity", convID)
	steps = append(steps, "restore artifacts into "+brainDir)
	if !opt.DryRun {
		if err := os.MkdirAll(brainDir, 0o755); err != nil {
			return "", err
		}
		artGZ, _ := base64.StdEncoding.DecodeString(snap.ArtifactsB64)
		if len(artGZ) > 0 {
			if err := untarGzipTo(artGZ, brainDir); err != nil {
				return "", fmt.Errorf("unpack artifacts: %w", err)
			}
		}
		trGZ, _ := base64.StdEncoding.DecodeString(snap.TranscriptB64)
		trRaw, err := ungzipBytes(trGZ)
		if err != nil {
			return "", fmt.Errorf("decompress transcript: %w", err)
		}
		logDir := filepath.Join(brainDir, ".system_generated", "logs")
		if err := os.MkdirAll(logDir, 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(logDir, "transcript.jsonl"), trRaw, 0o644); err != nil {
			return "", err
		}
	}

	msg := fmt.Sprintf("Restored session %s (%d turns) into %s. Open Antigravity to continue.",
		opt.SessionID, snap.TurnCount, opt.Workspace)
	if opt.DryRun {
		msg = "Dry-run: " + msg + " Steps: " + strings.Join(steps, "; ")
	}
	return msg, nil
}

func gitApplyDiff(root string, diff []byte) error {
	cmd := exec.Command("git", "-C", root, "apply", "--whitespace=fix", "-")
	cmd.Stdin = bytes.NewReader(diff)
	if out, err := cmd.CombinedOutput(); err != nil {
		cmd2 := exec.Command("git", "-C", root, "apply", "--3way", "-")
		cmd2.Stdin = bytes.NewReader(diff)
		if out2, err2 := cmd2.CombinedOutput(); err2 != nil {
			return fmt.Errorf("git apply failed: %s / 3way: %s", bytes.TrimSpace(out), bytes.TrimSpace(out2))
		}
	}
	return nil
}

func untarGzipTo(gzData []byte, dest string) error {
	zr, err := gzip.NewReader(bytes.NewReader(gzData))
	if err != nil {
		return err
	}
	defer zr.Close()
	tr := tar.NewReader(zr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(hdr.Name)
		if strings.Contains(name, "..") {
			continue
		}
		target := filepath.Join(dest, name)
		if hdr.Typeflag == tar.TypeDir {
			_ = os.MkdirAll(target, 0o755)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, tr); err != nil {
			_ = f.Close()
			return err
		}
		_ = f.Close()
	}
	return nil
}

func (d *Daemon) handleSessionRestore(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body struct {
		SessionID string `json:"session_id"`
		Workspace string `json:"workspace"`
		Harness   string `json:"harness"`
		DryRun    bool   `json:"dry_run"`
		Force     bool   `json:"force"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	ws := strings.TrimSpace(body.Workspace)
	if ws == "" {
		ws = d.Root
	}
	msg, err := RestoreSession(r.Context(), RestoreOptions{
		SessionID: body.SessionID,
		Workspace: ws,
		Harness:   body.Harness,
		DryRun:    body.DryRun,
		Force:     body.Force,
		ServerURL: d.ServerURL,
		Token:     d.ServerToken,
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": msg})
}

// handleLocalSnapshots proxies project snapshot list for the cockpit UI.
func (d *Daemon) handleLocalSnapshots(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
	if projectID == "" {
		if pid, err := d.ResolveProjectIDForRoot(r.Context()); err == nil {
			projectID = strings.TrimSpace(pid)
		}
	}
	if projectID == "" {
		writeJSON(w, http.StatusOK, map[string]any{"items": []any{}, "count": 0})
		return
	}
	base := strings.TrimSuffix(strings.TrimSpace(d.ServerURL), "/")
	if base == "" || strings.TrimSpace(d.ServerToken) == "" {
		writeErr(w, http.StatusServiceUnavailable, "not signed in")
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, base+"/projects/"+projectID+"/snapshots", nil)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	req.Header.Set("Authorization", "Bearer "+d.ServerToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 8<<20))
}
