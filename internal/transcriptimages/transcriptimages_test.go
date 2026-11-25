package transcriptimages

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var pngBytes = append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)

func line(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n"
}

func claudeImage(data []byte) map[string]any {
	return map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": base64.StdEncoding.EncodeToString(data)}}
}

func writeTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "")), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestListAndExtractClaudeUserAndToolImages(t *testing.T) {
	first := line(t, map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": "hello"}})
	user := line(t, map[string]any{"type": "user", "timestamp": "2026-11-25T10:00:00Z", "message": map[string]any{"role": "user", "content": []any{claudeImage(pngBytes), map[string]any{"type": "text", "text": "what is this"}}}})
	tool := line(t, map[string]any{"type": "user",
		"message":       map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": []any{claudeImage(pngBytes)}}}},
		"toolUseResult": map[string]any{"content": []any{claudeImage(pngBytes)}}, // duplicate copy: not listed twice
	})
	path := writeTranscript(t, first, user, tool)
	refs, err := List(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 {
		t.Fatalf("want 2 images, got %+v", refs)
	}
	// Position matches capture's turn index: the end of the line.
	if refs[0].Offset != int64(len(first)) || refs[0].Position != int64(len(first)+len(user)) || refs[0].Role != "user" || refs[0].At == "" {
		t.Fatalf("user image ref %+v", refs[0])
	}
	if refs[1].Offset != int64(len(first)+len(user)) || refs[1].Role != "tool" {
		t.Fatalf("tool image ref %+v", refs[1])
	}
	media, data, err := Extract(path, refs[1].Offset, refs[1].Index)
	if err != nil || media != "image/png" || len(data) != len(pngBytes) {
		t.Fatalf("extract: %v %s %d", err, media, len(data))
	}
	if _, _, err := Extract(path, refs[0].Offset, 5); err == nil {
		t.Fatal("missing index should fail")
	}
}

func TestCodexDataURIImage(t *testing.T) {
	uri := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes)
	rec := line(t, map[string]any{"type": "response_item", "payload": map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_image", "image_url": uri}}}})
	path := writeTranscript(t, rec)
	refs, err := List(path)
	if err != nil || len(refs) != 1 || refs[0].Role != "user" || refs[0].MediaType != "image/png" {
		t.Fatalf("refs %+v err %v", refs, err)
	}
}

func TestExtractRejectsNonImagesAndDamagedData(t *testing.T) {
	bad := line(t, map[string]any{"message": map[string]any{"role": "user", "content": []any{claudeImage([]byte("not an image at all"))}}})
	path := writeTranscript(t, bad)
	if _, _, err := Extract(path, 0, 0); err == nil {
		t.Fatal("a non-image payload must be refused")
	}
}

func TestOversizedLineIsSkippedButOffsetsStayExact(t *testing.T) {
	huge := `{"message":{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + strings.Repeat("A", MaxLineBytes) + `"}}]}}` + "\n"
	small := line(t, map[string]any{"message": map[string]any{"role": "user", "content": []any{claudeImage(pngBytes)}}})
	path := writeTranscript(t, huge, small)
	refs, err := List(path)
	if err != nil || len(refs) != 1 || refs[0].Offset != int64(len(huge)) {
		t.Fatalf("refs %+v err %v", refs, err)
	}
	if _, _, err := Extract(path, refs[0].Offset, 0); err != nil {
		t.Fatal(err)
	}
}

func TestValidNativeIDAndFind(t *testing.T) {
	for _, bad := range []string{"", "../x", `a\b`, "abc", strings.Repeat("a", 101)} {
		if ValidNativeID(bad) {
			t.Fatalf("%q accepted", bad)
		}
	}
	home := t.TempDir()
	homeDir = func() (string, error) { return home, nil }
	defer func() { homeDir = os.UserHomeDir }()
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	id := "0f8fad5b-d9cb-469f-a165-70867728950e"
	claude := filepath.Join(home, ".claude", "projects", "D--proj", id+".jsonl")
	codex := filepath.Join(home, ".codex", "sessions", "2026", "11", "25", "rollout-2026-11-25T10-00-00-"+id+".jsonl")
	for _, p := range []string{claude, codex} {
		_ = os.MkdirAll(filepath.Dir(p), 0700)
		_ = os.WriteFile(p, []byte("{}\n"), 0600)
	}
	if Find("claude", id) != claude || Find("codex", id) != codex || Find("cursor", id) != "" || Find("claude", "../../etc") != "" {
		t.Fatal("find did not locate the right transcripts")
	}
}
