package server

import (
	"net/http"
	"testing"
	"time"

	"central-memory/internal/extract"
	"central-memory/internal/store"
)

func TestCodeChangesWhyAndLineHistory(t *testing.T) {
	s, mem, pid := briefFixture(t)
	ctx := t.Context()
	owner := loginAs(t, s, "owner")
	sess, err := mem.UpsertAgentSession(ctx, &store.AgentSession{ProjectID: pid, OwnerUserID: "owner", Harness: "claude",
		NativeID: "native-why", Title: "Retry uploads"})
	if err != nil {
		t.Fatal(err)
	}
	item, err := s.persistHarvestMemory(ctx, &store.HarvestJob{ProjectID: pid}, extract.Proposal{
		Key: "upload/retry", Content: "Uploads retry 3 times with backoff in uploader.go because the CDN drops the first PUT.",
		Level: "project", Scope: "decision", Confidence: 0.9,
	}, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	url := "/v1/projects/" + pid + "/code-links"
	at := time.Now().Add(-time.Hour).UTC()
	body := map[string]any{"session_native_id": "native-why", "changes": []map[string]any{
		{"path": "internal/up/uploader.go", "tool_call_id": "toolu_1", "start_line": 40, "end_line": 42,
			"removed_lines": 1, "added_lines": 3, "before_snippet": "return put(req)",
			"after_snippet": "for i := 0; i < 3; i++ {\n\tif err = put(req); err == nil {\n\t\tbreak", "after_hash": "u1", "at": at},
		{"path": "../outside.go", "start_line": 1, "end_line": 1},
		{"path": "internal/up/uploader.go", "start_line": 0, "end_line": 1},
	}}
	rec := doJSON(t, s, http.MethodPost, url, owner, body)
	var res struct {
		Changes        int `json:"changes"`
		ChangesSkipped int `json:"changes_skipped"`
	}
	decodeBody(t, rec, &res)
	if rec.Code != http.StatusOK || res.Changes != 1 || res.ChangesSkipped != 2 {
		t.Fatalf("code-links changes: %d %+v %s", rec.Code, res, rec.Body.String())
	}
	// A later edit inserts 5 lines above: line 41 is now 46.
	rec = doJSON(t, s, http.MethodPost, url, owner, map[string]any{"session_id": sess.ID,
		"links": []map[string]any{{"path": "internal/up/uploader.go", "hash": "u2"}},
		"changes": []map[string]any{{"path": "internal/up/uploader.go", "tool_call_id": "toolu_2", "start_line": 3, "end_line": 7,
			"added_lines": 5, "after_snippet": "import \"time\"", "after_hash": "u2", "at": at.Add(time.Minute)}}})
	if rec.Code != http.StatusOK {
		t.Fatalf("second report: %d %s", rec.Code, rec.Body.String())
	}

	call := func(token, name string, args map[string]any) map[string]any {
		return mcpCall(t, s, token, pid, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": name, "arguments": args}})
	}
	out := call(owner, "why", map[string]any{"file": "uploader.go", "line": 46})
	changes, _ := out["changes"].([]any)
	if len(changes) != 1 {
		t.Fatalf("why: %v", out)
	}
	row, _ := changes[0].(map[string]any)
	if row["session_id"] != sess.ID || row["title"] != "Retry uploads" || row["agent"] != "claude" ||
		row["lines"] != "45-47" || row["recorded_lines"] != "40-42" || row["approximate"] != false ||
		row["before"] != "return put(req)" || row["tool_call_id"] != "toolu_1" || row["match"] != "line" {
		t.Fatalf("why row: %v", row)
	}
	decisions, _ := row["decisions"].([]any)
	if len(decisions) != 1 {
		t.Fatalf("decisions: %v", row["decisions"])
	}
	if d, _ := decisions[0].(map[string]any); d["id"] != item.ID || d["key"] != "upload/retry" {
		t.Fatalf("decision: %v", d)
	}
	if out := call(owner, "why", map[string]any{"file": "uploader.go", "line": 41}); out["count"].(float64) != 0 || out["note"] == nil {
		t.Fatalf("old line number must not match: %v", out)
	}
	noLine := doJSONWithHeaders(t, s, http.MethodPost, "/v1/agent/mcp", owner, map[string]string{"X-Nexus-Project": pid},
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": "why", "arguments": map[string]any{"file": "uploader.go"}}})
	var rpc struct {
		Error any `json:"error"`
	}
	decodeBody(t, noLine, &rpc)
	if rpc.Error == nil {
		t.Fatalf("why without line must fail: %s", noLine.Body.String())
	}

	hist := call(owner, "line_history", map[string]any{"file": "internal/up/uploader.go"})
	if n, _ := hist["count"].(float64); n != 2 {
		t.Fatalf("line_history: %v", hist)
	}
	first, _ := hist["changes"].([]any)[0].(map[string]any)
	if first["tool_call_id"] != "toolu_2" {
		t.Fatalf("line_history must be newest first: %v", first)
	}
	if ranged := call(owner, "line_history", map[string]any{"file": "uploader.go", "start_line": 1, "end_line": 10}); ranged["count"].(float64) != 1 {
		t.Fatalf("ranged history: %v", ranged)
	}

	// The file changed outside captured edits: ranges become approximate.
	if rec := doJSON(t, s, http.MethodPost, "/v1/projects/"+pid+"/files-changed", owner,
		map[string]any{"hashes": map[string]string{"internal/up/uploader.go": "u3"}}); rec.Code != http.StatusOK {
		t.Fatalf("files-changed: %d", rec.Code)
	}

	// REST for the app, same answer and visibility.
	rest := doJSON(t, s, http.MethodGet, "/v1/projects/"+pid+"/why?path=uploader.go&line=46", owner, nil)
	var why struct {
		Count   int              `json:"count"`
		Changes []map[string]any `json:"changes"`
	}
	decodeBody(t, rest, &why)
	if rest.Code != http.StatusOK || why.Count != 1 || why.Changes[0]["approximate"] != true || why.Changes[0]["note"] == nil {
		t.Fatalf("REST why: %d %s", rest.Code, rest.Body.String())
	}
	if bad := doJSON(t, s, http.MethodGet, "/v1/projects/"+pid+"/why?line=4", owner, nil); bad.Code != http.StatusBadRequest {
		t.Fatalf("no path: %d", bad.Code)
	}
	if bad := doJSON(t, s, http.MethodGet, "/v1/projects/"+pid+"/why?path=a.go&line=x", owner, nil); bad.Code != http.StatusBadRequest {
		t.Fatalf("bad line: %d", bad.Code)
	}
	if denied := doJSON(t, s, http.MethodGet, "/v1/projects/"+pid+"/why?path=uploader.go&line=46", loginAs(t, s, "stranger"), nil); denied.Code != http.StatusForbidden {
		t.Fatalf("stranger: %d", denied.Code)
	}
	// The editor is a member but cannot read the owner's private chat.
	ed := doJSON(t, s, http.MethodGet, "/v1/projects/"+pid+"/why?path=uploader.go&line=46", loginAs(t, s, "editor"), nil)
	decodeBody(t, ed, &why)
	if ed.Code != http.StatusOK || why.Count != 0 {
		t.Fatalf("editor sees private chat: %d %s", ed.Code, ed.Body.String())
	}
	if hist := call(loginAs(t, s, "editor"), "why", map[string]any{"file": "uploader.go", "line": 46}); hist["count"].(float64) != 0 {
		t.Fatalf("editor why: %v", hist)
	}
}
