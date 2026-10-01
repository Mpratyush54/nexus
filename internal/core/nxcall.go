// Package core is the in-process Nexus engine behind nx_call (spec 7.7).
// Data methods need a Cloud client. Capture, continue, cache, and file
// reads run locally. A nil Cloud returns ErrOffline for cloud methods.
package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"central-memory/internal/authbrowser"
	"central-memory/internal/cache"
	"central-memory/internal/continuex"
	"central-memory/internal/outbox"
	"central-memory/internal/teleport"
)

// ErrOffline is returned when a data method has no cloud client (D6, D7).
var ErrOffline = errors.New("not signed in or offline — open Settings to sign in")

// ErrUnknownMethod is an nx_call name this build does not implement.
var ErrUnknownMethod = errors.New("unknown nx_call method")

// Cloud is the thin /v1 client data methods wrap.
type Cloud interface {
	Do(ctx context.Context, method, path string, body []byte) ([]byte, error)
}

// Deps are the local subsystems nx_call can touch.
type Deps struct {
	Cloud  Cloud
	Cache  *cache.Cache
	Outbox *outbox.Spool
	Root   string
	Online bool
}

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
		"status.get",
		"diagnostics.get",
		"auth.callback",
		"auth.login",
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
		return map[string]any{"op_id": "op_" + req.SessionID, "command": argv}, nil
	case "teleport.redaction_preview", "teleport.prepare":
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
	case "files.read":
		var body struct {
			Path string `json:"path"`
		}
		if err := decode(args, &body); err != nil {
			return nil, err
		}
		return readWorkspace(deps.Root, body.Path)
	case "status.get", "diagnostics.get":
		return map[string]any{
			"ok":              true,
			"online":          deps.Online,
			"signed_in":       deps.Cloud != nil,
			"pending_uploads": pendingCount(deps),
		}, nil
	case "auth.status":
		signedIn := deps.Cloud != nil
		return map[string]any{"signed_in": signedIn, "online": deps.Online}, nil
	case "auth.login":
		res, err := authbrowser.Login(ctx, authbrowser.Options{})
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"signed_in": true,
			"user_id":   res.UserID,
			"username":  res.Username,
		}, nil
	case "auth.callback":
		return map[string]any{"redirect": "nexus://auth/callback"}, nil
	case "capture.pause", "capture.resume":
		return nil, errors.New(method + " is handled by the app shell; the core has no capture switch yet")
	case "agents.list":
		return capturedAgents(ctx, deps)
	case "agents.configure_mcp":
		return map[string]any{"ok": true, "note": "MCP proxy wiring is not enabled in this build"}, nil
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
			if method == "timeline.list" && strings.Contains(err.Error(), "404") {
				return map[string]any{
					"items": []any{},
					"note":  "Timeline API is not on this cloud build yet. Memory still works after sign-in.",
				}, nil
			}
			if (method == "teleport.inbox" || method == "teleport.sent") && strings.Contains(err.Error(), "404") {
				return map[string]any{"items": []any{}, "note": "Teleport API is not on this cloud build yet."}, nil
			}
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

