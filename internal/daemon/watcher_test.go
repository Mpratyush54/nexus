package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWatchFileTypeMapping(t *testing.T) {
	for _, spec := range InstructionFiles {
		got, ok := FileTypeForPath(spec.RelPath)
		if !ok || got != spec.FileType {
			t.Errorf("FileTypeForPath(%q) = %q,%v; want %q,true", spec.RelPath, got, ok, spec.FileType)
		}
	}
	// Windows separators and case resolve identically.
	if ft, ok := FileTypeForPath(`.github\copilot-instructions.md`); !ok || ft != "copilot_instructions" {
		t.Errorf("backslash variant = %q,%v", ft, ok)
	}
	if ft, ok := FileTypeForPath("claude.md"); !ok || ft != "claude_md" {
		t.Errorf("lowercase variant = %q,%v", ft, ok)
	}
	// Non-instruction paths never match — including nested lookalikes.
	for _, p := range []string{"main.go", "sub/CLAUDE.md", "docs/CLAUDE.md", "", ".", "CLAUDE.md.bak"} {
		if _, ok := FileTypeForPath(p); ok {
			t.Errorf("FileTypeForPath(%q) matched, want no match", p)
		}
	}
}

func TestWatchDetectChange(t *testing.T) {
	// Unchanged content: same hash, no diff.
	h := HashString("rules v1")
	newHash, changed, diff := DetectInstructionChange(h, []byte("rules v1"), []byte("rules v1"))
	if changed || diff != "" || newHash != h {
		t.Errorf("unchanged = %v,%q,%v", changed, diff, newHash != h)
	}
	// Edited: changed with a line diff.
	_, changed, diff = DetectInstructionChange(h, []byte("rules v1\n"), []byte("rules v1\nrules v2\n"))
	if !changed || !strings.Contains(diff, "+ rules v2\n") {
		t.Errorf("edited diff = %v,%q", changed, diff)
	}
	// New file (no old hash): full-addition diff.
	_, changed, diff = DetectInstructionChange("", nil, []byte("hello\n"))
	if !changed || !strings.Contains(diff, "+ hello\n") {
		t.Errorf("creation diff = %v,%q", changed, diff)
	}
	// Unknown "before" text still reports the change, without a line diff.
	_, changed, diff = DetectInstructionChange(h, nil, []byte("other"))
	if !changed || strings.Contains(diff, "+") || !strings.Contains(diff, "hash changed") {
		t.Errorf("retention-gap diff = %v,%q", changed, diff)
	}
}

func TestWatchPayloadKeys(t *testing.T) {
	p := InstructionChangedPayload("CLAUDE.md", "claude_md", "old", "new", "diff", 42, false)
	for _, k := range []string{"path", "file_type", "old_hash", "new_hash", "diff", "size", "deleted"} {
		if _, ok := p[k]; !ok {
			t.Errorf("payload missing key %q", k)
		}
	}
	if p["file_type"] != "claude_md" || p["deleted"] != false {
		t.Errorf("payload = %v", p)
	}
}

func TestWatchPollOnceEndToEnd(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("CLAUDE.md", "rule one\n")
	write("main.go", "package main\n")

	rec := &eventRecorder{}
	w, err := NewWatcher(root, rec.sink())
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	defer w.Close()

	// Steady state: no events.
	w.PollOnce()
	if n := rec.count(EventInstructionFileChanged); n != 0 {
		t.Fatalf("steady state emitted %d events", n)
	}

	// Edit an instruction file: exactly one hash-diff event.
	write("CLAUDE.md", "rule one\nrule two\n")
	w.PollOnce()
	if n := rec.count(EventInstructionFileChanged); n != 1 {
		t.Fatalf("edit emitted %d events, want 1", n)
	}
	p := rec.last(EventInstructionFileChanged)
	if p["path"] != "CLAUDE.md" || p["file_type"] != "claude_md" {
		t.Errorf("payload identity = %v", p)
	}
	if d, _ := p["diff"].(string); !strings.Contains(d, "+ rule two\n") {
		t.Errorf("payload diff:\n%v", d)
	}
	if p["old_hash"] == p["new_hash"] {
		t.Error("hashes did not rotate")
	}

	// Unrelated file edits are invisible to the watcher.
	write("main.go", "package main\n// changed\n")
	w.PollOnce()
	if n := rec.count(EventInstructionFileChanged); n != 1 {
		t.Fatalf("unrelated edit emitted, total %d", n)
	}

	// New instruction file appears: creation event with full-add diff.
	write(".cursorrules", "prefer tabs\n")
	w.PollOnce()
	if n := rec.count(EventInstructionFileChanged); n != 2 {
		t.Fatalf("creation emitted total %d, want 2", n)
	}
	if p := rec.last(EventInstructionFileChanged); p["path"] != ".cursorrules" || p["old_hash"] != "" {
		t.Errorf("creation payload = %v", p)
	}

	// Deletion emits with deleted=true.
	if err := os.Remove(filepath.Join(root, "CLAUDE.md")); err != nil {
		t.Fatal(err)
	}
	w.PollOnce()
	if n := rec.count(EventInstructionFileChanged); n != 3 {
		t.Fatalf("deletion emitted total %d, want 3", n)
	}
	if p := rec.last(EventInstructionFileChanged); p["deleted"] != true {
		t.Errorf("deletion payload = %v", p)
	}
}

