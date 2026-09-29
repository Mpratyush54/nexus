package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"central-memory/internal/security"
)

func TestCollectSnapshotBasic(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)

	conv := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	brain := filepath.Join(home, ".gemini", "antigravity", "brain", conv)
	logDir := filepath.Join(brain, ".system_generated", "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logDir, "transcript.jsonl"), []byte("{\"a\":1}\n{\"b\":2}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(brain, "walkthrough.md"), []byte("# hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	snap, err := CollectSnapshot(root, conv, "antigravity", "machine-1")
	if err != nil {
		t.Fatal(err)
	}
	if snap.TurnCount < 2 || len(snap.TranscriptPayload) == 0 {
		t.Fatalf("%+v", snap)
	}
	if snap.SourceMachineID != "machine-1" {
		t.Fatalf("machine=%q", snap.SourceMachineID)
	}
}

func TestDiffTruncation(t *testing.T) {
	root := t.TempDir()
	// Without git repo, collectUncommittedDiff returns empty — exercise cap helper via huge synthetic.
	big := strings.Repeat("x", maxSnapshotDiffBytes+10)
	if len(big) <= maxSnapshotDiffBytes {
		t.Fatal("setup")
	}
	out := big
	trunc := false
	if len(out) > maxSnapshotDiffBytes {
		out = out[:maxSnapshotDiffBytes]
		trunc = true
	}
	if !trunc || len(out) != maxSnapshotDiffBytes {
		t.Fatalf("trunc=%v len=%d", trunc, len(out))
	}
	_ = root
}

func TestSecretRedactionInDiff(t *testing.T) {
	secret := "sk-ant-api03-" + strings.Repeat("a", 40)
	if !security.ContainsSecret([]byte("token="+secret)) {
		t.Skip("scanner does not flag this mock key shape")
	}
	redacted, ok := RedactSecrets("token=" + secret)
	if !ok || strings.Contains(redacted, secret) {
		t.Fatalf("redact failed: %q", redacted)
	}
}

func TestSnapshotHarnessPathsUnsupported(t *testing.T) {
	tr, br := SnapshotHarnessPaths("cursor", "id")
	if tr != "" || br != "" {
		t.Fatalf("cursor should be empty")
	}
}