// capturedAgents is intentionally derived from sessions that actually reached
// the account. A supported-harness catalogue would make the desktop claim
// that agents are connected when no capture has occurred.
func capturedAgents(ctx context.Context, deps Deps) (any, error) {
	if deps.Cloud == nil {
		return map[string]any{"items": []any{}}, nil
	}
	raw, err := deps.Cloud.Do(ctx, "GET", "/v1/timeline?limit=100", nil)
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
	for i := 0; i < len(items); i++ {
		for j := i + 1; j < len(items); j++ {
			if strings.ToLower(items[j].Name) < strings.ToLower(items[i].Name) {
				items[i], items[j] = items[j], items[i]
			}
		}
	}
	return map[string]any{"items": items}, nil
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

// cloudRequest maps nx_call methods to real /v1 (and legacy) HTTP routes.
func cloudRequest(method string, args json.RawMessage) (httpMethod, path string, body []byte, err error) {
	id := argString(args, "session_id", "id", "teleport_id")
	switch method {
	case "timeline.list", "events.subscribe":
		return "GET", "/v1/timeline" + queryArgs(args, map[string]string{
			"project_id": "project_id",
			"project":    "project_id",
			"agent":      "harness",
			"harness":    "harness",
			"q":          "q",
			"limit":      "limit",
			"cursor":     "cursor",
		}), nil, nil
	case "memory.search":
		// Agent API accepts optional project_id in the JSON body.
		return "POST", "/v1/agent/memory/search", normalizeMemorySearchBody(args), nil
	case "memory.forget":
		if id == "" {
			id = argString(args, "memory_id")
		}
		if id == "" {
			return "", "", nil, errors.New("memory id is required")
		}
		return "DELETE", "/memory/" + id, nil, nil
	case "memory.pin", "memory.scope", "memory.update":
		if id == "" {
			id = argString(args, "memory_id")
		}
		if id == "" {
			return "", "", nil, errors.New("memory id is required")
		}
		return "PUT", "/memory/" + id, args, nil
	case "teleport.inbox":
		return "GET", "/v1/teleports/inbox", nil, nil
	case "teleport.sent":
		return "GET", "/v1/teleports/sent", nil, nil
	case "teleport.send":
		return "POST", "/v1/teleports", args, nil
	case "teleport.revoke":
		if id == "" {
			return "", "", nil, errors.New("teleport id is required")
		}
		return "POST", "/v1/teleports/" + id + "/revoke", args, nil
	case "teleport.apply":
		if id == "" {
			return "", "", nil, errors.New("teleport id is required")
		}
		return "POST", "/v1/teleports/" + id + "/accept", args, nil
	case "sessions.get":
		if id == "" {
			return "", "", nil, errors.New("session id is required")
		}
		return "GET", "/v1/agent-sessions/" + id, nil, nil
	case "sessions.turns":
		if id == "" {
			return "", "", nil, errors.New("session id is required")
		}
		return "GET", "/v1/agent-sessions/" + id + "/turns", nil, nil
	case "sessions.summary":
		if id == "" {
			return "", "", nil, errors.New("session id is required")
		}
		return "GET", "/v1/agent-sessions/" + id + "/summary", nil, nil
	case "sessions.versions":
		if id == "" {
			return "", "", nil, errors.New("session id is required")
		}
		return "GET", "/v1/agent-sessions/" + id, nil, nil
	case "sessions.files", "sessions.operations", "sessions.memories":
		// Not exposed as dedicated list routes yet — return empty via a no-op local shape.
		return "", "", nil, errEmptyLocal
	case "sessions.share", "sessions.grants":
		if id == "" {
			return "", "", nil, errors.New("session id is required")
		}
		return "POST", "/v1/agent-sessions/" + id + "/grants", args, nil
	case "sessions.unshare":
		if id == "" {
			return "", "", nil, errors.New("session id is required")
		}
		user := argString(args, "user", "user_id")
		if user == "" {
			return "DELETE", "/v1/agent-sessions/" + id + "/grants/team", nil, nil
		}
		return "DELETE", "/v1/agent-sessions/" + id + "/grants/" + user, nil, nil
	case "projects.list":
		// The desktop needs the projects visible to the signed-in agent. The
		// legacy /projects route is a portal/admin shape and can leave the
		// desktop without a usable project context.
		return "GET", "/v1/agent/projects", nil, nil
	case "settings.get", "git.status", "files.diff", "uploads.resolve_large_file",
		"runs.message", "runs.approve", "runs.cancel", "secrets.list", "secrets.restore":
		return "", "", nil, fmt.Errorf("%s is not available in this desktop build yet", method)
	default:
		return "POST", cloudPath(method), args, nil
	}
}

var errEmptyLocal = errors.New("empty_local")

func argString(args json.RawMessage, keys ...string) string {
	if len(args) == 0 {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(args, &m) != nil {
		return ""
	}
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch t := v.(type) {
			case string:
				if s := strings.TrimSpace(t); s != "" {
					return s
				}
			}
		}
	}
	return ""
}

func queryArgs(args json.RawMessage, mapping map[string]string) string {
	if len(args) == 0 {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(args, &m) != nil {
		return ""
	}
	q := make([]string, 0, len(mapping))
	seen := map[string]bool{}
	for from, to := range mapping {
		raw, ok := m[from]
		if !ok {
			continue
		}
		s := strings.TrimSpace(fmt.Sprint(raw))
		if s == "" || s == "<nil>" {
			continue
		}
		if seen[to] {
			continue
		}
		seen[to] = true
		q = append(q, to+"="+url.QueryEscape(s))
	}
	if len(q) == 0 {
		return ""
	}
	return "?" + strings.Join(q, "&")
}

func normalizeMemorySearchBody(args json.RawMessage) []byte {
	var m map[string]any
	if len(args) == 0 || json.Unmarshal(args, &m) != nil {
		m = map[string]any{}
	}
	if _, ok := m["query"]; !ok {
		if q, ok := m["q"].(string); ok {
			m["query"] = q
		}
	}
	// The desktop accepts its concise UI names, but the agent endpoint uses a
	// strict decoder. Do not forward aliases alongside the canonical fields.
	delete(m, "q")
	if _, ok := m["project_id"]; !ok {
		if p, ok := m["project"].(string); ok && strings.TrimSpace(p) != "" {
			m["project_id"] = p
		}
	}
	delete(m, "project")
	if _, ok := m["level"]; !ok {
		if scope, ok := m["scope"].(string); ok && strings.TrimSpace(scope) != "" {
			m["level"] = scope
		}
	}
	delete(m, "scope")
	raw, _ := json.Marshal(m)
	return raw
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
