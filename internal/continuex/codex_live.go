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
)

var codexLookPath = lookAgent

type codexRPC struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type codexApproval struct {
	id   json.RawMessage
	turn string
}
type codexMessage struct {
	ctx    context.Context
	text   string
	images []string
}

// codexInput is one turn's input: local images (the app-server reads the
// files itself) and then the text.
func codexInput(msg codexMessage) []any {
	input := []any{}
	for _, path := range msg.images {
		input = append(input, map[string]any{"type": "localImage", "path": path})
	}
	return append(input, map[string]any{"type": "text", "text": msg.text})
}

type codexLive struct {
	cmd           *exec.Cmd
	stdin         io.WriteCloser
	writes        chan struct{}
	done          chan struct{}
	closeOnce     sync.Once
	sequence      atomic.Uint64
	mu            sync.Mutex
	nativeID      string
	turnID        string
	busy          bool
	interrupt     bool
	interruptSent bool
	stopping      bool
	finished      bool
	queue         []LiveEvent
	wake          chan struct{}
	events        chan LiveEvent
	work          chan struct{}
	messages      []codexMessage
	calls         map[string]chan codexRPC
	pending       map[string]codexApproval
	items         map[string]json.RawMessage
	// model is the thread's model (thread/start response); turn sums
	// tokenUsage.last per model call while a turn runs.
	model string
	turn  turnUsage
}

// StartCodexLive runs one app-server over newline-delimited JSON-RPC on stdio.
// The v2 methods and shapes were checked against codex-cli 0.167.0's generated
// JSON schemas. Initialization and thread/turn start were also exercised live.
func StartCodexLive(ctx context.Context, opts LiveOptions) (LiveSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	info, err := os.Stat(opts.Workspace)
	if err != nil || !info.IsDir() {
		return nil, errors.New("codex live: workspace must be an existing directory")
	}
	if opts.PermissionMode != "" && opts.PermissionMode != "acceptEdits" && opts.PermissionMode != "plan" {
		return nil, errors.New("codex live: invalid permission mode")
	}
	if _, err := WithModel(LaunchPlan{Agent: "codex", Argv: []string{"codex"}}, opts.Model); err != nil {
		return nil, err
	}
	path, err := codexLookPath("codex")
	if err != nil {
		return nil, agentErrorf("codex", "not_installed", "Codex is not installed or not on PATH: %v", err)
	}
	path, prefix, err := directExecutable(path, "codex")
	if err != nil {
		return nil, AsAgentError("codex", "cannot_start", err)
	}
	cmd := exec.Command(path, append(prefix, "app-server")...)
	cmd.Dir = opts.Workspace
	cmd.WaitDelay = 500 * time.Millisecond
	hideConsole(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, childStdout, err := os.Pipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	cmd.Stdout = childStdout
	stderr := &liveStderr{}
	cmd.Stderr = stderr
	if err = cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = childStdout.Close()
		return nil, fmt.Errorf("codex live: start: %w", err)
	}
	containTree(cmd)
	_ = childStdout.Close()
	s := &codexLive{cmd: cmd, stdin: stdin, writes: make(chan struct{}, 1), done: make(chan struct{}), wake: make(chan struct{}, 1), events: make(chan LiveEvent, 256), work: make(chan struct{}, 1), calls: make(map[string]chan codexRPC), pending: make(map[string]codexApproval), items: make(map[string]json.RawMessage)}
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
		select {
		case <-readDone:
		case <-time.After(500 * time.Millisecond):
			_ = stdout.Close()
			<-readDone
		}
		_ = stdin.Close()
		if err != nil {
			s.emit(LiveEvent{Kind: "error", Text: err.Error() + ": " + stderr.String(), IsError: true})
		}
		s.mu.Lock()
		s.queue = append(s.queue, LiveEvent{Kind: "exited", IsError: err != nil, At: time.Now()})
		s.finished = true
		s.stopping = true
		s.messages = nil
		s.mu.Unlock()
		close(s.done)
		s.signal(s.wake)
	}()
	go func() {
		select {
		case <-ctx.Done():
			_ = s.Close()
		case <-s.done:
		}
	}()
	go s.run(ctx, opts)
	return s, nil
}

