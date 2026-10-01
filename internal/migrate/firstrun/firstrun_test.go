package firstrun

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImportDaemonStateCopiesKnownFiles(t *testing.T) {
	srcRoot := t.TempDir()
	cm := filepath.Join(srcRoot, ".central-memory")
	if err := os.MkdirAll(cm, 0o700); err != nil {
		t.Fatal(err)
	}
	mustWrite := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(cm, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("offsets.json", `{"a.jsonl":12}`)
	mustWrite("daemon.state.json", `{"v":1}`)
	mustWrite("daemon.offsets.json", `{"b":3}`)
	mustWrite("spool.json", `[]`)
	mustWrite("unrelated.txt", "skip me")

	outbox := t.TempDir()
	res, err := ImportDaemonState(srcRoot, outbox)
	if err != nil {
		t.Fatalf("ImportDaemonState: %v", err)
	}
	if res.Source != cm {
		t.Fatalf("source = %q, want %q", res.Source, cm)
	}
	wantCopied := map[string]bool{
		"offsets.json":        true,
		"daemon.state.json":   true,
		"daemon.offsets.json": true,
		"spool.json":          true,
	}
	for _, name := range res.CopiedFiles {
		if !wantCopied[name] {
			t.Errorf("unexpected copy %q", name)
		}
		delete(wantCopied, name)
	}
	for name := range wantCopied {
		t.Errorf("missing copy %q (got %v)", name, res.CopiedFiles)
	}

	dest := filepath.Join(outbox, "migrated-daemon", "offsets.json")
	raw, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read copied offsets: %v", err)
	}
	if string(raw) != `{"a.jsonl":12}` {
		t.Fatalf("offsets body = %q", raw)
	}
	if _, err := os.Stat(filepath.Join(outbox, "migrated-daemon", "unrelated.txt")); err == nil {
		t.Error("unrelated.txt should not be copied")
	}
}

func TestImportDaemonStateMissingSourceSkips(t *testing.T) {
	outbox := t.TempDir()
	missing := filepath.Join(t.TempDir(), "nope", ".central-memory")
	res, err := ImportDaemonState(missing, outbox)
	if err != nil {
		t.Fatalf("missing source should be best-effort, got %v", err)
	}
	if len(res.Skipped) == 0 {
		t.Error("expected skipped note for missing source")
	}
	if len(res.CopiedFiles) != 0 {
		t.Errorf("copied = %v, want empty", res.CopiedFiles)
	}
}

func TestImportDaemonStateDirectVaultPath(t *testing.T) {
	cm := t.TempDir()
	if err := os.WriteFile(filepath.Join(cm, "harvest_offsets.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	outbox := t.TempDir()
	res, err := ImportDaemonState(cm, outbox)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range res.CopiedFiles {
		if n == "harvest_offsets.json" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected harvest_offsets.json copied, got %v", res.CopiedFiles)
	}
}

func TestDetectAndClearAutostartFakeFile(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "run.txt")
	body := strings.Join([]string{
		"NexusDesktop=C:\\Apps\\nexus-desktop.exe",
		"OldDaemon=\"C:\\Nexus\\bin\\nexus-daemon.exe\" -root D:\\ws",
		"Other=notepad.exe",
		"",
	}, "\n")
	if err := os.WriteFile(fake, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvAutostartFile, fake)

	hits, err := DetectDaemonAutostart()
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Name != "OldDaemon" {
		t.Fatalf("hits = %+v, want OldDaemon only", hits)
	}

	n, err := ClearDaemonAutostart()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("cleared = %d, want 1", n)
	}
	raw, err := os.ReadFile(fake)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if strings.Contains(strings.ToLower(s), "nexus-daemon") {
		t.Fatalf("daemon entry still present:\n%s", s)
	}
	if !strings.Contains(s, "NexusDesktop=") || !strings.Contains(s, "Other=") {
		t.Fatalf("non-daemon entries lost:\n%s", s)
	}

	hits2, err := DetectDaemonAutostart()
	if err != nil {
		t.Fatal(err)
	}
	if len(hits2) != 0 {
		t.Fatalf("after clear, hits = %+v", hits2)
	}
}

func TestRunCombinesImportAndClear(t *testing.T) {
	srcRoot := t.TempDir()
	cm := filepath.Join(srcRoot, ".central-memory")
	if err := os.MkdirAll(cm, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cm, "offsets.json"), []byte(`{"x":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(t.TempDir(), "run.txt")
	if err := os.WriteFile(fake, []byte("D=nexus-daemon.exe\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvAutostartFile, fake)

	outbox := t.TempDir()
	r, err := Run(srcRoot, outbox, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Autostart) != 1 {
		t.Fatalf("autostart = %+v", r.Autostart)
	}
	if r.AutostartCleared != 1 {
		t.Fatalf("cleared = %d", r.AutostartCleared)
	}
	if len(r.Import.CopiedFiles) == 0 {
		t.Fatal("expected imported files")
	}
	joined := strings.Join(r.Notes, " ")
	if !strings.Contains(joined, ":7272") {
		t.Error("notes should mention not recommending :7272")
	}
}

func TestImportEmptyOutboxErrors(t *testing.T) {
	if _, err := ImportDaemonState(t.TempDir(), ""); err == nil {
		t.Error("empty outbox must error")
	}
}
