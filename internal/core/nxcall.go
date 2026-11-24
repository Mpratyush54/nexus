// Package core is the in-process Nexus engine behind nx_call (spec 7.7).
// Data methods need a Cloud client. Capture, continue, cache, and file
// reads run locally. A nil Cloud returns ErrOffline for cloud methods.
package core

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"central-memory/internal/agents/mcpconfig"
	"central-memory/internal/agentsetup"
	"central-memory/internal/authbrowser"
	"central-memory/internal/blobs"
	"central-memory/internal/cache"
	"central-memory/internal/capture"
	"central-memory/internal/config"
	"central-memory/internal/continuex"
	"central-memory/internal/idehistory"
	"central-memory/internal/outbox"
	"central-memory/internal/owned"
	"central-memory/internal/secrets"
	"central-memory/internal/teleport"
	"central-memory/internal/usage"
)

// ErrOffline is returned when a data method has no cloud client (D6, D7).
var ErrOffline = errors.New("Nexus is offline; memory unavailable")

// ErrUnknownMethod is an nx_call name this build does not implement.
var ErrUnknownMethod = errors.New("unknown nx_call method")

func sameWorkspacePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// Cloud is the thin /v1 client data methods wrap.
type Cloud interface {
	Do(ctx context.Context, method, path string, body []byte) ([]byte, error)
}

// CaptureController owns the host application's background harvester.  The
// core deliberately knows no daemon implementation: a desktop build can run
// the harvester in-process, while a CLI may leave this nil.
type CaptureController interface {
	Pause() error
	Resume() error
	Status() any
}

type CaptureWorkspaceController interface {
	ConfigureWorkspaces([]string) (any, error)
}

// Deps are the local subsystems nx_call can touch.
type Deps struct {
	Cloud            Cloud
	Cache            *cache.Cache
	Outbox           *outbox.Spool
	Root             string
	Online           bool
	Capture          CaptureController
	ContinueRunner   continuex.Runner                                        // optional; when set, continue.start records/starts via Runner
	ContinueExecutor continuex.TurnExecutor                                  // optional in-app native turn executor
	ContinueProgress func(requestID string, update continuex.StreamProgress) // optional in-process UI progress channel
	ContinueCancel   func(requestID string) bool                             // optional cancellation of an in-flight in-app turn
	ContinuationDir  string                                                  // optional override for durable local in-app turns
}

// capturedAgentSummary deliberately describes only harnesses that actually
// reached the signed-in account.  A static support catalogue made the native
// desktop claim dozens of agents were connected when no capture existed.
type capturedAgentSummary struct {
	Name          string `json:"name"`
	Harness       string `json:"harness"`
	ProjectID     string `json:"project_id,omitempty"`
	Status        string `json:"status"`
	ResumeMode    string `json:"resume_mode"`
	CapturedCount int    `json:"captured_count"`
	LastActivity  string `json:"last_activity,omitempty"`
}

func newerTimestamp(current, candidate string) string {
	if candidate == "" {
		return current
	}
	if current == "" {
		return candidate
	}
	currentAt, currentErr := time.Parse(time.RFC3339Nano, current)
	candidateAt, candidateErr := time.Parse(time.RFC3339Nano, candidate)
	if candidateErr == nil && (currentErr != nil || candidateAt.After(currentAt)) {
		return candidate
	}
	return current
}

// Methods is the IDL. Bindings in bindings/ must list the same names.
func Methods() []string {
	return []string{
		"timeline.list",
		"events.subscribe",
		"sessions.get",
		"sessions.create",
		"sessions.turns",
		"sessions.summary",
		"sessions.usage", "usage.summary",
		"files.read",
		"files.diff",
		"continue.start",
		"harness.status", "harness.models",
		"continue.here",
		"continue.cancel",
		"continue.local.list",
		"continue.record",
		"agents.setup",
		"agents.setup.share",
		"memory.search",
		"memory.forget",
		"memory.pin",
		"memory.scope",
		"sessions.share",
		"sharing.recipients",
		"uploads.status",
		"uploads.retry",
		"cache.clear",
		"net.status",
		"teleport.redaction_preview",
		"teleport.send",
		"teleport.restore_code",
		"teleport.inbox",
		"teleport.sent",
		"teleport.prepare",
		"teleport.apply",
		"teleport.revoke",
		"sessions.files",
		"sessions.operations",
		"sessions.forks",
		"sessions.fork",
		"sessions.memories",
		"sessions.versions",
		"sessions.unshare",
		"sessions.shared_with",
		"sessions.grants",
		"sessions.restore_ide_history",
		"sessions.delete",
		"agents.list",
		"agents.configure_mcp",
		"runs.message",
		"runs.approve",
		"runs.cancel",
		"memory.update",
		"memory.history",
		"memory.revert",
		"memory.confirm",
		"memory.reject",
		"projects.brief",
		"projects.brief_refresh",
		"projects.graph",
		"secrets.list",
		"secrets.restore",
		"uploads.resolve_large_file",
		"projects.list",
		"orgs.list", "orgs.create", "orgs.get", "orgs.member_role", "orgs.member_remove",
		"orgs.invite_create", "orgs.invites", "orgs.invite_revoke", "orgs.invite_accept",
		"projects.capture_get",
		"projects.capture_set",
		"git.status",
		"settings.get",
		"auth.login",
		"auth.logout",
		"auth.tokens.list",
		"auth.tokens.revoke",
		"auth.status",
		"capture.pause",
		"capture.resume",
		"capture.configure",
		"dashboard.get",
		"capture.walk",
		"capture.restore",
		"status.get",
		"diagnostics.get",
		"auth.callback",
	}
}

// MarshalResult is the JSON envelope nx_call and the nexuscore CLI return.
func MarshalResult(result any, err error) []byte {
	resp := map[string]any{"ok": err == nil}
	if err != nil {
		resp["error"] = err.Error()
	} else {
		resp["result"] = result
	}
	raw, merr := json.Marshal(resp)
	if merr != nil {
		return []byte(`{"ok":false,"error":"could not encode response"}`)
	}
	return raw
}

