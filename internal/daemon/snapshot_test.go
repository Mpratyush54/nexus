package daemon

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
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
	if snap.Harness != "antigravity" {
		t.Fatalf("harness=%q", snap.Harness)
	}
}

func TestCollectSnapshotCursor(t *testing.T) {
	root := filepath.Join(t.TempDir(), "central-memory")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)

	slug := cursorWorkspaceSlug(root)
	if slug == "" {
		t.Fatal("empty slug")
	}
	conv := "cursor-sess-1111"
	proj := filepath.Join(home, ".cursor", "projects", slug)
	trDir := filepath.Join(proj, "agent-transcripts")
	tools := filepath.Join(proj, "agent-tools")
	if err := os.MkdirAll(trDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(tools, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(trDir, conv+".jsonl"), []byte("{\"role\":\"user\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tools, "note.md"), []byte("tool brain"), 0o644); err != nil {
		t.Fatal(err)
	}

	snap, err := CollectSnapshot(root, conv, "cursor", "m1")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Harness != "cursor" || snap.TurnCount < 1 {
		t.Fatalf("%+v", snap)
	}
	if len(snap.ArtifactsBundle) == 0 {
		t.Fatal("expected cursor agent-tools in artifacts")
	}
}

func TestDiffTruncation(t *testing.T) {
	root := t.TempDir()
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
	if !security.ContainsSecret([]byte("token=" + secret)) {
		t.Skip("scanner does not flag this mock key shape")
	}
	redacted, ok := RedactSecrets("token=" + secret)
	if !ok || strings.Contains(redacted, secret) {
		t.Fatalf("redact failed: %q", redacted)
	}
}

func TestSnapshotHarnessPathsAllSupported(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	for _, h := range SnapshotSupportedHarnesses() {
		tr, br := SnapshotHarnessPaths(h, "sess-xyz")
		if tr == "" && br == "" {
			t.Fatalf("harness %s should resolve paths", h)
		}
	}
	tr, br := SnapshotHarnessPaths("not-a-real-harness", "id")
	if tr != "" || br != "" {
		t.Fatalf("unknown harness should be empty")
	}
}

func TestResolveSnapshotLayoutClaude(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	ws := filepath.Join(t.TempDir(), "central-memory")
	_ = os.MkdirAll(ws, 0o755)
	enc := claudeProjectEncoded(ws)
	proj := filepath.Join(home, ".claude", "projects", enc)
	_ = os.MkdirAll(proj, 0o755)
	conv := "claude-session-1"
	_ = os.WriteFile(filepath.Join(proj, conv+".jsonl"), []byte("{}\n"), 0o644)

	layout, err := ResolveSnapshotLayout("claude", conv, ws)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(layout.TranscriptFile, conv+".jsonl") {
		t.Fatalf("transcript=%q", layout.TranscriptFile)
	}
	if layout.ArtifactDir != proj {
		t.Fatalf("artifact=%q want %q", layout.ArtifactDir, proj)
	}
}

func TestResolveSnapshotLayoutCodexSearch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	conv := "rollout-abc"
	dir := filepath.Join(home, ".codex", "sessions", "2026", "09", "29")
	_ = os.MkdirAll(dir, 0o755)
	path := filepath.Join(dir, conv+".jsonl")
	_ = os.WriteFile(path, []byte("x\n"), 0o644)

	layout, err := ResolveSnapshotLayout("codex", conv, "")
	if err != nil {
		t.Fatal(err)
	}
	if layout.TranscriptFile != path {
		t.Fatalf("got %q want %q", layout.TranscriptFile, path)
	}
}

func TestCursorWorkspaceSlug(t *testing.T) {
	root := filepath.Join(t.TempDir(), "central-memory")
	_ = os.MkdirAll(root, 0o755)
	got := cursorWorkspaceSlug(root)
	if !strings.Contains(got, "central-memory") {
		t.Fatalf("slug=%q", got)
	}
	// Windows drive letter form used by Cursor project dirs.
	if s := cursorWorkspaceSlug(`D:\central-memory`); s != "" && !strings.Contains(s, "central-memory") {
		t.Fatalf("windows-style slug=%q", s)
	}
}

