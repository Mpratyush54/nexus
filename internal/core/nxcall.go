// Package core is the in-process Nexus engine behind nx_call (spec 7.7).
// Data methods need a Cloud client. Capture, continue, cache, and file
// reads run locally. A nil Cloud returns ErrOffline for cloud methods.
package core

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"central-memory/internal/cache"
	"central-memory/internal/continuex"
	"central-memory/internal/outbox"
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

// Deps are the local subsystems nx_call can touch.
type Deps struct {
	Cloud  Cloud
	Cache  *cache.Cache
	Outbox *outbox.Spool
	Root   string
	Online bool
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
		"teleport.prepare",
		"status.get",
		"diagnostics.get",
		"auth.callback",
	}
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
		return map[string]any{"ok": true, "online": deps.Online, "pending_uploads": pendingCount(deps)}, nil
	case "auth.callback":
		return map[string]any{"redirect": "nexus://auth/callback"}, nil
	default:
		if !known(method) {
			return nil, ErrUnknownMethod
		}
		if deps.Cloud == nil {
			return nil, ErrOffline
		}
		path := cloudPath(method)
		raw, err := deps.Cloud.Do(ctx, "POST", path, args)
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
	switch method {
	case "timeline.list", "events.subscribe":
		return "/v1/timeline"
	case "memory.search", "memory.forget", "memory.pin", "memory.scope":
		return "/v1/memory"
	case "sessions.get", "sessions.turns", "sessions.summary", "sessions.share":
		return "/v1/agent-sessions"
	case "files.diff":
		return "/v1/blobs"
	case "teleport.send":
		return "/v1/teleports"
	default:
		return "/v1/" + method
	}
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