// Call dispatches one nx_call method.
func Call(ctx context.Context, method string, args json.RawMessage, deps Deps) (any, error) {
	method = strings.TrimSpace(method)
	switch method {
	case "cache.clear":
		if deps.Cache != nil {
			deps.Cache.Clear()
		}
		return map[string]any{"cleared": true}, nil
	case "net.status":
		return map[string]any{"online": deps.Online}, nil
	case "uploads.status":
		n := 0
		if deps.Outbox != nil {
			n = len(deps.Outbox.Pending())
		}
		return map[string]any{"pending": n}, nil
	case "uploads.retry":
		if deps.Online && deps.Cloud != nil && deps.Outbox != nil {
			res, err := outbox.Drain(ctx, deps.Outbox, deps.Cloud)
			out := map[string]any{"pending": res.Pending, "uploaded": res.Uploaded}
			if err != nil {
				return out, err
			}
			return out, nil
		}
		return map[string]any{"pending": pendingCount(deps)}, nil
	case "continue.cancel":
		requestID := argString(args, "request_id")
		if requestID == "" {
			return nil, errors.New("request id is required")
		}
		if deps.ContinueCancel == nil {
			return nil, errors.New("in-app cancellation is unavailable")
		}
		return map[string]any{"cancelled": deps.ContinueCancel(requestID)}, nil
	case "harness.status":
		return continuex.DetectHarness(argString(args, "agent")), nil
	case "harness.models":
		// Cached per harness (memory + disk, 24h, background refresh) so the
		// picker does not spawn a CLI each time it opens.
		return continuex.HarnessModelsCached(ctx, argString(args, "agent"), argBool(args, "refresh"))
	case "continue.start":
		var req continuex.Request
		if err := decode(args, &req); err != nil {
			return nil, err
		}
		// open_in_agent must be the agent's own resume command: without a
		// native session id the only argv is `nexus continue …`, which needs
		// the separate CLI the desktop app does not ship.
		if strings.TrimSpace(req.Mode) == "open_in_agent" && strings.TrimSpace(req.NativeID) == "" {
			return nil, errors.New("this session has no native agent session id, so it cannot be resumed in the agent")
		}
		argv, err := continuex.Command(req)
		if strings.TrimSpace(req.NativeID) != "" && req.Mode == "open_in_agent" {
			var plan continuex.LaunchPlan
			plan, err = continuex.Plan(req.Agent, req.Mode, req.NativeID, req.Prompt)
			if err == nil {
				argv = plan.Argv
			}
		}
		if err != nil {
			return nil, err
		}
		// Desktop shows argv only (clipboard / copy resume). Core must never
		// spawn processes on continue.start — that caused visible cmd storms
		// when a runner was wired or argv pointed at shell shims.
		opID := "op_" + req.SessionID
		trackRun(opID, argv)
		return map[string]any{"op_id": opID, "command": argv, "spawn": false}, nil
	case "continue.here":
		var req struct {
			continuex.Request
			WorkspaceRoot  string   `json:"workspace_root"`
			Context        string   `json:"context"`
			Images         []string `json:"images"`
			Model          string   `json:"model"`
			TrustWorkspace bool     `json:"trust_workspace"`
			RequestID      string   `json:"request_id"`
			ProjectID      string   `json:"project_id"`
		}
		if err := decode(args, &req); err != nil {
			return nil, err
		}
		if strings.TrimSpace(req.WorkspaceRoot) == "" {
			return nil, errors.New("select a local workspace before sending")
		}
		source, err := filepath.Abs(req.WorkspaceRoot)
		if err != nil {
			return nil, err
		}
		if info, err := os.Stat(source); err != nil || !info.IsDir() {
			return nil, errors.New("the selected workspace does not exist on this machine")
		}
		prompt := strings.TrimSpace(req.Prompt)
		if prompt == "" || len(prompt) > 16000 {
			return nil, errors.New("enter a message of up to 16,000 characters")
		}
		req.Context = continuex.SafeContinuationContext(req.Context)
		if len(req.Context) > 12000 {
			return nil, errors.New("captured context is too long")
		}
		if len(req.Images) > 4 {
			return nil, errors.New("attach at most four images")
		}
		for _, path := range req.Images {
			if !continuex.AllowedImage(path) {
				return nil, errors.New("attach a local PNG, JPEG, or WebP image under 20 MB")
			}
		}
		var plan continuex.LaunchPlan
		// Seeded mode starts a fresh CLI with transcript context; native resume
		// continues a local harness session already known to this app.
		useSeeded := req.Resume == "seeded" || strings.TrimSpace(req.NativeID) == "" || len(req.Images) > 0
		if !useSeeded {
			req.Context = ""
		}
		if useSeeded {
			plan, err = continuex.SeededPlanTrusted(req.Agent, prompt, req.Context, req.Images, req.TrustWorkspace)
		} else {
			plan, err = continuex.Plan(req.Agent, "here", req.NativeID, prompt)
		}
		if err != nil {
			return nil, err
		}
		plan, err = continuex.WithModel(plan, req.Model)
		if err != nil {
			return nil, err
		}
		if plan.Native && (plan.Agent == "cursor" || plan.Agent == "agent") && req.TrustWorkspace {
			plan.Argv = append(plan.Argv[:len(plan.Argv)-1], "--trust", plan.Argv[len(plan.Argv)-1])
		}
		if deps.ContinueExecutor == nil {
			return nil, errors.New("in-app chat is unavailable in this build")
		}
		var answer, localNativeID string
		var changedFiles []string
		var fileDiffs []continuex.FileDiff
		var activities []continuex.ToolActivity
		var activityMu sync.Mutex
		if streamer, ok := deps.ContinueExecutor.(continuex.StreamingTurnExecutor); ok {
			output, turnErr := streamer.TurnStreaming(ctx, plan, source, func(update continuex.StreamProgress) {
				if update.Kind == "activity" {
					var activity continuex.ToolActivity
					if json.Unmarshal([]byte(update.Text), &activity) == nil && activity.ID != "" {
						activityMu.Lock()
						found := false
						for i := range activities {
							if activities[i].ID == activity.ID {
								activities[i] = activity
								found = true
								break
							}
						}
						if !found && len(activities) < 100 {
							activities = append(activities, activity)
						}
						activityMu.Unlock()
					}
				}
				if deps.ContinueProgress != nil {
					deps.ContinueProgress(req.RequestID, update)
				}
			})
			answer, localNativeID, changedFiles, err = output.Text, output.NativeID, output.ChangedFiles, turnErr
			fileDiffs = output.FileDiffs
		} else if detailed, ok := deps.ContinueExecutor.(continuex.DetailedTurnExecutor); ok {
			output, turnErr := detailed.TurnDetailed(ctx, plan, source)
			answer, localNativeID, changedFiles, err = output.Text, output.NativeID, output.ChangedFiles, turnErr
			fileDiffs = output.FileDiffs
		} else {
			answer, err = deps.ContinueExecutor.Turn(ctx, plan, source)
		}
		if err != nil {
			return nil, err
		}
		// Nexus saves this exchange itself (recordExchange), so capture must
		// not import a freshly started native session as a second chat. A
		// resumed session that kept its id was captured before; keep it.
		if localNativeID != "" && localNativeID != strings.TrimSpace(req.NativeID) {
			owned.Mark(localNativeID)
		}
		if localNativeID == "" && plan.Native {
			localNativeID = req.NativeID
		}
		return recordExchange(ctx, deps, req.SessionID, req.ProjectID, plan.Agent, localNativeID, prompt, answer, changedFiles, fileDiffs, activities, !plan.Native), nil
	case "agents.setup":
		// Local: what each agent is configured with (no secrets returned).
		var body struct {
			Workspace string `json:"workspace"`
		}
		if err := decode(args, &body); err != nil {
			return nil, err
		}
		return agentsetup.Scan(agentsetup.ScanOptions{Workspace: strings.TrimSpace(body.Workspace)}), nil
	case "agents.setup.share":
		var body struct {
			Item      agentsetup.Item `json:"item"`
			From      string          `json:"from"`
			To        []string        `json:"to"`
			Scope     string          `json:"scope"`
			Workspace string          `json:"workspace"`
			DryRun    bool            `json:"dry_run"`
		}
		if err := decode(args, &body); err != nil {
			return nil, err
		}
		if body.From == "" || len(body.To) == 0 || body.Item.Name == "" {
			return nil, errors.New("choose an item and at least one agent to copy it to")
		}
		return agentsetup.Share(agentsetup.ShareOptions{
			Workspace: strings.TrimSpace(body.Workspace), Item: body.Item, From: body.From,
			To: body.To, Scope: body.Scope, DryRun: body.DryRun,
		})
	case "continue.record":
		// Live sessions (cmd/nexuscore live.*) record each finished turn the
		// same way continue.here does, so the session view and cloud match.
		var body struct {
			SessionID   string                   `json:"session_id"`
			ProjectID   string                   `json:"project_id"`
			Agent       string                   `json:"agent"`
			NativeID    string                   `json:"native_id"`
			Prompt      string                   `json:"prompt"`
			Answer      string                   `json:"answer"`
			FileChanges []string                 `json:"file_changes"`
			FileDiffs   []continuex.FileDiff     `json:"file_diffs"`
			Activities  []continuex.ToolActivity `json:"activities"`
			Usage       []usage.Entry            `json:"usage"`
		}
		if err := decode(args, &body); err != nil {
			return nil, err
		}
		if strings.TrimSpace(body.Prompt) == "" && strings.TrimSpace(body.Answer) == "" {
			return nil, errors.New("nothing to record")
		}
		return recordExchangeUsage(ctx, deps, body.SessionID, body.ProjectID, body.Agent, body.NativeID, body.Prompt, body.Answer, body.FileChanges, body.FileDiffs, body.Activities, false, body.Usage), nil
	case "continue.local.list":
		id := argString(args, "session_id")
		return loadContinuations(deps.ContinuationDir, id)
	case "runs.message":
		var body struct {
			OpID    string `json:"op_id"`
			Message string `json:"message"`
			Text    string `json:"text"`
		}
		if err := decode(args, &body); err != nil {
			return nil, err
		}
		msg := body.Message
		if strings.TrimSpace(msg) == "" {
			msg = body.Text
		}
		return runMessage(body.OpID, msg)
	case "runs.approve":
		var body struct {
			OpID string `json:"op_id"`
		}
		if err := decode(args, &body); err != nil {
			return nil, err
		}
		return runApprove(body.OpID)
	case "runs.cancel":
		var body struct {
			OpID string `json:"op_id"`
		}
		if err := decode(args, &body); err != nil {
			return nil, err
		}
		return runCancel(body.OpID)
	case "teleport.redaction_preview":
		var body struct {
			Text     string `json:"text"`
			Path     string `json:"path"`
			FromOS   string `json:"from_os"`
			ToOS     string `json:"to_os"`
			FromHome string `json:"from_home"`
			ToHome   string `json:"to_home"`
			Home     string `json:"home"`
		}
		if err := decode(args, &body); err != nil {
			return nil, err
		}
		return map[string]any{
			"preview": teleport.RedactPreview(body.Text, body.Home),
			"path":    teleport.Remap(body.Path, body.FromOS, body.ToOS, body.FromHome, body.ToHome),
		}, nil
	case "teleport.prepare":
		var body teleport.PrepareArgs
		if err := decode(args, &body); err != nil {
			return nil, err
		}
		if strings.TrimSpace(body.Root) == "" {
			body.Root = strings.TrimSpace(deps.Root)
		}
		plan := teleport.BuildPreparePlan(body)
		return plan, nil
	case "teleport.send":
		var sendOptions struct {
			SessionID   string `json:"session_id"`
			ToUserID    string `json:"to_user_id"`
			IncludeCode bool   `json:"include_code"`
		}
		if err := json.Unmarshal(args, &sendOptions); err != nil {
			return nil, err
		}
		if sendOptions.IncludeCode {
			return teleportSendWithCode(ctx, deps, args)
		}
		if deps.Cloud == nil {
			return nil, ErrOffline
		}
		var out any
		if err := cloudDecode(ctx, deps.Cloud, http.MethodPost, "/v1/teleports", map[string]string{"session_id": sendOptions.SessionID, "to_user_id": sendOptions.ToUserID}, &out); err != nil {
			return nil, err
		}
		return out, nil
	case "teleport.restore_code":
		return teleportRestoreCode(ctx, deps, args)
	case "teleport.apply":
		// The desktop's inbox action carries only a teleport id: acknowledge it
		// on the cloud before opening a local restore plan.  Keep the existing
		// file/destination form local for the CLI restore workflow.
		if deps.Cloud != nil && argString(args, "id", "teleport_id") != "" && !hasJSONKey(args, "files") && !hasJSONKey(args, "dest") {
			id := argString(args, "id", "teleport_id")
			raw, err := deps.Cloud.Do(ctx, http.MethodPost, "/v1/teleports/"+url.PathEscape(id)+"/accept", args)
			if err != nil {
				return nil, err
			}
			var out any
			if len(raw) > 0 && json.Unmarshal(raw, &out) == nil {
				return out, nil
			}
			return map[string]any{"id": id, "status": "accepted"}, nil
		}
		return teleportApply(deps, args)
	case "files.read":
		var body struct {
			Path string `json:"path"`
		}
		if err := decode(args, &body); err != nil {
			return nil, err
		}
		return readWorkspace(deps.Root, body.Path)
	case "secrets.restore":
		return restoreSecret(deps, args)
	case "sessions.restore_ide_history":
		return restoreIDEHistory(args)
	case "sessions.usage":
		return sessionUsage(ctx, deps, args)
	case "status.get", "diagnostics.get":
		status := map[string]any{"ok": true, "online": deps.Online, "pending_uploads": pendingCount(deps), "workspace_root": deps.Root}
		if deps.Capture != nil {
			status["capture"] = deps.Capture.Status()
		}
		return status, nil
	case "auth.login":
		var request struct {
			AppURL    string `json:"app_url"`
			ServerURL string `json:"server_url"`
		}
		if err := decode(args, &request); err != nil {
			return nil, err
		}
		pending, err := authbrowser.BeginLogin(request.AppURL, request.ServerURL, time.Now())
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"url":        pending.AuthURL,
			"redirect":   pending.Redirect,
			"expires_at": pending.ExpiresAt.Format(time.RFC3339),
		}, nil
	case "auth.callback":
		rawURL := argString(args, "url", "callback_url", "deep_link")
		if rawURL == "" {
			return nil, errors.New("auth.callback requires url")
		}
		result, completed, err := authbrowser.CompleteLogin(rawURL, time.Now())
		if err != nil {
			return nil, err
		}
		if err := config.SaveFile(config.File{
			ServerURL: completed.ServerURL,
			AppURL:    completed.AppURL,
			Token:     result.Token,
			UserID:    result.UserID,
			Username:  result.Username,
		}); err != nil {
			return nil, err
		}
		return authStatus(), nil
	case "auth.logout":
		if err := config.ClearCredentials(); err != nil {
			return nil, err
		}
		return authStatus(), nil
	case "auth.status":
		return authStatus(), nil
	case "capture.pause":
		if deps.Capture == nil {
			return nil, errors.New("capture control is unavailable in this host")
		}
		if err := deps.Capture.Pause(); err != nil {
			return nil, err
		}
		return deps.Capture.Status(), nil
	case "capture.resume":
		if deps.Capture == nil {
			return nil, errors.New("capture control is unavailable in this host")
		}
		if err := deps.Capture.Resume(); err != nil {
			return nil, err
		}
		return deps.Capture.Status(), nil
	case "capture.configure":
		controller, ok := deps.Capture.(CaptureWorkspaceController)
		if !ok {
			return nil, errors.New("workspace configuration is unavailable in this host")
		}
		var request struct {
			Workspaces []string `json:"workspaces"`
			Confirm    bool     `json:"confirm"`
		}
		if err := decode(args, &request); err != nil {
			return nil, err
		}
		if !request.Confirm {
			return nil, errors.New("review and confirm the monitoring scope first")
		}
		return controller.ConfigureWorkspaces(request.Workspaces)
	case "capture.walk":
		return captureWalk(deps, args)
	case "capture.restore":
		return captureRestore(deps, args)
	case "agents.configure_mcp":
		var body struct {
			Agent        string   `json:"agent"`
			Path         string   `json:"path"`
			Home         string   `json:"home"`
			ServerURL    string   `json:"server_url"`
			ProjectID    string   `json:"project_id"`
			Transport    string   `json:"transport"`
			ProxyCommand string   `json:"proxy_command"`
			ProxyArgs    []string `json:"proxy_args"`
			AppDir       string   `json:"app_dir"`
		}
		if err := decode(args, &body); err != nil {
			return nil, err
		}
		return mcpconfig.Configure(mcpconfig.Options{
			AppDir:       body.AppDir,
			Agent:        body.Agent,
			Path:         body.Path,
			Home:         body.Home,
			ServerURL:    body.ServerURL,
			ProjectID:    body.ProjectID,
			Transport:    body.Transport,
			ProxyCommand: body.ProxyCommand,
			ProxyArgs:    body.ProxyArgs,
		})
	case "agents.list":
		return capturedAgents(ctx, args, deps)
	case "events.subscribe":
		// Live updates are GET /v1/timeline/stream, which a one-shot nx_call
		// cannot hold open. This used to fetch a whole timeline page on every
		// launch and throw it away.
		return map[string]any{"subscribed": false}, nil
	default:
		if !known(method) {
			return nil, ErrUnknownMethod
		}
		if deps.Cloud == nil {
			return nil, ErrOffline
		}
		httpMethod, path, body, err := cloudRequest(method, args)
		if err != nil {
			if errors.Is(err, errEmptyLocal) {
				return map[string]any{"items": []any{}}, nil
			}
			return nil, err
		}
		raw, err := deps.Cloud.Do(ctx, httpMethod, path, body)
		if err != nil {
			return nil, err
		}
		if len(raw) == 0 {
			return map[string]any{"ok": true}, nil
		}
		// Numbers pass through untouched: turn indexes are int64 values
		// beyond float64 precision (179533922286017891 would come back as
		// …900), and saved Nexus replies find their turn by that index.
		var out any
		if err := decodeExact(raw, &out); err != nil {
			return map[string]any{"raw": string(raw)}, nil
		}
		if method == "timeline.list" {
			out = hideNexusDrivenCopies(out, deps.ContinuationDir)
		}
		return out, nil
	}
}

