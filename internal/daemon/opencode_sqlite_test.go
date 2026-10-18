package daemon

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenCodeSQLiteSeparatesWorkspaceSessionsAndTools(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, schema := range []string{
		`CREATE TABLE session(id TEXT PRIMARY KEY, directory TEXT, title TEXT)`,
		`CREATE TABLE message(id TEXT PRIMARY KEY, session_id TEXT, data TEXT)`,
		`CREATE TABLE part(id TEXT PRIMARY KEY, message_id TEXT, session_id TEXT, data TEXT, time_created INTEGER)`,
	} {
		if _, err := db.Exec(schema); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []struct{ id, directory, title string }{
		{"ses-one", `D:/central-memory`, "Fix the sync"},
		{"ses-two", `D:\central-memory`, "Test capture"},
		{"ses-other", `D:/different`, "Unrelated"},
	} {
		if _, err := db.Exec(`INSERT INTO session VALUES(?,?,?)`, row.id, row.directory, row.title); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []struct{ id, sessionID, role string }{
		{"msg-one", "ses-one", "assistant"},
		{"msg-two", "ses-two", "user"},
		{"msg-other", "ses-other", "user"},
	} {
		if _, err := db.Exec(`INSERT INTO message VALUES(?,?,?)`, row.id, row.sessionID, `{"role":"`+row.role+`"}`); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []struct {
		id, messageID, sessionID, data string
		at                             int64
	}{
		{"part-one", "msg-one", "ses-one", `{"type":"text","text":"I updated the sync path."}`, 1789700000000},
		{"part-tool", "msg-one", "ses-one", `{"type":"tool","tool":"bash","state":{"input":{"command":"go test ./...","workdir":"D:/central-memory"}}}`, 1789700000001},
		{"part-file", "msg-one", "ses-one", `{"type":"tool","tool":"read","state":{"input":{"filePath":"D:/central-memory/go.mod"}}}`, 1789700000002},
		{"part-two", "msg-two", "ses-two", `{"type":"text","text":"Check OpenCode capture"}`, 1789700001000},
		{"part-other", "msg-other", "ses-other", `{"type":"text","text":"Do not import me"}`, 1789700002000},
	} {
		if _, err := db.Exec(`INSERT INTO part VALUES(?,?,?,?,?)`, row.id, row.messageID, row.sessionID, row.data, row.at); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	ex := &openCodeSQLite{Root: `D:\central-memory`}
	turns, err := ex.ExtractNewRows(path, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 2 {
		t.Fatalf("got %d turns, want 2: %+v", len(turns), turns)
	}
	if turns[0].SessionID != "ses-one" || turns[0].SessionTitle != "Fix the sync" || len(turns[0].ToolCalls) != 2 {
		t.Fatalf("session/tool mapping lost: %+v", turns[0])
	}
	if got := turns[0].ToolCalls[0]["input"].(map[string]any)["workdir"]; got != "D:/central-memory" {
		t.Fatalf("bash workdir lost: %+v", turns[0].ToolCalls[0])
	}
	if got := turns[0].ToolCalls[1]["input"].(map[string]any)["filePath"]; got != "D:/central-memory/go.mod" {
		t.Fatalf("read filePath lost: %+v", turns[0].ToolCalls[1])
	}
	parsed := ParseToolCalls(turnPayload("conversation_turn", "opencode", path, turns[0]), 0)
	if len(parsed) != 2 || parsed[0].WorkingDirectory != "D:/central-memory" || parsed[1].FilePath == "" {
		t.Fatalf("OpenCode file/command provenance lost: %+v", parsed)
	}
	if turns[1].SessionID != "ses-two" {
		t.Fatalf("second session lost: %+v", turns[1])
	}
	if got := extractOpenCodeSessionTurns(path, `D:\central-memory`, "ses-two"); len(got) != 1 || got[0].SessionID != "ses-two" {
		t.Fatalf("snapshot mixed sessions: %+v", got)
	}
	if got, err := ex.ExtractNewRows(path, time.UnixMilli(1789700001000)); err != nil || len(got) != 1 || got[0].SessionID != "ses-two" {
		t.Fatalf("incremental read: %+v, %v", got, err)
	}
	h, events := collectHarvester(`D:\central-memory`)
	h.agents[path] = "opencode"
	h.SetSQLiteExtractor("opencode", ex)
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !h.TrackSQLite(path, st) {
		t.Fatal("first OpenCode scan did not harvest sessions")
	}
	seen := map[string]bool{}
	for _, event := range *events {
		if event.Type == EventConversationTurn {
			seen[event.Payload["session_id"].(string)] = true
		}
	}
	if !seen["ses-one"] || !seen["ses-two"] || seen["opencode"] {
		t.Fatalf("OpenCode sessions were merged: %+v", seen)
	}
}
