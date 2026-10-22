package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUnknownAgentDateIsNotReplacedByCaptureTime(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	h, events := collectHarvester(root)
	now := time.Now().UTC()
	h.now = func() time.Time { return now }
	h.IdleTimeout = time.Minute
	writeLines(t, path, `{"role":"user","content":"no source timestamp"}`)
	turns, err := h.TailFile(path)
	if err != nil || len(turns) != 1 || !turns[0].Timestamp.IsZero() {
		t.Fatalf("invented source date: %+v %v", turns, err)
	}
	if _, exists := (*events)[0].Payload["timestamp"]; exists {
		t.Fatal("receipt date escaped into original timestamp")
	}
	now = now.Add(2 * time.Minute)
	complete := h.CheckIdle()
	if len(complete) != 1 {
		t.Fatalf("idle detection stopped working: %v", complete)
	}
	rows := complete[0].Payload["turns"].([]any)
	if _, exists := rows[0].(map[string]any)["timestamp"]; exists {
		t.Fatal("archived turn invented a source date")
	}
}

func TestSQLiteUnknownSourceTimeStillDrivesIdleDetection(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "opencode.db")
	if err := os.WriteFile(path, []byte("test-fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	h, events := collectHarvester(root)
	h.now = func() time.Time { return now }
	h.IdleTimeout = time.Minute
	h.agents[path] = "opencode"
	h.SetSQLiteExtractor("opencode", &fakeSQLiteExtractor{epoch: now.Add(time.Hour), turns: []Turn{{Speaker: "user", Content: "undated sqlite message", SessionID: "session-unknown-date"}}})
	st, _ := os.Stat(path)
	if !h.TrackSQLite(path, st) {
		t.Fatal("SQLite extraction did not run")
	}
	if len(*events) != 1 {
		t.Fatalf("events = %v", *events)
	}
	if _, exists := (*events)[0].Payload["timestamp"]; exists {
		t.Fatal("SQLite receipt time was exported as source time")
	}
	now = now.Add(2 * time.Minute)
	if len(h.CheckIdle()) != 1 {
		t.Fatal("undated SQLite message no longer completes its idle batch")
	}
}
