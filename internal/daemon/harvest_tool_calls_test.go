package daemon

import (
	"path/filepath"
	"strings"
	"testing"
)

// Fixture-based F3 coverage: harvester must retain structured tool_calls so
// ParseToolCalls / pushParsedToolOps yield non-empty file ops for sessions
// that edited files (Claude / Codex / Cursor-shaped JSONL).

func TestHarvestClaudeEditRetainsToolCalls(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "claude-session.jsonl")
	writeLines(t, p,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"Please fix the ACL check"}]},"timestamp":"2026-09-30T12:00:00Z"}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Updating the session ACL guard."},{"type":"tool_use","id":"toolu_edit1","name":"Edit","input":{"file_path":"internal/server/session_acl.go","old_string":"if !ok { return false }","new_string":"if !ok || !ownerMatch { return false }"}}]},"timestamp":"2026-09-30T12:00:01Z"}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_write1","name":"Write","input":{"file_path":"internal/server/session_acl_test.go","content":"package server\n"}}]},"timestamp":"2026-09-30T12:00:02Z"}`,
	)
	h, got := collectHarvester(dir)
	turns, err := h.TailFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) < 2 {
		t.Fatalf("turns=%d want >=2: %+v", len(turns), turns)
	}
	var editTurn *Turn
	for i := range turns {
		if len(turns[i].ToolCalls) > 0 {
			editTurn = &turns[i]
			break
		}
	}
	if editTurn == nil {
		t.Fatalf("no turn retained tool_calls: %+v", turns)
	}
	payload := turnPayload("conversation_turn", "claude", p, *editTurn)
	ops := ParseToolCalls(payload, 0)
	if len(ops) == 0 {
		t.Fatalf("ParseToolCalls empty for Claude Edit payload: %+v", payload["tool_calls"])
	}
	if ops[0].Type != "file_op" || ops[0].FilePath == "" {
		t.Fatalf("expected file_op with path, got %+v", ops[0])
	}
	if len(*got) == 0 {
		t.Fatal("expected CONVERSATION_TURN events")
	}
	foundTC := false
	for _, ev := range *got {
		if ev.Payload["tool_calls"] != nil {
			foundTC = true
			ops2 := ParseToolCalls(ev.Payload, 1)
			if len(ops2) == 0 {
				t.Fatalf("emitted event tool_calls did not parse: %+v", ev.Payload["tool_calls"])
			}
		}
	}
	if !foundTC {
		t.Fatal("no emitted event carried tool_calls")
	}
}

func TestHarvestCodexApplyPatchRetainsToolCalls(t *testing.T) {
	patch := "*** Begin Patch\\n*** Update File: internal/daemon/runtime.go\\n@@\\n-old line\\n+new line\\n*** End Patch"
	raw := strings.Join([]string{
		`{"type":"session_meta","payload":{"id":"rollout-1","cwd":"/workspace"}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Patch runtime to keep tool_calls"}]}}`,
		`{"type":"response_item","payload":{"type":"function_call","name":"apply_patch","arguments":"` + patch + `"}}`,
	}, "\n")
	turns, err := ParseTurns(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	var callTurn *Turn
	for i := range turns {
		if len(turns[i].ToolCalls) > 0 {
			callTurn = &turns[i]
			break
		}
	}
	if callTurn == nil {
		t.Fatalf("Codex function_call stripped: %+v", turns)
	}
	payload := turnPayload("conversation_turn", "codex", "/tmp/rollout.jsonl", *callTurn)
	ops := ParseToolCalls(payload, 2)
	if len(ops) == 0 {
		t.Fatalf("ParseToolCalls empty for Codex apply_patch: %+v", payload["tool_calls"])
	}
	if ops[0].Type != "file_op" || ops[0].OpType != "modify" {
		t.Fatalf("want file_op modify, got %+v", ops[0])
	}
	if !strings.Contains(ops[0].FilePath, "runtime.go") {
		t.Fatalf("path=%q want runtime.go", ops[0].FilePath)
	}
}

func TestHarvestCursorSearchReplaceRetainsToolCalls(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cursor-agent.jsonl")
	writeLines(t, p,
		`{"role":"user","content":"Tighten session ACL","timestamp":"2026-09-30T13:00:00Z"}`,
		`{"role":"assistant","content":[{"type":"text","text":"Applying the patch."},{"type":"tool_use","name":"search_replace","input":{"path":"internal/store/agent_sessions.go","old_string":"return true","new_string":"return ownerID == uid"}}],"timestamp":"2026-09-30T13:00:01Z"}`,
	)
	h, _ := collectHarvester(dir)
	turns, err := h.TailFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 2 {
		t.Fatalf("turns=%d want 2: %+v", len(turns), turns)
	}
	if len(turns[1].ToolCalls) != 1 {
		t.Fatalf("tool_calls stripped: %+v", turns[1])
	}
	payload := turnPayload("conversation_turn", "cursor", p, turns[1])
	payload["workspace_root"] = "/workspace"
	ops := ParseToolCalls(payload, 1)
	if len(ops) != 1 || ops[0].Type != "file_op" || ops[0].OpType != "modify" {
		t.Fatalf("ops=%+v", ops)
	}
	if !strings.Contains(ops[0].DiffHunk, "-return true") || !strings.Contains(ops[0].DiffHunk, "+return ownerID == uid") {
		t.Fatalf("diff=%q", ops[0].DiffHunk)
	}
}

func TestPushParsedToolOpsPathFromHarvestedClaudeTurn(t *testing.T) {
	// Simulates the runtime flush path: harvested turn payload → ParseToolCalls
	// must be non-empty (what pushParsedToolOps consumes before PushOperations).
	claudeLine := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Edit","input":{"file_path":"a.go","old_string":"x","new_string":"y"}}]}}`
	turns, err := ParseTurns(strings.NewReader(claudeLine + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 || len(turns[0].ToolCalls) == 0 {
		t.Fatalf("harvest: %+v", turns)
	}
	batch := []Event{{
		Type:    EventConversationTurn,
		Payload: turnPayload("conversation_turn", "claude", "/home/u/.claude/projects/x/sess.jsonl", turns[0]),
	}}
	var fileOps int
	for i, ev := range batch {
		for _, p := range ParseToolCalls(ev.Payload, i) {
			if p.Type == "file_op" {
				fileOps++
			}
		}
	}
	if fileOps == 0 {
		t.Fatal("push path would skip PushOperations: zero file ops from harvested tool_calls")
	}
}
