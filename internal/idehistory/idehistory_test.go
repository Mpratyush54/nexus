package idehistory

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteCursorHistorySidecarAndBackup(t *testing.T) {
	AllowlistedCursorVersions["99.0-test"] = true
	t.Cleanup(func() { delete(AllowlistedCursorVersions, "99.0-test") })

	root := t.TempDir()
	chats := filepath.Join(root, "User", "globalStorage")
	if err := os.MkdirAll(chats, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(chats, "state.vscdb")
	if err := os.WriteFile(target, []byte("sqlite-stub"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target+"-wal", []byte("wal"), 0o644); err != nil {
		t.Fatal(err)
	}
	backupDir := filepath.Join(root, "ide-backups")
	payload := SessionPayload{
		SessionID: "sess/abc:1",
		Version:   "99.0-test",
		Title:     "hello",
		Turns:     json.RawMessage(`[{"role":"user","text":"hi"}]`),
	}
	res, err := WriteCursorHistory(backupDir, target, payload)
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != "sidecar" {
		t.Fatalf("mode=%q", res.Mode)
	}
	if _, err := os.Stat(res.BackupPath); err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	if _, err := os.Stat(res.BackupPath + "-wal"); err != nil {
		t.Fatalf("wal backup missing: %v", err)
	}
	// Original DB must be untouched.
	raw, err := os.ReadFile(target)
	if err != nil || string(raw) != "sqlite-stub" {
		t.Fatalf("target mutated: %q %v", raw, err)
	}
	wantSide := filepath.Join(chats, "nexus-restored", "sess_abc_1.json")
	if res.SidecarPath != wantSide {
		t.Fatalf("sidecar path=%s want %s", res.SidecarPath, wantSide)
	}
	body, err := os.ReadFile(res.SidecarPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"nexus_restored": true`) {
		t.Fatalf("sidecar missing marker: %s", body)
	}
	if !strings.Contains(string(body), "version-gated") {
		t.Fatalf("sidecar missing gate note: %s", body)
	}
}

func TestWriteCursorHistoryUnsupportedVersion(t *testing.T) {
	_, err := WriteCursorHistory(t.TempDir(), filepath.Join(t.TempDir(), "state.vscdb"), SessionPayload{
		SessionID: "s1", Version: "3.4-unlisted",
	})
	var seeded *ErrSeededFallback
	if !errors.As(err, &seeded) || !seeded.SeededFallback() {
		t.Fatalf("want ErrSeededFallback, got %v", err)
	}
	if !strings.Contains(err.Error(), "Cursor") || !strings.Contains(err.Error(), "3.4-unlisted") {
		t.Fatalf("message: %v", err)
	}
}

func TestWriteAntigravityHistory(t *testing.T) {
	AllowlistedAntigravityVersions["2.0-test"] = true
	t.Cleanup(func() { delete(AllowlistedAntigravityVersions, "2.0-test") })

	dir := t.TempDir()
	target := filepath.Join(dir, "conversations", "c1.db")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("db"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := WriteAntigravityHistory(filepath.Join(dir, "bak"), target, SessionPayload{
		SessionID: "ag-1", Version: "2.0-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(res.SidecarPath); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(res.SidecarPath, filepath.Join("nexus-restored", "ag-1.json")) {
		t.Fatalf("sidecar=%s", res.SidecarPath)
	}
}

func TestWriteWindsurfDisabled(t *testing.T) {
	_, err := WriteWindsurfHistory(t.TempDir(), filepath.Join(t.TempDir(), "x.pb"), SessionPayload{SessionID: "w1"})
	if !errors.Is(err, ErrExperimentalDisabled) {
		t.Fatalf("got %v", err)
	}
}

func TestWriteCursorHistoryMissingTargetStillBacksUp(t *testing.T) {
	AllowlistedCursorVersions["1.0-missing"] = true
	t.Cleanup(func() { delete(AllowlistedCursorVersions, "1.0-missing") })

	root := t.TempDir()
	chats := filepath.Join(root, "chats")
	if err := os.MkdirAll(chats, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(chats, "state.vscdb") // does not exist
	res, err := WriteCursorHistory(filepath.Join(root, "bak"), target, SessionPayload{
		SessionID: "s-miss", Version: "1.0-missing",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(res.SidecarPath); err != nil {
		t.Fatal(err)
	}
}
