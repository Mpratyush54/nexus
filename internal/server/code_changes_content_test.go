package server

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"central-memory/internal/store"
)

func lineHashes(lines ...string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = store.LineHash(l)
	}
	return out
}

// The file right after the edits (one chat, two tool calls):
//
//	1  package up
//	2
//	3  func Upload() error {
//	4  	var err error
//	5  	for i := 0; i < 3; i++ {   <- toolu_loop added 5-7
//	6  		retry()
//	7  	}
//	8  	return err
//	9  }
//	10
//	11 func other() {
//	12 	other()
//	13 }                           <- toolu_other added 13
func TestWhyMatchesByContent(t *testing.T) {
	s, mem, pid := briefFixture(t)
	ctx := t.Context()
	owner := loginAs(t, s, "owner")
	sess, err := mem.UpsertAgentSession(ctx, &store.AgentSession{ProjectID: pid, OwnerUserID: "owner", Harness: "codex",
		NativeID: "native-content", Title: "Retry loop"})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Hour).UTC()
	rec := doJSON(t, s, http.MethodPost, "/v1/projects/"+pid+"/code-links", owner, map[string]any{"session_id": sess.ID,
		"links": []map[string]any{{"path": "up/up.go", "hash": "a1"}},
		"changes": []map[string]any{
			{"path": "up/up.go", "tool_call_id": "toolu_loop", "start_line": 5, "end_line": 7, "removed_lines": 1, "added_lines": 3,
				"after_snippet": "\tfor i := 0; i < 3; i++ {\n\t\tretry()\n\t}", "after_hash": "a1", "at": at,
				"added_hashes":   lineHashes("\tfor i := 0; i < 3; i++ {", "\t\tretry()", "\t}"),
				"context_before": lineHashes("func Upload() error {", "\tvar err error"),
				"context_after":  lineHashes("\treturn err", "}")},
			{"path": "up/up.go", "tool_call_id": "toolu_other", "start_line": 13, "end_line": 13, "added_lines": 1,
				"after_snippet": "}", "after_hash": "a1", "at": at.Add(time.Second),
				"added_hashes":   lineHashes("}"),
				"context_before": lineHashes("func other() {", "\tother()")},
		}})
	if rec.Code != http.StatusOK {
		t.Fatalf("code-links: %d %s", rec.Code, rec.Body.String())
	}
	// Then the file changes outside captured tool calls: 4 lines are
	// inserted above (retry() is now line 10) and its hash differs.
	if rec := doJSON(t, s, http.MethodPost, "/v1/projects/"+pid+"/files-changed", owner,
		map[string]any{"hashes": map[string]string{"up/up.go": "outside"}}); rec.Code != http.StatusOK {
		t.Fatalf("files-changed: %d", rec.Code)
	}

	why := func(args map[string]any) []map[string]any {
		t.Helper()
		out := mcpCall(t, s, owner, pid, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": "why", "arguments": args}})
		list, _ := out["changes"].([]any)
		rows := make([]map[string]any, 0, len(list))
		for _, it := range list {
			row, _ := it.(map[string]any)
			rows = append(rows, row)
		}
		return rows
	}
	expect := func(name string, rows []map[string]any, call, match string, approximate bool) {
		t.Helper()
		if len(rows) == 0 || rows[0]["tool_call_id"] != call || rows[0]["match"] != match || rows[0]["approximate"] != approximate {
			t.Fatalf("%s: %v", name, rows)
		}
	}

	// Lines inserted above and an outside edit do not lose the change.
	expect("exact", why(map[string]any{"file": "up.go", "line_text": "\t\tretry()", "context": []string{"\tfor i := 0; i < 3; i++ {", "\t}"}, "line": 10}),
		"toolu_loop", "exact", false)
	// Normalisation: CRLF, trailing and re-indented whitespace.
	expect("normalised", why(map[string]any{"file": "up.go", "line_text": "    retry()   \r"}), "toolu_loop", "hash", false)
	// A neighbour changed since (a line was added between) but is still
	// near the change: context.
	expect("context", why(map[string]any{"file": "up.go", "line_text": "\t\tretry()", "context": []string{"\t\tlog.Print(i)", "\t}"}}),
		"toolu_loop", "context", false)
	// "}" is everywhere: its neighbours pick the change.
	expect("} of the loop", why(map[string]any{"file": "up.go", "line_text": "\t}", "context": []string{"\t\tretry()", "\treturn err"}}),
		"toolu_loop", "exact", false)
	// (Last line of the file: only the previous line is known.)
	expect("} of other", why(map[string]any{"file": "up.go", "line_text": "}", "context": []string{"\tother()"}}),
		"toolu_other", "exact", false)
	if rows := why(map[string]any{"file": "up.go", "line_text": "}"}); len(rows) != 0 {
		t.Fatalf("a bare } must not match by hash alone: %v", rows)
	}
	// Modified text: the line number is the approximate fallback.
	expect("line fallback", why(map[string]any{"file": "up.go", "line_text": "\t\tretry(5)", "line": 6}), "toolu_loop", "line", true)
	if rows := why(map[string]any{"file": "up.go", "line_text": "\t\tretry(5)"}); len(rows) != 0 {
		t.Fatalf("modified text without a line must not match: %v", rows)
	}

	// line_history by the range's current text.
	hist := mcpCall(t, s, owner, pid, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "line_history", "arguments": map[string]any{"file": "up/up.go", "start_line": 9,
			"lines": []string{"\tfor i := 0; i < 3; i++ {", "\t\tretry()", "\t}"}}}})
	if n, _ := hist["count"].(float64); n != 1 {
		t.Fatalf("line_history by lines: %v", hist)
	}
	if first, _ := hist["changes"].([]any)[0].(map[string]any); first["match"] != "exact" || first["tool_call_id"] != "toolu_loop" {
		t.Fatalf("line_history row: %v", first)
	}

	// REST: text= and repeated context=.
	v := url.Values{"path": {"up.go"}, "text": {"\t}"}, "context": {"\tother()"}}
	rest := doJSON(t, s, http.MethodGet, "/v1/projects/"+pid+"/why?"+v.Encode(), owner, nil)
	var res struct {
		Count   int              `json:"count"`
		Changes []map[string]any `json:"changes"`
	}
	decodeBody(t, rest, &res)
	if rest.Code != http.StatusOK || res.Count != 1 || res.Changes[0]["tool_call_id"] != "toolu_other" || res.Changes[0]["match"] != "exact" {
		t.Fatalf("REST why by text: %d %s", rest.Code, rest.Body.String())
	}
	if bad := doJSON(t, s, http.MethodGet, "/v1/projects/"+pid+"/why?path=up.go&text=x&context=a&context=b&context=c", owner, nil); bad.Code != http.StatusBadRequest {
		t.Fatalf("3 context lines: %d", bad.Code)
	}
}
