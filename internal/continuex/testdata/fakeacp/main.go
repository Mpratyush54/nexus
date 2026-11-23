// fakeacp is an independent protocol peer: assertions fail the subprocess.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

type message struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code int `json:"code"`
	} `json:"error"`
}

func send(v any)                       { b, _ := json.Marshal(v); fmt.Println(string(b)) }
func result(id json.RawMessage, v any) { send(map[string]any{"jsonrpc": "2.0", "id": id, "result": v}) }
func fail(text string)                 { fmt.Fprintln(os.Stderr, text); os.Exit(7) }
func check(ok bool, text string) {
	if !ok {
		fail(text)
	}
}
func update(kind string, fields map[string]any) {
	fields["sessionUpdate"] = kind
	send(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": "fake-session", "update": fields}})
}
func delta(text string) {
	update("agent_message_chunk", map[string]any{"content": map[string]any{"type": "text", "text": text}})
}
func finish(id json.RawMessage, reason string) { result(id, map[string]any{"stopReason": reason}) }
func main() {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 65536), 8*1024*1024)
	var active json.RawMessage
	var permissionID json.RawMessage
	var mode string
	initialized, session, model, cancelReceived, permissionCancelled := false, false, false, false, false
	features := strings.Join(os.Args[1:], " ")
	if strings.Contains(features, "no-model") {
		model = true
	}
	turn := 0
	for scanner.Scan() {
		var m message
		if json.Unmarshal(scanner.Bytes(), &m) != nil {
			fail("bad client JSON")
		}
		switch m.Method {
		case "initialize":
			var p struct {
				Version int `json:"protocolVersion"`
				Caps    struct {
					Terminal bool `json:"terminal"`
					FS       struct {
						Read  bool `json:"readTextFile"`
						Write bool `json:"writeTextFile"`
					} `json:"fs"`
				} `json:"clientCapabilities"`
			}
			_ = json.Unmarshal(m.Params, &p)
			check(p.Version == 1 && !p.Caps.Terminal && !p.Caps.FS.Read && !p.Caps.FS.Write, "invalid capabilities")
			initialized = true
			result(m.ID, map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{"loadSession": !strings.Contains(features, "no-load")}, "authMethods": []any{map[string]any{"id": "login", "name": "Login"}}})
		case "session/new", "session/load":
			check(initialized, "session before initialize")
			var p struct {
				CWD string `json:"cwd"`
				ID  string `json:"sessionId"`
				MCP []any  `json:"mcpServers"`
			}
			_ = json.Unmarshal(m.Params, &p)
			check(p.CWD != "" && p.MCP != nil && len(p.MCP) == 0, "bad session params")
			if m.Method == "session/load" {
				check(!strings.Contains(features, "no-load"), "load used without capability")
				check(p.ID == "fake-session", "wrong resume id")
				delta("OLD HISTORY")
			}
			session = true
			response := map[string]any{"sessionId": "fake-session"}
			if strings.Contains(features, "config") {
				response["configOptions"] = []any{map[string]any{"id": "model", "category": "model", "type": "select", "options": []any{map[string]any{"value": "test-model", "name": "Test"}}}}
			} else if !strings.Contains(features, "no-model") {
				response["models"] = map[string]any{"availableModels": []any{map[string]any{"modelId": "test-model"}}}
			}
			result(m.ID, response)
		case "session/set_model":
			check(!strings.Contains(features, "config") && !strings.Contains(features, "no-model"), "unadvertised model API")
			var p struct {
				Model string `json:"modelId"`
			}
			_ = json.Unmarshal(m.Params, &p)
			check(p.Model == "test-model", "incorrect model")
			model = true
			result(m.ID, map[string]any{})
		case "session/set_config_option":
			check(strings.Contains(features, "config"), "unadvertised config API")
			var p struct {
				Config string `json:"configId"`
				Value  string `json:"value"`
			}
			_ = json.Unmarshal(m.Params, &p)
			check(p.Config == "model" && p.Value == "test-model", "incorrect config selection")
			model = true
			result(m.ID, map[string]any{})
		case "session/prompt":
			check(initialized && session && model, "prompt before setup/model")
			check(active == nil, "overlapping prompts")
			var p struct {
				ID     string `json:"sessionId"`
				Prompt []struct {
					Text string `json:"text"`
				} `json:"prompt"`
			}
			_ = json.Unmarshal(m.Params, &p)
			check(p.ID == "fake-session" && len(p.Prompt) == 1, "bad prompt")
			text := p.Prompt[0].Text
			if strings.HasPrefix(text, "Context from the earlier conversation:") {
				check(turn == 0, "context repeated")
				check(strings.Contains(text, "seed context\n\nNew user message:\n"), "bad seeded context")
				text = strings.SplitN(text, "New user message:\n", 2)[1]
			}
			turn++
			mode = text
			switch text {
			case "finish-exit":
				delta("goodbye")
				finish(m.ID, "end_turn")
				return
			case "crash":
				fail("fake failure api_key=supersecret")
			case "hang":
				delta("hanging")
				for {
					time.Sleep(time.Hour)
				}
			case "auth":
				send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": map[string]any{"code": -32000, "message": "Authentication required"}})
			case "refusal":
				finish(m.ID, "refusal")
			case "tool-allow", "tool-deny", "interrupt":
				active = m.ID
				permissionID = json.RawMessage(`"permission-1"`)
				if text == "tool-deny" {
					permissionID = json.RawMessage(`9007199254740993`)
				}
				update("tool_call", map[string]any{"toolCallId": "tool-1", "title": "Write", "kind": "edit", "rawInput": map[string]any{"api_key": "original-secret"}})
				send(map[string]any{"jsonrpc": "2.0", "id": permissionID, "method": "session/request_permission", "params": map[string]any{"toolCall": map[string]any{"toolCallId": "tool-1", "title": "Write", "rawInput": map[string]any{"api_key": "original-secret"}}, "options": []any{map[string]any{"optionId": "always", "kind": "allow_always"}, map[string]any{"optionId": "never", "kind": "reject_always"}, map[string]any{"optionId": "yes", "kind": "allow_once"}, map[string]any{"optionId": "no", "kind": "reject_once"}}}})
			case "unknown":
				active = m.ID
				send(map[string]any{"jsonrpc": "2.0", "id": "unknown-id", "method": "fs/read_text_file", "params": map[string]any{}})
			case "wait":
				active = m.ID
				delta("waiting")
			case "flood":
				for i := 0; i < 2000; i++ {
					delta("Hello")
					delta(" world")
				}
				finish(m.ID, "end_turn")
			default:
				fmt.Println("{malformed")
				delta("Hello")
				delta(" world")
				finish(m.ID, "end_turn")
			}
		case "session/cancel":
			check(active != nil, "cancel without prompt")
			cancelReceived = true
			if mode != "interrupt" || permissionCancelled {
				finish(active, "cancelled")
				active = nil
			}
		case "":
			if string(m.ID) == `"unknown-id"` {
				check(m.Error != nil && m.Error.Code == -32601, "unknown method response")
				delta("unknown rejected")
				finish(active, "end_turn")
				active = nil
				continue
			}
			check(string(m.ID) == string(permissionID), "permission ID not echoed exactly")
			var r struct {
				Outcome struct {
					Outcome string `json:"outcome"`
					Option  string `json:"optionId"`
				} `json:"outcome"`
			}
			_ = json.Unmarshal(m.Result, &r)
			if mode == "interrupt" {
				check(r.Outcome.Outcome == "cancelled", "permission not cancelled")
				permissionCancelled = true
				if cancelReceived {
					finish(active, "cancelled")
					active = nil
				}
				continue
			}
			want := "yes"
			if mode == "tool-deny" {
				want = "no"
			}
			check(r.Outcome.Outcome == "selected" && r.Outcome.Option == want, "wrong option: expected once")
			status := "completed"
			if mode == "tool-deny" {
				status = "failed"
			}
			update("tool_call_update", map[string]any{"toolCallId": "tool-1", "status": status, "content": []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": "api_key=supersecret\n"}}, map[string]any{"type": "diff", "path": "hello.txt", "oldText": "old", "newText": "new"}}})
			finish(active, "end_turn")
			active = nil
		default:
			fail("unexpected client method " + m.Method)
		}
	}
}
