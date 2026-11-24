// fakeclaude implements just the live SDK envelopes exercised by the tests.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

func emit(v any) { b, _ := json.Marshal(v); fmt.Println(string(b)) }
func result() {
	emit(map[string]any{"type": "result", "subtype": "success", "session_id": "fake-session", "total_cost_usd": 0.125})
}
func main() {
	emit(map[string]any{"type": "system", "subtype": "init", "session_id": "fake-session"})
	tool := false
	wait := false
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 8*1024*1024)
	for scanner.Scan() {
		var m struct {
			Type    string `json:"type"`
			Message struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"message"`
			Response struct {
				RequestID string `json:"request_id"`
				Response  struct {
					Behavior     string         `json:"behavior"`
					UpdatedInput map[string]any `json:"updatedInput"`
					Message      string         `json:"message"`
				} `json:"response"`
			} `json:"response"`
			Request struct {
				Subtype string `json:"subtype"`
			} `json:"request"`
		}
		if json.Unmarshal(scanner.Bytes(), &m) != nil {
			continue
		}
		switch m.Type {
		case "user":
			text := m.Message.Content[0].Text
			if text == "crash" {
				fmt.Fprintln(os.Stderr, "fake failure password=supersecret")
				os.Exit(7)
			}
			if text == "hang" {
				emit(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{map[string]any{"type": "text", "text": "hanging"}}}})
				for { /* Deliberately ignore EOF until killed. */
					time.Sleep(time.Hour)
				}
			}
			if text == "argv" {
				b, _ := json.Marshal(os.Args[1:])
				emit(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{map[string]any{"type": "text", "text": string(b)}}}})
				result()
				continue
			}
			fmt.Println("{malformed")
			emit(map[string]any{"type": "future_unknown", "value": 1})
			count := 2
			if text == "flood" {
				count = 4000
			}
			for i := 0; i < count; i++ {
				delta := []string{"Hello", " world"}[i%2]
				emit(map[string]any{"type": "stream_event", "event": map[string]any{"type": "content_block_delta", "delta": map[string]any{"type": "text_delta", "text": delta}}})
			}
			if strings.HasPrefix(text, "tool") {
				tool = true
				emit(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{map[string]any{"type": "tool_use", "id": "tool-1", "name": "Bash", "input": map[string]any{"command": "echo hello", "token": "original-secret"}}}}})
				emit(map[string]any{"type": "control_request", "request_id": "permission-1", "request": map[string]any{"subtype": "can_use_tool", "tool_use_id": "tool-1", "tool_name": "Bash", "input": map[string]any{"command": "echo hello", "token": "original-secret"}}})
			} else if text == "wait" {
				wait = true
				emit(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{map[string]any{"type": "text", "text": "waiting"}}}})
			} else {
				result()
			}
		case "control_response":
			if !tool || m.Response.RequestID != "permission-1" {
				os.Exit(8)
			}
			allow := m.Response.Response.Behavior == "allow"
			if allow && m.Response.Response.UpdatedInput["token"] != "original-secret" {
				os.Exit(9)
			}
			if !allow && m.Response.Response.Message != "no thanks" {
				os.Exit(10)
			}
			output := "allowed password=supersecret " + strings.Repeat("x", 70000)
			if !allow {
				output = "denied"
			}
			emit(map[string]any{"type": "user", "message": map[string]any{"content": []any{map[string]any{"type": "tool_result", "tool_use_id": "tool-1", "content": []any{map[string]any{"type": "text", "text": output}}, "is_error": !allow}}}})
			tool = false
			result()
		case "control_request":
			if m.Request.Subtype == "interrupt" && wait {
				wait = false
				emit(map[string]any{"type": "result", "subtype": "error_during_execution", "is_error": true, "result": "interrupted"})
			}
		}
	}
}