func (s *codexLive) Events() <-chan LiveEvent { return s.events }
func (s *codexLive) NativeID() string         { s.mu.Lock(); defer s.mu.Unlock(); return s.nativeID }
func (s *codexLive) signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}
func (s *codexLive) emit(e LiveEvent) {
	e.At = time.Now()
	s.mu.Lock()
	if s.finished {
		s.mu.Unlock()
		return
	}
	n := len(s.queue)
	if e.Kind == "text_delta" && n >= 256 && s.queue[n-1].Kind == "text_delta" && s.queue[n-1].ToolID == e.ToolID {
		s.queue[n-1].Text += e.Text
	} else {
		s.queue = append(s.queue, e)
	}
	s.mu.Unlock()
	s.signal(s.wake)
}
func (s *codexLive) deliver() {
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
func (s *codexLive) write(ctx context.Context, value any) error {
	select {
	case s.writes <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	case <-s.done:
		return errors.New("codex live: process exited")
	}
	defer func() { <-s.writes }()
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = s.stdin.Write(append(data, '\n'))
	return err
}
func (s *codexLive) rpc(ctx context.Context, method string, params any) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	id := fmt.Sprintf("nexus-%d", s.sequence.Add(1))
	ch := make(chan codexRPC, 1)
	s.mu.Lock()
	s.calls[id] = ch
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.calls, id); s.mu.Unlock() }()
	if err := s.write(ctx, map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	select {
	case m := <-ch:
		if m.Error != nil {
			return nil, errors.New(liveText(m.Error.Message, liveLineLimit))
		}
		return m.Result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.done:
		return nil, errors.New("codex live: process exited")
	}
}
func (s *codexLive) run(ctx context.Context, opts LiveOptions) {
	fail := func(err error) {
		s.emit(LiveEvent{Kind: "error", Text: liveText(err.Error(), liveLineLimit), IsError: true})
		_ = s.Close()
	}
	if _, err := s.rpc(ctx, "initialize", map[string]any{"clientInfo": map[string]string{"name": "nexus", "version": "1.0.0"}}); err != nil {
		fail(err)
		return
	}
	if err := s.write(ctx, map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
		fail(err)
		return
	}
	params := map[string]any{"cwd": opts.Workspace, "approvalPolicy": "on-request", "approvalsReviewer": "user", "sandbox": "workspace-write"}
	if opts.Model != "" {
		params["model"] = strings.TrimSpace(opts.Model)
	}
	if len(opts.MCPServers) > 0 {
		// Same keys as ~/.codex/config.toml [mcp_servers.<name>] (streamable HTTP).
		servers := map[string]any{}
		for _, srv := range opts.MCPServers {
			servers[srv.Name] = map[string]any{"url": srv.URL, "http_headers": srv.Headers}
		}
		params["config"] = map[string]any{"mcp_servers": servers}
	}
	method := "thread/start"
	if opts.ResumeID == "" && opts.SystemContext != "" {
		// developerInstructions preserves the standard Codex base instructions.
		params["developerInstructions"] = opts.SystemContext
	}
	if opts.ResumeID != "" {
		plan, err := Plan("codex", "open_in_agent", opts.ResumeID, "")
		if err != nil {
			fail(err)
			return
		}
		params["threadId"] = plan.Argv[2]
		method = "thread/resume"
	}
	result, err := s.rpc(ctx, method, params)
	if err != nil && method == "thread/resume" {
		// The thread may not exist on this machine (or was never saved);
		// start a new one carrying the earlier conversation.
		delete(params, "threadId")
		method = "thread/start"
		s.emit(LiveEvent{Kind: "status", Text: "Couldn't reopen the earlier Codex thread; continuing in a new one with the conversation so far."})
		if opts.SystemContext != "" {
			params["developerInstructions"] = opts.SystemContext
		}
		result, err = s.rpc(ctx, method, params)
	}
	if err != nil {
		fail(err)
		return
	}
	var response struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
		Model string `json:"model"`
	}
	if json.Unmarshal(result, &response) != nil || response.Thread.ID == "" {
		fail(errors.New("codex live: missing thread id"))
		return
	}
	s.mu.Lock()
	s.nativeID = response.Thread.ID
	s.model = response.Model
	s.mu.Unlock()
	s.emit(LiveEvent{Kind: "status", Text: "Codex is ready", NativeID: response.Thread.ID})
	s.signal(s.work)
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.done:
			return
		case <-s.work:
		}
		for {
			s.mu.Lock()
			if s.stopping || s.busy || len(s.messages) == 0 {
				s.mu.Unlock()
				break
			}
			msg := s.messages[0]
			s.messages[0] = codexMessage{}
			s.messages = s.messages[1:]
			// Send's context covers handing the message over, not the turn.
			// Dropping the message here left the desktop waiting forever.
			s.busy = true
			s.interrupt = false
			s.interruptSent = false
			s.turnID = ""
			s.mu.Unlock()
			// Follow-ups are FIFO turns, deliberately not mid-turn steering.
			result, err = s.rpc(ctx, "turn/start", map[string]any{"threadId": response.Thread.ID, "input": codexInput(msg)})
			if err != nil {
				s.emit(LiveEvent{Kind: "turn_done", Text: liveText(err.Error(), liveLineLimit), IsError: true})
				// A timeout or transport error leaves the server's turn state
				// uncertain. Do not start a queued turn over that active work.
				fail(err)
				return
			}
			var r struct {
				Turn struct {
					ID string `json:"id"`
				} `json:"turn"`
			}
			if json.Unmarshal(result, &r) != nil || r.Turn.ID == "" {
				fail(errors.New("codex live: missing turn id"))
				return
			}
			s.mu.Lock()
			if s.busy && s.turnID == "" {
				s.turnID = r.Turn.ID
			}
			interrupt := s.busy && s.interrupt
			turn := s.turnID
			s.mu.Unlock()
			if interrupt && turn != "" {
				if err := s.interruptTurn(turn); err != nil {
					s.emit(LiveEvent{Kind: "error", Text: liveText(err.Error(), liveLineLimit), IsError: true})
				}
			}
		}
	}
}
func (s *codexLive) Send(ctx context.Context, text string) error {
	return s.SendWithImages(ctx, text, nil)
}

