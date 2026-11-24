//go:build nexuscorelib

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"central-memory/internal/agentfiles"
	"central-memory/internal/daemon"
)

func agentMemoryCall(t *testing.T, name string, args any) map[string]any {
	t.Helper()
	raw, _ := json.Marshal(args)
	out, err := agentMemoryFilesCall(context.Background(), name, raw)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return out.(map[string]any)
}

func TestAgentMemoryFilesGetDefaultsOff(t *testing.T) {
	t.Setenv("CENTRAL_MEMORY_CONFIG_DIR", t.TempDir())
	root := t.TempDir()
	out := agentMemoryCall(t, "agents.memory_files.get", map[string]any{"workspace_root": root})
	s := out["settings"].(agentfiles.Settings)
	if s.Enabled || s.CreateMissing || !reflect.DeepEqual(s.Files, agentfiles.AllFiles) {
		t.Fatalf("default settings = %+v", s)
	}
	if _, err := agentMemoryFilesCall(context.Background(), "agents.memory_files.get", json.RawMessage(`{}`)); err == nil {
		t.Fatal("missing workspace_root should fail")
	}
}

func TestAgentMemoryFilesSetSavesAndSyncsWithoutRuntime(t *testing.T) {
	t.Setenv("CENTRAL_MEMORY_CONFIG_DIR", t.TempDir())
	root := t.TempDir()
	claude := filepath.Join(root, "CLAUDE.md")
	if err := os.WriteFile(claude, []byte("# mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := agentMemoryCall(t, "agents.memory_files.set", map[string]any{
		"workspace_root": root, "enabled": true, "files": []string{"GEMINI", "claude", "bogus"}, "create_missing": false,
	})
	s := out["settings"].(agentfiles.Settings)
	if !s.Enabled || !reflect.DeepEqual(s.Files, []string{"claude", "gemini"}) {
		t.Fatalf("saved settings = %+v", s)
	}
	// No capture runtime and no sign-in: the forced sync reports "not
	// connected" and leaves the user's file alone.
	report, ok := out["report"].(agentfiles.SyncReport)
	if !ok || report.Skipped != "not connected" || !report.Enabled {
		t.Fatalf("report = %#v", out["report"])
	}
	if raw, _ := os.ReadFile(claude); string(raw) != "# mine\n" {
		t.Fatalf("CLAUDE.md changed: %q", raw)
	}
	got := agentMemoryCall(t, "agents.memory_files.get", map[string]any{"workspace_root": root})
	if !reflect.DeepEqual(got["settings"], s) {
		t.Fatalf("get after set = %+v", got["settings"])
	}
	// Turning it off saves without a sync.
	off := agentMemoryCall(t, "agents.memory_files.set", map[string]any{"workspace_root": root, "enabled": false})
	if _, synced := off["report"]; synced {
		t.Fatalf("disabled set ran a sync: %+v", off)
	}
	if s := off["settings"].(agentfiles.Settings); s.Enabled || !reflect.DeepEqual(s.Files, []string{"claude", "gemini"}) {
		t.Fatalf("disabled settings = %+v", s)
	}
}

func TestAgentMemoryFilesSyncUsesWorkspaceRuntime(t *testing.T) {
	t.Setenv("CENTRAL_MEMORY_CONFIG_DIR", t.TempDir())
	root := t.TempDir()
	nxState.mu.Lock()
	previous := nxState.captureOthers
	// A zero Runtime has no Daemon, so reaching it reports "no workspace"
	// instead of the Runtime-less "disabled"/"not connected" path.
	nxState.captureOthers = map[string]*daemon.Runtime{root: {}}
	nxState.mu.Unlock()
	defer func() {
		nxState.mu.Lock()
		nxState.captureOthers = previous
		nxState.mu.Unlock()
	}()
	out := agentMemoryCall(t, "agents.memory_files.sync", map[string]any{"workspace_root": root})
	if report := out["report"].(agentfiles.SyncReport); report.Skipped != "no workspace" {
		t.Fatalf("report = %+v", report)
	}
	if _, err := agentMemoryFilesCall(context.Background(), "agents.memory_files.sync", json.RawMessage(`{"workspace_root":"`+filepath.ToSlash(filepath.Join(root, "missing"))+`"}`)); err == nil {
		t.Fatal("missing workspace folder should fail")
	}
}
