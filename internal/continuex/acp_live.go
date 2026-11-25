package continuex

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

var acpLookPath = exec.LookPath

// ACPAgents lists agents with an ACP driver, keyed by normalized agent name.
var ACPAgents = map[string]bool{"cursor": true, "opencode": true, "gemini": true, "kimi": true}

// Keep argv independent of lookup so tests can run a real subprocess fake.
var acpArgv = map[string][]string{
	"cursor": {"cursor-agent", "acp"}, "opencode": {"opencode", "acp"},
	"gemini": {"gemini", "--acp"}, "kimi": {"kimi", "acp"},
}

func ACPSupported(agent string) bool { _, ok := acpArgv[acpAgent(agent)]; return ok }

func acpAgent(agent string) string {
	agent = normalizeAgent(agent)
	if agent == "agent" {
		return "cursor"
	}
	return agent
}

// The shared resolver handles native executables and Cursor's bundled Node.
// Gemini's npm distribution additionally needs its JavaScript entry point;
// invoke Node directly so Windows never launches a visible command shell.
func acpExecutable(path, agent string) (string, []string, error) {
	exe, prefix, err := directExecutable(path, agent)
	if err == nil || runtime.GOOS != "windows" || !strings.EqualFold(filepath.Ext(path), ".cmd") {
		return exe, prefix, err
	}
	dir := filepath.Dir(path)
	if agent == "gemini" {
		entry := filepath.Join(dir, "node_modules", "@google", "gemini-cli", "dist", "index.js")
		if info, statErr := os.Stat(entry); statErr == nil && info.Mode().IsRegular() {
			node := filepath.Join(dir, "node.exe")
			if _, statErr = os.Stat(node); statErr != nil {
				node, statErr = acpLookPath("node")
			}
			if statErr == nil {
				return node, []string{entry}, nil
			}
		}
	}
	return exe, prefix, err
}

type acpMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *acpError       `json:"error,omitempty"`
}
type acpError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}
type acpPermission struct {
	id      json.RawMessage
	options []struct {
		ID   string `json:"optionId"`
		Kind string `json:"kind"`
	}
}
type acpPrompt struct {
	ctx    context.Context
	text   string
	images []string
}
type acpResponse struct {
	method string
	ch     chan acpMessage
}

type acpLive struct {
	cmd                          *exec.Cmd
	stdin                        io.WriteCloser
	done                         chan struct{}
	closeOnce                    sync.Once
	mu                           sync.Mutex
	agent, nativeID              string
	usageModel                   string // model named on usage, when the agent says which
	sequence                     uint64
	responses                    map[string]acpResponse
	pending                      map[string]acpPermission
	prompts                      []acpPrompt
	promptWake                   chan struct{}
	output                       [][]byte
	writeWake                    chan struct{}
	queue                        []LiveEvent
	wake                         chan struct{}
	events                       chan LiveEvent
	finished, closing, replaying bool
	resumed                      bool
	imageInput                   bool // agent advertised promptCapabilities.image
}

