package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"central-memory/internal/blobs"
	"central-memory/internal/cache"
	"central-memory/internal/continuex"
	"central-memory/internal/idehistory"
	"central-memory/internal/outbox"
	"central-memory/internal/secrets"
	"central-memory/internal/teleport"
)

type recordingCloud struct {
	mu    sync.Mutex
	posts []string
}

func (r *recordingCloud) Do(_ context.Context, method, path string, body []byte) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.posts = append(r.posts, method+" "+path)
	return []byte(`{"ok":true}`), nil
}

func TestContinueAndOffline(t *testing.T) {
	ResetRunsForTest()
	raw, _ := json.Marshal(map[string]string{"session_id": "s1", "mode": "here"})
	out, err := Call(context.Background(), "continue.start", raw, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["op_id"] != "op_s1" {
		t.Fatalf("op = %v", m["op_id"])
	}
	if _, err := Call(context.Background(), "timeline.list", nil, Deps{}); err != ErrOffline {
		t.Fatalf("offline: %v", err)
	}
	if _, err := Call(context.Background(), "nope", nil, Deps{}); err != ErrUnknownMethod {
		t.Fatalf("unknown: %v", err)
	}
}

func TestContinueRunnerRecordsArgv(t *testing.T) {
	ResetRunsForTest()
	runner := &continuex.Controllable{NextID: "op_runner_1"}
	raw, _ := json.Marshal(map[string]string{"session_id": "s9", "mode": "here", "prompt": "hi"})
	out, err := Call(context.Background(), "continue.start", raw, Deps{ContinueRunner: runner})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["op_id"] != "op_runner_1" {
		t.Fatalf("op = %v", m["op_id"])
	}
	argv := runner.LastArgv()
	if len(argv) == 0 || argv[0] != "nexus" {
		t.Fatalf("recorded argv=%v", argv)
	}
	cmd, _ := m["command"].([]string)
	if len(cmd) == 0 {
		t.Fatal("missing command in result")
	}
}

func TestRunsApproveCancelTransitions(t *testing.T) {
	ResetRunsForTest()
	raw, _ := json.Marshal(map[string]string{"session_id": "run1", "mode": "here"})
	out, err := Call(context.Background(), "continue.start", raw, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	opID := out.(map[string]any)["op_id"].(string)

	msgRaw, _ := json.Marshal(map[string]string{"op_id": opID, "message": "do it"})
	got, err := Call(context.Background(), "runs.message", msgRaw, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	if got.(map[string]any)["status"] != "awaiting_approval" {
		t.Fatalf("after message: %+v", got)
	}

	apRaw, _ := json.Marshal(map[string]string{"op_id": opID})
	got, err = Call(context.Background(), "runs.approve", apRaw, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	if got.(map[string]any)["status"] != "approved" {
		t.Fatalf("after approve: %+v", got)
	}

	// Fresh run for cancel path.
	ResetRunsForTest()
	out, err = Call(context.Background(), "continue.start", raw, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	opID = out.(map[string]any)["op_id"].(string)
	cancelRaw, _ := json.Marshal(map[string]string{"op_id": opID})
	got, err = Call(context.Background(), "runs.cancel", cancelRaw, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	if got.(map[string]any)["status"] != "cancelled" {
		t.Fatalf("after cancel: %+v", got)
	}
	if _, err := Call(context.Background(), "runs.approve", cancelRaw, Deps{}); err == nil {
		t.Fatal("approve after cancel should fail")
	}
}

func TestCacheOutboxAndFile(t *testing.T) {
	var c cache.Cache
	c.Put("k", []byte("v"))
	sp, err := outbox.Open("")
	if err != nil {
		t.Fatal(err)
	}
	if err := sp.Enqueue("1", "turn", []byte("t")); err != nil {
		t.Fatal(err)
	}
	out, err := Call(context.Background(), "uploads.status", nil, Deps{Outbox: sp})
	if err != nil || out.(map[string]any)["pending"].(int) != 1 {
		t.Fatalf("pending: %v %v", out, err)
	}
	if _, err := Call(context.Background(), "cache.clear", nil, Deps{Cache: &c}); err != nil {
		t.Fatal(err)
	}
	if c.Len() != 0 {
		t.Fatal("cache not cleared")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]string{"path": "a.txt"})
	got, err := Call(context.Background(), "files.read", raw, Deps{Root: dir})
	if err != nil || got.(map[string]any)["content"] != "hello" {
		t.Fatalf("read: %v %v", got, err)
	}
	raw, _ = json.Marshal(map[string]string{"path": "../a.txt"})
	if _, err := Call(context.Background(), "files.read", raw, Deps{Root: dir}); err == nil {
		t.Fatal("escape")
	}
}

func TestSecretsRestoreLocal(t *testing.T) {
	dir := t.TempDir()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	plain := []byte("TOKEN=abc\n")
	ct, err := secrets.Encrypt(key, plain)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]string{
		"path":           ".env",
		"ciphertext_b64": base64.StdEncoding.EncodeToString(ct),
		"data_key_b64":   base64.StdEncoding.EncodeToString(key),
	})
	out, err := Call(context.Background(), "secrets.restore", raw, Deps{Root: dir})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["restored"] != true {
		t.Fatalf("out=%v", out)
	}
	got, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil || string(got) != string(plain) {
		t.Fatalf("file=%q err=%v", got, err)
	}
	if _, err := Call(context.Background(), "secrets.restore", json.RawMessage(`{"path":".env"}`), Deps{Root: dir}); err == nil {
		t.Fatal("expected clear error without data key")
	}
}

func TestUploadsRetryDrainsWhenOnline(t *testing.T) {
	sp, err := outbox.Open("")
	if err != nil {
		t.Fatal(err)
	}
	if err := outbox.CaptureEnqueue(sp, "t1", "turn", "POST", "/v1/agent-sessions/s/turns", []byte(`{"idx":1}`)); err != nil {
		t.Fatal(err)
	}
	cloud := &recordingCloud{}
	out, err := Call(context.Background(), "uploads.retry", nil, Deps{Online: true, Cloud: cloud, Outbox: sp})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["pending"].(int) != 0 || m["uploaded"].(int) != 1 {
		t.Fatalf("retry result = %+v", m)
	}
	if len(cloud.posts) != 1 || cloud.posts[0] != "POST /v1/agent-sessions/s/turns" {
		t.Fatalf("posts = %+v", cloud.posts)
	}
	// Offline / no cloud: do not drain.
	if err := outbox.CaptureEnqueue(sp, "t2", "turn", "POST", "/v1/agent-sessions/s/turns", []byte(`{"idx":2}`)); err != nil {
		t.Fatal(err)
	}
	out, err = Call(context.Background(), "uploads.retry", nil, Deps{Online: false, Cloud: cloud, Outbox: sp})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["pending"].(int) != 1 {
		t.Fatalf("offline should leave pending: %+v", out)
	}
	if len(cloud.posts) != 1 {
		t.Fatalf("offline should not post: %+v", cloud.posts)
	}
}

func TestBindingsMatchIDL(t *testing.T) {
	root := filepath.Join("..", "..", "bindings")
	cs, err := os.ReadFile(filepath.Join(root, "csharp", "NxMethods.g.cs"))
	if err != nil {
		t.Fatal(err)
	}
	sw, err := os.ReadFile(filepath.Join(root, "swift", "NxMethods.swift"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range Methods() {
		if !strings.Contains(string(cs), name) || !strings.Contains(string(sw), name) {
			t.Fatalf("binding missing %s", name)
		}
	}
	cb, err := Call(context.Background(), "auth.callback", nil, Deps{})
	if err != nil || cb.(map[string]any)["redirect"] != "nexus://auth/callback" {
		t.Fatalf("callback: %v %v", cb, err)
	}
}

func TestTeleportPreparePlanAndApply(t *testing.T) {
	dir := t.TempDir()
	raw, _ := json.Marshal(map[string]any{
		"session_id": "s-prepare",
		"from_os":    "mac",
		"to_os":      "unix",
		"from_home":  "/Users/a",
		"to_home":    "/home/b",
		"agent":      "claude",
		"commit":     "abc12345deadbeef",
		"secrets":    []string{".env"},
		"text":       "mail ada@ex.com",
		"home":       "/Users/a",
	})
	out, err := Call(context.Background(), "teleport.prepare", raw, Deps{Root: dir})
	if err != nil {
		t.Fatal(err)
	}
	plan, ok := out.(teleport.PreparePlan)
	if !ok {
		// json-shaped via map when? BuildPreparePlan returns struct directly.
		t.Fatalf("type %T", out)
	}
	if !plan.DryRun || !plan.Repo.Found || plan.Secrets.Mode != "names_only" {
		t.Fatalf("plan=%+v", plan)
	}
	if len(plan.Continue) < 2 || plan.Target.Mode != "worktree" {
		t.Fatalf("checklist incomplete: %+v", plan)
	}

	stub, err := Call(context.Background(), "teleport.apply", json.RawMessage(`{"session_id":"s1"}`), Deps{Root: dir})
	if err != nil {
		t.Fatal(err)
	}
	sm := stub.(map[string]any)
	if sm["applied"] != false {
		t.Fatalf("stub apply: %+v", sm)
	}
	if _, ok := sm["next_steps"]; !ok {
		t.Fatalf("missing next_steps: %+v", sm)
	}

	dest := filepath.Join(dir, "restore-out")
	content := []byte("hello teleport")
	sum := blobs.HashBytes(content)
	applyArgs, _ := json.Marshal(map[string]any{
		"session_id": "s1",
		"dest":       dest,
		"files": []map[string]any{{
			"path": "hello.txt", "sha256": sum, "size": len(content),
			"bytes_b64": base64.StdEncoding.EncodeToString(content),
		}},
	})
	applied, err := Call(context.Background(), "teleport.apply", applyArgs, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	am := applied.(map[string]any)
	if am["applied"] != true || am["verified"].(int) != 1 {
		t.Fatalf("apply: %+v", am)
	}
	got, err := os.ReadFile(filepath.Join(dest, "hello.txt"))
	if err != nil || string(got) != string(content) {
		t.Fatalf("restored=%q err=%v", got, err)
	}
}

func TestRestoreIDEHistoryCursor(t *testing.T) {
	idehistory.AllowlistedCursorVersions["0.0-core-test"] = true
	t.Cleanup(func() { delete(idehistory.AllowlistedCursorVersions, "0.0-core-test") })

	root := t.TempDir()
	chats := filepath.Join(root, "globalStorage")
	if err := os.MkdirAll(chats, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(chats, "state.vscdb")
	if err := os.WriteFile(target, []byte("db"), 0o644); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{
		"agent":      "cursor",
		"backup_dir": filepath.Join(root, "bak"),
		"target_db":  target,
		"session_id": "core-sess-1",
		"version":    "0.0-core-test",
		"title":      "from core",
	})
	out, err := Call(context.Background(), "sessions.restore_ide_history", raw, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(*idehistory.Result)
	if res.Mode != "sidecar" {
		t.Fatalf("%+v", res)
	}
	if _, err := os.Stat(res.SidecarPath); err != nil {
		t.Fatal(err)
	}

	_, err = Call(context.Background(), "sessions.restore_ide_history", json.RawMessage(`{"agent":"windsurf","session_id":"w","backup_dir":"/tmp","target_db":"/tmp/x"}`), Deps{})
	if err == nil || !strings.Contains(err.Error(), "experimental") {
		t.Fatalf("windsurf: %v", err)
	}
}