func pendingCount(deps Deps) int {
	if deps.Outbox == nil {
		return 0
	}
	return len(deps.Outbox.Pending())
}

func authStatus() map[string]any {
	cfg, err := config.LoadFile()
	status := map[string]any{
		"signed_in": false,
		"redirect":  authbrowser.AppCallback,
	}
	if err != nil {
		status["error"] = err.Error()
		return status
	}
	status["signed_in"] = strings.TrimSpace(cfg.Token) != ""
	status["user_id"] = strings.TrimSpace(cfg.UserID)
	status["username"] = strings.TrimSpace(cfg.Username)
	status["server_url"] = strings.TrimSpace(cfg.ServerURL)
	status["app_url"] = strings.TrimSpace(cfg.AppURL)
	return status
}

func known(method string) bool {
	for _, m := range Methods() {
		if m == method {
			return true
		}
	}
	return false
}

// cloudRequest maps the native IDL to the actual HTTP verbs and resource
// routes.  The old generic POST mapper was the reason the desktop produced
// 400/404 errors for Memory, Timeline, and Teleport despite a healthy API.
func cloudRequest(method string, args json.RawMessage) (httpMethod, path string, body []byte, err error) {
	id := argString(args, "session_id", "id", "teleport_id")
	switch method {
	case "auth.tokens.list":
		return http.MethodGet, "/auth/tokens", nil, nil
	case "auth.tokens.revoke":
		tokenID := argString(args, "token_id", "id")
		if tokenID == "" {
			return "", "", nil, errors.New("token id is required")
		}
		return http.MethodDelete, "/auth/tokens/" + url.PathEscape(tokenID), nil, nil
	case "orgs.list":
		return http.MethodGet, "/orgs", nil, nil
	case "orgs.create":
		if argString(args, "name") == "" {
			return "", "", nil, errors.New("organization name is required")
		}
		body, _ := json.Marshal(map[string]string{"name": argString(args, "name"), "slug": argString(args, "slug")})
		return http.MethodPost, "/orgs", body, nil
	case "orgs.invite_accept":
		if argString(args, "token") == "" {
			return "", "", nil, errors.New("invitation code is required")
		}
		body, _ := json.Marshal(map[string]string{"token": argString(args, "token")})
		return http.MethodPost, "/orgs/invites/accept", body, nil
	case "orgs.get", "orgs.member_role", "orgs.member_remove", "orgs.invite_create", "orgs.invites", "orgs.invite_revoke":
		orgID := argString(args, "org_id", "id")
		if orgID == "" {
			return "", "", nil, errors.New("organization id is required")
		}
		path := "/orgs/" + url.PathEscape(orgID)
		switch method {
		case "orgs.get":
			return http.MethodGet, path, nil, nil
		case "orgs.invites":
			return http.MethodGet, path + "/invites", nil, nil
		case "orgs.invite_revoke":
			inviteID := argString(args, "invite_id")
			if inviteID == "" {
				return "", "", nil, errors.New("invitation id is required")
			}
			return http.MethodDelete, path + "/invites/" + url.PathEscape(inviteID), nil, nil
		case "orgs.invite_create":
			if argString(args, "email") == "" {
				return "", "", nil, errors.New("invitation email is required")
			}
			body, _ := json.Marshal(map[string]string{"email": argString(args, "email"), "role": argDefault(args, "role", "MEMBER")})
			return http.MethodPost, path + "/invites", body, nil
		default:
			userID := argString(args, "user_id")
			if userID == "" {
				return "", "", nil, errors.New("member id is required")
			}
			path += "/members/" + url.PathEscape(userID)
			if method == "orgs.member_remove" {
				return http.MethodDelete, path, nil, nil
			}
			if argString(args, "role") == "" {
				return "", "", nil, errors.New("member role is required")
			}
			body, _ := json.Marshal(map[string]string{"role": argString(args, "role")})
			return http.MethodPut, path + "/role", body, nil
		}
	case "timeline.list":
		return http.MethodGet, "/v1/timeline" + queryArgs(args, map[string]string{
			"project_id": "project_id", "project": "project_id", "agent": "harness",
			"harness": "harness", "q": "q", "limit": "limit", "cursor": "cursor",
		}), nil, nil
	case "memory.search":
		if hasJSONKey(args, "browse") {
			projectID := argString(args, "project_id", "project")
			if projectID == "" {
				return "", "", nil, errors.New("select a project before browsing memory")
			}
			query := url.Values{}
			query.Set("project_id", projectID)
			query.Set("limit", argDefault(args, "limit", "50"))
			query.Set("offset", argDefault(args, "offset", "0"))
			if term := argString(args, "query", "q"); term != "" {
				query.Set("q", term)
			}
			if level := argString(args, "level", "scope"); level != "" {
				query.Set("level", level)
			}
			// GET /memory/search filters by tag (comma-separated, all must match).
			if tags := argString(args, "tags", "tag"); tags != "" {
				query.Set("tags", tags)
			}
			return http.MethodGet, "/memory/search?" + query.Encode(), nil, nil
		}
		// The remote agent search endpoint intentionally requires a non-empty
		// query.  A Memory page opening without one should show the selected
		// project's retained knowledge through the documented, versioned list
		// route instead of manufacturing the old 404 / 400 combination.
		if query := argString(args, "query", "q"); query == "" {
			projectID := argString(args, "project_id", "project")
			if projectID == "" {
				return "", "", nil, errors.New("select a project or enter a memory search")
			}
			limit := argString(args, "limit")
			if limit == "" {
				limit = "50"
			}
			return http.MethodGet, "/v1/projects/" + url.PathEscape(projectID) + "/knowledge?limit=" + url.QueryEscape(limit), nil, nil
		}
		return http.MethodPost, "/v1/agent/memory/search", normalizeMemorySearchBody(args), nil
	case "memory.forget":
		// Memory ids must not be shadowed by a session_id in the same args.
		id = argString(args, "memory_id", "id")
		if id == "" {
			return "", "", nil, errors.New("memory id is required")
		}
		return http.MethodPost, "/memory/" + url.PathEscape(id) + "/forget", []byte(`{}`), nil
	case "memory.pin", "memory.scope":
		// Memory ids must not be shadowed by a session_id in the same args.
		id = argString(args, "memory_id", "id")
		if id == "" {
			return "", "", nil, errors.New("memory id is required")
		}
		action := strings.TrimPrefix(method, "memory.")
		if action == "pin" {
			return http.MethodPost, "/memory/" + url.PathEscape(id) + "/pin", []byte(`{}`), nil
		}
		level := argString(args, "level")
		if level == "" {
			return "", "", nil, errors.New("memory level is required")
		}
		payload, _ := json.Marshal(map[string]string{"level": level})
		return http.MethodPost, "/memory/" + url.PathEscape(id) + "/scope", payload, nil
	case "memory.update":
		id = argString(args, "memory_id", "id")
		if id == "" {
			return "", "", nil, errors.New("memory id is required")
		}
		// PUT /memory/{id} decodes strictly: the id lives in the path, and
		// any extra key (id, memory_id) is a 400.
		patch := map[string]json.RawMessage{}
		_ = json.Unmarshal(args, &patch)
		for _, key := range []string{"id", "memory_id", "session_id", "teleport_id"} {
			delete(patch, key)
		}
		body, _ := json.Marshal(patch)
		return http.MethodPut, "/memory/" + url.PathEscape(id), body, nil
	case "memory.history", "memory.revert", "memory.confirm", "memory.reject":
		id = argString(args, "memory_id", "id")
		if id == "" {
			return "", "", nil, errors.New("memory id is required")
		}
		path := "/memory/" + url.PathEscape(id) + "/" + strings.TrimPrefix(method, "memory.")
		switch method {
		case "memory.history":
			return http.MethodGet, path, nil, nil
		case "memory.revert":
			// POST /memory/{id}/revert decodes {"version":n} strictly.
			version, convErr := strconv.Atoi(argDefault(args, "version", ""))
			if convErr != nil || version < 1 {
				return "", "", nil, errors.New("memory version is required")
			}
			body, _ := json.Marshal(map[string]int{"version": version})
			return http.MethodPost, path, body, nil
		default:
			// confirmed_by / rejected_by default to the caller server-side.
			return http.MethodPost, path, []byte(`{}`), nil
		}
	case "projects.graph":
		// The memory graph: memories, the chats they came from, the files
		// they touch. limit caps the nodes (server clamps to 300).
		projectID := argString(args, "project_id")
		if projectID == "" {
			return "", "", nil, errors.New("project id is required")
		}
		return http.MethodGet, "/v1/projects/" + url.PathEscape(projectID) + "/graph" +
			queryArgs(args, map[string]string{"limit": "limit"}), nil, nil
	case "projects.brief", "projects.brief_refresh":
		projectID := argString(args, "project_id")
		if projectID == "" {
			return "", "", nil, errors.New("project id is required")
		}
		path := "/v1/projects/" + url.PathEscape(projectID) + "/brief"
		if method == "projects.brief" {
			return http.MethodGet, path, nil, nil
		}
		return http.MethodPost, path + "/refresh", nil, nil
	case "teleport.inbox":
		return http.MethodGet, "/v1/teleports/inbox", nil, nil
	case "teleport.sent":
		return http.MethodGet, "/v1/teleports/sent", nil, nil
	case "teleport.send":
		return http.MethodPost, "/v1/teleports", args, nil
	case "teleport.revoke":
		if id == "" {
			return "", "", nil, errors.New("teleport id is required")
		}
		return http.MethodPost, "/v1/teleports/" + url.PathEscape(id) + "/revoke", args, nil
	case "sessions.get":
		if id == "" {
			return "", "", nil, errors.New("session id is required")
		}
		return http.MethodGet, "/v1/agent-sessions/" + url.PathEscape(id), nil, nil
	case "sessions.create":
		if argString(args, "project_id") == "" || argString(args, "harness") == "" || argString(args, "native_id") == "" {
			return "", "", nil, errors.New("project, harness, and native id are required to create a chat")
		}
		return http.MethodPost, "/v1/agent-sessions", args, nil
	case "sessions.turns":
		if id == "" {
			return "", "", nil, errors.New("session id is required")
		}
		path := "/v1/agent-sessions/" + url.PathEscape(id)
		if !hasJSONKey(args, "limit") && !hasJSONKey(args, "cursor") {
			return http.MethodGet, path + "/turns", nil, nil
		}
		// direction=backward returns the newest turns (a "recent" preview).
		return http.MethodGet, path + "/fetch" + queryArgs(args, map[string]string{
			"limit": "limit", "cursor": "cursor", "direction": "direction",
		}), nil, nil
	case "sessions.summary":
		if id == "" {
			return "", "", nil, errors.New("session id is required")
		}
		return http.MethodGet, "/v1/agent-sessions/" + url.PathEscape(id) + "/summary", nil, nil
	case "sessions.versions":
		if id == "" {
			return "", "", nil, errors.New("session id is required")
		}
		return http.MethodGet, "/v1/agent-sessions/" + url.PathEscape(id) + "/versions", nil, nil
	case "sessions.files", "sessions.operations", "sessions.forks", "sessions.memories":
		if id == "" {
			return "", "", nil, errors.New("session id is required")
		}
		suffix := strings.TrimPrefix(method, "sessions.")
		return http.MethodGet, "/v1/agent-sessions/" + url.PathEscape(id) + "/" + suffix, nil, nil
	case "sessions.fork":
		if id == "" {
			return "", "", nil, errors.New("session id is required")
		}
		return http.MethodPost, "/v1/agent-sessions/" + url.PathEscape(id) + "/fork", []byte("{}"), nil
	case "sessions.shared_with":
		if id == "" {
			return "", "", nil, errors.New("session id is required")
		}
		return http.MethodGet, "/v1/agent-sessions/" + url.PathEscape(id) + "/grants", nil, nil
	case "sessions.delete":
		// Owner only on the server; removes the chat, its turns, usage and
		// shares. Memories made from it stay.
		if id == "" {
			return "", "", nil, errors.New("session id is required")
		}
		return http.MethodDelete, "/v1/agent-sessions/" + url.PathEscape(id), nil, nil
	case "sessions.unshare":
		// {user_id}: that person; {team:true}: the project team; neither:
		// everyone. (It used to fall through to the create-session route.)
		if id == "" {
			return "", "", nil, errors.New("session id is required")
		}
		path := "/v1/agent-sessions/" + url.PathEscape(id) + "/grants"
		if userID := argString(args, "user_id"); userID != "" {
			return http.MethodDelete, path + "/" + url.PathEscape(userID), nil, nil
		}
		if argBool(args, "team") {
			return http.MethodDelete, path + "/team", nil, nil
		}
		return http.MethodDelete, path, nil, nil
	case "sessions.share", "sessions.grants":
		if id == "" {
			return "", "", nil, errors.New("session id is required")
		}
		if method == "sessions.share" {
			if argString(args, "organization_id") != "" {
				if !argBool(args, "confirm") {
					return "", "", nil, errors.New("organization share requires confirmation")
				}
				body, _ := json.Marshal(map[string]any{"organization_id": argString(args, "organization_id"), "confirm": true})
				return http.MethodPost, "/v1/agent-sessions/" + url.PathEscape(id) + "/grants/organization", body, nil
			}
			if argBool(args, "team") {
				if !argBool(args, "confirm") {
					return "", "", nil, errors.New("project share requires confirmation")
				}
				body, _ := json.Marshal(map[string]any{"confirm": true, "live": true})
				return http.MethodPut, "/v1/agent-sessions/" + url.PathEscape(id) + "/grants/team", body, nil
			}
			userID := argString(args, "user_id")
			if userID == "" {
				return "", "", nil, errors.New("choose a person, project team, or organization")
			}
			body, _ := json.Marshal(map[string]any{"live": argBoolDefault(args, "live", true)})
			return http.MethodPut, "/v1/agent-sessions/" + url.PathEscape(id) + "/grants/" + url.PathEscape(userID), body, nil
		}
		userID := argString(args, "user_id")
		if userID == "" {
			return "", "", nil, errors.New("recipient user id is required")
		}
		grantBody, _ := json.Marshal(map[string]string{"user_id": userID})
		return http.MethodPost, "/v1/agent-sessions/" + url.PathEscape(id) + "/grants", grantBody, nil
	case "projects.list":
		return http.MethodGet, "/v1/agent/projects", nil, nil
	case "projects.capture_get", "projects.capture_set":
		projectID := argString(args, "project_id")
		if projectID == "" {
			return "", "", nil, errors.New("project id is required")
		}
		path := "/projects/" + url.PathEscape(projectID) + "/capture"
		if method == "projects.capture_get" {
			return http.MethodGet, path, nil, nil
		}
		if !hasJSONKey(args, "enabled") {
			return "", "", nil, errors.New("capture enabled is required")
		}
		body, _ := json.Marshal(map[string]bool{"enabled": argBool(args, "enabled")})
		return http.MethodPut, path, body, nil
	case "sharing.recipients":
		projectID := argString(args, "project_id")
		if projectID == "" {
			return "", "", nil, errors.New("project id is required")
		}
		return http.MethodGet, "/v1/sharing/recipients?project_id=" + url.QueryEscape(projectID), nil, nil
	case "usage.summary":
		return http.MethodGet, usageSummaryPath(args), nil, nil
	case "dashboard.get":
		projectID := argString(args, "project_id")
		if projectID == "" {
			return "", "", nil, errors.New("project id is required")
		}
		return http.MethodGet, "/projects/" + url.PathEscape(projectID) + "/dashboard" + queryArgs(args, map[string]string{"utc_offset_minutes": "utc_offset_minutes"}), nil, nil
	default:
		// No server route exists for files.diff, secrets.list,
		// uploads.resolve_large_file, git.status, or settings.get.  The old
		// fallback POSTed to a guessed path (e.g. sessions.unshare hit the
		// create-session route); fail plainly instead of calling the wrong
		// endpoint.
		return "", "", nil, fmt.Errorf("%s is not available in this version of Nexus", method)
	}
}