// SendWithImages queues a turn whose input lists each image as localImage.
func (s *codexLive) SendWithImages(ctx context.Context, text string, images []string) error {
	if len(images) > MaxImages {
		return errors.New("attach at most four images")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		return errors.New("codex live: session closed")
	}
	s.messages = append(s.messages, codexMessage{ctx, text, append([]string(nil), images...)})
	s.mu.Unlock()
	s.signal(s.work)
	return nil
}
func codexID(raw json.RawMessage) string {
	var id string
	if json.Unmarshal(raw, &id) == nil {
		return id
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil && n != "" {
		return string(n)
	}
	return ""
}
func (s *codexLive) Approve(id string, allow bool, reason string) error {
	s.mu.Lock()
	p, ok := s.pending[id]
	if ok {
		delete(s.pending, id)
	}
	s.mu.Unlock()
	if !ok {
		return errors.New("codex live: unknown permission request")
	}
	decision := "decline"
	if allow {
		decision = "accept"
	}
	// Approval responses have no free-form reason field in v2.
	return s.write(context.Background(), map[string]any{"id": p.id, "result": map[string]string{"decision": decision}})
}
func (s *codexLive) Interrupt() error {
	s.mu.Lock()
	busy := s.busy
	turn := s.turnID
	if busy {
		s.interrupt = true
	}
	ids := make([]string, 0, len(s.pending))
	for id := range s.pending {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	for _, id := range ids {
		if err := s.Approve(id, false, "interrupted"); err != nil {
			return err
		}
	}
	if busy && turn != "" {
		return s.interruptTurn(turn)
	}
	return nil
}
func (s *codexLive) interruptTurn(turn string) error {
	s.mu.Lock()
	if !s.busy || s.turnID != turn || s.interruptSent {
		s.mu.Unlock()
		return nil
	}
	s.interruptSent = true
	s.mu.Unlock()
	_, err := s.rpc(context.Background(), "turn/interrupt", map[string]string{"threadId": s.NativeID(), "turnId": turn})
	return err
}
func (s *codexLive) Close() error {
	s.closeOnce.Do(func() { s.mu.Lock(); s.stopping = true; s.messages = nil; s.mu.Unlock(); _ = s.stdin.Close() })
	select {
	case <-s.done:
		return nil
	case <-time.After(3 * time.Second):
	}
	killTree(s.cmd)
	_ = s.cmd.Process.Kill()
	select {
	case <-s.done:
		return nil
	case <-time.After(time.Second):
		return errors.New("codex live: timed out waiting for process exit")
	}
}

func (s *codexLive) parse(line []byte) {
	var m codexRPC
	if json.Unmarshal(line, &m) != nil {
		return
	}
	id := codexID(m.ID)
	if m.Method == "" {
		if id != "" {
			s.mu.Lock()
			ch := s.calls[id]
			s.mu.Unlock()
			if ch != nil {
				select {
				case ch <- m:
				default:
				}
			}
		}
		return
	}
	if id != "" && m.Method != "item/commandExecution/requestApproval" && m.Method != "item/fileChange/requestApproval" {
		// Respond even when an unknown method has absent or unfamiliar params.
		go func() {
			_ = s.write(context.Background(), map[string]any{"id": m.ID, "error": map[string]any{"code": -32601, "message": "Method not found"}})
		}()
		return
	}
	var p struct {
		ThreadID  string          `json:"threadId"`
		TurnID    string          `json:"turnId"`
		ItemID    string          `json:"itemId"`
		RequestID json.RawMessage `json:"requestId"`
		Delta     string          `json:"delta"`
		Item      json.RawMessage `json:"item"`
		Turn      struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		} `json:"turn"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		TokenUsage *struct {
			Last  codexTokenUsage `json:"last"`
			Total codexTokenUsage `json:"total"`
		} `json:"tokenUsage"`
	}
	if json.Unmarshal(m.Params, &p) != nil {
		return
	}
	if p.ThreadID != "" {
		native := s.NativeID()
		if native != "" && p.ThreadID != native {
			return
		}
	}
	if id != "" {
		name := ""
		switch m.Method {
		case "item/commandExecution/requestApproval":
			name = "Bash"
		case "item/fileChange/requestApproval":
			name = "Edit"
		}
		if p.ItemID == "" || p.TurnID == "" {
			return
		}
		s.mu.Lock()
		s.pending[id] = codexApproval{append(json.RawMessage(nil), m.ID...), p.TurnID}
		item := s.items[p.ItemID]
		interrupt := s.interrupt
		s.mu.Unlock()
		var input map[string]any
		_ = json.Unmarshal(m.Params, &input)
		if name == "Edit" && len(item) > 0 {
			var i map[string]any
			_ = json.Unmarshal(item, &i)
			input["changes"] = i["changes"]
		}
		raw, _ := json.Marshal(input)
		s.emit(LiveEvent{Kind: "approval", RequestID: id, ToolID: p.ItemID, ToolName: name, ToolInput: liveInput(raw)})
		if interrupt {
			go func() { _ = s.Approve(id, false, "interrupted") }()
		}
		return
	}
	switch m.Method {
	case "item/agentMessage/delta":
		s.emit(LiveEvent{Kind: "text_delta", ToolID: p.ItemID, Text: liveText(p.Delta, liveLineLimit)})
	case "item/started", "item/completed":
		s.item(p.Item, m.Method == "item/completed")
	case "turn/started":
		s.mu.Lock()
		if s.busy {
			s.turnID = p.Turn.ID
		}
		s.mu.Unlock()
	case "thread/tokenUsage/updated":
		// One update per model call; Codex can repeat an update, so the
		// cumulative total identifies the call.
		if p.TokenUsage != nil && p.TokenUsage.Total.TotalTokens > 0 {
			s.mu.Lock()
			model := s.model
			s.mu.Unlock()
			if s.turn.observe(fmt.Sprint(p.TokenUsage.Total.TotalTokens), p.TokenUsage.Last.entry(model)) {
				s.emit(LiveEvent{Kind: "usage", Usage: s.turn.byModel()})
			}
		}
	case "turn/completed":
		if p.Turn.ID == "" || p.Turn.Status == "" {
			return
		}
		text := ""
		failed := p.Turn.Status != "completed"
		if p.Turn.Error != nil {
			text = p.Turn.Error.Message
		} else if failed {
			text = p.Turn.Status
		}
		turn := s.turn.byModel()
		s.turn.reset()
		s.emit(LiveEvent{Kind: "turn_done", IsError: failed, Text: liveText(text, liveLineLimit), Usage: turn})
		s.mu.Lock()
		s.busy = false
		s.turnID = ""
		s.interrupt = false
		s.items = make(map[string]json.RawMessage)
		for id, a := range s.pending {
			if a.turn == p.Turn.ID {
				delete(s.pending, id)
			}
		}
		s.mu.Unlock()
		s.signal(s.work)
	case "serverRequest/resolved":
		s.mu.Lock()
		delete(s.pending, codexID(p.RequestID))
		s.mu.Unlock()
	case "error":
		s.emit(LiveEvent{Kind: "error", Text: liveText(p.Error.Message, liveLineLimit), IsError: true})
	}
}
func (s *codexLive) item(raw json.RawMessage, completed bool) {
	var i struct {
		Type             string          `json:"type"`
		ID               string          `json:"id"`
		Text             string          `json:"text"`
		Tool             string          `json:"tool"`
		Status           string          `json:"status"`
		AggregatedOutput string          `json:"aggregatedOutput"`
		ExitCode         *int            `json:"exitCode"`
		Result           json.RawMessage `json:"result"`
		Error            json.RawMessage `json:"error"`
	}
	if json.Unmarshal(raw, &i) != nil {
		return
	}
	if completed && i.Type == "agentMessage" {
		s.emit(LiveEvent{Kind: "text", Text: liveText(i.Text, liveLineLimit)})
		return
	}
	name := ""
	switch i.Type {
	case "commandExecution":
		name = "Bash"
	case "fileChange":
		name = "Edit"
	case "mcpToolCall":
		name = i.Tool
	case "collabAgentToolCall":
		// Codex sub-agents ("collab" agents).
		name = "Agent"
	default:
		return
	}
	if !completed {
		s.mu.Lock()
		s.items[i.ID] = append(json.RawMessage(nil), raw...)
		s.mu.Unlock()
		s.emit(LiveEvent{Kind: "tool_start", ToolID: i.ID, ToolName: name, ToolInput: liveInput(raw)})
		return
	}
	s.mu.Lock()
	delete(s.items, i.ID)
	s.mu.Unlock()
	output := i.AggregatedOutput
	if i.Type == "fileChange" {
		output = string(liveInput(raw))
	}
	if i.Type == "mcpToolCall" {
		output = string(liveInput(i.Result))
		if len(i.Error) > 0 && string(i.Error) != "null" {
			output += "\n" + string(liveInput(i.Error))
		}
	}
	failed := i.Status == "failed" || i.Status == "declined" || (i.ExitCode != nil && *i.ExitCode != 0) || (len(i.Error) > 0 && string(i.Error) != "null")
	s.emit(LiveEvent{Kind: "tool_end", ToolID: i.ID, ToolName: name, ToolOutput: liveText(output, 64*1024), IsError: failed})
}