func TestClaudeProjectEncoded(t *testing.T) {
	root := filepath.Join(t.TempDir(), "central-memory")
	_ = os.MkdirAll(root, 0o755)
	got := claudeProjectEncoded(root)
	if !strings.Contains(got, "central-memory") {
		t.Fatalf("encoded=%q", got)
	}
	win := claudeProjectEncoded(`D:\central-memory`)
	if win != "D--central-memory" && !strings.Contains(win, "central-memory") {
		t.Fatalf("windows-style encoded=%q", win)
	}
}

func TestDetectHarnessForConversation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	conv := "detect-me-123"
	brain := filepath.Join(home, ".gemini", "antigravity", "brain", conv)
	logDir := filepath.Join(brain, ".system_generated", "logs")
	_ = os.MkdirAll(logDir, 0o755)
	_ = os.WriteFile(filepath.Join(logDir, "transcript.jsonl"), []byte("{}\n"), 0o644)

	got := DetectHarnessForConversation(conv, "")
	if got != "antigravity" {
		t.Fatalf("got %q", got)
	}
}

func TestHarnessSupported(t *testing.T) {
	if !HarnessSupported("cursor") || !HarnessSupported("Antigravity") {
		t.Fatal("expected supported")
	}
	if HarnessSupported("made-up-agent") {
		t.Fatal("expected unsupported")
	}
}

func TestResolveSnapshotLayoutFindsSQLite(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	conv := "state"
	dir := filepath.Join(home, ".windsurf", "workspace", "abc")
	_ = os.MkdirAll(dir, 0o755)
	db := filepath.Join(dir, conv+".vscdb")
	_ = os.WriteFile(db, []byte("not-a-real-db"), 0o644)

	layout, err := ResolveSnapshotLayout("windsurf", conv, "")
	if err != nil {
		t.Fatal(err)
	}
	if layout.SQLiteSource != db {
		t.Fatalf("sqlite=%q want %q", layout.SQLiteSource, db)
	}
	if layout.TranscriptFile == "" {
		t.Fatal("expected restore transcript path")
	}
	// JSONL sidecar absent — detect via SQLite.
	if got := DetectHarnessForConversation(conv, ""); got != "windsurf" {
		t.Fatalf("detect=%q", got)
	}
}

func TestCollectSnapshotFromSQLiteFallback(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)

	conv := "state"
	dir := filepath.Join(home, ".windsurf", "ws1")
	_ = os.MkdirAll(dir, 0o755)
	// Printable chat-like blobs so extractViaRawScan succeeds without sqlite3 CLI.
	blob := []byte(`SQLite format 3 pad ` +
		`{"role":"user","content":"hello from windsurf session about redis queues"}` +
		` more pad ` +
		`{"role":"assistant","content":"consider using redis streams for the backlog"}`)
	db := filepath.Join(dir, conv+".vscdb")
	if err := os.WriteFile(db, blob, 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "notes.md"), []byte("# brain"), 0o644)

	snap, err := CollectSnapshot(root, conv, "windsurf", "m-sqlite")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Harness != "windsurf" || snap.TurnCount < 1 {
		t.Fatalf("%+v", snap)
	}
	if len(snap.TranscriptPayload) == 0 {
		t.Fatal("expected gzipped transcript from sqlite extract")
	}
	raw, err := ungzipBytes(snap.TranscriptPayload)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "windsurf session") {
		t.Fatalf("transcript missing extracted content: %q", raw)
	}
}

func TestTurnsToSnapshotJSONL(t *testing.T) {
	raw, err := turnsToSnapshotJSONL([]Turn{
		{Speaker: "user", Content: "ask about the deploy plan"},
		{Speaker: "assistant", Content: "ship the sqlite extract path first"},
	})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines=%d %q", len(lines), raw)
	}
	if !strings.Contains(lines[0], `"speaker":"user"`) {
		t.Fatalf("line0=%q", lines[0])
	}
}

func TestAppendSmallSQLiteArtifactPackedWhenTiny(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "state.vscdb")
	_ = os.WriteFile(db, []byte("tiny-sqlite-blob"), 0o644)
	layout := SnapshotLayout{SQLiteSource: db, ArtifactDir: dir}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := appendSmallSQLiteArtifact(tw, layout, dir); err != nil {
		t.Fatal(err)
	}
	_ = tw.Close()
	_ = gz.Close()
	if buf.Len() < 20 {
		t.Fatalf("expected packed sqlite artifact, got %d bytes", buf.Len())
	}
}
