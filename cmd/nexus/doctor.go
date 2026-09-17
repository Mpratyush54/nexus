package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// probeResult is one row of `nexus doctor` output.
type probeResult struct {
	Probe  string `json:"probe"`
	Target string `json:"target"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// parseDoctorArgs parses `nexus doctor [--json]`.
func parseDoctorArgs(args []string) (bool, error) {
	fs := flag.NewFlagSet("nexus doctor", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	asJSON := fs.Bool("json", false, "emit JSON instead of tables")
	if err := fs.Parse(args); err != nil {
		return false, err
	}
	if len(fs.Args()) > 0 {
		return false, fmt.Errorf("doctor takes no positional arguments, got %q", strings.Join(fs.Args(), " "))
	}
	return *asJSON, nil
}

// runDoctor probes the central server, the workspace daemon, and git.
// It always prints a report; it returns an error (exit 1) when any probe fails.
func runDoctor(ctx context.Context, cfg Config, args []string, stdout io.Writer) error {
	flagJSON, err := parseDoctorArgs(args)
	if err != nil {
		return err
	}
	asJSON := cfg.JSON || flagJSON

	results := []probeResult{
		probeServer(ctx, cfg),
		probeDaemon(ctx, cfg),
		probeGit(ctx),
	}

	if asJSON {
		if err := printJSON(stdout, map[string]any{"probes": results}); err != nil {
			return err
		}
	} else {
		rows := make([][]string, 0, len(results))
		for _, r := range results {
			state := "ok"
			if !r.OK {
				state = "FAIL"
			}
			rows = append(rows, []string{r.Probe, r.Target, state, r.Detail})
		}
		printTable(stdout, []string{"PROBE", "TARGET", "STATE", "DETAIL"}, rows)
	}

	var failed []string
	for _, r := range results {
		if !r.OK {
			failed = append(failed, r.Probe)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("doctor: failing probes: %s", strings.Join(failed, ", "))
	}
	return nil
}

// probeServer hits the unauthenticated /healthz endpoint.
func probeServer(ctx context.Context, cfg Config) probeResult {
	target := strings.TrimSuffix(cfg.ServerURL, "/")
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target+"/healthz", nil)
	if err != nil {
		return probeResult{"server", target, false, err.Error()}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return probeResult{"server", target, false, "unreachable: " + err.Error()}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode != http.StatusOK {
		return probeResult{"server", target, false, fmt.Sprintf("HTTP %d: %s", resp.StatusCode, truncate(string(body), 120))}
	}
	var health map[string]any
	if json.Unmarshal(body, &health) == nil {
		if ok, _ := health["ok"].(bool); ok {
			return probeResult{"server", target, true, `{"ok":true}`}
		}
	}
	return probeResult{"server", target, true, truncate(strings.TrimSpace(string(body)), 120)}
}

// probeDaemon hits the daemon's GET /git/status. Without a token a 401 still
// proves reachability, so it is reported distinctly from a refusal/timeout.
func probeDaemon(ctx context.Context, cfg Config) probeResult {
	target := strings.TrimSuffix(cfg.DaemonURL, "/")
	token, source := resolveDaemonToken(cfg)
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target+"/git/status", nil)
	if err != nil {
		return probeResult{"daemon", target, false, err.Error()}
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return probeResult{"daemon", target, false, "unreachable: " + err.Error()}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	switch resp.StatusCode {
	case http.StatusOK:
		detail := "git status ok"
		if source != "" {
			detail += " (auth via " + source + ")"
		}
		return probeResult{"daemon", target, true, detail}
	case http.StatusUnauthorized, http.StatusForbidden:
		return probeResult{"daemon", target, false, "reachable, needs NEXUS_DAEMON_TOKEN (HTTP " + fmt.Sprint(resp.StatusCode) + ")"}
	default:
		return probeResult{"daemon", target, false, fmt.Sprintf("HTTP %d: %s", resp.StatusCode, truncate(string(body), 120))}
	}
}

// resolveDaemonToken prefers the explicit config, then the workspace-local
// token file (<cwd>/.central-memory/daemon.token) written by the daemon.
func resolveDaemonToken(cfg Config) (token, source string) {
	if strings.TrimSpace(cfg.DaemonToken) != "" {
		return strings.TrimSpace(cfg.DaemonToken), "NEXUS_DAEMON_TOKEN"
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", ""
	}
	raw, err := os.ReadFile(filepath.Join(cwd, ".central-memory", "daemon.token"))
	if err != nil {
		return "", ""
	}
	if t := strings.TrimSpace(string(raw)); t != "" {
		return t, ".central-memory/daemon.token"
	}
	return "", ""
}

// probeGit verifies a git binary exists and reports the current branch.
func probeGit(ctx context.Context) probeResult {
	git, err := exec.LookPath("git")
	if err != nil {
		return probeResult{"git", "git", false, "git binary not on PATH"}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	branchOut, err := exec.CommandContext(ctx, git, "rev-parse", "--abbrev-ref", HEAD).Output()
	if err != nil {
		return probeResult{"git", git, false, "not inside a git work tree (or git failed)"}
	}
	branch := strings.TrimSpace(string(branchOut))
	dirtyOut, _ := exec.CommandContext(ctx, git, "status", "--porcelain").Output()
	detail := "branch " + branch + ", clean"
	if len(strings.TrimSpace(string(dirtyOut))) > 0 {
		detail = "branch " + branch + ", dirty"
	}
	return probeResult{"git", git, true, detail}
}

// HEAD is the git ref used by the doctor probe (a const so tests can name it).
const HEAD = "HEAD"
