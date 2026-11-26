package agentsetup

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// TestMCP starts (or connects to) one server, runs the MCP handshake,
// lists its tools and stops it again, within a timeout. Errors come back
// in plain words in the result, not as a Go error.

// MCPTestResult is what the Test button shows.
type MCPTestResult struct {
	OK            bool     `json:"ok"`
	ToolCount     int      `json:"tool_count"`
	Tools         []string `json:"tools"`
	ServerName    string   `json:"server_name,omitempty"`
	ServerVersion string   `json:"server_version,omitempty"`
	// Message is one plain sentence: what worked or what went wrong.
	Message string `json:"message"`
	// Detail is extra output (the server's error text), secrets hidden.
	Detail     string `json:"detail,omitempty"`
	DurationMS int64  `json:"duration_ms"`
}

// MCPTestRequest names a saved server, or gives a spec (hidden values are
// taken from the saved server named by Agent/Path/OriginalName).
type MCPTestRequest struct {
	Agent        string      `json:"agent"`
	Scope        string      `json:"scope"`
	Path         string      `json:"path"`
	Name         string      `json:"name"`
	Spec         *ServerSpec `json:"spec"`
	TimeoutMS    int         `json:"timeout_ms"`
	OriginalName string      `json:"original_name"`
}

const protocolVersion = "2025-06-18"

// TestMCP resolves the request and probes the server.
func TestMCP(ctx context.Context, o EditOptions, req MCPTestRequest) MCPTestResult {
	spec, err := o.testSpec(req)
	if err != nil {
		return MCPTestResult{Message: err.Error()}
	}
	timeout := time.Duration(req.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 25 * time.Second
	}
	if timeout > 90*time.Second {
		timeout = 90 * time.Second
	}
	return ProbeMCP(ctx, spec, timeout)
}

func (o EditOptions) testSpec(req MCPTestRequest) (ServerSpec, error) {
	name := req.OriginalName
	if name == "" {
		name = req.Name
	}
	var saved *ServerSpec
	if req.Agent != "" && name != "" {
		if raw, _, _, _, err := o.rawServer(req.Agent, req.Scope, req.Path, name); err == nil {
			s := specFrom(raw, req.Agent, name)
			saved = &s
		} else if req.Spec == nil {
			return ServerSpec{}, err
		}
	}
	if req.Spec == nil {
		if saved == nil {
			return ServerSpec{}, fmt.Errorf("Choose a server to test")
		}
		return *saved, nil
	}
	spec := *req.Spec
	if saved != nil {
		s, err := unmaskSpec(spec, *saved)
		if err != nil {
			return spec, err
		}
		spec = s
	}
	if spec.Name == "" {
		spec.Name = "server"
	}
	if strings.Contains(fmt.Sprint(spec.Env, spec.Headers, spec.Args, spec.URL), Mask) {
		return spec, fmt.Errorf("Some values are still hidden (••••••). Type them in full to test")
	}
	if spec.Name == NexusServerName {
		spec.Name = "nexus-test" // checkName refuses "nexus"; testing it is fine
	}
	return cleanSpec(spec, "")
}

// ProbeMCP runs the handshake against a spec.
func ProbeMCP(ctx context.Context, s ServerSpec, timeout time.Duration) MCPTestResult {
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var res MCPTestResult
	var err error
	if normalTransport(s.Transport) == "stdio" {
		res, err = probeStdio(ctx, s)
	} else {
		res, err = probeHTTP(ctx, s)
	}
	res.DurationMS = time.Since(start).Milliseconds()
	secrets := []string{}
	for _, v := range s.Env {
		if len(v) >= 4 {
			secrets = append(secrets, v)
		}
	}
	for _, v := range s.Headers {
		if len(v) >= 4 {
			secrets = append(secrets, v)
			if f := strings.Fields(v); len(f) == 2 && len(f[1]) >= 4 {
				secrets = append(secrets, f[1])
			}
		}
	}
	n := 0
	res.Detail = maskText(redact(res.Detail, secrets), &n)
	if err != nil {
		res.OK = false
		if ctx.Err() == context.DeadlineExceeded {
			res.Message = fmt.Sprintf("The server didn't answer within %d seconds.", int(timeout.Seconds()))
			if c := strings.ToLower(filepath.Base(s.Command)); strings.HasPrefix(c, "npx") || strings.HasPrefix(c, "uvx") || strings.HasPrefix(c, "docker") {
				res.Message += " The first run can take a while to download it. Try again in a minute."
			}
		} else {
			res.Message = err.Error()
		}
		return res
	}
	res.OK = true
	sort.Strings(res.Tools)
	res.ToolCount = len(res.Tools)
	switch res.ToolCount {
	case 0:
		res.Message = "Connected. The server works but offers no tools."
	case 1:
		res.Message = "Connected. 1 tool is ready."
	default:
		res.Message = fmt.Sprintf("Connected. %d tools are ready.", res.ToolCount)
	}
	if len(res.Tools) > 60 {
		res.Tools = res.Tools[:60]
	}
	return res
}

type rpcMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func rpcRequest(id int, method string, params any) []byte {
	m := map[string]any{"jsonrpc": "2.0", "method": method}
	if id > 0 {
		m["id"] = id
	}
	if params != nil {
		m["params"] = params
	}
	b, _ := json.Marshal(m)
	return b
}

func initParams() map[string]any {
	return map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "nexus-desktop", "version": "1"},
	}
}

func isID(raw json.RawMessage, id int) bool {
	return strings.TrimSpace(string(raw)) == fmt.Sprint(id) || strings.Trim(strings.TrimSpace(string(raw)), `"`) == fmt.Sprint(id)
}

// handshake drives initialize + tools/list over a send/receive pair.
func handshake(ctx context.Context, send func([]byte) error, recv func(int) (rpcMessage, error), res *MCPTestResult) error {
	if err := send(rpcRequest(1, "initialize", initParams())); err != nil {
		return err
	}
	msg, err := recv(1)
	if err != nil {
		return err
	}
	if msg.Error != nil {
		return fmt.Errorf("The server refused to start a session: %s", msg.Error.Message)
	}
	var init struct {
		ServerInfo struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	_ = json.Unmarshal(msg.Result, &init)
	res.ServerName, res.ServerVersion = init.ServerInfo.Name, init.ServerInfo.Version
	if err := send(rpcRequest(0, "notifications/initialized", nil)); err != nil {
		return err
	}
	cursor := ""
	for page, id := 0, 2; page < 10; page, id = page+1, id+1 {
		var params any
		if cursor != "" {
			params = map[string]any{"cursor": cursor}
		}
		if err := send(rpcRequest(id, "tools/list", params)); err != nil {
			return err
		}
		msg, err := recv(id)
		if err != nil {
			return err
		}
		if msg.Error != nil {
			if msg.Error.Code == -32601 && page == 0 {
				return nil // no tools capability
			}
			return fmt.Errorf("The server started, but listing its tools failed: %s", msg.Error.Message)
		}
		var list struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if err := json.Unmarshal(msg.Result, &list); err != nil {
			return fmt.Errorf("The server sent a tool list Nexus couldn't read")
		}
		for _, t := range list.Tools {
			res.Tools = append(res.Tools, t.Name)
		}
		if list.NextCursor == "" {
			break
		}
		cursor = list.NextCursor
	}
	return nil
}

// tailBuffer keeps the last bytes written (the server's stderr).
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > 4096 {
		t.buf = t.buf[len(t.buf)-4096:]
	}
	t.mu.Unlock()
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.buf))
}

// needs names what to install for a missing command.
func needs(command string) string {
	switch strings.ToLower(strings.TrimSuffix(filepath.Base(command), filepath.Ext(command))) {
	case "npx", "node", "npm":
		return "Install Node.js (it includes npx), then try again."
	case "uvx", "uv":
		return "Install uv (docs.astral.sh/uv), then try again."
	case "docker":
		return "Install Docker Desktop and start it, then try again."
	case "python", "python3", "py":
		return "Install Python, then try again."
	case "bunx", "bun":
		return "Install Bun, then try again."
	}
	return "Check the command, or install the program it needs."
}

