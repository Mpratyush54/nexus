package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// roundTrip feeds input lines to ServeStdio and returns the response lines.
func roundTrip(t *testing.T, s *Server, input string) []map[string]any {
	t.Helper()
	var out bytes.Buffer
	if err := s.ServeStdio(context.Background(), strings.NewReader(input), &out); err != nil {
		t.Fatalf("ServeStdio: %v", err)
	}
	var lines []map[string]any
	for _, ln := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(ln), &m); err != nil {
			t.Fatalf("response not JSON (%v): %s", err, ln)
		}
		lines = append(lines, m)
	}
	return lines
}

func TestServeStdioToolsCallDispatch(t *testing.T) {
	s, _, _ := newTestServer(t.TempDir())
	lines := roundTrip(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"workspace_info","arguments":{}}}`+"\n")
	if len(lines) != 1 {
		t.Fatalf("got %d responses, want 1", len(lines))
	}
	res, _ := lines[0]["result"].(map[string]any)
	if res["project"] != "nexus" {
		t.Errorf("result = %v, want project nexus", res)
	}
	if lines[0]["jsonrpc"] != "2.0" {
		t.Errorf("envelope = %v, want jsonrpc 2.0", lines[0])
	}
}

func TestServeStdioDirectDispatch(t *testing.T) {
	s, _, _ := newTestServer(t.TempDir())
	lines := roundTrip(t, s, `{"jsonrpc":"2.0","id":2,"method":"memory_write","params":{"key":"auth/policy","content":"All APIs must use JWT authentication tokens."}}`+"\n")
	if len(lines) != 1 {
		t.Fatalf("got %d responses, want 1", len(lines))
	}
	res, _ := lines[0]["result"].(map[string]any)
	if res["status"] != "PROPOSED" {
		t.Errorf("result = %v, want PROPOSED write", res)
	}
}

func TestServeStdioToolsList(t *testing.T) {
	s, _, _ := newTestServer(t.TempDir())
	lines := roundTrip(t, s, `{"jsonrpc":"2.0","id":3,"method":"tools/list"}`+"\n")
	res, _ := lines[0]["result"].(map[string]any)
	tools, _ := res["tools"].([]any)
	if len(tools) != 8 {
		t.Errorf("tools/list = %d tools, want 8", len(tools))
	}
}

func TestServeStdioInitializeAndPing(t *testing.T) {
	s, _, _ := newTestServer(t.TempDir())
	lines := roundTrip(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`+"\n"+
			`{"jsonrpc":"2.0","id":2,"method":"ping"}`+"\n")
	if len(lines) != 2 {
		t.Fatalf("got %d responses, want 2", len(lines))
	}
	init, _ := lines[0]["result"].(map[string]any)
	if init["protocolVersion"] != ProtocolVersion {
		t.Errorf("initialize result = %v", init)
	}
}

func TestServeStdioErrorsAndNotifications(t *testing.T) {
	s, _, _ := newTestServer(t.TempDir())
	lines := roundTrip(t, s,
		`not json at all`+"\n"+
			`{"jsonrpc":"2.0","id":7,"method":"does_not_exist"}`+"\n"+
			`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`+"\n"+
			`{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"memory_search","arguments":{}}}`+"\n")
	if len(lines) != 3 { // notification produces no response
		t.Fatalf("got %d responses, want 3 (notification silent)", len(lines))
	}
	if errObj, _ := lines[0]["error"].(map[string]any); errObj["code"] != float64(CodeParseError) {
		t.Errorf("line 1 error = %v, want code %d", lines[0], CodeParseError)
	}
	if lines[0]["id"] != nil {
		t.Errorf("parse error id = %v, want null", lines[0]["id"])
	}
	if errObj, _ := lines[1]["error"].(map[string]any); errObj["code"] != float64(CodeMethodNotFound) {
		t.Errorf("line 2 error = %v, want code %d", lines[1], CodeMethodNotFound)
	}
	if errObj, _ := lines[2]["error"].(map[string]any); errObj["code"] != float64(CodeInvalidParams) {
		t.Errorf("line 3 error = %v, want code %d (missing query)", lines[2], CodeInvalidParams)
	}
}

func TestServeStdioMemorySearchCarriesHint(t *testing.T) {
	ctx := context.Background()
	s, mem, _ := newTestServer(t.TempDir())
	mustWrite(t, ctx, mem, "auth/policy", "All APIs must use JWT authentication and reject expired tokens.")
	lines := roundTrip(t, s, `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"memory_search","arguments":{"query":"auth"}}}`+"\n")
	res, _ := lines[0]["result"].(map[string]any)
	if res["reflection_hint"] != ReflectionHint {
		t.Errorf("reflection_hint = %v, want piggyback hint", res["reflection_hint"])
	}
	if _, ok := res["token_count"]; !ok {
		t.Errorf("result missing token_count: %v", res)
	}
	if _, ok := res["budget_remaining"]; !ok {
		t.Errorf("result missing budget_remaining: %v", res)
	}
}
