package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

const uploaderGo = "package up\n" + //  1
	"\n" + //                                  2
	"import \"time\"\n" + //                   3
	"\n" + //                                  4
	"func Upload(req Req) error {\n" + //      5
	"\tvar err error\n" + //                   6
	"\tfor i := 0; i < 3; i++ {\n" + //        7
	"\t\tif err = put(req); err == nil {\n" + // 8
	"\t\t\tbreak\n" + //                       9
	"\t\t}\n" + //                             10
	"\t\ttime.Sleep(time.Second)\n" + //       11
	"\t}\n" + //                               12
	"\treturn err\n" + //                      13
	"}\n" //                                   14

func writeFixture(t *testing.T, root, rel, content string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func hunkSummary(changes []codeChange) map[string][]string {
	out := map[string][]string{}
	for _, c := range changes {
		out[c.ToolCallID] = append(out[c.ToolCallID], fmt.Sprintf("%s:%d-%d+%d-%d", c.Path, c.StartLine, c.EndLine, c.AddedLines, c.RemovedLines))
	}
	for _, v := range out {
		sort.Strings(v)
	}
	return out
}

func TestEditHunksForEachToolShape(t *testing.T) {
	root := filepath.Join(t.TempDir(), "proj")
	const up = "internal/up/uploader.go"
	writeFixture(t, root, up, uploaderGo)
	writeFixture(t, root, "docs/a.md", "hello\nworld\n")
	writeFixture(t, root, "docs/b.md", "notes\n")
	writeFixture(t, root, "win.txt", "a\r\nB\r\nB2\r\nc\r\n")
	writeFixture(t, root, "rep.txt", "a\nx\nb\nx\n")
	abs := func(rel string) string { return filepath.Join(root, filepath.FromSlash(rel)) }
	body := strings.Join(strings.Split(uploaderGo, "\n")[4:14], "\n") // lines 5-14

	calls := []map[string]any{
		// Claude Code Edit: lines 6-13 replaced "return put(req)".
		{"id": "claude-edit", "name": "Edit", "input": map[string]any{"file_path": abs(up),
			"old_string": "func Upload(req Req) error {\n\treturn put(req)\n}", "new_string": body}},
		// Claude Code MultiEdit: two pieces, each a partial line.
		{"id": "claude-multi", "name": "MultiEdit", "input": map[string]any{"file_path": abs(up), "edits": []any{
			map[string]any{"old_string": `import "fmt"`, "new_string": `import "time"`},
			map[string]any{"old_string": "i < 5", "new_string": "i < 3"},
		}}},
		// Claude Code Write: the whole file.
		{"id": "claude-write", "name": "Write", "input": map[string]any{"file_path": abs(up), "content": uploaderGo}},
		// replace_all: every occurrence.
		{"id": "replace-all", "name": "Edit", "input": map[string]any{"file_path": abs("rep.txt"), "old_string": "y", "new_string": "x", "replace_all": true}},
		// CRLF file, LF edit text.
		{"id": "crlf", "name": "Edit", "input": map[string]any{"file_path": abs("win.txt"), "old_string": "b", "new_string": "B\nB2"}},
		// Codex apply_patch (raw string): one changed line between context.
		{"id": "codex-patch", "name": "apply_patch", "arguments": "*** Begin Patch\n*** Update File: " + up + "\n@@ func Upload(req Req) error {\n \tvar err error\n-\tfor i := 0; i < 5; i++ {\n+\tfor i := 0; i < 3; i++ {\n \t\tif err = put(req); err == nil {\n*** End Patch"},
		// Codex shell running apply_patch, adding a file.
		{"id": "codex-shell", "name": "shell", "args": map[string]any{"command": []any{"apply_patch", "*** Begin Patch\n*** Add File: docs/a.md\n+hello\n+world\n*** End Patch"}}},
		// Unified diff through a patch tool (header line numbers).
		{"id": "unified", "name": "patch", "input": map[string]any{"patch": "--- a/" + up + "\n+++ b/" + up + "\n@@ -10,3 +10,3 @@\n \t\t}\n-\t\ttime.Sleep(1)\n+\t\ttime.Sleep(time.Second)\n \t}\n"}},
		// Cursor search_replace and edit_file with existing-code markers.
		{"id": "cursor-sr", "name": "search_replace", "args": map[string]any{"file_path": up, "old_string": "\treturn nil", "new_string": "\treturn err"}},
		{"id": "cursor-edit", "name": "edit_file", "args": map[string]any{"target_file": up, "code_edit": "// ... existing code ...\n\t\ttime.Sleep(time.Second)\n// ... existing code ..."}},
		// OpenCode edit / write.
		{"id": "opencode-edit", "name": "edit", "input": map[string]any{"filePath": abs(up), "oldString": "func Upload(r Req) error {", "newString": "func Upload(req Req) error {"}},
		{"id": "opencode-write", "name": "write", "input": map[string]any{"filePath": abs("docs/b.md"), "content": "notes\n"}},
		// Windsurf replace_file_content chunks.
		{"id": "windsurf", "name": "replace_file_content", "args": map[string]any{"TargetFile": abs(up), "ReplacementChunks": []any{
			map[string]any{"TargetContent": "package upload", "ReplacementContent": "package up"}}}},
		// Anthropic text editor tool.
		{"id": "str-replace", "name": "str_replace_editor", "input": map[string]any{"command": "str_replace", "path": abs(up), "old_str": "\t\t\treturn", "new_str": "\t\t\tbreak"}},
		// Not found in the file now (a later edit replaced it): no hunk.
		{"id": "gone", "name": "Edit", "input": map[string]any{"file_path": abs(up), "old_string": "a", "new_string": "no such text"}},
		// Reads, notebooks, outside the workspace, ignored folders: nothing.
		{"id": "read", "name": "Read", "input": map[string]any{"file_path": abs(up)}},
		{"id": "outside", "name": "Write", "input": map[string]any{"file_path": filepath.Join(filepath.Dir(root), "x.go"), "content": "x"}},
		{"id": "env", "name": "Edit", "input": map[string]any{"file_path": abs(".env"), "old_string": "A=1", "new_string": "A=2"}},
	}
	at := time.Date(2026, 11, 20, 9, 30, 0, 0, time.UTC)
	ev := turnWithCalls(calls...)
	ev.CreatedAt = at
	edits := EditsFromEvents([]Event{ev}, root)
	changes, hashes := locateEdits(root, edits)
	got := hunkSummary(changes)
	want := map[string][]string{
		"claude-edit":    {up + ":6-13+8-1"},
		"claude-multi":   {up + ":3-3+1-1", up + ":7-7+1-1"},
		"claude-write":   {up + ":1-14+14-0"},
		"replace-all":    {"rep.txt:2-2+1-1", "rep.txt:4-4+1-1"},
		"crlf":           {"win.txt:2-3+2-1"},
		"codex-patch":    {up + ":7-7+1-1"},
		"codex-shell":    {"docs/a.md:1-2+2-0"},
		"unified":        {up + ":11-11+1-1"},
		"cursor-sr":      {up + ":13-13+1-1"},
		"cursor-edit":    {up + ":11-11+1-0"},
		"opencode-edit":  {up + ":5-5+1-1"},
		"opencode-write": {"docs/b.md:1-1+1-0"},
		"windsurf":       {up + ":1-1+1-1"},
		"str-replace":    {up + ":9-9+1-1"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("hunks =\n%v\nwant\n%v", got, want)
	}
	sum := sha256.Sum256([]byte(uploaderGo))
	for _, c := range changes {
		if c.Path == up && (c.AfterHash != hex.EncodeToString(sum[:]) || hashes[up] != c.AfterHash) {
			t.Fatalf("after hash: %+v", c)
		}
		if c.At != "2026-11-20T09:30:00Z" {
			t.Fatalf("at: %+v", c)
		}
		if strings.Count(c.BeforeSnippet, "\n") > 2 || strings.Count(c.AfterSnippet, "\n") > 2 {
			t.Fatalf("snippet over 3 lines: %+v", c)
		}
	}
	for _, c := range changes {
		switch c.ToolCallID {
		case "claude-edit":
			if c.BeforeSnippet != "\treturn put(req)" || c.AfterSnippet != "\tvar err error\n\tfor i := 0; i < 3; i++ {\n\t\tif err = put(req); err == nil {" {
				t.Fatalf("edit snippets: %q / %q", c.BeforeSnippet, c.AfterSnippet)
			}
		case "claude-multi":
			if c.HunkIndex != 0 && c.HunkIndex != 1 {
				t.Fatalf("hunk index: %+v", c)
			}
		case "codex-patch":
			if c.BeforeSnippet != "\tfor i := 0; i < 5; i++ {" {
				t.Fatalf("patch before: %q", c.BeforeSnippet)
			}
		}
	}
}

func TestPatchEditsIgnoresLookalikeLines(t *testing.T) {
	// A removed SQL comment ("-- x") is "--- x" inside a hunk, not a header.
	got := patchEdits("*** Begin Patch\n*** Update File: db/q.sql\n@@\n SELECT 1;\n--- old note\n+-- new note\n*** End Patch")
	if len(got) != 1 || got[0].path != "db/q.sql" || len(got[0].pieces) != 1 {
		t.Fatalf("edits: %+v", got)
	}
	if p := got[0].pieces[0]; p.old != "SELECT 1;\n-- old note" || p.new != "SELECT 1;\n-- new note" {
		t.Fatalf("piece: %+v", p)
	}
}

type hunkCapture struct {
	mu          sync.Mutex
	bodies      []map[string]any
	rejectHunks bool
}

func (c *hunkCapture) handler(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bodies = append(c.bodies, body)
	if _, has := body["changes"]; has && c.rejectHunks {
		http.Error(w, `invalid JSON body: json: unknown field "changes"`, http.StatusBadRequest)
		return
	}
	_, _ = w.Write([]byte(`{"linked":1}`))
}

func TestCodeLinksReporterSendsHunks(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "internal/up/uploader.go", uploaderGo)
	for _, reject := range []bool{false, true} {
		cap := &hunkCapture{rejectHunks: reject}
		srv := httptest.NewServer(http.HandlerFunc(cap.handler))
		hs := NewHTTPMemoryStore(srv.URL, "tok", testProjectUUID)
		rep := NewCodeLinksReporter(root, hs.mirrorConnection)
		rep.NoteEvents("", "native-1", "claude", []Event{turnWithCalls(
			map[string]any{"id": "toolu_9", "name": "Edit", "input": map[string]any{"file_path": filepath.Join(root, "internal", "up", "uploader.go"),
				"old_string": "\t\ttime.Sleep(1)", "new_string": "\t\ttime.Sleep(time.Second)"}},
		)})
		if n := rep.Flush(context.Background()); n != 1 {
			t.Fatalf("reject=%v sent = %d", reject, n)
		}
		srv.Close()
		cap.mu.Lock()
		bodies := cap.bodies
		cap.mu.Unlock()
		if !reject {
			if len(bodies) != 1 {
				t.Fatalf("bodies = %d", len(bodies))
			}
			changes, _ := bodies[0]["changes"].([]any)
			links, _ := bodies[0]["links"].([]any)
			if len(changes) != 1 || len(links) != 1 {
				t.Fatalf("body: %v", bodies[0])
			}
			c, _ := changes[0].(map[string]any)
			l, _ := links[0].(map[string]any)
			if c["path"] != "internal/up/uploader.go" || c["start_line"] != float64(11) || c["end_line"] != float64(11) ||
				c["tool_call_id"] != "toolu_9" || c["after_snippet"] != "\t\ttime.Sleep(time.Second)" ||
				c["before_snippet"] != "\t\ttime.Sleep(1)" || c["after_hash"] != l["hash"] || c["after_hash"] == "" ||
				!reflect.DeepEqual(c["added_hashes"], []any{lineHash("\t\ttime.Sleep(time.Second)")}) ||
				!reflect.DeepEqual(c["context_after"], []any{lineHash("\t}"), lineHash("\treturn err")}) {
				t.Fatalf("change: %v link: %v", c, l)
			}
			if rep.editBytes != 0 {
				t.Fatalf("edit bytes leak: %d", rep.editBytes)
			}
			continue
		}
		// An older server rejects changes: the links are resent alone and
		// later reports omit hunks.
		if len(bodies) != 2 || bodies[1]["changes"] != nil || bodies[1]["links"] == nil || !rep.noChanges {
			t.Fatalf("fallback bodies: %v", bodies)
		}
	}
}