func TestWatchCheckPathIdempotent(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := &eventRecorder{}
	w, err := NewWatcher(root, rec.sink())
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	defer w.Close()

	// Re-checking unchanged content emits nothing, however often called.
	w.checkPath("CLAUDE.md")
	w.checkPath("CLAUDE.md")
	if n := rec.count(EventInstructionFileChanged); n != 0 {
		t.Fatalf("idempotent check emitted %d events", n)
	}
}

func TestWatchRunEmitsOnWrite(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := &eventRecorder{}
	w, err := NewWatcher(root, rec.sink())
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	defer w.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	// Live fsnotify path: modify the file, expect one debounced event.
	time.Sleep(200 * time.Millisecond) // let Run start watching
	if err := os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("v1\nv2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for rec.count(EventInstructionFileChanged) == 0 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if n := rec.count(EventInstructionFileChanged); n == 0 {
		t.Fatal("Run emitted no event within 10s of write")
	}
	if p := rec.last(EventInstructionFileChanged); !strings.Contains(p["diff"].(string), "+ v2\n") {
		t.Errorf("live diff:\n%v", p["diff"])
	}
}

func TestWatchHashStorePersistsAcrossRestarts(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hashes := NewMemoryHashStore()

	rec := &eventRecorder{}
	w, err := NewWatcher(root, rec.sink())
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	w.SetWorkspaceID("ws-1")
	w.SetHashStore(hashes)

	// Edit: PollOnce emits and persists the new hash.
	if err := os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("v1\nv2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w.PollOnce()
	if n := rec.count(EventInstructionFileChanged); n != 1 {
		t.Fatalf("edit emitted %d events, want 1", n)
	}
	persisted, ok := hashes.GetHash("ws-1", "CLAUDE.md")
	if !ok || persisted != HashString("v1\nv2\n") {
		t.Fatalf("persisted hash = %q,%v; want current content hash", persisted, ok)
	}
	w.Close()

	// Restart with the same store and unchanged disk: the persisted hash
	// seeds the baseline, so PollOnce stays silent.
	rec2 := &eventRecorder{}
	w2, err := NewWatcherWithStore(root, rec2.sink(), "ws-1", hashes)
	if err != nil {
		t.Fatalf("NewWatcherWithStore: %v", err)
	}
	defer w2.Close()
	w2.PollOnce()
	if n := rec2.count(EventInstructionFileChanged); n != 0 {
		t.Fatalf("restart with unchanged content emitted %d events", n)
	}
}

func TestWatchHashStoreSurfacesEditWhileDown(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hashes := NewMemoryHashStore()
	if err := hashes.SetHash("ws-1", "CLAUDE.md", HashString("v1\n")); err != nil {
		t.Fatal(err)
	}

	// Edit while the daemon is down, then boot with persistence: the
	// persisted (stale) hash seeds the baseline so PollOnce emits.
	if err := os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("v1\nv2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := &eventRecorder{}
	w, err := NewWatcherWithStore(root, rec.sink(), "ws-1", hashes)
	if err != nil {
		t.Fatalf("NewWatcherWithStore: %v", err)
	}
	defer w.Close()
	w.PollOnce()
	if n := rec.count(EventInstructionFileChanged); n != 1 {
		t.Fatalf("edit-while-down emitted %d events, want 1", n)
	}
	if p := rec.last(EventInstructionFileChanged); p["old_hash"] != HashString("v1\n") {
		t.Errorf("old_hash = %v, want pre-restart hash", p["old_hash"])
	}
}

func TestWatchHashStoreDeletionClearsPersistedHash(t *testing.T) {
	root := t.TempDir()
	abs := filepath.Join(root, "CLAUDE.md")
	if err := os.WriteFile(abs, []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hashes := NewMemoryHashStore()
	rec := &eventRecorder{}
	w, err := NewWatcher(root, rec.sink())
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	defer w.Close()
	w.SetWorkspaceID("ws-1")
	w.SetHashStore(hashes)

	if err := os.WriteFile(abs, []byte("v1\nv2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w.PollOnce()
	if _, ok := hashes.GetHash("ws-1", "CLAUDE.md"); !ok {
		t.Fatal("edit must persist a hash")
	}
	if err := os.Remove(abs); err != nil {
		t.Fatal(err)
	}
	w.PollOnce()
	if n := rec.count(EventInstructionFileChanged); n != 2 {
		t.Fatalf("deletion emitted total %d, want 2", n)
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