var errEmptyLocal = errors.New("empty local result")

func argString(args json.RawMessage, keys ...string) string {
	var values map[string]any
	if len(args) == 0 || json.Unmarshal(args, &values) != nil {
		return ""
	}
	for _, key := range keys {
		if value, ok := values[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func argDefault(args json.RawMessage, key, fallback string) string {
	if value := argString(args, key); value != "" {
		return value
	}
	var values map[string]any
	if json.Unmarshal(args, &values) == nil {
		if number, ok := values[key].(float64); ok && number >= 0 {
			return strconv.FormatInt(int64(number), 10)
		}
	}
	return fallback
}

// decodeExact decodes a cloud reply keeping numbers as json.Number, so they
// re-encode exactly as the server sent them.
func decodeExact(raw []byte, out *any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	if decoder.More() {
		return errors.New("trailing data after JSON value")
	}
	return nil
}

func hasJSONKey(args json.RawMessage, key string) bool {
	var values map[string]json.RawMessage
	return len(args) > 0 && json.Unmarshal(args, &values) == nil && values[key] != nil
}

func argBool(args json.RawMessage, key string) bool {
	var values map[string]json.RawMessage
	if len(args) == 0 || json.Unmarshal(args, &values) != nil {
		return false
	}
	var value bool
	return json.Unmarshal(values[key], &value) == nil && value
}

func argBoolDefault(args json.RawMessage, key string, fallback bool) bool {
	var values map[string]json.RawMessage
	if len(args) == 0 || json.Unmarshal(args, &values) != nil || values[key] == nil {
		return fallback
	}
	var value bool
	if json.Unmarshal(values[key], &value) != nil {
		return fallback
	}
	return value
}

func queryArgs(args json.RawMessage, mapping map[string]string) string {
	var values map[string]any
	if len(args) == 0 || json.Unmarshal(args, &values) != nil {
		return ""
	}
	query := make([]string, 0, len(mapping))
	seen := map[string]bool{}
	for from, to := range mapping {
		value, ok := values[from]
		if !ok || seen[to] {
			continue
		}
		text := strings.TrimSpace(fmt.Sprint(value))
		if text == "" || text == "<nil>" {
			continue
		}
		seen[to] = true
		query = append(query, to+"="+url.QueryEscape(text))
	}
	if len(query) == 0 {
		return ""
	}
	return "?" + strings.Join(query, "&")
}

func normalizeMemorySearchBody(args json.RawMessage) []byte {
	values := map[string]any{}
	if len(args) > 0 {
		_ = json.Unmarshal(args, &values)
	}
	if _, ok := values["query"]; !ok {
		if q, ok := values["q"].(string); ok {
			values["query"] = q
		}
	}
	if _, ok := values["project_id"]; !ok {
		if project, ok := values["project"].(string); ok && strings.TrimSpace(project) != "" {
			values["project_id"] = project
		}
	}
	if _, ok := values["level"]; !ok {
		if level, ok := values["scope"].(string); ok && strings.TrimSpace(level) != "" {
			values["level"] = level
		}
	}
	// The agent endpoint uses a strict decoder.  Never send desktop-only
	// aliases with the canonical fields or it returns the observed 400.
	delete(values, "q")
	delete(values, "project")
	delete(values, "scope")
	raw, _ := json.Marshal(values)
	return raw
}

func capturedAgents(ctx context.Context, args json.RawMessage, deps Deps) (any, error) {
	if deps.Cloud == nil {
		return map[string]any{"items": []any{}}, nil
	}
	byHarness := map[string]*capturedAgentSummary{}
	cursor := ""
	projectID := argString(args, "project_id", "project")
	for page := 0; page < 100; page++ {
		path := "/v1/timeline?limit=100"
		if projectID != "" {
			path += "&project_id=" + url.QueryEscape(projectID)
		}
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		raw, err := deps.Cloud.Do(ctx, http.MethodGet, path, nil)
		if err != nil {
			return nil, err
		}
		var payload struct {
			Items []struct {
				Harness      string `json:"harness"`
				ProjectID    string `json:"project_id"`
				VersionState string `json:"version_state"`
				LastActiveAt string `json:"last_active_at"`
			} `json:"items"`
			NextCursor string `json:"next_cursor"`
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			return nil, err
		}
		for _, item := range payload.Items {
			name := strings.TrimSpace(item.Harness)
			if name == "" {
				name = "Unknown harness"
			}
			key := strings.ToLower(name)
			entry := byHarness[key]
			if entry == nil {
				status := "Captured sessions"
				if item.VersionState == "legacy_snapshot" {
					status = "Archived snapshots"
				}
				entry = &capturedAgentSummary{Name: name, Harness: name, ProjectID: item.ProjectID, Status: status, ResumeMode: "Open in Timeline"}
				byHarness[key] = entry
			}
			entry.CapturedCount++
			entry.LastActivity = newerTimestamp(entry.LastActivity, item.LastActiveAt)
		}
		if payload.NextCursor == "" || payload.NextCursor == cursor {
			break
		}
		cursor = payload.NextCursor
	}
	items := make([]capturedAgentSummary, 0, len(byHarness))
	for _, item := range byHarness {
		items = append(items, *item)
	}
	for i := range items {
		for j := i + 1; j < len(items); j++ {
			if strings.ToLower(items[j].Name) < strings.ToLower(items[i].Name) {
				items[i], items[j] = items[j], items[i]
			}
		}
	}
	return map[string]any{"items": items}, nil
}

func decode(raw json.RawMessage, dst any) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, dst)
}

func readWorkspace(root, rel string) (any, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, errors.New("core: workspace root is required")
	}
	rel = filepath.Clean(strings.TrimSpace(rel))
	if rel == "." || rel == "" || strings.HasPrefix(rel, "..") {
		return nil, errors.New("core: path escapes the workspace")
	}
	full := filepath.Join(root, rel)
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	absFull, err := filepath.Abs(full)
	if err != nil {
		return nil, err
	}
	if absFull != absRoot && !strings.HasPrefix(absFull, absRoot+string(os.PathSeparator)) {
		return nil, errors.New("core: path escapes the workspace")
	}
	f, err := os.Open(absFull)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	body, err := io.ReadAll(io.LimitReader(f, 1<<20))
	if err != nil {
		return nil, err
	}
	return map[string]any{"path": rel, "content": string(body)}, nil
}

