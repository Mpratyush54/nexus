// democlaude is a scripted stand-in for Claude Code's stream-json mode, for
// trying the desktop's live chats without a real agent (UI checks, demos,
// screenshots). Put the built binary first on PATH as "claude" for an
// isolated NEXUS_PROFILE instance. It never runs a command or edits a file:
// tool calls are only described.
//
// A reply streams word by word (DEMO_WORD_MS, default 90 ms), after a moment
// of "thinking". Messages that mention "test" ask to run `npm test`, ones that
// mention "edit" ask to edit a file in the folder, the way Claude Code asks
// its permission host. --permission-mode acceptEdits skips the edit request
// and bypassPermissions skips both, like the real CLI. An interrupt control
// request ends the turn early.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	outMu sync.Mutex
	mode  string
)

func emit(v any) {
	b, _ := json.Marshal(v)
	outMu.Lock()
	fmt.Println(string(b))
	outMu.Unlock()
}

type inbound struct {
	Type    string `json:"type"`
	Message struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"message"`
	Request struct {
		Subtype string `json:"subtype"`
	} `json:"request"`
	Response struct {
		RequestID string `json:"request_id"`
		Response  struct {
			Behavior string `json:"behavior"`
		} `json:"response"`
	} `json:"response"`
}

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-v") {
		fmt.Println("2.1.332 (Claude Code)")
		return
	}
	session := "demo-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	for i := 1; i < len(os.Args)-1; i++ {
		switch os.Args[i] {
		case "--session-id", "--resume":
			session = os.Args[i+1]
		case "--permission-mode":
			mode = os.Args[i+1]
		}
	}
	emit(map[string]any{"type": "system", "subtype": "init", "session_id": session})

	users := make(chan string, 16)
	interrupts := make(chan struct{}, 4)
	answers := make(chan string, 4)
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
		for scanner.Scan() {
			var m inbound
			if json.Unmarshal(scanner.Bytes(), &m) != nil {
				continue
			}
			switch m.Type {
			case "user":
				text := ""
				for _, c := range m.Message.Content {
					if c.Type == "text" {
						text += c.Text
					}
				}
				users <- text
			case "control_request":
				if m.Request.Subtype == "interrupt" {
					interrupts <- struct{}{}
				}
			case "control_response":
				answers <- m.Response.Response.Behavior
			}
		}
		os.Exit(0)
	}()
	wordDelay := 90 * time.Millisecond
	if ms, err := strconv.Atoi(os.Getenv("DEMO_WORD_MS")); err == nil && ms > 0 {
		wordDelay = time.Duration(ms) * time.Millisecond
	}
	seq := 0
	for text := range users {
		seq++
		turn(session, text, seq, wordDelay, interrupts, answers)
	}
}

func turn(session, text string, seq int, delay time.Duration, interrupts <-chan struct{}, answers <-chan string) {
	stopped := func() bool {
		select {
		case <-interrupts:
			emit(map[string]any{"type": "result", "subtype": "error_during_execution", "is_error": true, "result": "interrupted", "session_id": session})
			return true
		default:
			return false
		}
	}
	for _, t := range []string{"Looking at ", "the request ", "and the project…"} {
		emit(map[string]any{"type": "stream_event", "event": map[string]any{"type": "content_block_delta", "delta": map[string]any{"type": "thinking_delta", "thinking": t}}})
		time.Sleep(300 * time.Millisecond)
	}
	// Like Claude Code, each text block (before a tool, and the last one)
	// ends with its own assistant message.
	var reply strings.Builder
	block := func() {
		if text := strings.TrimSpace(reply.String()); text != "" {
			emit(map[string]any{"type": "assistant", "session_id": session, "message": map[string]any{"id": fmt.Sprintf("msg-%d-%d", seq, time.Now().UnixNano()), "model": "claude-demo", "content": []any{map[string]any{"type": "text", "text": text}}}})
		}
		reply.Reset()
	}
	say := func(s string) bool {
		for _, w := range strings.SplitAfter(s, " ") {
			if stopped() {
				return false
			}
			reply.WriteString(w)
			emit(map[string]any{"type": "stream_event", "event": map[string]any{"type": "content_block_delta", "delta": map[string]any{"type": "text_delta", "text": w}}})
			time.Sleep(delay)
		}
		return true
	}
	lower := strings.ToLower(text)
	if !say(fmt.Sprintf("Sure. I'll work through this step by step (request %d). ", seq)) {
		return
	}
	if strings.Contains(lower, "test") && mode != "bypassPermissions" {
		if !say("First I'll run the test suite to see where things stand. ") {
			return
		}
		block()
		if !ask(session, seq, "Bash", map[string]any{"command": "npm test", "description": "Run the tests"}, answers, interrupts, "> checkout@1.0.0 test\n> vitest run\n\n✓ 42 passed (1.8s)") {
			return
		}
		if !say("All 42 tests pass. ") {
			return
		}
	}
	if strings.Contains(lower, "edit") && mode != "acceptEdits" && mode != "bypassPermissions" {
		cwd, _ := os.Getwd()
		input := map[string]any{"file_path": filepath.Join(cwd, "src", "retry.ts"), "old_string": "const MAX_RETRIES = 5;", "new_string": "const MAX_RETRIES = 3; // retries reuse order_key"}
		block()
		if !ask(session, seq, "Edit", input, answers, interrupts, "The file was updated.") {
			return
		}
	}
	body := "Here is what I found. The retry path reuses the same order key, so a second charge can't happen; " +
		"the duplicate came from a client that generated a fresh key on every attempt. I kept the change small and covered it with a test. " +
		"Nothing else in the checkout flow needs to change, and the logs now carry the request ID on every line."
	if !say(body) {
		return
	}
	block()
	emit(map[string]any{"type": "result", "subtype": "success", "session_id": session, "total_cost_usd": 0.01})
}

// ask describes a tool call, asks the permission host, and reports the result.
func ask(session string, seq int, tool string, input map[string]any, answers <-chan string, interrupts <-chan struct{}, output string) bool {
	id := fmt.Sprintf("tool-%d-%s", seq, strings.ToLower(tool))
	emit(map[string]any{"type": "assistant", "session_id": session, "message": map[string]any{"id": id + "-use", "content": []any{map[string]any{"type": "tool_use", "id": id, "name": tool, "input": input}}}})
	emit(map[string]any{"type": "control_request", "request_id": "perm-" + id, "request": map[string]any{"subtype": "can_use_tool", "tool_use_id": id, "tool_name": tool, "input": input}})
	select {
	case behavior := <-answers:
		allowed := behavior == "allow"
		result := output
		if !allowed {
			result = "The user declined this action."
		}
		emit(map[string]any{"type": "user", "session_id": session, "message": map[string]any{"content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "content": []any{map[string]any{"type": "text", "text": result}}, "is_error": !allowed}}}})
		return true
	case <-interrupts:
		emit(map[string]any{"type": "result", "subtype": "error_during_execution", "is_error": true, "result": "interrupted", "session_id": session})
		return false
	}
}
