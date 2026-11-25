package continuex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"central-memory/internal/usage"
)

var claudeLookPath = exec.LookPath

const liveLineLimit = 8 * 1024 * 1024

type claudeLive struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	writes    chan struct{}
	done      chan struct{}
	closeOnce sync.Once
	mu        sync.Mutex
	nativeID  string
	announced bool // native id sent to the caller once
	pending   map[string]json.RawMessage
	queue     []LiveEvent
	finished  bool
	wake      chan struct{}
	events    chan LiveEvent
	sequence  atomic.Uint64
	// turn accumulates message.usage while a turn streams; cumulative is
	// the process-wide modelUsage from the previous result event.
	turn       turnUsage
	cumulative map[string]usage.Entry
}

// StartClaudeLive starts one independent, persistent Claude Code process.
// The stream/partial-message flags and permission-prompts host are confirmed
// by the installed CLI's --help. Control envelopes follow its SDK protocol.
func StartClaudeLive(ctx context.Context, opts LiveOptions) (LiveSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	info, err := os.Stat(opts.Workspace)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("claude live: workspace must be an existing directory")
	}
	if opts.PermissionMode != "" && opts.PermissionMode != "acceptEdits" && opts.PermissionMode != "plan" {
		return nil, errors.New("claude live: invalid permission mode")
	}
	// Reuse the public model validator; its argument insertion is not used here.
	if _, err := WithModel(LaunchPlan{Agent: "claude", Argv: []string{"claude"}}, opts.Model); err != nil {
		return nil, err
	}
	path, err := claudeLookPath("claude")
	if err != nil {
		return nil, fmt.Errorf("Claude Code is not installed or not on PATH: %w", err)
	}
	path, prefix, err := directExecutable(path, "claude")
	if err != nil {
		return nil, err
	}
	// Verified against the real CLI (2026-11-22): without
	// --permission-prompt-tool stdio, "host" prompts are denied on the spot
	// ("write permission ... hasn't been granted") instead of being sent to us.
	args := []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--include-partial-messages", "--permission-prompts", "host", "--permission-prompt-tool", "stdio"}
	// A new chat gets its session id from us, so the caller knows which
	// transcript file it will write before the first byte is written.
	newSessionID := ""
	if opts.ResumeID == "" {
		newSessionID = newUUID()
		args = append(args, "--session-id", newSessionID)
	}
	for _, pair := range [][2]string{{"--resume", opts.ResumeID}, {"--model", strings.TrimSpace(opts.Model)}, {"--permission-mode", opts.PermissionMode}, {"--append-system-prompt", opts.SystemContext}} {
		if pair[1] != "" {
			args = append(args, pair[0], pair[1])
		}
	}
	// Extra MCP servers go through a private temp file, not argv, so tokens
	// never appear in the process list.
	mcpFile, allow, err := claudeMCPConfig(opts.MCPServers)
	if err != nil {
		return nil, err
	}
	if mcpFile != "" {
		args = append(args, "--mcp-config", mcpFile)
		if len(allow) > 0 {
			args = append(args, "--allowedTools", strings.Join(allow, " "))
		}
	}
	cmd := exec.Command(path, append(prefix, args...)...)
	cmd.Dir = opts.Workspace
	cmd.WaitDelay = 500 * time.Millisecond
	hideConsole(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	// Own the stdout pipe so Cmd.Wait cannot close it before the final
	// buffered result/exited lines have been parsed.
	stdout, childStdout, err := os.Pipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	cmd.Stdout = childStdout
	stderr := &liveStderr{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = childStdout.Close()
		return nil, fmt.Errorf("claude live: start: %w", err)
	}
	containTree(cmd)
	_ = childStdout.Close()
	s := &claudeLive{cmd: cmd, stdin: stdin, writes: make(chan struct{}, 1), done: make(chan struct{}), pending: make(map[string]json.RawMessage), wake: make(chan struct{}, 1), events: make(chan LiveEvent, 256), nativeID: newSessionID}
	go s.deliver()
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		defer stdout.Close()
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 64*1024), liveLineLimit+2)
		for scanner.Scan() {
			s.parse(scanner.Bytes())
		}
		if err := scanner.Err(); err != nil {
			s.emit(LiveEvent{Kind: "error", Text: err.Error(), IsError: true})
		}
	}()
	go func() {
		err := cmd.Wait()
		killTree(cmd)
		if mcpFile != "" {
			_ = os.Remove(mcpFile)
		}
		select {
		case <-readDone:
		case <-time.After(500 * time.Millisecond):
			// A descendant may retain the inherited stdout handle after exit.
			_ = stdout.Close()
			<-readDone
		}
		_ = stdin.Close()
		if err != nil {
			s.emit(LiveEvent{Kind: "error", Text: err.Error() + ": " + stderr.String(), IsError: true})
		}
		s.emit(LiveEvent{Kind: "exited", IsError: err != nil})
		s.mu.Lock()
		s.finished = true
		s.mu.Unlock()
		s.signal()
		close(s.done)
	}()
	go func() {
		select {
		case <-ctx.Done():
			_ = s.Close()
		case <-s.done:
		}
	}()
	// The SDK opens every control session with an initialize request; the
	// CLI answers with a control_response (ignored by parse).
	if err := s.write(ctx, map[string]any{"type": "control_request", "request_id": "nexus-init", "request": map[string]any{"subtype": "initialize"}}); err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("claude live: initialize: %w", err)
	}
	return s, nil
}