func probeStdio(ctx context.Context, s ServerSpec) (MCPTestResult, error) {
	var res MCPTestResult
	path, err := exec.LookPath(s.Command)
	if err != nil {
		return res, fmt.Errorf("Nexus couldn't find %q on this computer. %s", s.Command, needs(s.Command))
	}
	cmd := exec.Command(path, s.Args...)
	cmd.Env = os.Environ()
	for k, v := range s.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	hideWindow(cmd)
	stderr := &tailBuffer{}
	cmd.Stderr = stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return res, fmt.Errorf("Nexus couldn't start the server")
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return res, fmt.Errorf("Nexus couldn't start the server")
	}
	if err := cmd.Start(); err != nil {
		return res, fmt.Errorf("Nexus couldn't start %q: %v", s.Command, err)
	}
	containTree(cmd)
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	defer func() {
		_ = stdin.Close()
		select {
		case <-exited:
		case <-time.After(300 * time.Millisecond):
		}
		killTree(cmd)
		_ = cmd.Process.Kill()
		select {
		case <-exited:
		case <-time.After(3 * time.Second):
		}
	}()

	lines := make(chan rpcMessage, 16)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 64<<10), 16<<20)
		for sc.Scan() {
			var m rpcMessage
			if json.Unmarshal(bytes.TrimSpace(sc.Bytes()), &m) == nil && (len(m.ID) > 0 || m.Method != "") {
				lines <- m
			}
		}
	}()
	send := func(b []byte) error {
		if _, err := stdin.Write(append(b, '\n')); err != nil {
			return stopped(stderr, exited)
		}
		return nil
	}
	recv := func(id int) (rpcMessage, error) {
		for {
			select {
			case <-ctx.Done():
				res.Detail = stderr.String()
				return rpcMessage{}, ctx.Err()
			case m, ok := <-lines:
				if !ok {
					return rpcMessage{}, stopped(stderr, exited)
				}
				if m.Method != "" && len(m.ID) > 0 && m.Result == nil && m.Error == nil {
					// A request from the server (roots, sampling): decline.
					_ = send([]byte(`{"jsonrpc":"2.0","id":` + string(m.ID) + `,"error":{"code":-32601,"message":"not supported"}}`))
					continue
				}
				if isID(m.ID, id) {
					return m, nil
				}
			}
		}
	}
	if err := handshake(ctx, send, recv, &res); err != nil {
		if res.Detail == "" {
			res.Detail = stderr.String()
		}
		if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			return res, ctx.Err()
		}
		return res, err
	}
	return res, nil
}

type stopErr struct{ msg string }

func (e stopErr) Error() string { return e.msg }

func stopped(stderr *tailBuffer, exited chan struct{}) error {
	select {
	case <-exited:
	case <-time.After(500 * time.Millisecond):
	}
	detail := stderr.String()
	msg := "The server stopped before it finished starting."
	if detail != "" {
		lines := strings.Split(detail, "\n")
		last := strings.TrimSpace(lines[len(lines)-1])
		if len(last) > 200 {
			last = last[:200] + "…"
		}
		msg += " It said: " + last
	}
	return stopErr{msg}
}

// ── HTTP (streamable) and older SSE servers ────────────────────────────

func probeHTTP(ctx context.Context, s ServerSpec) (MCPTestResult, error) {
	var res MCPTestResult
	client := &http.Client{}
	session := ""
	var pending []rpcMessage
	var post func(b []byte) error
	streamed := make(chan rpcMessage, 16)
	legacy := normalTransport(s.Transport) == "sse"
	endpoint := s.URL

	if legacy {
		ep, err := openSSE(ctx, client, s, streamed)
		if err != nil {
			return res, err
		}
		endpoint = ep
	}
	post = func(b []byte) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(b))
		if err != nil {
			return fmt.Errorf("That address isn't valid")
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if session != "" {
			req.Header.Set("Mcp-Session-Id", session)
			req.Header.Set("MCP-Protocol-Version", protocolVersion)
		}
		for k, v := range s.Headers {
			req.Header.Set(k, v)
		}
		resp, err := client.Do(req)
		if err != nil {
			return netError(err)
		}
		defer resp.Body.Close()
		if id := resp.Header.Get("Mcp-Session-Id"); id != "" {
			session = id
		}
		if err := statusError(resp); err != nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			res.Detail = strings.TrimSpace(string(body))
			return err
		}
		if legacy || resp.StatusCode == http.StatusAccepted {
			return nil
		}
		ct := resp.Header.Get("Content-Type")
		if strings.HasPrefix(ct, "text/event-stream") {
			return readEvents(resp.Body, func(event, data string) bool {
				var m rpcMessage
				if json.Unmarshal([]byte(data), &m) == nil && len(m.ID) > 0 && m.Method == "" {
					pending = append(pending, m)
					return false // one answer per request
				}
				return true
			})
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		if err != nil {
			return netError(err)
		}
		if len(bytes.TrimSpace(body)) == 0 {
			return nil
		}
		var m rpcMessage
		if err := json.Unmarshal(body, &m); err != nil {
			var batch []rpcMessage
			if json.Unmarshal(body, &batch) != nil {
				return fmt.Errorf("The address answered, but not like an MCP server. Check the URL")
			}
			pending = append(pending, batch...)
			return nil
		}
		pending = append(pending, m)
		return nil
	}
	recv := func(id int) (rpcMessage, error) {
		for {
			for i, m := range pending {
				if isID(m.ID, id) {
					pending = append(pending[:i], pending[i+1:]...)
					return m, nil
				}
			}
			if !legacy {
				return rpcMessage{}, fmt.Errorf("The server didn't answer the request")
			}
			select {
			case <-ctx.Done():
				return rpcMessage{}, ctx.Err()
			case m, ok := <-streamed:
				if !ok {
					return rpcMessage{}, fmt.Errorf("The server closed the connection")
				}
				pending = append(pending, m)
			}
		}
	}
	if err := handshake(ctx, post, recv, &res); err != nil {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		return res, err
	}
	if session != "" && !legacy {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodDelete, endpoint, nil)
		if req != nil {
			req.Header.Set("Mcp-Session-Id", session)
			for k, v := range s.Headers {
				req.Header.Set(k, v)
			}
			c := &http.Client{Timeout: 2 * time.Second}
			if resp, err := c.Do(req); err == nil {
				resp.Body.Close()
			}
		}
	}
	return res, nil
}

