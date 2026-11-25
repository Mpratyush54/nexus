//go:build nexuscorelib

package main

// One-click "Install Nexus into all my agents" for the desktop Agents page.
// Routed here (like agents.memory_files.*) because it combines the MCP
// install, agent-setup sharing and the memory-files sync, and the latter
// needs the per-workspace capture Runtime.
//
//	agents.install_all {dry_run, include_setup_share, include_memory_files,
//	                    scope: user|project, project_id?, workspace_root?,
//	                    app_dir? (folder with nexus-mcp-proxy; default: host exe's)}
//	    -> {dry_run, scope, server_url, harnesses: [HarnessReport],
//	        setup_share?: {writes, skipped, error?},
//	        memory_files?: {action, reason?, workspace_root?, settings?, report?, error?}}
//	agents.uninstall_all {dry_run, scope, workspace_root?}
//	    -> {dry_run, scope, harnesses: [HarnessReport]}
//
// Agents get their own scoped API token (POST /auth/tokens, bound to the
// harness as agent_id), never the user's session token. A harness that
// already holds a Nexus agent token for the same server keeps it, so a
// reinstall is idempotent and mints nothing. Dry runs never mint or write.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"

	"central-memory/internal/agentfiles"
	"central-memory/internal/agentsetup"
	"central-memory/internal/core"
	"central-memory/internal/mcpproxy"
)

func agentsInstallCall(ctx context.Context, name string, args json.RawMessage) (any, error) {
	var req struct {
		DryRun             bool   `json:"dry_run"`
		IncludeSetupShare  bool   `json:"include_setup_share"`
		IncludeMemoryFiles bool   `json:"include_memory_files"`
		Scope              string `json:"scope"`
		ProjectID          string `json:"project_id"`
		WorkspaceRoot      string `json:"workspace_root"`
		AppDir             string `json:"app_dir"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, fmt.Errorf("invalid request: %w", err)
		}
	}
	scope := strings.TrimSpace(req.Scope)
	if scope == "" {
		scope = "user"
	}
	workspace := ""
	if strings.TrimSpace(req.WorkspaceRoot) != "" {
		root, err := agentMemoryWorkspace(req.WorkspaceRoot)
		if err != nil {
			return nil, err
		}
		workspace = root
	}

	nxState.mu.Lock()
	cloud, _ := nxState.deps.Cloud.(*core.HTTPCloud)
	nxState.mu.Unlock()
	serverURL, session := "", ""
	if cloud != nil {
		serverURL, session = strings.TrimSpace(cloud.BaseURL), strings.TrimSpace(cloud.Token)
	}

	// Antigravity runs the proxy bundled next to Nexus.exe/nexuscore.dll
	// (the host process), never a separately installed nexus CLI.
	appDir := strings.TrimSpace(req.AppDir)
	if appDir == "" {
		appDir = mcpproxy.DefaultAppDir()
	}
	opts := agentsetup.InstallOptions{
		AppDir:       appDir,
		Workspace:    workspace,
		Scope:        scope,
		ServerURL:    serverURL,
		ProjectID:    strings.TrimSpace(req.ProjectID),
		DryRun:       req.DryRun,
		SessionToken: session,
	}
	out := map[string]any{"dry_run": req.DryRun, "scope": scope}

	if name == "agents.uninstall_all" {
		reports, err := agentsetup.UninstallMCP(opts)
		if err != nil {
			return nil, err
		}
		out["harnesses"] = reports
		return out, nil
	}

	if serverURL == "" || session == "" {
		return nil, errors.New("sign in to Nexus first so agents can reach your memory")
	}
	opts.Token = agentTokenMinter(ctx, cloud)
	reports, err := agentsetup.InstallMCP(opts)
	if err != nil {
		return nil, err
	}
	out["server_url"] = serverURL
	out["harnesses"] = reports

	if req.IncludeSetupShare {
		installed := agentsetup.InstalledHarnesses(opts)
		agents := []string{}
		for id, ok := range installed {
			if ok {
				agents = append(agents, id)
			}
		}
		share := map[string]any{}
		if len(agents) < 2 {
			share["writes"], share["skipped"] = []any{}, []string{"fewer than two agents installed"}
		} else {
			res, err := agentsetup.ShareAll(agentsetup.ShareAllOptions{Workspace: workspace, Scope: scope, Agents: agents, DryRun: req.DryRun})
			share["writes"], share["skipped"] = res.Writes, res.Skipped
			if err != nil {
				share["error"] = err.Error()
			}
		}
		out["setup_share"] = share
	}

	if req.IncludeMemoryFiles {
		out["memory_files"] = installMemoryFiles(ctx, workspace, req.DryRun)
	}
	return out, nil
}

// installMemoryFiles turns on the managed CLAUDE.md/AGENTS.md/GEMINI.md
// section for the workspace (agents.memory_files.set), or previews it.
func installMemoryFiles(ctx context.Context, workspace string, dryRun bool) map[string]any {
	if workspace == "" {
		return map[string]any{"action": "skipped", "reason": "choose a project folder first"}
	}
	if err := requireDir(workspace); err != nil {
		return map[string]any{"action": "error", "reason": err.Error()}
	}
	settings, err := agentfiles.GetSettings(workspace)
	if err != nil {
		return map[string]any{"action": "error", "reason": err.Error()}
	}
	if dryRun {
		action := "updated"
		if settings.Enabled {
			action = "unchanged"
		}
		return map[string]any{"action": action, "workspace_root": workspace, "settings": settings}
	}
	raw, _ := json.Marshal(map[string]any{"workspace_root": workspace, "enabled": true, "create_missing": settings.CreateMissing})
	res, err := agentMemoryFilesCall(ctx, "agents.memory_files.set", raw)
	if err != nil {
		return map[string]any{"action": "error", "reason": err.Error()}
	}
	m, _ := res.(map[string]any)
	if m == nil {
		m = map[string]any{}
	}
	m["action"] = "updated"
	if settings.Enabled {
		m["action"] = "unchanged"
	}
	if e, _ := m["error"].(string); e != "" {
		m["action"], m["reason"] = "error", e
	}
	return m
}

// agentTokenMinter creates one scoped API token per harness, bound to it as
// agent_id so the server applies that agent's permissions. The first
// failure is remembered so an offline server is not hit once per harness.
func agentTokenMinter(ctx context.Context, cloud *core.HTTPCloud) func(string) (string, error) {
	var mu sync.Mutex
	var failed error
	host, _ := os.Hostname()
	return func(harness string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if failed != nil {
			return "", failed
		}
		label := "Nexus MCP · " + harness
		if host != "" {
			label += " (" + host + ")"
		}
		body, _ := json.Marshal(map[string]any{"name": label, "scopes": []string{"agent:mcp"}, "agent_id": harness})
		raw, err := cloud.Do(ctx, http.MethodPost, "/auth/tokens", body)
		if err != nil {
			failed = err
			return "", err
		}
		var minted struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal(raw, &minted); err != nil || !strings.HasPrefix(minted.Token, "nxs_") {
			failed = errors.New("the server did not return an agent token")
			return "", failed
		}
		return minted.Token, nil
	}
}
