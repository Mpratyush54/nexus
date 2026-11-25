//go:build nexuscorelib

package main

// Agent instruction files (CLAUDE.md / AGENTS.md / GEMINI.md) for the
// desktop Settings page. The sync itself lives in internal/agentfiles and
// internal/daemon; these methods are routed here (not internal/core)
// because they need the per-workspace capture Runtime, and core must not
// import daemon.
//
//	agents.memory_files.get  {workspace_root}
//	    -> {workspace_root, settings}
//	agents.memory_files.set  {workspace_root, enabled, files?, create_missing}
//	    -> {workspace_root, settings, report?, error?}
//	    (when enabled, a forced sync runs immediately)
//	agents.memory_files.sync {workspace_root}
//	    -> {workspace_root, settings, report, error?}
//
// settings is {enabled, files: [claude|agents|gemini], create_missing};
// report is agentfiles.SyncReport. A sync failure is returned in "error"
// next to the saved settings rather than failing the call, so the UI can
// show both.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"central-memory/internal/agentfiles"
	"central-memory/internal/core"
	"central-memory/internal/daemon"
)

const agentMemoryFilesSyncTimeout = 2 * time.Minute

func agentMemoryFilesCall(ctx context.Context, name string, args json.RawMessage) (any, error) {
	var req struct {
		WorkspaceRoot string    `json:"workspace_root"`
		Enabled       bool      `json:"enabled"`
		Files         *[]string `json:"files"`
		CreateMissing bool      `json:"create_missing"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, fmt.Errorf("invalid request: %w", err)
		}
	}
	root, err := agentMemoryWorkspace(req.WorkspaceRoot)
	if err != nil {
		return nil, err
	}
	switch name {
	case "agents.memory_files.get":
		settings, err := agentfiles.GetSettings(root)
		if err != nil {
			return nil, err
		}
		return map[string]any{"workspace_root": root, "settings": settings}, nil
	case "agents.memory_files.set":
		if err := requireDir(root); err != nil {
			return nil, err
		}
		next := agentfiles.Settings{Enabled: req.Enabled, CreateMissing: req.CreateMissing}
		if req.Files != nil {
			next.Files = *req.Files
		} else if current, err := agentfiles.GetSettings(root); err == nil {
			next.Files = current.Files
		} else {
			next.Files = agentfiles.DefaultSettings().Files
		}
		saved, err := agentfiles.SetSettings(root, next)
		if err != nil {
			return nil, err
		}
		out := map[string]any{"workspace_root": root, "settings": saved}
		if saved.Enabled {
			report, err := syncAgentMemoryFiles(ctx, root)
			out["report"] = report
			if err != nil {
				out["error"] = err.Error()
			}
		}
		return out, nil
	case "agents.memory_files.sync":
		if err := requireDir(root); err != nil {
			return nil, err
		}
		settings, err := agentfiles.GetSettings(root)
		if err != nil {
			return nil, err
		}
		report, err := syncAgentMemoryFiles(ctx, root)
		out := map[string]any{"workspace_root": root, "settings": settings, "report": report}
		if err != nil {
			out["error"] = err.Error()
		}
		return out, nil
	}
	return nil, fmt.Errorf("unknown method %s", name)
}

func agentMemoryWorkspace(raw string) (string, error) {
	root := strings.TrimSpace(raw)
	if root == "" {
		return "", errors.New("workspace_root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

func requireDir(root string) error {
	st, err := os.Stat(root)
	if err != nil || !st.IsDir() {
		return fmt.Errorf("workspace folder not found: %s", root)
	}
	return nil
}

// syncAgentMemoryFiles forces a sync through the workspace's capture Runtime
// when one is running, and otherwise through a one-off syncer built from the
// signed-in cloud connection.
func syncAgentMemoryFiles(ctx context.Context, root string) (agentfiles.SyncReport, error) {
	ctx, cancel := context.WithTimeout(ctx, agentMemoryFilesSyncTimeout)
	defer cancel()
	runtime, serverURL, token, projectID := agentMemoryConnection(root)
	if runtime != nil {
		return runtime.SyncAgentMemoryFiles(ctx, true)
	}
	return daemon.SyncAgentMemoryFilesFor(ctx, root, serverURL, token, projectID)
}

// agentMemoryConnection finds the capture Runtime for root (primary or an
// additional workspace) plus the cloud connection for a Runtime-less sync.
func agentMemoryConnection(root string) (runtime *daemon.Runtime, serverURL, token, projectID string) {
	nxState.mu.Lock()
	defer nxState.mu.Unlock()
	if cfg := nxState.captureConfig; cfg != nil {
		serverURL, token = cfg.serverURL, cfg.serverToken
		if samePath(cfg.root, root) {
			runtime = nxState.captureRuntime
			projectID = nxState.captureProjectID
		}
	}
	if cloud, ok := nxState.deps.Cloud.(*core.HTTPCloud); ok && cloud != nil {
		serverURL, token = cloud.BaseURL, cloud.Token
	}
	if runtime == nil {
		for other, r := range nxState.captureOthers {
			if r != nil && samePath(other, root) {
				runtime = r
				break
			}
		}
	}
	for other, id := range nxState.captureWorkProjects {
		if id != "" && samePath(other, root) {
			projectID = id
			break
		}
	}
	return runtime, serverURL, token, projectID
}
