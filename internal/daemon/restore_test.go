package daemon

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRestoreSessionRejectsUnsupportedHarness(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"harness":                "not-real",
			"conversation_id":        "c1",
			"turn_count":             1,
			"transcript_payload_b64": "",
		})
	}))
	defer srv.Close()

	ws := t.TempDir()
	_ = os.MkdirAll(filepath.Join(ws, ".git"), 0o755)
	_, err := RestoreSession(t.Context(), RestoreOptions{
		SessionID: "s1", Workspace: ws, Force: true,
		ServerURL: srv.URL, Token: "t",
		Harness: "not-real",
	})
	if err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("err=%v", err)
	}
}

func TestRestoreSessionAcceptsCursor(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)

	ws := filepath.Join(t.TempDir(), "central-memory")
	_ = os.MkdirAll(filepath.Join(ws, ".git"), 0o755)

	trRaw := []byte("{\"role\":\"user\",\"content\":\"hi\"}\n")
	gzTr, err := gzipBytes(trRaw)
	if err != nil {
		t.Fatal(err)
	}
	art := mustTarGzip(t, map[string]string{"agent-tools/note.md": "# tool note"})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"harness":                "cursor",
			"conversation_id":        "restore-cursor-1",
			"turn_count":             1,
			"transcript_payload_b64": base64.StdEncoding.EncodeToString(gzTr),
			"artifacts_bundle_b64":   base64.StdEncoding.EncodeToString(art),
		})
	}))
	defer srv.Close()

	msg, err := RestoreSession(t.Context(), RestoreOptions{
		SessionID: "s1", Workspace: ws, Force: true,
		ServerURL: srv.URL, Token: "t", Harness: "cursor",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "cursor") {
		t.Fatalf("msg=%q", msg)
	}
	slug := cursorWorkspaceSlug(ws)
	trPath := filepath.Join(home, ".cursor", "projects", slug, "agent-transcripts", "restore-cursor-1.jsonl")
	got, err := os.ReadFile(trPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, trRaw) {
		t.Fatalf("transcript mismatch: %q", got)
	}
	note := filepath.Join(home, ".cursor", "projects", slug, "agent-tools", "note.md")
	if _, err := os.Stat(note); err != nil {
		t.Fatalf("artifact missing: %v", err)
	}
}

func TestRestoreSessionAcceptsAntigravity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	ws := t.TempDir()
	_ = os.MkdirAll(filepath.Join(ws, ".git"), 0o755)

	conv := "ag-restore-1"
	trRaw := []byte("{\"a\":1}\n")
	gzTr, _ := gzipBytes(trRaw)
	art := mustTarGzip(t, map[string]string{"walkthrough.md": "# restored"})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"harness":                "antigravity",
			"conversation_id":        conv,
			"turn_count":             1,
			"transcript_payload_b64": base64.StdEncoding.EncodeToString(gzTr),
			"artifacts_bundle_b64":   base64.StdEncoding.EncodeToString(art),
		})
	}))
	defer srv.Close()

	if _, err := RestoreSession(t.Context(), RestoreOptions{
		SessionID: "s1", Workspace: ws, Force: true,
		ServerURL: srv.URL, Token: "t",
	}); err != nil {
		t.Fatal(err)
	}
	trPath := filepath.Join(home, ".gemini", "antigravity", "brain", conv, ".system_generated", "logs", "transcript.jsonl")
	got, err := os.ReadFile(trPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, trRaw) {
		t.Fatalf("got %q", got)
	}
	if _, err := os.Stat(filepath.Join(home, ".gemini", "antigravity", "brain", conv, "walkthrough.md")); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreSessionDryRunClaude(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	ws := filepath.Join(t.TempDir(), "proj")
	_ = os.MkdirAll(filepath.Join(ws, ".git"), 0o755)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"harness": "claude", "conversation_id": "c1", "turn_count": 0,
		})
	}))
	defer srv.Close()

	msg, err := RestoreSession(t.Context(), RestoreOptions{
		SessionID: "s1", Workspace: ws, Force: true, DryRun: true,
		ServerURL: srv.URL, Token: "t", Harness: "claude",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "Dry-run") || !strings.Contains(msg, "claude") {
		t.Fatalf("msg=%q", msg)
	}
	// Dry-run must not create files.
	enc := claudeProjectEncoded(ws)
	if _, err := os.Stat(filepath.Join(home, ".claude", "projects", enc)); !os.IsNotExist(err) {
		t.Fatalf("dry-run wrote files: %v", err)
	}
}

func mustTarGzip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}
