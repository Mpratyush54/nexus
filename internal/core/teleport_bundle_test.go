package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type bundleCloudFixture struct {
	root     string
	manifest json.RawMessage
	blobs    map[string][]byte
	sent     bool
}

func (f *bundleCloudFixture) Do(_ context.Context, method, path string, body []byte) ([]byte, error) {
	respond := func(v any) ([]byte, error) { return json.Marshal(v) }
	switch {
	case method == http.MethodGet && path == "/v1/agent-sessions/s1":
		return respond(map[string]any{"id": "s1", "project_id": "p1", "workspace_root_hint": f.root, "harness": "codex", "native_id": "n1"})
	case method == http.MethodGet && path == "/v1/agent-sessions/s1/versions":
		items := []any{map[string]any{"version": 1, "state": "transcript_only", "manifest": map[string]any{"transcript": map[string]any{"blob": "sha256:" + strings.Repeat("a", 64)}}}}
		if len(f.manifest) > 0 {
			items = append(items, map[string]any{"version": 2, "state": "complete", "manifest": json.RawMessage(f.manifest)})
		}
		return respond(map[string]any{"items": items})
	case method == http.MethodPost && path == "/v1/blobs/missing":
		var req struct {
			Hashes []string `json:"hashes"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, err
		}
		return respond(map[string]any{"missing": req.Hashes})
	case method == http.MethodPut && strings.HasPrefix(path, "/v1/blobs/"):
		var req struct {
			Body string `json:"body_b64"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, err
		}
		plain, err := base64.StdEncoding.DecodeString(req.Body)
		if err != nil {
			return nil, err
		}
		f.blobs[strings.TrimPrefix(path, "/v1/blobs/")] = plain
		return respond(map[string]any{"ok": true})
	case method == http.MethodPost && path == "/v1/agent-sessions/s1/versions":
		return respond(map[string]any{"version": 2})
	case method == http.MethodPost && path == "/v1/agent-sessions/s1/versions/2/complete":
		f.manifest = append([]byte(nil), body...)
		return respond(map[string]any{"state": "complete"})
	case method == http.MethodPost && path == "/v1/teleports":
		f.sent = true
		return respond(map[string]any{"id": "t1", "status": "sent"})
	case method == http.MethodGet && strings.HasPrefix(path, "/v1/agent-sessions/s1/versions/2/blobs/"):
		hash := strings.TrimPrefix(path, "/v1/agent-sessions/s1/versions/2/blobs/")
		if data, ok := f.blobs[hash]; ok {
			return data, nil
		}
		return nil, errors.New("missing blob")
	}
	return nil, errors.New("unexpected cloud route: " + method + " " + path)
}

func TestTeleportWithCodeUploadsAndRestoresWithoutOverwrite(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture := &bundleCloudFixture{root: root, blobs: map[string][]byte{}}
	send, err := teleportSendWithCode(t.Context(), Deps{Cloud: fixture, Root: root}, json.RawMessage(`{"session_id":"s1","to_user_id":"friend","include_code":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if !fixture.sent || send.(map[string]any)["code_files"] != 1 {
		t.Fatalf("send: %#v", send)
	}
	dest := filepath.Join(t.TempDir(), "nested", "restored")
	got, err := teleportRestoreCode(t.Context(), Deps{Cloud: fixture}, json.RawMessage(`{"session_id":"s1","dest":"`+filepath.ToSlash(dest)+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got.(map[string]any)["verified"] != 1 {
		t.Fatalf("restore: %#v", got)
	}
	data, err := os.ReadFile(filepath.Join(dest, "main.go"))
	if err != nil || string(data) != "package main\n" {
		t.Fatalf("file %q, %v", data, err)
	}
	if _, err := teleportRestoreCode(t.Context(), Deps{Cloud: fixture}, json.RawMessage(`{"session_id":"s1","dest":"`+filepath.ToSlash(dest)+`"}`)); err == nil || !strings.Contains(err.Error(), "will not overwrite") {
		t.Fatalf("expected overwrite refusal, got %v", err)
	}
}
