//go:build nexuscorelib

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCaptureRootsFindsCodexProjectsWithoutDuplicatingSessions(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("APPDATA", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	primary := filepath.Join(t.TempDir(), "selected")
	extra := filepath.Join(t.TempDir(), "other-project")
	for _, root := range []string{primary, extra} {
		if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	sessions := filepath.Join(os.Getenv("CODEX_HOME"), "sessions", "2026", "10", "18")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	for i, root := range []string{primary, extra, extra} {
		line, _ := json.Marshal(map[string]any{"type": "session_meta", "payload": map[string]any{"cwd": root}})
		if err := os.WriteFile(filepath.Join(sessions, string(rune('a'+i))+".jsonl"), append(line, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	roots := captureRoots(primary)
	if len(roots) != 2 || !samePath(roots[0], primary) || !samePath(roots[1], extra) {
		t.Fatalf("capture roots = %#v", roots)
	}
}