func captureWalk(deps Deps, args json.RawMessage) (any, error) {
	var body struct {
		Root string `json:"root"`
	}
	if err := decode(args, &body); err != nil {
		return nil, err
	}
	root := strings.TrimSpace(body.Root)
	if root == "" {
		root = strings.TrimSpace(deps.Root)
	}
	if root == "" {
		return nil, errors.New("capture.walk requires a workspace root")
	}
	store := blobs.NewByteaStore()
	man, err := capture.CaptureWalk(root, store)
	if err != nil {
		return nil, err
	}
	files := make([]map[string]any, 0, len(man.Files))
	for _, f := range man.Files {
		files = append(files, map[string]any{"path": f.Path, "sha256": f.SHA256, "size": f.Size})
	}
	rebuild := make([]map[string]any, 0, len(man.Rebuild))
	for _, r := range man.Rebuild {
		rebuild = append(rebuild, map[string]any{
			"rule": r.Rule, "dir": r.Dir, "lockfile": r.Lockfile, "command": r.Command,
		})
	}
	return map[string]any{
		"files":       files,
		"exclude":     man.Exclude,
		"rebuild":     rebuild,
		"refuse":      man.Refuse,
		"new_uploads": man.NewUploads,
	}, nil
}

func captureRestore(deps Deps, args json.RawMessage) (any, error) {
	var body struct {
		Root  string `json:"root"`
		Dest  string `json:"dest"`
		Files []struct {
			Path     string `json:"path"`
			SHA256   string `json:"sha256"`
			Size     int64  `json:"size"`
			BytesB64 string `json:"bytes_b64"`
		} `json:"files"`
		Rebuild []struct {
			Rule     string `json:"rule"`
			Dir      string `json:"dir"`
			Lockfile string `json:"lockfile"`
			Command  string `json:"command"`
		} `json:"rebuild"`
	}
	if err := decode(args, &body); err != nil {
		return nil, err
	}
	dest := strings.TrimSpace(body.Dest)
	if dest == "" {
		return nil, errors.New("capture.restore requires dest")
	}
	store := blobs.NewByteaStore()
	man := &capture.Manifest{}
	for _, f := range body.Files {
		if strings.TrimSpace(f.BytesB64) != "" {
			raw, err := base64.StdEncoding.DecodeString(f.BytesB64)
			if err != nil {
				return nil, errors.New("capture.restore: bad bytes_b64 for " + f.Path)
			}
			sum := f.SHA256
			if sum == "" {
				sum = blobs.HashBytes(raw)
			}
			if _, err := store.Put(sum, raw); err != nil {
				return nil, err
			}
			f.SHA256 = sum
		}
		man.Files = append(man.Files, capture.FileRecord{Path: f.Path, SHA256: f.SHA256, Size: f.Size})
	}
	for _, r := range body.Rebuild {
		man.Rebuild = append(man.Rebuild, capture.RebuildRecipe{
			Rule: r.Rule, Dir: r.Dir, Lockfile: r.Lockfile, Command: r.Command,
		})
	}
	rep, err := capture.RestoreTree(dest, man, store)
	if err != nil {
		return nil, err
	}
	rebuilds := make([]map[string]any, 0, len(rep.Rebuilds))
	for _, r := range rep.Rebuilds {
		rebuilds = append(rebuilds, map[string]any{
			"rule": r.Recipe.Rule, "ran": r.Ran, "command": r.Command, "note": r.Note, "error": r.Err,
		})
	}
	return map[string]any{"verified": rep.Verified, "rebuilds": rebuilds}, nil
}

