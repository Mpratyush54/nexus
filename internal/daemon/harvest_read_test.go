package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFormatTranscriptPreviewJSONL(t *testing.T) {
	raw := []byte(strings.Join([]string{
		`{"role":"user","content":"Hello Redis"}`,
		`{"role":"assistant","content":"Use Redis for pub/sub."}`,
		`{"type":"tool_result","content":"skip"}`,
		`{"role":"user","message":{"content":[{"type":"text","text":"Ship it"}]}}`,
	}, "\n"))
	got := FormatTranscriptPreview(raw, 10)
	for _, need := range []string{"USER", "ASSISTANT", "Hello Redis", "Ship it", "turns"} {
		if !strings.Contains(got, need) {
			t.Fatalf("missing %q in:\n%s", need, got)
		}
	}
}

func TestReadHarvestFileAllowlist(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sess.jsonl")
	body := `{"role":"user","content":"hi from harvest"}` + "\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(dir, "secret.txt")
	if err := os.WriteFile(outside, []byte("nope"), 0o600); err != nil {
		t.Fatal(err)
	}

	d := &Daemon{Root: dir}
	if _, _, err := d.ReadHarvestFile(p); err == nil {
		t.Fatal("expected reject without allowlist")
	}
	if _, _, err := d.ReadHarvestFile("relative.jsonl"); err == nil {
		t.Fatal("relative path must fail")
	}

	d.harvestAllowExtra = []string{p}
	data, abs, err := d.ReadHarvestFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if abs != filepath.Clean(p) || !strings.Contains(string(data), "hi from harvest") {
		t.Fatalf("%q %q", abs, data)
	}
	if _, _, err := d.ReadHarvestFile(outside); err == nil {
		t.Fatal("outside secret must fail")
	}
}

func TestFormatTranscriptPreviewEmpty(t *testing.T) {
	got := FormatTranscriptPreview([]byte("not json at all\njust text"), 5)
	if !strings.Contains(got, "raw content") && !strings.Contains(got, "just text") {
		t.Fatalf("%s", got)
	}
}
