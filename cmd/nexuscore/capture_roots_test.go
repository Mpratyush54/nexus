//go:build nexuscorelib

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"central-memory/internal/config"
)

func TestCaptureRootsRequiresConsentAndNeverAddsTranscriptProjects(t *testing.T) {
	t.Setenv("CENTRAL_MEMORY_CONFIG_DIR", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	primary, extra := t.TempDir(), t.TempDir()
	if err := config.SaveFile(config.File{WorkspaceRoot: primary, Token: "keep-credential"}); err != nil {
		t.Fatal(err)
	}
	sessions := filepath.Join(os.Getenv("CODEX_HOME"), "sessions")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	line, _ := json.Marshal(map[string]any{"type": "session_meta", "payload": map[string]any{"cwd": extra}})
	if err := os.WriteFile(filepath.Join(sessions, "unapproved.jsonl"), line, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := captureRoots(primary); len(got) != 0 {
		t.Fatalf("monitoring without consent: %v", got)
	}
	if err := config.SetCaptureWorkspaces([]string{primary}); err != nil {
		t.Fatal(err)
	}
	roots := captureRoots(primary)
	if len(roots) != 1 || !samePath(roots[0], primary) {
		t.Fatalf("unexpected scope: %v", roots)
	}
	if err := config.SetCaptureWorkspaces([]string{primary, extra, primary}); err != nil {
		t.Fatal(err)
	}
	if roots := captureRoots(primary); len(roots) != 2 {
		t.Fatalf("explicit multi-workspace scope: %v", roots)
	}
	if err := config.SetCaptureWorkspaces(nil); err != nil {
		t.Fatal(err)
	}
	if roots := captureRoots(primary); len(roots) != 0 {
		t.Fatalf("capture continued after revocation: %v", roots)
	}
	saved, _ := config.LoadFile()
	if saved.Token != "keep-credential" || saved.CaptureConsentVersion != 1 {
		t.Fatalf("consent update changed account or was not persisted")
	}
}

func TestCaptureRootValidationRejectsMissingRelativeAndBroadFolders(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{"relative-folder", filepath.Join(root, "missing"), filepath.VolumeName(root) + string(os.PathSeparator)} {
		if _, err := validateCaptureRoots([]string{path}, true); err == nil {
			t.Fatalf("accepted %q", path)
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		if _, err := validateCaptureRoots([]string{home}, true); err == nil {
			t.Fatal("accepted home directory")
		}
	}
}
