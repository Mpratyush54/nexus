package owned

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func isolate(t *testing.T) string {
	t.Helper()
	state := filepath.Join(t.TempDir(), "owned.json")
	prev := StatePath
	t.Cleanup(func() { StatePath = prev; ResetForTest() })
	StatePath = func() string { return state }
	ResetForTest()
	return state
}

func TestNexusOwnedTranscripts(t *testing.T) {
	state := isolate(t)

	id := "5d727dae-31b4-4c9d-90b9-96b90df255eb"
	other := "c7e18540-31b4-4c9d-90b9-96b90df255eb"
	Mark(id)
	Mark("ses_abc123")

	ownedPaths := []string{
		`C:\Users\u\.claude\projects\D--app\` + id + `.jsonl`,
		`C:\Users\u\.codex\sessions\2026\11\22\rollout-2026-11-22T07-59-00-` + id + `.jsonl`,
		`C:\Users\u\.cursor\projects\d-app\agent-transcripts\` + id + `.jsonl`,
		`C:\Users\u\.cursor\chats\0c2feaa6\` + id + `\store.db`,
		`C:\Users\u\.kimi-code\sessions\wd_001\session_` + id + `\context.jsonl`,
		`C:\Users\u\.gemini\tmp\hash\chats\session-2026-11-22T07-59-5d727dae.jsonl`,
	}
	for _, p := range ownedPaths {
		if !IsTranscript(p) {
			t.Errorf("should skip %s", p)
		}
	}
	for _, p := range []string{
		`C:\Users\u\.claude\projects\D--app\` + other + `.jsonl`,
		`C:\Users\u\.gemini\tmp\hash\chats\session-2026-11-22T07-59-c7e18540.jsonl`,
	} {
		if IsTranscript(p) {
			t.Errorf("must keep capturing %s", p)
		}
	}
	kimi := "6a3c732e-c7f9-4d63-9712-89904c6b5314"
	Mark("session_" + kimi)
	if !IsTranscript(`C:\Users\u\.kimi-code\sessions\wd_001\session_` + kimi + `\agents\main\wire.jsonl`) {
		t.Error("Kimi session_<uuid> transcript not skipped")
	}
	if nexusOwned.short["session_"] {
		t.Error("a prefixed id produced a bogus short key")
	}
	if !IsSession("ses_abc123") || IsSession("ses_other") {
		t.Fatal("opencode session ids")
	}

	// Survives a restart.
	ResetForTest()
	if _, err := os.Stat(state); err != nil {
		t.Fatal(err)
	}
	if !IsTranscript(ownedPaths[0]) {
		t.Fatal("registry not reloaded from disk")
	}
}

func TestConcurrentMarksAllPersist(t *testing.T) {
	state := isolate(t)
	const n = 64
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			Mark(fmt.Sprintf("ses_concurrent_%03d", i))
		}(i)
	}
	wg.Wait()

	ResetForTest()
	for i := 0; i < n; i++ {
		if !IsSession(fmt.Sprintf("ses_concurrent_%03d", i)) {
			t.Fatalf("id %d lost after reload", i)
		}
	}
	leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(state), "*.tmp"))
	if len(leftovers) != 0 {
		t.Fatalf("temp files left behind: %v", leftovers)
	}
}

func TestCorruptRegistryKeptAside(t *testing.T) {
	state := isolate(t)
	garbage := []byte(`{"ses_a":"2026-11-22T00:00:00Z", trunc`)
	if err := os.WriteFile(state, garbage, 0o600); err != nil {
		t.Fatal(err)
	}
	Mark("ses_new")
	kept, err := os.ReadFile(state + ".corrupt")
	if err != nil || string(kept) != string(garbage) {
		t.Fatalf("corrupt file not kept aside: %q, %v", kept, err)
	}
	ResetForTest()
	if !IsSession("ses_new") {
		t.Fatal("new mark not persisted after setting the corrupt file aside")
	}
}