func (s *claudeLive) Events() <-chan LiveEvent { return s.events }
func (s *claudeLive) NativeID() string         { s.mu.Lock(); defer s.mu.Unlock(); return s.nativeID }
func (s *claudeLive) write(ctx context.Context, value any) error {
	select {
	case s.writes <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	case <-s.done:
		return errors.New("claude live: process exited")
	}
	defer func() { <-s.writes }()
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	// Closing stdin (including on session cancellation) unblocks a pending write.
	_, err = s.stdin.Write(append(data, '\n'))
	return err
}
func (s *claudeLive) Send(ctx context.Context, text string) error {
	return s.write(ctx, map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": text}}}})
}
func (s *claudeLive) Approve(id string, allow bool, reason string) error {
	s.mu.Lock()
	input, ok := s.pending[id]
	if ok {
		delete(s.pending, id)
	}
	s.mu.Unlock()
	if !ok {
		return errors.New("claude live: unknown permission request")
	}
	answer := map[string]any{"behavior": "deny", "message": reason}
	if allow {
		answer = map[string]any{"behavior": "allow", "updatedInput": input}
	}
	return s.write(context.Background(), map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": id, "response": answer}})
}
func (s *claudeLive) Interrupt() error {
	return s.write(context.Background(), map[string]any{"type": "control_request", "request_id": fmt.Sprintf("nexus-interrupt-%d", s.sequence.Add(1)), "request": map[string]any{"subtype": "interrupt"}})
}
func (s *claudeLive) Close() error {
	s.closeOnce.Do(func() { _ = s.stdin.Close() })
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	select {
	case <-s.done:
		return nil
	case <-timer.C:
	}
	killTree(s.cmd)
	_ = s.cmd.Process.Kill()
	select {
	case <-s.done:
		return nil
	case <-time.After(time.Second):
		return errors.New("claude live: timed out waiting for process exit")
	}
}

func (s *claudeLive) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
func (s *claudeLive) emit(e LiveEvent) {
	e.At = time.Now()
	s.mu.Lock()
	n := len(s.queue)
	if e.Kind == "text_delta" && n >= 256 && s.queue[n-1].Kind == "text_delta" {
		s.queue[n-1].Text += e.Text
	} else {
		s.queue = append(s.queue, e)
	}
	s.mu.Unlock()
	s.signal()
}

// Critical events are retained in a private queue; stdout never waits for the
// consumer. After exit, Events closes once the retained events have drained.
func (s *claudeLive) deliver() {
	defer close(s.events)
	for {
		s.mu.Lock()
		if len(s.queue) == 0 {
			finished := s.finished
			s.mu.Unlock()
			if finished {
				return
			}
			<-s.wake
			continue
		}
		e := s.queue[0]
		s.queue[0] = LiveEvent{}
		s.queue = s.queue[1:]
		s.mu.Unlock()
		s.events <- e
	}
}

// Keep delta whitespace: activityDetail otherwise trims it, breaking words.
func liveText(text string, limit int) string {
	trimmed := strings.TrimSpace(text)
	result := text
	if trimmed != "" {
		start := strings.Index(text, trimmed)
		result = text[:start] + activityDetail(trimmed, liveLineLimit) + text[start+len(trimmed):]
	}
	if len(result) > limit {
		result = result[:limit]
		for !utf8.ValidString(result) {
			result = result[:len(result)-1]
		}
	}
	return result
}
func liveInput(raw json.RawMessage) json.RawMessage {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	b, _ := json.Marshal(redactActivityInput(value, 0))
	return b
}
func liveContent(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var blocks []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		var parts []string
		for _, b := range blocks {
			if b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return string(raw)
}
func (s *claudeLive) parse(line []byte) {
	var m struct {
		Type      string  `json:"type"`
		Subtype   string  `json:"subtype"`
		SessionID string  `json:"session_id"`
		ParentID  string  `json:"parent_tool_use_id"`
		RequestID string  `json:"request_id"`
		IsError   bool    `json:"is_error"`
		Cost      float64 `json:"total_cost_usd"`
		// modelUsage is cumulative for the CLI process (with costUSD).
		ModelUsage map[string]claudeModelUsage `json:"modelUsage"`
		Result     string                      `json:"result"`
		Errors     []string                    `json:"errors"`
		Event      struct {
			Type  string `json:"type"`
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
		} `json:"event"`
		Message struct {
			ID    string `json:"id"`
			Model string `json:"model"`
			Usage *struct {
				Input      int64 `json:"input_tokens"`
				Output     int64 `json:"output_tokens"`
				CacheRead  int64 `json:"cache_read_input_tokens"`
				CacheWrite int64 `json:"cache_creation_input_tokens"`
			} `json:"usage"`
			Content []struct {
				Type      string          `json:"type"`
				Text      string          `json:"text"`
				ID        string          `json:"id"`
				Name      string          `json:"name"`
				Input     json.RawMessage `json:"input"`
				ToolUseID string          `json:"tool_use_id"`
				Content   json.RawMessage `json:"content"`
				IsError   bool            `json:"is_error"`
			} `json:"content"`
		} `json:"message"`
		Request struct {
			Subtype   string          `json:"subtype"`
			ToolName  string          `json:"tool_name"`
			ToolUseID string          `json:"tool_use_id"`
			Input     json.RawMessage `json:"input"`
		} `json:"request"`
	}
	if json.Unmarshal(line, &m) != nil {
		return
	}
	switch m.Type {
	case "system", "stream_event", "assistant", "user", "control_request", "result":
	default:
		return
	}
	if m.SessionID != "" {
		s.mu.Lock()
		first := !s.announced
		s.announced = true
		s.nativeID = m.SessionID
		s.mu.Unlock()
		if first {
			s.emit(LiveEvent{Kind: "status", Text: "Claude Code session initialized", NativeID: m.SessionID})
		}
	}
	switch m.Type {
	case "system":
		// init is surfaced via the session id above; other subtypes
		// (status, commands_changed, thinking_tokens, ...) are CLI internals
		// that would only clutter the chat.
		if m.Subtype == "compact_boundary" {
			s.emit(LiveEvent{Kind: "status", Text: "Conversation compacted"})
		}
	case "stream_event":
		// Sub-agent token deltas would interleave with the main reply.
		if m.ParentID == "" && m.Event.Type == "content_block_delta" && m.Event.Delta.Type == "text_delta" {
			s.emit(LiveEvent{Kind: "text_delta", Text: liveText(m.Event.Delta.Text, liveLineLimit)})
		}
	case "assistant", "user":
		if u := m.Message.Usage; m.Type == "assistant" && u != nil && m.Message.Model != "<synthetic>" {
			// Sub-agent calls cost too, so they count toward the turn.
			if s.turn.observe(m.Message.ID, usage.Entry{Model: m.Message.Model, Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite}) {
				s.emit(LiveEvent{Kind: "usage", Usage: s.turn.byModel()})
			}
		}
		for _, b := range m.Message.Content {
			switch b.Type {
			case "text":
				if m.ParentID != "" {
					// A sub-agent's own prose is progress of that sub-agent,
					// not part of the main reply.
					if m.Type == "assistant" {
						s.emit(LiveEvent{Kind: "subagent_text", ToolID: m.ParentID, ParentToolID: m.ParentID, Text: liveText(b.Text, 4*1024)})
					}
					continue
				}
				s.emit(LiveEvent{Kind: "text", Text: liveText(b.Text, liveLineLimit)})
			case "tool_use":
				s.emit(LiveEvent{Kind: "tool_start", ToolID: b.ID, ToolName: b.Name, ToolInput: liveInput(b.Input), ParentToolID: m.ParentID})
			case "tool_result":
				s.emit(LiveEvent{Kind: "tool_end", ToolID: b.ToolUseID, ToolOutput: liveText(liveContent(b.Content), 64*1024), IsError: b.IsError, ParentToolID: m.ParentID})
			}
		}
	case "control_request":
		if m.Request.Subtype == "can_use_tool" && m.RequestID != "" {
			input := m.Request.Input
			if len(input) == 0 {
				input = json.RawMessage(`{}`)
			}
			s.mu.Lock()
			s.pending[m.RequestID] = append(json.RawMessage(nil), input...)
			s.mu.Unlock()
			s.emit(LiveEvent{Kind: "approval", RequestID: m.RequestID, ToolID: m.Request.ToolUseID, ToolName: m.Request.ToolName, ToolInput: liveInput(input)})
		}
	case "result":
		turn := s.turn.byModel()
		s.turn.reset()
		if len(m.ModelUsage) > 0 {
			// The CLI's own per-model totals include its cost; prefer them.
			s.mu.Lock()
			if s.cumulative == nil {
				s.cumulative = map[string]usage.Entry{}
			}
			turn = claudeTurnDelta(m.ModelUsage, s.cumulative)
			s.mu.Unlock()
		}
		cost := m.Cost // older CLIs without modelUsage: pass through as before
		if len(m.ModelUsage) > 0 {
			cost = 0
			for _, e := range turn {
				if e.CostUSD != nil {
					cost += *e.CostUSD
				}
			}
		}
		s.emit(LiveEvent{Kind: "turn_done", Text: liveText(strings.Join(append(m.Errors, m.Result), "\n"), liveLineLimit), NativeID: m.SessionID, IsError: m.IsError, CostUSD: cost, Usage: turn})
	}
}

type liveStderr struct {
	mu   sync.Mutex
	data []byte
}

func (b *liveStderr) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := min(len(p), 64*1024-len(b.data))
	b.data = append(b.data, p[:n]...)
	return len(p), nil
}
func (b *liveStderr) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return liveText(string(b.data), 64*1024)
}

// claudeMCPConfig writes the extra MCP servers to a private temp file in
// Claude Code's --mcp-config format and returns the auto-allowed tool names
// (mcp__<server>__<tool>). "" when there are none.
func claudeMCPConfig(servers []MCPServer) (string, []string, error) {
	if len(servers) == 0 {
		return "", nil, nil
	}
	cfg := map[string]any{}
	var allow []string
	for _, srv := range servers {
		if srv.Name == "" || srv.URL == "" {
			continue
		}
		cfg[srv.Name] = map[string]any{"type": "http", "url": srv.URL, "headers": srv.Headers}
		for _, tool := range srv.AutoAllow {
			allow = append(allow, "mcp__"+srv.Name+"__"+tool)
		}
	}
	if len(cfg) == 0 {
		return "", nil, nil
	}
	data, err := json.Marshal(map[string]any{"mcpServers": cfg})
	if err != nil {
		return "", nil, err
	}
	f, err := os.CreateTemp("", "nexus-mcp-*.json")
	if err != nil {
		return "", nil, err
	}
	name := f.Name()
	_ = f.Chmod(0o600)
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(name)
		return "", nil, err
	}
	if err := f.Close(); err != nil {
		os.Remove(name)
		return "", nil, err
	}
	return name, allow, nil
}
