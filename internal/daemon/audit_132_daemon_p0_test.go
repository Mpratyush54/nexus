package daemon

// Regression tests for issue #132 P0s: session-batch drain, secret
// screening on all passive paths, git allowlist escapes, -exec denial.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCheckIdleDrainsBatch(t *testing.T) {
	h := NewHarvesterWithPoll(t.TempDir(), nil, time.Second, time.Minute)
	path := filepath.Join(t.TempDir(), "conv.jsonl")
	if err := os.WriteFile(path, []byte("{\"speaker\":\"user\",\"content\":\"we decided on redis for caching layers\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := h.TailFile(path); err != nil {
		t.Fatal(err)
	}
	h.lastActive[path] = h.now().UTC().Add(-10 * h.IdleTimeout)
	out := h.CheckIdle()
	if len(out) != 1 {
		t.Fatalf("idle events = %d, want 1", len(out))
	}
	h.mu.Lock()
	left := len(h.batch[path])
	h.mu.Unlock()
	if left != 0 {
		t.Fatalf("batch retained %d turns after idle emit, want drained", left)
	}
	// Second idle pass must not re-emit (completed, and batch is empty).
	if again := h.CheckIdle(); len(again) != 0 {
		t.Fatalf("second idle emitted %d events, want 0", len(again))
	}
}

func TestTurnPayloadRedactsSecrets(t *testing.T) {
	p := turnPayload("conversation_turn", "claude", "/x/s.jsonl", Turn{
		Speaker: "user",
		Content: "the api key is sk-ant-1234567890abcdefghij use it well",
	})
	if got, _ := p["content"].(string); strings.Contains(got, "sk-ant-1234567890abcdefghij") {
		t.Fatalf("secret survived turn payload: %q", got)
	}
}

func TestWatcherDiffRedacted(t *testing.T) {
	dir := t.TempDir()
	rel := "CLAUDE.md"
	if err := os.WriteFile(filepath.Join(dir, rel), []byte("key=ghp_abcdefghijklmnopqrstuvwxyz1234567890 ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	w := NewWatcher(dir, nil, time.Second, nil)
	w.SeedBaseline()
	if err := os.WriteFile(filepath.Join(dir, rel), []byte("key=ghp_abcdefghijklmnopqrstuvwxyz1234567890 rotated"), 0o644); err != nil {
		t.Fatal(err)
	}
	evs := w.CheckOnce()
	if len(evs) == 0 {
		t.Fatal("expected INSTRUCTION_FILE_CHANGED")
	}
	diff, _ := evs[0].Payload["diff"].(string)
	if strings.Contains(diff, "ghp_abcdefghijklmnopqrstuvwxyz1234567890") {
		t.Fatalf("secret survived watcher diff: %q", diff)
	}
}

func TestGitRepoLocationFlagsDenied(t *testing.T) {
	for _, argv := range [][]string{
		{"git", "status", "--git-dir=/outside"},
		{"git", "log", "--work-tree=/outside"},
		{"git", "status", "--bare"},
		{"git", "log", "--namespace=evil"},
		{"go", "test", "-exec", "/tmp/evil"},
		{"cargo", "test", "-exec=evil"},
	} {
		if IsAllowed(argv) {
			t.Errorf("%v should be denied", argv)
		}
	}
	for _, argv := range [][]string{
		{"git", "status"},
		{"git", "log", "--oneline"},
		{"go", "test", "./..."},
	} {
		if !IsAllowed(argv) {
			t.Errorf("%v should be allowed", argv)
		}
	}
}
