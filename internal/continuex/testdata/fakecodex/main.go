// fakecodex implements v2 shapes from codex-cli 0.167.0's generated schemas.
// It deliberately
// rejects incorrect sequencing, unsafe options, and session-wide approvals.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

func main() {
	enc := json.NewEncoder(os.Stdout)
	send := func(v any) {
		if err := enc.Encode(v); err != nil {
			os.Exit(4)
		}
	}
	fatal := func(msg string) { fmt.Fprintln(os.Stderr, "fake protocol failure: "+msg); os.Exit(3) }
	if strings.Join(os.Args[1:], " ") != "app-server" {
		fatal("argv")
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 65536), 8*1024*1024)
	initialized, ready, thread := false, false, false
	turn := 0
	active := ""
	pending := ""
	expect := ""
	file := false
	notify := func(method string, params any) { send(map[string]any{"method": method, "params": params}) }
	item := func(completed bool, i map[string]any) {
		method := "item/started"
		if completed {
			method = "item/completed"
		}
		params := map[string]any{"threadId": "fake-thread", "turnId": active, "item": i}
		if completed {
			params["completedAtMs"] = time.Now().UnixMilli()
		} else {
			params["startedAtMs"] = time.Now().UnixMilli()
		}
		if i["type"] == "commandExecution" {
			i["commandActions"] = []any{}
		}
		notify(method, params)
	}
	text := func(value string) { item(true, map[string]any{"id": "message", "type": "agentMessage", "text": value}) }
	done := func(status string) {
		notify("turn/completed", map[string]any{"threadId": "fake-thread", "turn": map[string]any{"id": active, "items": []any{}, "status": status, "error": nil}})
		active = ""
	}
	for scanner.Scan() {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result struct {
				Decision string `json:"decision"`
			} `json:"result"`
			Error struct {
				Code int `json:"code"`
			} `json:"error"`
		}
		if json.Unmarshal(scanner.Bytes(), &m) != nil {
			fatal("malformed client JSON")
		}
		reply := func(result any) { send(map[string]any{"id": m.ID, "result": result}) }
		if m.Method == "" {
			if string(m.ID) == `"unknown-request"` {
				if m.Error.Code != -32601 {
					fatal("expected method-not-found")
				}
				text("unknown answered")
				done("completed")
				continue
			}
			if string(m.ID) != pending || pending == "" {
				fatal("wrong approval id")
			}
			if m.Result.Decision != expect {
				fatal("wrong decision: " + m.Result.Decision)
			}
			pending = ""
			status := "completed"
			exit := 0
			if expect == "decline" {
				status = "declined"
				exit = 1
			}
			if file {
				item(true, map[string]any{"id": "tool", "type": "fileChange", "changes": []any{map[string]any{"path": "hello.txt", "kind": map[string]any{"type": "add"}, "diff": "nexus"}}, "status": status})
			} else {
				item(true, map[string]any{"id": "tool", "type": "commandExecution", "command": "echo nexus", "cwd": "/tmp", "status": status, "exitCode": exit, "aggregatedOutput": "token=supersecret\n" + strings.Repeat("x", 70000)})
			}
			if expect != "decline" || !strings.HasPrefix(active, "interrupt") {
				done("completed")
			}
			continue
		}
		switch m.Method {
		case "initialize":
			var p struct {
				ClientInfo struct {
					Name    string `json:"name"`
					Version string `json:"version"`
				} `json:"clientInfo"`
			}
			_ = json.Unmarshal(m.Params, &p)
			if initialized || p.ClientInfo.Name != "nexus" || p.ClientInfo.Version == "" {
				fatal("initialize")
			}
			initialized = true
			reply(map[string]any{"userAgent": "fakecodex", "codexHome": os.TempDir(), "platformFamily": "windows", "platformOs": "windows"})
		case "initialized":
			if !initialized {
				fatal("not initialized")
			}
			ready = true
		case "thread/start", "thread/resume":
			var p struct {
				Cwd          string `json:"cwd"`
				Approval     string `json:"approvalPolicy"`
				Reviewer     string `json:"approvalsReviewer"`
				Sandbox      string `json:"sandbox"`
				ThreadID     string `json:"threadId"`
				Model        string `json:"model"`
				Instructions string `json:"developerInstructions"`
			}
			_ = json.Unmarshal(m.Params, &p)
			if !ready || thread || p.Cwd == "" || !validPolicy(p.Approval, p.Sandbox) || p.Reviewer != "user" || p.Model != "test-model" ||
				// Context seeds new threads only; a resumed thread has its history.
				(m.Method == "thread/start" && p.Instructions != "seed context") || (m.Method == "thread/resume" && p.Instructions != "") {
				fatal("thread options")
			}
			if m.Method == "thread/resume" && p.ThreadID != "12345678-1234-5678-9012-123456789abc" {
				fatal("resume UUID")
			}
			thread = true
			reply(map[string]any{
				"thread":         map[string]any{"id": "fake-thread", "sessionId": "fake-thread", "cliVersion": "0.167.0", "createdAt": time.Now().Unix(), "updatedAt": time.Now().Unix(), "cwd": p.Cwd, "ephemeral": false, "modelProvider": "openai", "preview": "", "projectId": nil, "source": "vscode", "status": map[string]any{"type": "idle"}, "turns": []any{}},
				"approvalPolicy": "on-request", "approvalsReviewer": "user", "cwd": p.Cwd, "model": p.Model, "modelProvider": "openai",
				"sandbox": map[string]any{"type": "workspaceWrite", "writableRoots": []string{p.Cwd}, "networkAccess": false, "excludeTmpdirEnvVar": false, "excludeSlashTmp": false},
			})
		case "turn/start":
			var p struct {
				ThreadID string `json:"threadId"`
				Input    []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"input"`
			}
			_ = json.Unmarshal(m.Params, &p)
			if !thread || active != "" || p.ThreadID != "fake-thread" || len(p.Input) != 1 || p.Input[0].Type != "text" {
				fatal("turn start sequencing")
			}
			turn++
			prompt := p.Input[0].Text
			active = fmt.Sprintf("turn-%d", turn)
			if prompt == "interrupt" {
				active = "interrupt-turn"
			}
			reply(map[string]any{"turn": map[string]any{"id": active, "items": []any{}, "status": "inProgress", "error": nil}})
			notify("turn/started", map[string]any{"threadId": "fake-thread", "turn": map[string]any{"id": active, "items": []any{}, "status": "inProgress"}})
			switch prompt {
			case "crash":
				fmt.Fprintln(os.Stderr, "fake failure token=supersecret")
				os.Exit(7)
			case "hang":
				text("hanging")
				time.Sleep(time.Hour)
			case "wait":
				text("waiting")
			case "argv":
				b, _ := json.Marshal(os.Args[1:])
				text(string(b))
				done("completed")
			case "unknown":
				send(map[string]any{"id": "unknown-request", "method": "future/request"})
			case "mcp":
				i := map[string]any{"id": "mcp-1", "type": "mcpToolCall", "server": "example", "tool": "lookup", "arguments": map[string]any{"token": "original-secret", "query": "nexus"}, "status": "inProgress"}
				item(false, i)
				i["status"] = "failed"
				i["result"] = map[string]any{"content": []any{map[string]any{"type": "text", "text": "token=supersecret"}}}
				i["error"] = map[string]any{"message": "lookup failed"}
				item(true, i)
				done("completed")
			case "allow", "decline", "file", "interrupt":
				file = prompt == "file"
				expect = "accept"
				if prompt == "decline" || prompt == "interrupt" {
					expect = "decline"
				}
				method := "item/commandExecution/requestApproval"
				params := map[string]any{"threadId": "fake-thread", "turnId": active, "itemId": "tool", "command": "echo nexus", "cwd": "/tmp", "startedAtMs": time.Now().UnixMilli()}
				pending = "42"
				if file {
					method = "item/fileChange/requestApproval"
					delete(params, "command")
					delete(params, "cwd")
					pending = `"file-approval"`
					item(false, map[string]any{"id": "tool", "type": "fileChange", "status": "inProgress", "changes": []any{map[string]any{"path": "hello.txt", "kind": map[string]any{"type": "add"}, "diff": "nexus"}}})
				} else {
					item(false, map[string]any{"id": "tool", "type": "commandExecution", "command": "echo nexus", "cwd": "/tmp", "status": "inProgress"})
				}
				var rid any
				_ = json.Unmarshal([]byte(pending), &rid)
				send(map[string]any{"id": rid, "method": method, "params": params})
			default:
				fmt.Fprintln(os.Stdout, "{malformed")
				notify("item/reasoning/textDelta", map[string]any{"delta": "private reasoning"})
				count := 1
				if prompt == "flood" {
					count = 2000
				}
				for i := 0; i < count; i++ {
					for _, delta := range []string{"Hello", " world"} {
						notify("item/agentMessage/delta", map[string]any{"threadId": "fake-thread", "turnId": active, "itemId": "message", "delta": delta})
					}
				}
				text("Hello world")
				done("completed")
			}
		case "turn/interrupt":
			var p struct {
				ThreadID string `json:"threadId"`
				TurnID   string `json:"turnId"`
			}
			_ = json.Unmarshal(m.Params, &p)
			if active == "" || p.ThreadID != "fake-thread" || p.TurnID != active || pending != "" {
				fatal("interrupt")
			}
			reply(map[string]any{})
			done("interrupted")
		default:
			fatal("unexpected method " + m.Method)
		}
	}
}

// validPolicy accepts the approval policy and sandbox pairs Nexus sends for
// its permission modes (ask, auto-approve edits, full access, plan).
func validPolicy(approval, sandbox string) bool {
	switch approval + "/" + sandbox {
	case "untrusted/workspace-write", "on-request/workspace-write", "never/danger-full-access", "on-request/read-only":
		return true
	}
	return false
}
