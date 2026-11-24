package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestCodeHunksBackfillReplaysRecentTranscript(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "main.go")
	if err := os.WriteFile(file, []byte("package main\n\nfunc main() { println(2) }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := t.TempDir()
	line, _ := json.Marshal(map[string]any{
		"type": "assistant", "cwd": root, "sessionId": "sess-1", "timestamp": "2026-11-20T10:00:00Z",
		"message": map[string]any{"role": "assistant", "content": []any{map[string]any{
			"type": "tool_use", "id": "toolu_9", "name": "Edit",
			"input": map[string]any{"file_path": file, "old_string": "println(1)", "new_string": "println(2)"},
		}}},
	})
	transcript := filepath.Join(store, "sess-1.jsonl")
	if err := os.WriteFile(transcript, append(line, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(store, "old.jsonl")
	_ = os.WriteFile(old, append(line, '\n'), 0o644)
	stale := time.Now().Add(-2 * codeBackfillMaxAge)
	_ = os.Chtimes(old, stale, stale)

	h := NewHarvesterWithPoll(root, nil, time.Second, time.Second)
	h.Sources = []TranscriptSource{{Agent: "claude", Dirs: []string{store}, Format: FormatJSONL, CwdMatch: true}}
	files := codeBackfillCandidates(h, time.Now())
	if len(files) != 1 || files[0].path != transcript {
		t.Fatalf("candidates = %+v", files)
	}

	var mu sync.Mutex
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		mu.Lock()
		bodies = append(bodies, b)
		mu.Unlock()
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	hs := NewHTTPMemoryStore(srv.URL, "tok", testProjectUUID)
	rep := NewCodeLinksReporter(root, hs.mirrorConnection)
	if n := replayTranscriptEdits(context.Background(), rep, files[0], nil); n != 1 {
		t.Fatalf("notes = %d", n)
	}
	rep.Flush(context.Background())
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 1 || bodies[0]["session_native_id"] != "sess-1" {
		t.Fatalf("bodies = %v", bodies)
	}
	changes, _ := bodies[0]["changes"].([]any)
	if len(changes) != 1 {
		t.Fatalf("changes = %v", bodies[0])
	}
	c := changes[0].(map[string]any)
	if c["path"] != "main.go" || c["start_line"] != float64(3) || c["tool_call_id"] != "toolu_9" || c["at"] != "2026-11-20T10:00:00Z" {
		t.Fatalf("change = %v", c)
	}
}