// StartACPLive starts an ACP v1 JSON-RPC connection. No client filesystem or
// terminal capabilities are advertised: tools remain owned by the agent.
func StartACPLive(ctx context.Context, agent string, opts LiveOptions) (LiveSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	agent = acpAgent(agent)
	argv, ok := acpArgv[agent]
	if !ok {
		return nil, fmt.Errorf("ACP live: unsupported agent %q", agent)
	}
	info, err := os.Stat(opts.Workspace)
	if err != nil || !info.IsDir() {
		return nil, errors.New("ACP live: workspace must be an existing directory")
	}
	if opts.PermissionMode != "" && opts.PermissionMode != "acceptEdits" && opts.PermissionMode != "plan" {
		return nil, errors.New("ACP live: invalid permission mode")
	}
	if _, err := WithModel(LaunchPlan{Agent: agent, Argv: []string{argv[0]}}, opts.Model); err != nil {
		return nil, err
	}
	opts.Workspace, err = filepath.Abs(opts.Workspace)
	if err != nil {
		return nil, err
	}
	path, err := acpLookPath(argv[0])
	if err != nil && agent == "cursor" {
		// Never fall back to a bare "agent" on PATH: other CLIs (e.g. Grok)
		// install a binary with that name. Use Cursor's own install folder.
		for _, name := range []string{"cursor-agent.cmd", "agent.cmd", "cursor-agent.exe"} {
			candidate := filepath.Join(os.Getenv("LOCALAPPDATA"), "cursor-agent", name)
			if st, statErr := os.Stat(candidate); statErr == nil && st.Mode().IsRegular() {
				path, err = candidate, nil
				break
			}
		}
	}
	if err != nil {
		return nil, fmt.Errorf("%s ACP is not installed or not on PATH: %w", agent, err)
	}
	path, prefix, err := acpExecutable(path, agent)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(path, append(prefix, argv[1:]...)...)
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
		return nil, fmt.Errorf("%s ACP: start: %w", agent, err)
	}
	containTree(cmd)
	_ = childStdout.Close()
	s := &acpLive{cmd: cmd, stdin: stdin, agent: agent, done: make(chan struct{}), responses: make(map[string]acpResponse), pending: make(map[string]acpPermission), promptWake: make(chan struct{}, 1), writeWake: make(chan struct{}, 1), wake: make(chan struct{}, 1), events: make(chan LiveEvent, 256)}
	go s.deliver()
	go s.writer()
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
		s.mu.Lock()
		// Finish atomically: neither the protocol worker nor writer may enqueue
		// an event after exited, even if they were woken by process termination.
		if err != nil {
			s.queue = append(s.queue, LiveEvent{Kind: "error", Text: liveText(err.Error()+": "+stderr.String(), 64*1024), IsError: true, At: time.Now()})
		}
		s.queue = append(s.queue, LiveEvent{Kind: "exited", IsError: err != nil, At: time.Now()})
		s.finished = true
		close(s.done)
		s.mu.Unlock()
		acpSignal(s.wake)
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

func acpSignal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}
func (s *acpLive) Events() <-chan LiveEvent { return s.events }
func (s *acpLive) NativeID() string         { s.mu.Lock(); defer s.mu.Unlock(); return s.nativeID }