func teleportApply(deps Deps, args json.RawMessage) (any, error) {
	var body struct {
		SessionID string `json:"session_id"`
		Dest      string `json:"dest"`
		Files     []struct {
			Path     string `json:"path"`
			SHA256   string `json:"sha256"`
			Size     int64  `json:"size"`
			BytesB64 string `json:"bytes_b64"`
		} `json:"files"`
	}
	if err := decode(args, &body); err != nil {
		return nil, err
	}
	dest := strings.TrimSpace(body.Dest)
	if dest != "" && len(body.Files) > 0 {
		store := blobs.NewByteaStore()
		man := &capture.Manifest{}
		for _, f := range body.Files {
			if strings.TrimSpace(f.BytesB64) != "" {
				raw, err := base64.StdEncoding.DecodeString(f.BytesB64)
				if err != nil {
					return nil, errors.New("teleport.apply: bad bytes_b64 for " + f.Path)
				}
				sum := f.SHA256
				if sum == "" {
					sum = blobs.HashBytes(raw)
				}
				if _, err := store.Put(sum, raw); err != nil {
					return nil, err
				}
				f.SHA256 = sum
			}
			man.Files = append(man.Files, capture.FileRecord{Path: f.Path, SHA256: f.SHA256, Size: f.Size})
		}
		rep, err := capture.RestoreTree(dest, man, store)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"applied":    true,
			"dest":       dest,
			"verified":   rep.Verified,
			"session_id": strings.TrimSpace(body.SessionID),
		}, nil
	}
	next := []string{
		"Run teleport.prepare to review the Restore checklist",
		"Confirm worktree or clean checkout target",
		"Provide files[] with bytes_b64 (or local blob cache) and dest to apply tree restore",
		"Fill secret name-only templates, then continue.start",
	}
	if deps.Root != "" {
		next = append([]string{"Workspace root is " + deps.Root}, next...)
	}
	return map[string]any{
		"applied":    false,
		"session_id": strings.TrimSpace(body.SessionID),
		"next_steps": next,
		"note":       "teleport.apply is a stub unless files and dest are provided; then it restores via capture.RestoreTree",
	}, nil
}

