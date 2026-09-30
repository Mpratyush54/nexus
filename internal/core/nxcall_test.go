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

	"central-memory/internal/cache"
	"central-memory/internal/outbox"
	"central-memory/internal/secrets"
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