// Writes have their own queue and goroutine. In particular, answering unknown
// client requests never stalls the stdout reader on a full stdin pipe.
func (s *acpLive) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished || s.closing {
		return errors.New("ACP live: process closed")
	}
	s.output = append(s.output, append(b, '\n'))
	acpSignal(s.writeWake)
	return nil
}
func (s *acpLive) writer() {
	for {
		s.mu.Lock()
		if s.finished || s.closing {
			s.mu.Unlock()
			return
		}
		if len(s.output) == 0 {
			s.mu.Unlock()
			select {
			case <-s.writeWake:
			case <-s.done:
				return
			}
			continue
		}
		b := s.output[0]
		s.output[0] = nil
		s.output = s.output[1:]
		s.mu.Unlock()
		if _, err := s.stdin.Write(b); err != nil {
			s.emit(LiveEvent{Kind: "error", Text: liveText(err.Error(), 64*1024), IsError: true})
			go s.Close()
			return
		}
	}
}
func (s *acpLive) rpc(ctx context.Context, method string, params any) (acpMessage, error) {
	s.mu.Lock()
	s.sequence++
	id := fmt.Sprint(s.sequence)
	ch := make(chan acpMessage, 1)
	s.responses[id] = acpResponse{method, ch}
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.responses, id); s.mu.Unlock() }()
	if err := s.write(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "method": method, "params": params}); err != nil {
		return acpMessage{}, err
	}
	select {
	case m := <-ch:
		if m.Error != nil {
			return m, s.protocolError(m.Error)
		}
		return m, nil
	case <-ctx.Done():
		return acpMessage{}, ctx.Err()
	case <-s.done:
		return acpMessage{}, errors.New("ACP live: process exited")
	}
}
func (s *acpLive) protocolError(e *acpError) error {
	text := e.Message
	// Verified with Kimi 0.31.1 (2026-11-22): session/new reports a
	// filesystem EPERM as "Internal error", with the actual cause in data.details.
	// Include redacted details so that failure is not mistaken for missing login.
	if len(e.Data) > 0 && string(e.Data) != "null" {
		text += ": " + string(e.Data)
	}
	low := strings.ToLower(text + " " + string(e.Data))
	if e.Code == -32000 || strings.Contains(low, "auth") || strings.Contains(low, "sign in") || strings.Contains(low, "login") || strings.Contains(low, "log in") || strings.Contains(low, "unauthorized") {
		text = fmt.Sprintf("Sign in to %s in its own app or CLI first: %s", s.agent, text)
	}
	return errors.New(liveText(text, 64*1024))
}
func (s *acpLive) run(ctx context.Context, opts LiveOptions) {
	err := s.initialize(ctx, opts)
	if err != nil {
		s.emit(LiveEvent{Kind: "error", Text: err.Error(), IsError: true})
		_ = s.Close()
		return
	}
	first := true
	for {
		s.mu.Lock()
		if s.closing || s.finished {
			s.mu.Unlock()
			return
		}
		if len(s.prompts) == 0 {
			s.mu.Unlock()
			select {
			case <-s.promptWake:
			case <-s.done:
				return
			}
			continue
		}
		p := s.prompts[0]
		s.prompts[0] = acpPrompt{}
		s.prompts = s.prompts[1:]
		s.mu.Unlock()
		// Send's context covers handing the message over, not the turn: the
		// desktop cancels it as soon as Send returns, and treating that as an
		// abort ended real turns with "context canceled".
		text := p.text
		s.mu.Lock()
		resumed := s.resumed
		s.mu.Unlock()
		// A resumed session already has its history; only a new one needs the
		// earlier conversation as context.
		if first && opts.SystemContext != "" && !resumed {
			text = "Context from the earlier conversation:\n" + opts.SystemContext + "\n\nNew user message:\n" + text
		}
		first = false
		// Once submitted, await this response even if Send's context expires;
		// only session/cancel ends a turn without overlapping the next prompt.
		prompt, problem := s.acpPromptBlocks(text, p.images)
		if problem != nil {
			s.emit(LiveEvent{Kind: "turn_done", Text: problem.Error(), IsError: true, NativeID: s.NativeID()})
			continue
		}
		m, err := s.rpc(ctx, "session/prompt", map[string]any{"sessionId": s.NativeID(), "prompt": prompt})
		// Emit protocol turn_done on the stdout reader to preserve ordering
		// when the peer exits immediately after writing its response.
		if err != nil && m.Error == nil {
			s.emit(LiveEvent{Kind: "turn_done", Text: err.Error(), IsError: true, NativeID: s.NativeID()})
			return
		}
	}
}
func (s *acpLive) initialize(ctx context.Context, opts LiveOptions) error {
	m, err := s.rpc(ctx, "initialize", map[string]any{"protocolVersion": 1, "clientCapabilities": map[string]any{"fs": map[string]bool{"readTextFile": false, "writeTextFile": false}, "terminal": false}})
	if err != nil {
		return err
	}
	var init struct {
		Version      int `json:"protocolVersion"`
		Capabilities struct {
			Load   bool `json:"loadSession"`
			Prompt struct {
				Image bool `json:"image"`
			} `json:"promptCapabilities"`
			MCP struct {
				HTTP bool `json:"http"`
			} `json:"mcpCapabilities"`
		} `json:"agentCapabilities"`
		Auth []json.RawMessage `json:"authMethods"`
	}
	if err = json.Unmarshal(m.Result, &init); err != nil {
		return err
	}
	if init.Version != 1 {
		return fmt.Errorf("ACP live: unsupported protocol version %d", init.Version)
	}
	s.mu.Lock()
	s.imageInput = init.Capabilities.Prompt.Image
	s.mu.Unlock()
	// authMethods lists available methods, not whether authentication is needed.
	// Existing CLI credentials are used; auth-required errors explain sign-in.
	// Kimi 0.31.1 advertises terminal-auth and loadSession even when local
	// session storage is inaccessible; initialization alone does not prove readiness.
	mcpServers := []any{}
	if init.Capabilities.MCP.HTTP {
		for _, srv := range opts.MCPServers {
			headers := []any{}
			for k, v := range srv.Headers {
				headers = append(headers, map[string]any{"name": k, "value": v})
			}
			mcpServers = append(mcpServers, map[string]any{"type": "http", "name": srv.Name, "url": srv.URL, "headers": headers})
		}
	} else if len(opts.MCPServers) > 0 {
		s.emit(LiveEvent{Kind: "status", Text: s.agent + " can't use remote MCP servers, so Nexus memory isn't available in this chat."})
	}
	params := map[string]any{"cwd": opts.Workspace, "mcpServers": mcpServers}
	method := "session/new"
	if opts.ResumeID != "" && init.Capabilities.Load {
		method = "session/load"
		params["sessionId"] = opts.ResumeID
		s.mu.Lock()
		s.replaying = true
		s.mu.Unlock()
	}
	m, err = s.rpc(ctx, method, params)
	if err != nil && method == "session/load" {
		// The saved session may never have been persisted by the agent (an
		// interrupted first turn). Start fresh; the first prompt carries the
		// earlier conversation instead.
		s.mu.Lock()
		s.replaying = false
		s.mu.Unlock()
		delete(params, "sessionId")
		method = "session/new"
		s.emit(LiveEvent{Kind: "status", Text: "Couldn't reopen the earlier session; continuing in a new one with the conversation so far."})
		m, err = s.rpc(ctx, method, params)
	}
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.resumed = method == "session/load"
	s.mu.Unlock()
	var session struct {
		ID     string `json:"sessionId"`
		Models *struct {
			Current   string `json:"currentModelId"`
			Available []struct {
				ID          string `json:"modelId"`
				Name        string `json:"name"`
				Description string `json:"description"`
			} `json:"availableModels"`
		} `json:"models"`
		Configs []struct {
			ID       string `json:"id"`
			Category string `json:"category"`
			Options  []struct {
				Value       string `json:"value"`
				Name        string `json:"name"`
				Description string `json:"description"`
			} `json:"options"`
		} `json:"configOptions"`
	}
	if err = json.Unmarshal(m.Result, &session); err != nil {
		return err
	}
	if session.Models != nil {
		s.mu.Lock()
		s.usageModel = session.Models.Current
		s.mu.Unlock()
	}
	// Reuse what the live session advertised for the composer's model picker.
	var advertised []HarnessModel
	if session.Models != nil {
		for _, m := range session.Models.Available {
			advertised = append(advertised, HarnessModel{ID: m.ID, Name: m.Name, Description: m.Description})
		}
	} else {
		for _, c := range session.Configs {
			if c.Category == "model" {
				for _, o := range c.Options {
					advertised = append(advertised, HarnessModel{ID: o.Value, Name: o.Name, Description: o.Description})
				}
				break
			}
		}
	}
	RecordAdvertisedModels(s.agent, advertised)
	if method == "session/load" {
		session.ID = opts.ResumeID
	}
	if session.ID == "" {
		return errors.New("ACP live: missing sessionId")
	}
	s.mu.Lock()
	s.nativeID = session.ID
	s.mu.Unlock()
	// Model APIs are optional extensions. Only use explicitly advertised
	// selections; agents with no model state keep their configured default.
	// A model the agent does not offer (e.g. "auto" from Cursor's CLI catalog,
	// rejected by its ACP mode as "Invalid model value") must not end the
	// chat: apply only advertised models and otherwise keep the default.
	model := strings.TrimSpace(opts.Model)
	if model != "" {
		applied := false
		if session.Models != nil {
			for _, m := range session.Models.Available {
				if m.ID == model {
					_, err = s.rpc(ctx, "session/set_model", map[string]any{"sessionId": session.ID, "modelId": model})
					applied = err == nil
					break
				}
			}
		} else {
			for _, c := range session.Configs {
				if c.Category != "model" {
					continue
				}
				for _, o := range c.Options {
					if o.Value == model {
						_, err = s.rpc(ctx, "session/set_config_option", map[string]any{"sessionId": session.ID, "configId": c.ID, "value": model})
						applied = err == nil
						break
					}
				}
				break
			}
		}
		if !applied {
			s.emit(LiveEvent{Kind: "status", Text: fmt.Sprintf("Model %q isn't available in %s's chat mode; using its default model.", model, s.agent)})
		} else {
			s.mu.Lock()
			s.usageModel = model
			s.mu.Unlock()
		}
	}
	s.emit(LiveEvent{Kind: "status", Text: "Connected", NativeID: session.ID})
	// Verified with Kimi 0.31.1: when signed out it advertises a model option
	// with no choices and then ends every turn silently. Say so up front.
	if session.Models == nil {
		for _, c := range session.Configs {
			if c.Category == "model" && len(c.Options) == 0 {
				s.emit(LiveEvent{Kind: "error", IsError: true, Text: fmt.Sprintf("%s has no model available. Sign in first (for example run `%s login` in a terminal), then send again.", s.agent, s.agent)})
				break
			}
		}
	}
	return nil
}
func (s *acpLive) Send(ctx context.Context, text string) error {
	return s.SendWithImages(ctx, text, nil)
}

