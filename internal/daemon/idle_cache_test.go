package daemon

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestPeekTranscriptCwdCacheRereadsAfterTruncate(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(p, []byte(`{"cwd":"D:\\one"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := PeekTranscriptCwd(p); got != `D:\one` {
		t.Fatalf("cwd = %q", got)
	}
	// Append: head unchanged, cached value kept.
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.WriteString(`{"cwd":"D:\\later"}` + "\n")
	_ = f.Close()
	if got := PeekTranscriptCwd(p); got != `D:\one` {
		t.Fatalf("after append cwd = %q", got)
	}
	// Replaced by a shorter file: read again.
	if err := os.WriteFile(p, []byte(`{"cwd":"D:\\two"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := PeekTranscriptCwd(p); got != `D:\two` {
		t.Fatalf("after truncate cwd = %q", got)
	}
}

func TestFileMentionsFolderCacheSeesGrowth(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(p, []byte(`{"x":"nothing here"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if fileMentionsFolder(p, "pathlab") {
		t.Fatal("unexpected match")
	}
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.WriteString(`{"cwd":"D:\\Pathlab\\src"}` + "\n")
	_ = f.Close()
	if !fileMentionsFolder(p, "Pathlab") {
		t.Fatal("growth below the peek window must be re-read")
	}
}

func TestFileHashStoreDeleteMissingDoesNotWrite(t *testing.T) {
	p := filepath.Join(t.TempDir(), "h.json")
	s, err := NewFileHashStore(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteHash("ws", "CLAUDE.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("deleting an absent hash wrote the file: %v", err)
	}
}

func TestClaudeArtifactsSkipOtherSessions(t *testing.T) {
	proj := t.TempDir()
	me := "11111111-1111-1111-1111-111111111111"
	other := "22222222-2222-2222-2222-222222222222"
	write := func(rel, body string) {
		full := filepath.Join(proj, filepath.FromSlash(rel))
		_ = os.MkdirAll(filepath.Dir(full), 0o700)
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(me+".jsonl", "mine\n")
	write(me+"/subagents/a.jsonl", "sub\n")
	write(other+".jsonl", "theirs\n")
	write(other+"/subagents/b.jsonl", "theirs\n")
	write("memory/notes.md", "notes\n")
	layout := SnapshotLayout{Harness: "claude", ConversationID: me, TranscriptFile: filepath.Join(proj, me+".jsonl"), ArtifactDir: proj}
	names := func() []string {
		raw, err := tarGzipArtifacts(layout)
		if err != nil {
			t.Fatal(err)
		}
		zr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		tr := tar.NewReader(zr)
		var out []string
		for {
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, h.Name)
		}
		sort.Strings(out)
		return out
	}
	got := names()
	want := []string{me + "/subagents/a.jsonl", "memory/notes.md"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("artifacts = %v, want %v", got, want)
	}
	// A changed file must not be served from the cache.
	write("memory/extra.md", "more\n")
	if got := names(); len(got) != 3 {
		t.Fatalf("after change artifacts = %v", got)
	}
}
