// Package core is the in-process Nexus engine behind nx_call (spec 7.7).
// Data methods need a Cloud client. Capture, continue, cache, and file
// reads run locally. A nil Cloud returns ErrOffline for cloud methods.
package core

import (
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
	"strings"

	"central-memory/internal/agents/mcpconfig"
	"central-memory/internal/blobs"
	"central-memory/internal/cache"
	"central-memory/internal/capture"
	"central-memory/internal/continuex"
	"central-memory/internal/idehistory"
	"central-memory/internal/outbox"
	"central-memory/internal/secrets"
	"central-memory/internal/teleport"
)

// ErrOffline is returned when a data method has no cloud client (D6, D7).
var ErrOffline = errors.New("Nexus is offline; memory unavailable")

// ErrUnknownMethod is an nx_call name this build does not implement.
var ErrUnknownMethod = errors.New("unknown nx_call method")

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

// Deps are the local subsystems nx_call can touch.
type Deps struct {
	Cloud          Cloud
	Cache          *cache.Cache
	Outbox         *outbox.Spool
	Root           string
	Online         bool
	Capture        CaptureController
	ContinueRunner continuex.Runner // optional; when set, continue.start records/starts via Runner
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
}

// Methods is the IDL. Bindings in bindings/ must list the same names.
func Methods() []string {
	return []string{
		"timeline.list",
		"events.subscribe",
		"sessions.get",
		"sessions.turns",
		"sessions.summary",
		"files.read",
		"files.diff",
		"continue.start",
		"memory.search",
		"memory.forget",
		"memory.pin",
		"memory.scope",
		"sessions.share",
		"uploads.status",
		"uploads.retry",
		"cache.clear",
		"net.status",
		"teleport.redaction_preview",
		"teleport.send",
		"teleport.inbox",
		"teleport.sent",
		"teleport.prepare",
		"teleport.apply",
		"teleport.revoke",
		"sessions.files",
		"sessions.operations",
		"sessions.memories",
		"sessions.versions",
		"sessions.unshare",
		"sessions.grants",
		"sessions.restore_ide_history",
		"agents.list",
		"agents.configure_mcp",
		"runs.message",
		"runs.approve",
		"runs.cancel",
		"memory.update",
		"secrets.list",
		"secrets.restore",
		"uploads.resolve_large_file",
		"projects.list",
		"git.status",
		"settings.get",
		"auth.status",
		"capture.pause",
		"capture.resume",
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
	case "continue.start":
		var req continuex.Request
		if err := decode(args, &req); err != nil {
			return nil, err
		}
		argv, err := continuex.Command(req)
		if err != nil {
			return nil, err
		}
		opID := "op_" + req.SessionID
		if deps.ContinueRunner != nil {
			plan := continuex.LaunchPlan{
				Agent: strings.TrimSpace(req.Agent),
				Mode:  strings.TrimSpace(req.Mode),
				Argv:  argv,
			}
			if id, err := deps.ContinueRunner.Start(ctx, plan); err != nil {
				return nil, err
			} else if strings.TrimSpace(id) != "" {
				opID = id
			}
		}
		trackRun(opID, argv)
		return map[string]any{"op_id": opID, "command": argv}, nil
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
	case "status.get", "diagnostics.get":
		return map[string]any{"ok": true, "online": deps.Online, "pending_uploads": pendingCount(deps)}, nil
	case "auth.callback":
		return map[string]any{"redirect": "nexus://auth/callback"}, nil
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
		}
		if err := decode(args, &body); err != nil {
			return nil, err
		}
		return mcpconfig.Configure(mcpconfig.Options{
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
		return capturedAgents(ctx, deps)
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
		var out any
		if err := json.Unmarshal(raw, &out); err != nil {
			return map[string]any{"raw": string(raw)}, nil
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

func known(method string) bool {
	for _, m := range Methods() {
		if m == method {
			return true
		}
	}
	return false
}

func cloudPath(method string) string {
	switch {
	case method == "timeline.list" || method == "events.subscribe":
		return "/v1/timeline"
	case strings.HasPrefix(method, "memory."):
		return "/v1/memory"
	case strings.HasPrefix(method, "sessions."):
		return "/v1/agent-sessions"
	case strings.HasPrefix(method, "teleport."):
		return "/v1/teleports"
	case strings.HasPrefix(method, "secrets."):
		return "/v1/secrets"
	case strings.HasPrefix(method, "files."):
		return "/v1/blobs"
	default:
		return "/v1/" + method
	}
}

// cloudRequest maps the native IDL to the actual HTTP verbs and resource
// routes.  The old generic POST mapper was the reason the desktop produced
// 400/404 errors for Memory, Timeline, and Teleport despite a healthy API.
func cloudRequest(method string, args json.RawMessage) (httpMethod, path string, body []byte, err error) {
	id := argString(args, "session_id", "id", "teleport_id")
	switch method {
	case "timeline.list", "events.subscribe":
		return http.MethodGet, "/v1/timeline" + queryArgs(args, map[string]string{
			"project_id": "project_id", "project": "project_id", "agent": "harness",
			"harness": "harness", "limit": "limit", "cursor": "cursor",
		}), nil, nil
	case "memory.search":
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
		if id == "" {
			id = argString(args, "memory_id")
		}
		if id == "" {
			return "", "", nil, errors.New("memory id is required")
		}
		return http.MethodDelete, "/memory/" + url.PathEscape(id), nil, nil
	case "memory.pin", "memory.scope", "memory.update":
		if id == "" {
			id = argString(args, "memory_id")
		}
		if id == "" {
			return "", "", nil, errors.New("memory id is required")
		}
		return http.MethodPut, "/memory/" + url.PathEscape(id), args, nil
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
	case "sessions.turns":
		if id == "" {
			return "", "", nil, errors.New("session id is required")
		}
		return http.MethodGet, "/v1/agent-sessions/" + url.PathEscape(id) + "/turns", nil, nil
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
	case "sessions.files", "sessions.operations", "sessions.memories":
		// These panels are intentionally empty until the cloud exposes the
		// corresponding, permission-checked list routes.  Do not fabricate
		// a POST to agent-sessions and surface it as a transport error.
		return "", "", nil, errEmptyLocal
	case "sessions.share", "sessions.grants":
		if id == "" {
			return "", "", nil, errors.New("session id is required")
		}
		return http.MethodPost, "/v1/agent-sessions/" + url.PathEscape(id) + "/grants", args, nil
	case "projects.list":
		return http.MethodGet, "/v1/agent/projects", nil, nil
	default:
		return http.MethodPost, cloudPath(method), args, nil
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

func hasJSONKey(args json.RawMessage, key string) bool {
	var values map[string]json.RawMessage
	return len(args) > 0 && json.Unmarshal(args, &values) == nil && values[key] != nil
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

func capturedAgents(ctx context.Context, deps Deps) (any, error) {
	if deps.Cloud == nil {
		return map[string]any{"items": []any{}}, nil
	}
	raw, err := deps.Cloud.Do(ctx, http.MethodGet, "/v1/timeline?limit=100", nil)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Items []struct {
			Harness      string `json:"harness"`
			ProjectID    string `json:"project_id"`
			VersionState string `json:"version_state"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	byHarness := map[string]*capturedAgentSummary{}
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
