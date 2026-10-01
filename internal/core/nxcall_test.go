package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"central-memory/internal/cache"
	"central-memory/internal/outbox"
)

type coreTestCloud struct {
	body []byte
	err  error
	seen string
}

func (c *coreTestCloud) Do(_ context.Context, method, path string, _ []byte) ([]byte, error) {
	c.seen = method + " " + path
	return c.body, c.err
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

func TestDesktopMemorySearchUsesAgentRequestShape(t *testing.T) {
	raw, err := json.Marshal(map[string]string{
		"q":       "redis cache",
		"project": "project-123",
		"scope":   "project",
	})
	if err != nil {
		t.Fatal(err)
	}
	method, path, body, err := cloudRequest("memory.search", raw)
	if err != nil {
		t.Fatal(err)
	}
	if method != "POST" || path != "/v1/agent/memory/search" {
		t.Fatalf("request = %s %s", method, path)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got["query"] != "redis cache" || got["project_id"] != "project-123" || got["level"] != "project" {
		t.Fatalf("wrong agent search body: %#v", got)
	}
	for _, alias := range []string{"q", "project", "scope"} {
		if _, found := got[alias]; found {
			t.Fatalf("agent body must not include UI alias %q: %#v", alias, got)
		}
	}
}

func TestDesktopProjectsUseAgentDiscovery(t *testing.T) {
	method, path, _, err := cloudRequest("projects.list", nil)
	if err != nil {
		t.Fatal(err)
	}
	if method != "GET" || path != "/v1/agent/projects" {
		t.Fatalf("request = %s %s", method, path)
	}
}

func TestAgentsListUsesCapturedTimeline(t *testing.T) {
	cloud := &coreTestCloud{body: []byte(`{"items":[{"harness":"cursor","project_id":"p1","version_state":"legacy_snapshot"},{"harness":"cursor","project_id":"p1","version_state":"legacy_snapshot"},{"harness":"codex","project_id":"p2"}]}`)}
	out, err := Call(context.Background(), "agents.list", nil, Deps{Cloud: cloud})
	if err != nil {
		t.Fatal(err)
	}
	if cloud.seen != "GET /v1/timeline?limit=100" {
		t.Fatalf("request = %q", cloud.seen)
	}
	items := out.(map[string]any)["items"].([]capturedAgentSummary)
	if len(items) != 2 || items[0].Name != "codex" || items[1].CapturedCount != 2 || items[1].Status != "Archived snapshots" {
		t.Fatalf("items = %#v", items)
	}
}
