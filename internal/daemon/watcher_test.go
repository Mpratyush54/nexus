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

func TestWatchHashStorePersistsAcrossRestarts(t *testing.T) {
	root := t.TempDir()
	writeWSFile(t, root, "CLAUDE.md", "v1\n")
	hashes := NewMemoryHashStore()

	w := NewWatcherWithStore(root, nil, time.Second, nil, "ws-1", hashes)
	w.SeedBaseline()

	// Edit: CheckOnce emits and persists the new hash.
	writeWSFile(t, root, "CLAUDE.md", "v1\nv2\n")
	if evs := w.CheckOnce(); len(evs) != 1 {
		t.Fatalf("edit emitted %d events, want 1", len(evs))
	}
	persisted, ok := hashes.GetHash("ws-1", "CLAUDE.md")
	if !ok || persisted != HashString("v1\nv2\n") {
		t.Fatalf("persisted hash = %q,%v; want current content hash", persisted, ok)
	}

	// Restart with the same store and unchanged disk: the persisted hash
	// seeds the baseline, so CheckOnce stays silent.
	w2 := NewWatcherWithStore(root, nil, time.Second, nil, "ws-1", hashes)
	w2.SeedBaseline()
	if evs := w2.CheckOnce(); len(evs) != 0 {
		t.Fatalf("restart with unchanged content emitted %d events", len(evs))
	}
}

func TestWatchHashStoreSurfacesEditWhileDown(t *testing.T) {
	root := t.TempDir()
	writeWSFile(t, root, "CLAUDE.md", "v1\n")
	hashes := NewMemoryHashStore()
	if err := hashes.SetHash("ws-1", "CLAUDE.md", HashString("v1\n")); err != nil {
		t.Fatal(err)
	}

	// Edit while the daemon is down, then boot with persistence: the
	// persisted (stale) hash seeds the baseline so CheckOnce emits.
	writeWSFile(t, root, "CLAUDE.md", "v1\nv2\n")
	w := NewWatcherWithStore(root, nil, time.Second, nil, "ws-1", hashes)
	w.SeedBaseline()
	evs := w.CheckOnce()
	if len(evs) != 1 {
		t.Fatalf("edit-while-down emitted %d events, want 1", len(evs))
	}
	if evs[0].Payload["old_hash"] != HashString("v1\n") {
		t.Errorf("old_hash = %v, want pre-restart hash", evs[0].Payload["old_hash"])
	}
}

func TestWatchHashStoreDeletionClearsPersistedHash(t *testing.T) {
	root := t.TempDir()
	writeWSFile(t, root, "CLAUDE.md", "v1\n")
	hashes := NewMemoryHashStore()
	w := NewWatcherWithStore(root, nil, time.Second, nil, "ws-1", hashes)
	w.SeedBaseline()

	writeWSFile(t, root, "CLAUDE.md", "v1\nv2\n")
	if evs := w.CheckOnce(); len(evs) != 1 {
		t.Fatalf("edit emitted %d events, want 1", len(evs))
	}
	if _, ok := hashes.GetHash("ws-1", "CLAUDE.md"); !ok {
		t.Fatal("edit must persist a hash")
	}
	if err := os.Remove(filepath.Join(root, "CLAUDE.md")); err != nil {
		t.Fatal(err)
	}
	// Deletion is forgotten silently (no event) and drops the persisted hash.
	if evs := w.CheckOnce(); len(evs) != 0 {
		t.Fatalf("deletion must not emit, got %d events", len(evs))
	}
	if _, ok := hashes.GetHash("ws-1", "CLAUDE.md"); ok {
		t.Error("deletion must drop the persisted hash")
	}
}

func TestFileHashStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "hashes.json")
	s, err := NewFileHashStore(path)
	if err != nil {
		t.Fatalf("NewFileHashStore: %v", err)
	}
	if s.Path() != path {
		t.Errorf("Path() = %q, want %q", s.Path(), path)
	}
	if err := s.SetHash("ws-1", "CLAUDE.md", "abc"); err != nil {
		t.Fatalf("SetHash: %v", err)
	}
	// Reload from disk: the hash survives the process boundary.
	reloaded, err := NewFileHashStore(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if h, ok := reloaded.GetHash("ws-1", "CLAUDE.md"); !ok || h != "abc" {
		t.Errorf("reloaded hash = %q,%v; want abc,true", h, ok)
	}
	if err := reloaded.DeleteHash("ws-1", "CLAUDE.md"); err != nil {
		t.Fatalf("DeleteHash: %v", err)
	}
	if _, ok := reloaded.GetHash("ws-1", "CLAUDE.md"); ok {
		t.Error("deleted hash must be gone")
	}
	// Corrupt state is an error, never silent amnesia.
	if err := os.WriteFile(path, []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileHashStore(path); err == nil {
		t.Error("corrupt state file must fail to load")
	}
}

// TestFileHashStoreNeverHalfWritten pins the issue-#109 atomic-write
// guarantee: after a large burst of mutations (every one rewriting the
// file), the state file must always parse — never truncated — and no temp
// files may be left behind.
func TestFileHashStoreNeverHalfWritten(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hashes.json")
	s, err := NewFileHashStore(path)
	if err != nil {
		t.Fatalf("NewFileHashStore: %v", err)
	}
	const n = 500
	for i := 0; i < n; i++ {
		ws := "ws-large"
		p := "file-" + strings.Repeat("x", 64) + "-" + string(rune('0'+i%10)) + "-" + strings.Repeat("y", 32) + ".md"
		if err := s.SetHash(ws, p, HashString(p)); err != nil {
			t.Fatalf("SetHash %d: %v", i, err)
		}
		if i == n/2 {
			// Mid-burst reload: the file must already be fully parseable.
			mid, err := NewFileHashStore(path)
			if err != nil {
				t.Fatalf("mid-burst reload: %v", err)
			}
			if _, ok := mid.GetHash(ws, p); !ok {
				t.Fatalf("mid-burst reload missing just-written hash for %q", p)
			}
		}
	}
	// Final reload parses and every entry survived.
	reloaded, err := NewFileHashStore(path)
	if err != nil {
		t.Fatalf("final reload: %v", err)
	}
	for i := 0; i < 10; i++ {
		p := "file-" + strings.Repeat("x", 64) + "-" + string(rune('0'+i%10)) + "-" + strings.Repeat("y", 32) + ".md"
		if h, ok := reloaded.GetHash("ws-large", p); !ok || h != HashString(p) {
			t.Errorf("entry %q = %q,%v; want hash,true", p, h, ok)
		}
	}
	// No temp-file debris: every save either renamed or cleaned up.
	leftovers, err := filepath.Glob(filepath.Join(dir, ".hashes-*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(leftovers) != 0 {
		t.Errorf("temp files left behind: %v", leftovers)
	}
}
