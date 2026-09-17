package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHashBytesStableAndSensitive(t *testing.T) {
	a := HashString("hello")
	b := HashString("hello")
	c := HashString("hello!")
	if a != b {
		t.Fatal("same input hashed differently")
	}
	if a == c {
		t.Fatal("different inputs hashed equally")
	}
	if len(a) != 64 {
		t.Fatalf("expected 64-char hex sha256, got %q", a)
	}
}

func TestHashFileMatchesBytes(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	content := "instruction content\nline2\n"
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	h1, err := HashFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != HashBytes([]byte(content)) {
		t.Fatal("file hash differs from bytes hash")
	}
	if _, err := HashFile(filepath.Join(dir, "missing.txt")); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestDefaultTargets(t *testing.T) {
	targets := DefaultWatchedTargets()
	if len(targets) != 4 {
		t.Fatalf("expected 4 targets, got %d", len(targets))
	}
	want := map[string]string{
		"CLAUDE.md":                       "claude_md",
		".cursorrules":                    "cursorrules",
		".github/copilot-instructions.md": "copilot_instructions",
		".windsurfrules":                  "windsurfrules",
	}
	for _, tg := range targets {
		if want[tg.RelPath] != tg.FileType {
			t.Errorf("target %s: got type %s", tg.RelPath, tg.FileType)
		}
	}
}

func writeWSFile(t *testing.T, root, rel, content string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWatcherBaselineSilentThenDetectsChange(t *testing.T) {
	root := t.TempDir()
	writeWSFile(t, root, "CLAUDE.md", "rule one\n")

	w := NewWatcher(root, nil, time.Second, nil)
	if evs := w.CheckOnce(); len(evs) != 0 {
		t.Fatalf("first sight must baseline silently, got %d events", len(evs))
	}

	// No change -> no event.
	if evs := w.CheckOnce(); len(evs) != 0 {
		t.Fatalf("unchanged poll emitted %d events", len(evs))
	}

	// Modify -> exactly one INSTRUCTION_FILE_CHANGED with diff.
	writeWSFile(t, root, "CLAUDE.md", "rule one\nrule two\n")
	evs := w.CheckOnce()
	if len(evs) != 1 {
		t.Fatalf("expected 1 change event, got %d", len(evs))
	}
	ev := evs[0]
	if ev.Type != ToolEventInstructionFileChanged {
		t.Fatalf("wrong type: %s", ev.Type)
	}
	if ev.Payload["path"] != "CLAUDE.md" || ev.Payload["file_type"] != "claude_md" {
		t.Fatalf("bad payload identity: %v", ev.Payload)
	}
	diff, _ := ev.Payload["diff"].(string)
	if !strings.Contains(diff, "+ rule two") {
		t.Fatalf("diff missing added line: %q", diff)
	}
	if ev.Payload["old_hash"] == ev.Payload["new_hash"] {
		t.Fatal("hashes must differ after change")
	}

	// Steady state again -> silent.
	if evs := w.CheckOnce(); len(evs) != 0 {
		t.Fatalf("post-change poll should be silent, got %d", len(evs))
	}
}

func TestWatcherMissingFilesSkipped(t *testing.T) {
	root := t.TempDir() // no instruction files at all
	w := NewWatcher(root, nil, time.Second, nil)
	if evs := w.CheckOnce(); len(evs) != 0 {
		t.Fatalf("empty workspace must emit nothing, got %d", len(evs))
	}
	// Deleted file is forgotten, not an event.
	writeWSFile(t, root, ".cursorrules", "x\n")
	w.SeedBaseline()
	if err := os.Remove(filepath.Join(root, ".cursorrules")); err != nil {
		t.Fatal(err)
	}
	if evs := w.CheckOnce(); len(evs) != 0 {
		t.Fatalf("deletion must not emit, got %d", len(evs))
	}
}

func TestWatcherEmitsToEmitter(t *testing.T) {
	root := t.TempDir()
	writeWSFile(t, root, ".windsurfrules", "v1\n")
	ch := NewChanEmitter(16)
	w := NewWatcher(root, nil, time.Second, ch)
	w.SeedBaseline()
	writeWSFile(t, root, ".windsurfrules", "v2\n")
	if evs := w.CheckOnce(); len(evs) != 1 {
		t.Fatalf("expected 1 event, got %d", len(evs))
	}
	select {
	case ev := <-ch.Ch:
		if ev.Type != ToolEventInstructionFileChanged {
			t.Fatalf("wrong type: %s", ev.Type)
		}
	default:
		t.Fatal("expected event forwarded to emitter")
	}
}

func TestWatcherDiffCapped(t *testing.T) {
	root := t.TempDir()
	writeWSFile(t, root, "CLAUDE.md", "seed\n")
	w := NewWatcher(root, nil, time.Second, nil)
	w.SeedBaseline()
	writeWSFile(t, root, "CLAUDE.md", "seed\n"+strings.Repeat("new rule line that is fairly long\n", 2000))
	evs := w.CheckOnce()
	if len(evs) != 1 {
		t.Fatalf("expected 1 event, got %d", len(evs))
	}
	diff, _ := evs[0].Payload["diff"].(string)
	if len(diff) > MaxDiffBytes+len("\n[truncated]")+1 {
		t.Fatalf("watcher diff over cap: %d", len(diff))
	}
}

func TestWatcherNestedCopilotPath(t *testing.T) {
	root := t.TempDir()
	writeWSFile(t, root, ".github/copilot-instructions.md", "a\n")
	w := NewWatcher(root, nil, time.Second, nil)
	w.SeedBaseline()
	writeWSFile(t, root, ".github/copilot-instructions.md", "a\nb\n")
	evs := w.CheckOnce()
	if len(evs) != 1 {
		t.Fatalf("nested path change missed: %d events", len(evs))
	}
	if evs[0].Payload["file_type"] != "copilot_instructions" {
		t.Fatalf("wrong file_type: %v", evs[0].Payload)
	}
}

func TestWatcherStartStopsOnCancel(t *testing.T) {
	root := t.TempDir()
	w := NewWatcher(root, nil, 10*time.Millisecond, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); w.Start(ctx) }()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after ctx cancel")
	}
}

func TestToStoreWatchedFile(t *testing.T) {
	wf := ToStoreWatchedFile("ws_1", WatchedTarget{RelPath: "CLAUDE.md", FileType: "claude_md"}, "abc")
	if wf.WorkspaceID != "ws_1" || wf.Path != "CLAUDE.md" || wf.LastHash != "abc" || wf.FileType != "claude_md" {
		t.Fatalf("bad mapping: %+v", wf)
	}
}