func restoreSecret(deps Deps, args json.RawMessage) (any, error) {
	var body struct {
		Path          string `json:"path"`
		CiphertextB64 string `json:"ciphertext_b64"`
		DataKeyB64    string `json:"data_key_b64"`
		BlobID        string `json:"blob_id"`
	}
	if err := decode(args, &body); err != nil {
		return nil, err
	}
	root := strings.TrimSpace(deps.Root)
	rel := filepath.Clean(strings.TrimSpace(body.Path))
	if root == "" || rel == "" || rel == "." {
		return nil, errors.New("secrets.restore requires a workspace root and path")
	}
	if strings.HasPrefix(rel, "..") {
		return nil, errors.New("core: path escapes the workspace")
	}
	full := filepath.Join(root, rel)
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	absFull, err := filepath.Abs(full)
	if err != nil {
		return nil, err
	}
	if absFull != absRoot && !strings.HasPrefix(absFull, absRoot+string(os.PathSeparator)) {
		return nil, errors.New("core: path escapes the workspace")
	}

	ctB64 := strings.TrimSpace(body.CiphertextB64)
	keyB64 := strings.TrimSpace(body.DataKeyB64)
	if ctB64 == "" || keyB64 == "" {
		if deps.Cloud == nil {
			return nil, errors.New("secrets.restore: waiting for cloud to return an unwrapped data key")
		}
		if strings.TrimSpace(body.BlobID) == "" {
			return nil, errors.New("secrets.restore: need ciphertext_b64 and data_key_b64, or a blob_id once cloud returns the wrapped key")
		}
		return nil, errors.New("secrets.restore: cloud returned no unwrapped data key yet; pass data_key_b64 after decrypt")
	}
	ct, err := base64.StdEncoding.DecodeString(ctB64)
	if err != nil {
		return nil, errors.New("secrets.restore: bad ciphertext_b64")
	}
	key, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil {
		return nil, errors.New("secrets.restore: bad data_key_b64")
	}
	if err := secrets.RestoreFile(absFull, ct, key); err != nil {
		return nil, err
	}
	return map[string]any{"path": rel, "restored": true}, nil
}

