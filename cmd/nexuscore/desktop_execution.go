//go:build nexuscorelib

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"time"

	"central-memory/internal/config"
	"central-memory/internal/core"
	"central-memory/internal/desktopbridge"
	"central-memory/internal/desktopworker"
	"central-memory/internal/diagnostics"
)

var desktopExecution struct {
	sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	pending map[string]chan bool
	problem string
}

func stopDesktopExecution() {
	desktopExecution.Lock()
	cancel, done := desktopExecution.cancel, desktopExecution.done
	desktopExecution.cancel = nil
	desktopExecution.done = nil
	desktopExecution.Unlock()
	if cancel != nil {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	}
}
func startDesktopExecution() {
	saved, e := config.LoadFile()
	if e != nil || !saved.DesktopExecutionEnabled {
		return
	}
	nxState.mu.Lock()
	cloud, _ := nxState.deps.Cloud.(*core.HTTPCloud)
	nxState.mu.Unlock()
	if cloud == nil {
		return
	}
	id, secret, e := config.DesktopIdentity()
	if e != nil {
		return
	}
	desktopExecution.Lock()
	if desktopExecution.cancel != nil {
		desktopExecution.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	desktopExecution.cancel, desktopExecution.done = cancel, done
	desktopExecution.pending = map[string]chan bool{}
	desktopExecution.Unlock()
	name, _ := os.Hostname()
	w := &desktopworker.Worker{Client: desktopbridge.Client{BaseURL: cloud.BaseURL, Token: cloud.Token, Key: secret}, ID: id, Name: name, Roots: desktopProjectRoots,
		Deps: func() core.Deps { nxState.mu.Lock(); defer nxState.mu.Unlock(); return nxState.deps }, Approve: approveDesktopRequest,
		Report: func(problem string) {
			desktopExecution.Lock()
			desktopExecution.problem = problem
			desktopExecution.Unlock()
		},
	}
	go func() { defer close(done); defer diagnostics.Guard("desktop-execution")(); w.Run(ctx) }()
}
func desktopProjectRoots() map[string]string {
	saved, e := config.LoadFile()
	if e != nil || !saved.DesktopExecutionEnabled {
		return map[string]string{}
	}
	nxState.mu.Lock()
	defer nxState.mu.Unlock()
	out := map[string]string{}
	// Multiple checkouts may map to one canonical project. Prefer the active
	// approved workspace, otherwise the first consented folder, not map order.
	for _, approved := range saved.CaptureWorkspaces {
		for root, id := range nxState.captureWorkProjects {
			if id != "" && samePath(root, approved) && (out[id] == "" || samePath(root, nxState.deps.Root)) {
				out[id] = root
			}
		}
	}
	return out
}
func approveDesktopRequest(ctx context.Context, j desktopbridge.Job, root string, images []string) bool {
	ch := make(chan bool, 1)
	desktopExecution.Lock()
	desktopExecution.pending[j.ID] = ch
	desktopExecution.Unlock()
	defer func() {
		desktopExecution.Lock()
		delete(desktopExecution.pending, j.ID)
		desktopExecution.Unlock()
		emitCoreEvent(map[string]any{"type": "desktop.request.closed", "request_id": j.ID})
	}()
	emitCoreEvent(map[string]any{"type": "desktop.request", "request_id": j.ID, "agent": j.Agent, "model": j.Model, "prompt": j.Prompt, "workspace": root, "session_id": j.SessionID, "images": images})
	select {
	case <-ctx.Done():
		return false
	case yes := <-ch:
		return yes
	}
}
func desktopExecutionCall(name string, args json.RawMessage) (any, error) {
	switch name {
	case "desktop.execution.status":
		cfg, e := config.LoadFile()
		if e != nil {
			return nil, e
		}
		desktopExecution.Lock()
		problem := desktopExecution.problem
		running := desktopExecution.cancel != nil
		desktopExecution.Unlock()
		supported := problem != desktopworker.UnsupportedProblem
		if !supported {
			problem = "The connected Nexus server does not offer browser-to-desktop execution yet."
		}
		return map[string]any{"enabled": cfg.DesktopExecutionEnabled, "running": running, "problem": problem, "server_supported": supported, "mapped_projects": len(desktopProjectRoots()), "approval_required": true}, nil
	case "desktop.execution.configure":
		var req struct {
			Enabled bool `json:"enabled"`
		}
		if e := json.Unmarshal(args, &req); e != nil {
			return nil, e
		}
		if e := config.SetDesktopExecution(req.Enabled); e != nil {
			return nil, e
		}
		stopDesktopExecution()
		if req.Enabled {
			startDesktopExecution()
		}
		return desktopExecutionCall("desktop.execution.status", nil)
	case "desktop.execution.approve":
		var req struct {
			ID       string `json:"request_id"`
			Approved bool   `json:"approved"`
		}
		if e := json.Unmarshal(args, &req); e != nil {
			return nil, e
		}
		desktopExecution.Lock()
		ch := desktopExecution.pending[req.ID]
		desktopExecution.Unlock()
		if ch == nil {
			return nil, errors.New("request is no longer awaiting approval")
		}
		select {
		case ch <- req.Approved:
			return map[string]bool{"acknowledged": true}, nil
		default:
			return nil, errors.New("request was already answered")
		}
	}
	return nil, core.ErrUnknownMethod
}