// openSSE connects to an older SSE server and returns its message URL.
// Messages from the server go to out until ctx ends.
func openSSE(ctx context.Context, client *http.Client, s ServerSpec, out chan<- rpcMessage) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	if err != nil {
		return "", fmt.Errorf("That address isn't valid")
	}
	req.Header.Set("Accept", "text/event-stream")
	for k, v := range s.Headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", netError(err)
	}
	if err := statusError(resp); err != nil {
		resp.Body.Close()
		return "", err
	}
	endpoint := make(chan string, 1)
	go func() {
		defer resp.Body.Close()
		defer close(out)
		_ = readEvents(resp.Body, func(event, data string) bool {
			if event == "endpoint" {
				select {
				case endpoint <- data:
				default:
				}
				return true
			}
			var m rpcMessage
			if json.Unmarshal([]byte(data), &m) == nil {
				select {
				case out <- m:
				case <-ctx.Done():
					return false
				}
			}
			return true
		})
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case ep := <-endpoint:
		base, _ := url.Parse(s.URL)
		ref, err := url.Parse(strings.TrimSpace(ep))
		if err != nil || base == nil {
			return "", fmt.Errorf("The server sent an address Nexus couldn't use")
		}
		return base.ResolveReference(ref).String(), nil
	}
}

// readEvents parses a text/event-stream; on returns false to stop.
func readEvents(r io.Reader, on func(event, data string) bool) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	event, data := "", []string{}
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" {
			if len(data) > 0 {
				if !on(event, strings.Join(data, "\n")) {
					return nil
				}
			}
			event, data = "", nil
			continue
		}
		switch {
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(line[6:])
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(line[5:], " "))
		}
	}
	if len(data) > 0 {
		on(event, strings.Join(data, "\n"))
	}
	return sc.Err()
}

func statusError(resp *http.Response) error {
	switch c := resp.StatusCode; {
	case c >= 200 && c < 300:
		return nil
	case c == 401 || c == 403:
		return fmt.Errorf("The server turned down the sign-in (%d). Check the token or key in the headers", c)
	case c == 404:
		return fmt.Errorf("Nothing answered at that address (404). Check the URL")
	case c == 405:
		return fmt.Errorf("That address doesn't accept MCP requests (405). If it's an older SSE server, set the type to SSE")
	case c >= 500:
		return fmt.Errorf("The server had a problem (%d). Try again later", c)
	default:
		return fmt.Errorf("The server answered with an error (%d)", c)
	}
}

func netError(err error) error {
	var dns *net.DNSError
	var op *net.OpError
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return err
	case errors.As(err, &dns):
		return fmt.Errorf("Nexus couldn't find that server's address. Check the URL and your internet connection")
	case errors.As(err, &op) && op.Op == "dial":
		return fmt.Errorf("Nothing is answering at that address. Check the URL, or start the server first")
	case strings.Contains(err.Error(), "certificate"):
		return fmt.Errorf("The server's security certificate isn't trusted")
	}
	return fmt.Errorf("Nexus couldn't reach the server. Check the URL and your connection")
}