func restoreIDEHistory(args json.RawMessage) (any, error) {
	var body struct {
		Agent          string          `json:"agent"`
		BackupDir      string          `json:"backup_dir"`
		TargetDB       string          `json:"target_db"`
		SessionID      string          `json:"session_id"`
		Version        string          `json:"version"`
		Title          string          `json:"title"`
		Turns          json.RawMessage `json:"turns"`
		SessionPayload json.RawMessage `json:"session_payload"`
	}
	if err := decode(args, &body); err != nil {
		return nil, err
	}
	agent := strings.ToLower(strings.TrimSpace(body.Agent))
	payload := idehistory.SessionPayload{
		SessionID: strings.TrimSpace(body.SessionID),
		Agent:     agent,
		Version:   strings.TrimSpace(body.Version),
		Title:     strings.TrimSpace(body.Title),
		Turns:     body.Turns,
	}
	if len(body.SessionPayload) > 0 {
		var embedded idehistory.SessionPayload
		if err := json.Unmarshal(body.SessionPayload, &embedded); err != nil {
			return nil, err
		}
		if payload.SessionID == "" {
			payload.SessionID = embedded.SessionID
		}
		if payload.Version == "" {
			payload.Version = embedded.Version
		}
		if payload.Title == "" {
			payload.Title = embedded.Title
		}
		if len(payload.Turns) == 0 {
			payload.Turns = embedded.Turns
		}
		if len(embedded.Raw) > 0 {
			payload.Raw = embedded.Raw
		} else {
			payload.Raw = body.SessionPayload
		}
	}
	var (
		res *idehistory.Result
		err error
	)
	switch agent {
	case "cursor", "cursor-ide":
		res, err = idehistory.WriteCursorHistory(body.BackupDir, body.TargetDB, payload)
	case "antigravity", "antigravity-ide", "agy-ide":
		res, err = idehistory.WriteAntigravityHistory(body.BackupDir, body.TargetDB, payload)
	case "windsurf", "devin", "devin-desktop":
		res, err = idehistory.WriteWindsurfHistory(body.BackupDir, body.TargetDB, payload)
	case "":
		return nil, errors.New("sessions.restore_ide_history requires agent")
	default:
		return nil, errors.New("sessions.restore_ide_history: unsupported agent " + agent)
	}
	if err != nil {
		return nil, err
	}
	return res, nil
}
