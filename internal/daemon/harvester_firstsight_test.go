package daemon

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFirstSightOffsetDefaultWindow(t *testing.T) {
	t.Setenv("NEXUS_HARVEST_BACKFILL", "")
	t.Setenv("NEXUS_HARVEST_KEEP_TAIL", "")
	const size int64 = 10 << 20 // 10 MiB
	off := firstSightOffset(size)
	want := size - int64(defaultKeepTail)
	if off != want {
		t.Fatalf("firstSightOffset(%d) = %d, want %d (8MiB window)", size, off, want)
	}
	if firstSightOffset(defaultKeepTail/2) != 0 {
		t.Fatal("files smaller than keepTail must start at 0")
	}
}

func TestFirstSightOffsetBackfillAll(t *testing.T) {
	t.Setenv("NEXUS_HARVEST_BACKFILL", "all")
	if got := firstSightOffset(50 << 20); got != 0 {
		t.Fatalf("backfill all: got %d want 0", got)
	}
}

func TestMatchesTranscriptWorkspaceJSON(t *testing.T) {
	home := t.TempDir()
	ws := filepath.Join(home, "central-memory")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	// VS Code–style hash dir with workspace.json pointing at central-memory.
	hashDir := filepath.Join(home, "AppData", "Roaming", "Antigravity", "User", "workspaceStorage", "abc123hash")
	if runtime.GOOS != "windows" {
		hashDir = filepath.Join(home, ".config", "Antigravity", "User", "workspaceStorage", "abc123hash")
	}
	if err := os.MkdirAll(hashDir, 0o755); err != nil {
		t.Fatal(err)
	}
	folderURI := "file://" + filepath.ToSlash(ws)
	if runtime.GOOS == "windows" {
		folderURI = "file:///" + strings.ReplaceAll(filepath.ToSlash(ws), ":", "%3A")
	}
	wj := []byte(`{"folder":` + `"` + folderURI + `"}`)
	if err := os.WriteFile(filepath.Join(hashDir, "workspace.json"), wj, 0o644); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(hashDir, "state.vscdb")
	if err := os.WriteFile(db, []byte("sqlite"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := NewHarvester(ws, nil)
	if h.MatchesWorkspace(db) {
		t.Fatal("hash path must not path-match")
	}
	if !h.MatchesTranscript(db, false) {
		t.Fatal("MatchesTranscript must accept workspace.json ProjectOf even without CwdMatch")
	}
}