// SendWithImages queues a prompt with ACP image content blocks. Only agents
// that advertise promptCapabilities.image receive them; others get a clear
// error instead of a silently dropped picture.
func (s *acpLive) SendWithImages(ctx context.Context, text string, images []string) error {
	if len(images) > MaxImages {
		return errors.New("attach at most four images")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished || s.closing {
		return errors.New("ACP live: process closed")
	}
	s.prompts = append(s.prompts, acpPrompt{ctx, text, append([]string(nil), images...)})
	acpSignal(s.promptWake)
	return nil
}
func (s *acpLive) acpPromptBlocks(text string, images []string) ([]any, error) {
	blocks := []any{}
	if len(images) > 0 {
		s.mu.Lock()
		ok := s.imageInput
		s.mu.Unlock()
		if !ok {
			return nil, fmt.Errorf("%s does not accept images in chat. Remove the image or choose Claude Code or Codex", s.agent)
		}
		for _, path := range images {
			mediaType, data, err := ReadImage(path, MaxInlineImageBytes)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, map[string]any{"type": "image", "mimeType": mediaType, "data": base64.StdEncoding.EncodeToString(data)})
		}
	}
	return append(blocks, map[string]any{"type": "text", "text": text}), nil
}
func (s *acpLive) Approve(id string, allow bool, reason string) error {
	s.mu.Lock()
	p, ok := s.pending[id]
	if ok {
		delete(s.pending, id)
	}
	s.mu.Unlock()
	if !ok {
		return errors.New("ACP live: unknown permission request")
	}
	kinds := []string{"reject_once", "reject_always"}
	if allow {
		kinds = []string{"allow_once", "allow_always"}
	}
	for _, kind := range kinds {
		for _, opt := range p.options {
			if opt.Kind == kind {
				return s.answer(p.id, map[string]any{"outcome": "selected", "optionId": opt.ID})
			}
		}
	}
	return s.answer(p.id, map[string]any{"outcome": "cancelled"})
}
func (s *acpLive) answer(id json.RawMessage, outcome any) error {
	return s.write(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"outcome": outcome}})
}
func (s *acpLive) Interrupt() error {
	s.mu.Lock()
	pending := s.pending
	s.pending = make(map[string]acpPermission)
	id := s.nativeID
	s.mu.Unlock()
	for _, p := range pending {
		if err := s.answer(p.id, map[string]any{"outcome": "cancelled"}); err != nil {
			return err
		}
	}
	return s.write(map[string]any{"jsonrpc": "2.0", "method": "session/cancel", "params": map[string]any{"sessionId": id}})
}
func (s *acpLive) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closing = true
		s.mu.Unlock()
		acpSignal(s.writeWake)
		acpSignal(s.promptWake)
		_ = s.stdin.Close()
	})
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
		return errors.New("ACP live: timed out waiting for process exit")
	}
}
func (s *acpLive) emit(e LiveEvent) {
	e.At = time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished {
		return
	}
	n := len(s.queue)
	if e.Kind == "text_delta" && n >= 256 && s.queue[n-1].Kind == "text_delta" {
		s.queue[n-1].Text += e.Text
	} else {
		s.queue = append(s.queue, e)
	}
	acpSignal(s.wake)
}
func (s *acpLive) deliver() {
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
func acpID(id json.RawMessage) string {
	var str string
	if json.Unmarshal(id, &str) == nil {
		return str
	}
	return string(id)
}
func acpKey(id json.RawMessage) string { return string(id) } // distinguish numeric 1 from string "1"
func (s *acpLive) parse(line []byte) {
	var m acpMessage
	if json.Unmarshal(line, &m) != nil || m.JSONRPC != "2.0" {
		return
	}
	hasID := len(m.ID) > 0 && string(m.ID) != "null"
	if m.Method == "" {
		if !hasID {
			return
		}
		s.mu.Lock()
		response, ok := s.responses[acpKey(m.ID)]
		// session/load's response is the replay boundary, on this reader,
		// rather than on the asynchronously scheduled initialization worker.
		if ok && response.method == "session/load" && s.replaying {
			s.replaying = false
		}
		s.mu.Unlock()
		if ok {
			if response.method == "session/prompt" {
				var result struct {
					StopReason string `json:"stopReason"`
				}
				_ = json.Unmarshal(m.Result, &result)
				s.mu.Lock()
				model := s.usageModel
				s.mu.Unlock()
				e := LiveEvent{Kind: "turn_done", NativeID: s.NativeID(), IsError: m.Error != nil || result.StopReason == "refusal", Usage: acpPromptUsage(m.Result, model)}
				if m.Error != nil {
					e.Text = s.protocolError(m.Error).Error()
				} else if e.IsError {
					e.Text = result.StopReason
				}
				s.emit(e)
			}
			select {
			case response.ch <- m:
			default:
			}
		}
		return
	}
	if hasID {
		if m.Method != "session/request_permission" {
			_ = s.write(map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": map[string]any{"code": -32601, "message": "Method not found"}})
			return
		}
		var p struct {
			Tool struct {
				ID    string          `json:"toolCallId"`
				Title string          `json:"title"`
				Kind  string          `json:"kind"`
				Input json.RawMessage `json:"rawInput"`
			} `json:"toolCall"`
			Options []struct {
				ID   string `json:"optionId"`
				Kind string `json:"kind"`
			} `json:"options"`
		}
		if json.Unmarshal(m.Params, &p) != nil {
			_ = s.answer(m.ID, map[string]any{"outcome": "cancelled"})
			return
		}
		id := acpID(m.ID)
		s.mu.Lock()
		s.pending[id] = acpPermission{append(json.RawMessage(nil), m.ID...), p.Options}
		s.mu.Unlock()
		name := p.Tool.Title
		if name == "" {
			name = p.Tool.Kind
		}
		s.emit(LiveEvent{Kind: "approval", RequestID: id, ToolID: p.Tool.ID, ToolName: liveText(name, 4096), ToolInput: liveInput(p.Tool.Input)})
		return
	}
	if m.Method != "session/update" {
		return
	}
	s.mu.Lock()
	replaying := s.replaying
	s.mu.Unlock()
	if replaying {
		return
	}
	var p struct {
		SessionID string `json:"sessionId"`
		Update    struct {
			Type      string          `json:"sessionUpdate"`
			ID        string          `json:"toolCallId"`
			Title     string          `json:"title"`
			Kind      string          `json:"kind"`
			Status    string          `json:"status"`
			Input     json.RawMessage `json:"rawInput"`
			Locations json.RawMessage `json:"locations"`
			Content   json.RawMessage `json:"content"`
		} `json:"update"`
	}
	if json.Unmarshal(m.Params, &p) != nil {
		return
	}
	if id := s.NativeID(); id != "" && p.SessionID != id {
		return
	}
	u := p.Update
	name := u.Title
	if name == "" {
		name = u.Kind
	}
	switch u.Type {
	case "agent_message_chunk":
		var b struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(u.Content, &b) == nil && b.Type == "text" {
			s.emit(LiveEvent{Kind: "text_delta", Text: liveText(b.Text, liveLineLimit)})
		}
	case "tool_call":
		input := u.Input
		if len(input) == 0 || string(input) == "null" {
			input, _ = json.Marshal(map[string]any{"title": u.Title, "locations": u.Locations})
		}
		s.emit(LiveEvent{Kind: "tool_start", ToolID: u.ID, ToolName: liveText(name, 4096), ToolInput: liveInput(input)})
	case "tool_call_update":
		if u.Status == "completed" || u.Status == "failed" {
			s.emit(LiveEvent{Kind: "tool_end", ToolID: u.ID, ToolName: liveText(name, 4096), ToolOutput: acpToolContent(u.Content), IsError: u.Status == "failed"})
		}
	}
}
func acpToolContent(raw json.RawMessage) string {
	var blocks []struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Path    string `json:"path"`
		Old     string `json:"oldText"`
		New     string `json:"newText"`
		Content struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var text strings.Builder
	for _, b := range blocks {
		if text.Len() >= 64*1024 {
			break
		}
		switch b.Type {
		case "content":
			text.WriteString(b.Content.Text)
		case "text":
			text.WriteString(b.Text)
		case "diff":
			text.WriteString("--- " + b.Path + "\n+++ " + b.Path + "\n-" + strings.ReplaceAll(b.Old, "\n", "\n-") + "\n+" + strings.ReplaceAll(b.New, "\n", "\n+"))
		}
		text.WriteByte('\n')
	}
	return liveText(text.String(), 64*1024)
}
