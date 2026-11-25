package continuex

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// OpenCode's run --format json reports completed text parts, not token deltas.
// Its localhost server exposes the live message.part.delta event stream.
type openCodeInput struct {
	session string
	prompt  string
	images  []string
	model   string
}

type openCodePreflightError struct{ err error }

func (e openCodePreflightError) Error() string { return e.err.Error() }
func (e openCodePreflightError) Unwrap() error { return e.err }
func preflightOpenCode(err error) error        { return openCodePreflightError{err: err} }
func isOpenCodePreflightError(err error) bool {
	var target openCodePreflightError
	return errors.As(err, &target)
}

func openCodeInputFromPlan(plan LaunchPlan) (openCodeInput, error) {
	if normalizeAgent(plan.Agent) != "opencode" || plan.Mode != "here" || len(plan.Argv) < 4 || plan.Argv[1] != "run" {
		return openCodeInput{}, errors.New("invalid OpenCode in-app plan")
	}
	var input openCodeInput
	for i := 2; i < len(plan.Argv)-1; i++ {
		switch plan.Argv[i] {
		case "--format":
			i++
			if i >= len(plan.Argv)-1 || plan.Argv[i] != "json" {
				return openCodeInput{}, errors.New("invalid OpenCode output format")
			}
		case "-s":
			i++
			if i >= len(plan.Argv)-1 {
				return openCodeInput{}, errors.New("missing OpenCode session ID")
			}
			input.session = plan.Argv[i]
		case "--file":
			i++
			if i >= len(plan.Argv)-1 || !AllowedImage(plan.Argv[i]) {
				return openCodeInput{}, errors.New("invalid OpenCode image attachment")
			}
			input.images = append(input.images, plan.Argv[i])
		case "--model":
			i++
			if i >= len(plan.Argv)-1 || !strings.Contains(plan.Argv[i], "/") {
				return openCodeInput{}, errors.New("OpenCode model must be provider/model")
			}
			input.model = plan.Argv[i]
		default:
			return openCodeInput{}, errors.New("unsupported OpenCode in-app option")
		}
	}
	input.prompt = strings.TrimSpace(plan.Argv[len(plan.Argv)-1])
	if input.prompt == "" {
		return openCodeInput{}, errors.New("OpenCode message is empty")
	}
	return input, nil
}

func openCodeServerTurn(ctx context.Context, plan LaunchPlan, workspace string, onProgress func(StreamProgress)) (TurnOutput, error) {
	input, err := openCodeInputFromPlan(plan)
	if err != nil {
		return TurnOutput{}, err
	}
	path, err := exec.LookPath("opencode")
	if err != nil {
		return TurnOutput{}, errors.New("OpenCode is not installed or is not on PATH")
	}
	path, prefix, err := directExecutable(path, "opencode")
	if err != nil {
		return TurnOutput{}, err
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		out, err := openCodeServerTurnOnce(ctx, path, prefix, workspace, input, onProgress)
		if err == nil {
			return out, nil
		}
		if !errors.Is(err, errOpenCodePortUnavailable) {
			return TurnOutput{}, err
		}
		lastErr = err
	}
	return TurnOutput{}, lastErr
}

var errOpenCodePortUnavailable = errors.New("OpenCode server port is unavailable")

func openCodeServerTurnOnce(ctx context.Context, path string, prefix []string, workspace string, input openCodeInput, onProgress func(StreamProgress)) (TurnOutput, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return TurnOutput{}, preflightOpenCode(fmt.Errorf("cannot reserve a localhost port for OpenCode: %w", err))
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return TurnOutput{}, preflightOpenCode(err)
	}
	password := hex.EncodeToString(secret[:])
	serverCtx, stop := context.WithCancel(ctx)
	defer stop()
	args := append(prefix, "serve", "--hostname", "127.0.0.1", "--port", fmt.Sprint(port))
	cmd := exec.CommandContext(serverCtx, path, args...)
	cmd.WaitDelay = 5 * time.Second
	hideCursorToolConsole(cmd)
	cmd.Dir = workspace
	cmd.Stdin = nil
	cmd.Stdout = io.Discard
	var stderr cappedOutput
	cmd.Stderr = &stderr
	cmd.Env = append(os.Environ(), "OPENCODE_SERVER_USERNAME=nexus", "OPENCODE_SERVER_PASSWORD="+password)
	if err := cmd.Start(); err != nil {
		return TurnOutput{}, preflightOpenCode(fmt.Errorf("could not start the OpenCode event server: %w", err))
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	defer func() {
		stop()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		select {
		case <-exited:
		case <-time.After(6 * time.Second):
		}
	}()
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: 3 * time.Second}
	ready := time.NewTimer(20 * time.Second)
	defer ready.Stop()
	for {
		if ctx.Err() != nil {
			return TurnOutput{}, ctx.Err()
		}
		var health struct {
			Healthy bool `json:"healthy"`
		}
		if err := openCodeJSON(ctx, client, base, password, http.MethodGet, "/global/health", nil, &health); err == nil && health.Healthy {
			break
		}
		select {
		case <-ready.C:
			return TurnOutput{}, preflightOpenCode(errors.New("OpenCode server did not become ready"))
		case <-exited:
			detail := stderr.String()
			if openCodePortBusy(detail) {
				return TurnOutput{}, preflightOpenCode(fmt.Errorf("%w: %s", errOpenCodePortUnavailable, shortDiagnostic(detail)))
			}
			msg := "OpenCode server exited before it became ready"
			if line := shortDiagnostic(detail); line != "" {
				msg += ": " + line
			}
			return TurnOutput{}, preflightOpenCode(errors.New(msg))
		case <-ctx.Done():
			return TurnOutput{}, ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
	if onProgress != nil {
		onProgress(StreamProgress{Kind: "status", Text: "Connected to OpenCode - waiting for reply..."})
	}
	return runOpenCodeHTTP(ctx, &http.Client{}, base, password, input, onProgress)
}

func openCodePortBusy(detail string) bool {
	d := strings.ToLower(detail)
	return strings.Contains(d, "address already in use") || strings.Contains(d, "eaddrinuse") || strings.Contains(d, "only one usage of each socket address")
}

func shortDiagnostic(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 300 {
		s = strings.TrimSpace(s[:300])
	}
	return s
}

type openCodeAPIError struct {
	path    string
	status  int
	problem error
}

func (e *openCodeAPIError) Error() string {
	return fmt.Sprintf("OpenCode API %s failed (%d): %s", e.path, e.status, e.problem)
}

func openCodeJSON(ctx context.Context, client *http.Client, base, password, method, path string, body any, out any) error {
	requestCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var source io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		source = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(requestCtx, method, base+path, source)
	if err != nil {
		return err
	}
	req.SetBasicAuth("nexus", password)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if resp.StatusCode == 401 || resp.StatusCode == 403 || resp.StatusCode == 402 || resp.StatusCode == 429 {
			raw = []byte(fmt.Sprint(resp.StatusCode))
		}
		return &openCodeAPIError{path: path, status: resp.StatusCode, problem: openCodeProblem(raw)}
	}
	if out != nil {
		return json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(out)
	}
	return nil
}

type openCodeEvent struct {
	Type       string          `json:"type"`
	Properties json.RawMessage `json:"properties"`
}

type openCodePacket struct {
	event openCodeEvent
	err   error
}

func openCodeEvents(ctx context.Context, client *http.Client, base, password string) (<-chan openCodePacket, func(), error) {
	streamCtx, cancel := context.WithCancel(ctx)
	req, err := http.NewRequestWithContext(streamCtx, http.MethodGet, base+"/event", nil)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	req.SetBasicAuth("nexus", password)
	req.Header.Set("Accept", "text/event-stream")
	headerWait := time.AfterFunc(5*time.Second, cancel)
	resp, err := client.Do(req)
	headerWait.Stop()
	if err != nil {
		cancel()
		return nil, nil, err
	}
	if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		resp.Body.Close()
		cancel()
		return nil, nil, fmt.Errorf("OpenCode event stream unavailable (%d)", resp.StatusCode)
	}
	packets := make(chan openCodePacket, 128)
	go func() {
		defer close(packets)
		defer resp.Body.Close()
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 64*1024), 2<<20)
		var data strings.Builder
		emit := func() {
			if data.Len() == 0 {
				return
			}
			var event openCodeEvent
			if json.Unmarshal([]byte(data.String()), &event) == nil && event.Type != "" {
				select {
				case packets <- openCodePacket{event: event}:
				case <-streamCtx.Done():
				}
			}
			data.Reset()
		}
		for scanner.Scan() {
			line := strings.TrimSuffix(scanner.Text(), "\r")
			if line == "" {
				emit()
				continue
			}
			if strings.HasPrefix(line, "data:") {
				if data.Len() > 0 {
					data.WriteByte('\n')
				}
				data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			}
		}
		emit()
		if streamCtx.Err() == nil {
			err := scanner.Err()
			if err == nil {
				err = io.EOF
			}
			select {
			case packets <- openCodePacket{err: err}:
			case <-streamCtx.Done():
			}
		}
	}()
	return packets, cancel, nil
}

type openCodeMessage struct {
	Info struct {
		ID         string          `json:"id"`
		Role       string          `json:"role"`
		Error      json.RawMessage `json:"error"`
		ProviderID string          `json:"providerID"`
		ModelID    string          `json:"modelID"`
		Model      struct {
			ProviderID string `json:"providerID"`
			ModelID    string `json:"modelID"`
		} `json:"model"`
	} `json:"info"`
	Parts []openCodePart `json:"parts"`
}

func openCodeMessages(ctx context.Context, client *http.Client, base, password, session string) ([]openCodeMessage, error) {
	var messages []openCodeMessage
	err := openCodeJSON(ctx, client, base, password, http.MethodGet, "/session/"+session+"/message?limit=20", nil, &messages)
	return messages, err
}

func latestOpenCodeAnswer(messages []openCodeMessage, before string) (string, string) {
	var chunks []string
	var latest string
	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i]
		if message.Info.Role != "assistant" {
			continue
		}
		if message.Info.ID == before {
			break
		}
		var parts []string
		for _, part := range message.Parts {
			if part.Type == "text" && strings.TrimSpace(part.Text) != "" {
				parts = append(parts, part.Text)
			}
		}
		if len(parts) > 0 {
			chunks = append(chunks, strings.Join(parts, "\n\n"))
			if latest == "" {
				latest = message.Info.ID
			}
		}
	}
	for i, j := 0, len(chunks)-1; i < j; i, j = i+1, j-1 {
		chunks[i], chunks[j] = chunks[j], chunks[i]
	}
	return strings.Join(chunks, "\n\n"), latest
}

func openCodePromptParts(input openCodeInput) ([]map[string]string, error) {
	parts := []map[string]string{{"type": "text", "text": input.prompt}}
	for _, image := range input.images {
		if !AllowedImage(image) {
			return nil, errors.New("an OpenCode image attachment is no longer available")
		}
		contents, err := os.ReadFile(image)
		if err != nil {
			return nil, err
		}
		mime := "image/" + strings.TrimPrefix(strings.ToLower(filepath.Ext(image)), ".")
		if mime == "image/jpg" {
			mime = "image/jpeg"
		}
		parts = append(parts, map[string]string{"type": "file", "mime": mime,
			"filename": filepath.Base(image), "url": "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(contents)})
	}
	return parts, nil
}

func runOpenCodeHTTP(ctx context.Context, client *http.Client, base, password string, input openCodeInput, onProgress func(StreamProgress)) (TurnOutput, error) {
	return runOpenCodeHTTPWithPolicy(ctx, client, base, password, input, onProgress, openCodeWaitPolicy{poll: time.Second, firstText: 120 * time.Second})
}

func runOpenCodeHTTPWithPolicy(ctx context.Context, client *http.Client, base, password string, input openCodeInput, onProgress func(StreamProgress), policy openCodeWaitPolicy) (TurnOutput, error) {
	session := input.session
	if session == "" {
		var created struct {
			ID string `json:"id"`
		}
		if err := openCodeJSON(ctx, client, base, password, http.MethodPost, "/session", map[string]any{}, &created); err != nil {
			return TurnOutput{}, preflightOpenCode(err)
		}
		session = created.ID
	}
	if !validOpenCodeSessionID(session) {
		return TurnOutput{}, preflightOpenCode(errors.New("OpenCode returned an invalid session ID"))
	}
	var previous string
	var history []openCodeMessage
	if input.session != "" {
		messages, err := openCodeMessages(ctx, client, base, password, session)
		if err != nil {
			return TurnOutput{}, preflightOpenCode(fmt.Errorf("cannot load the OpenCode session: %w", err))
		}
		history = messages
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].Info.Role == "assistant" {
				previous = messages[i].Info.ID
				break
			}
		}
	}
	if err := validateOpenCodeModel(ctx, client, base, password, &input, history); err != nil {
		return TurnOutput{}, preflightOpenCode(err)
	}
	parts, err := openCodePromptParts(input)
	if err != nil {
		return TurnOutput{}, preflightOpenCode(err)
	}
	packets, closeStream, err := openCodeEvents(ctx, client, base, password)
	if err != nil {
		return TurnOutput{}, preflightOpenCode(err)
	}
	defer closeStream()
	// The connected sentinel proves the subscription is active before the
	// asynchronous prompt can emit its first token.
	connected := time.NewTimer(5 * time.Second)
	defer connected.Stop()
	for {
		select {
		case packet, ok := <-packets:
			if !ok || packet.err != nil {
				return TurnOutput{}, preflightOpenCode(errors.New("OpenCode event stream closed before the prompt"))
			}
			if packet.event.Type == "server.connected" {
				goto subscribed
			}
		case <-connected.C:
			return TurnOutput{}, preflightOpenCode(errors.New("OpenCode event stream did not connect"))
		case <-ctx.Done():
			return TurnOutput{}, ctx.Err()
		}
	}
subscribed:
	if input.session != "" {
		var status map[string]struct {
			Type string `json:"type"`
		}
		if err := openCodeJSON(ctx, client, base, password, http.MethodGet, "/session/status", nil, &status); err == nil {
			if current, ok := status[session]; ok && current.Type != "idle" {
				return TurnOutput{}, errors.New("This OpenCode session already has a running request. Wait for it to finish or stop it in OpenCode before sending again.")
			}
		}
	}
	payload := map[string]any{"parts": parts}
	if input.model != "" {
		provider, model, _ := strings.Cut(input.model, "/")
		payload["model"] = map[string]string{"providerID": provider, "modelID": model}
	}
	if err := openCodeJSON(ctx, client, base, password, http.MethodPost, "/session/"+session+"/prompt_async", payload, nil); err != nil {
		return TurnOutput{}, err
	}
	completed := false
	defer func() {
		if !completed {
			abortCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = openCodeJSON(abortCtx, client, base, password, http.MethodPost, "/session/"+session+"/abort", map[string]any{}, nil)
		}
	}()
	tracker := newOpenCodeTextTracker(session, onProgress)
	for _, message := range history {
		tracker.ignore[message.Info.ID] = true
	}
	if onProgress != nil {
		onProgress(StreamProgress{Kind: "status", Text: "OpenCode accepted the request · waiting for model output…"})
	}
	poll := time.NewTicker(policy.poll)
	defer poll.Stop()
	idlePolls := 0
	started := time.Now()
	lastWaitingNotice := 0
	retryNotice := ""
	retryStarted := time.Time{}
	observeStatus := func(status openCodeSessionStatus) error {
		if status.Type != "retry" {
			retryNotice = ""
			retryStarted = time.Time{}
			return nil
		}
		message, err := openCodeStatusHealth(status, time.Now())
		if err != nil {
			return err
		}
		if retryStarted.IsZero() {
			retryStarted = time.Now()
		}
		if time.Since(retryStarted) > maxProviderRetryWait*maxProviderRetries {
			return &harnessFailure{agent: "opencode", kind: "retry"}
		}
		if message != retryNotice && onProgress != nil {
			onProgress(StreamProgress{Kind: "status", Text: message})
		}
		retryNotice = message
		return nil
	}
	for {
		select {
		case packet, ok := <-packets:
			if !ok || packet.err != nil {
				packets = nil // Poll status if the stream drops mid-turn.
				if onProgress != nil {
					onProgress(StreamProgress{Kind: "status", Text: "OpenCode live stream disconnected · recovering incremental text from the local session…"})
				}
				continue
			}
			if err := openCodeEventProblem(packet.event, session, tracker.ignore); err != nil {
				return TurnOutput{}, err
			}
			if packet.event.Type == "session.status" {
				var row struct {
					SessionID string                `json:"sessionID"`
					Status    openCodeSessionStatus `json:"status"`
				}
				if json.Unmarshal(packet.event.Properties, &row) == nil && row.SessionID == session {
					if err := observeStatus(row.Status); err != nil {
						return TurnOutput{}, err
					}
				}
			}
			if tracker.accept(packet.event) {
				out, err := finishOpenCodeTurn(ctx, client, base, password, session, previous, tracker)
				if errors.Is(err, errOpenCodeNoReply) {
					idlePolls++
					if idlePolls < 3 {
						continue
					} // Ignore an initial/stale idle event.
				}
				completed = err == nil
				return out, err
			}
		case <-poll.C:
			tracker.report(tracker.text(), false)
			// Also read partial text and errors while busy. If SSE drops, this
			// remains incremental rather than waiting for final completion.
			pollCtx, cancelPoll := context.WithTimeout(ctx, 2*time.Second)
			messages, messageErr := openCodeMessages(pollCtx, client, base, password, session)
			cancelPoll()
			if messageErr == nil {
				if err := newOpenCodeMessageError(messages, previous); err != nil {
					return TurnOutput{}, err
				}
				tracker.observeMessages(messages, previous)
			}
			// A tool-using model may do substantial work before writing prose.
			// Only time out the initial response if there is no real activity;
			// the outer turn deadline still bounds active work.
			if tracker.reported == "" && !tracker.active && time.Since(started) > policy.firstText {
				return TurnOutput{}, errors.New("OpenCode produced no text or tool activity before the initial response timeout. Check the saved model, provider sign-in, and pending permissions. The request was stopped; retry explicitly after fixing the cause.")
			}
			if onProgress != nil && tracker.reported == "" && retryNotice == "" {
				seconds := int(time.Since(started).Seconds())
				if seconds >= 15 && seconds/15 > lastWaitingNotice {
					lastWaitingNotice = seconds / 15
					status := fmt.Sprintf("Waiting for OpenCode's provider (%ds) · no activity received yet", seconds)
					if tracker.active {
						status = fmt.Sprintf("OpenCode is working (%ds) · %d tool calls · no written reply yet", seconds, len(tracker.tools))
						if len(tracker.tools) == 0 {
							status = fmt.Sprintf("OpenCode is processing the request (%ds) · no written reply yet", seconds)
						}
					}
					onProgress(StreamProgress{Kind: "status", Text: status})
				}
			}
			// Some OpenCode releases omit session.idle from /event. A local
			// status check guarantees that a completed turn still resolves.
			var status map[string]openCodeSessionStatus
			statusCtx, cancelStatus := context.WithTimeout(ctx, 2*time.Second)
			statusErr := openCodeJSON(statusCtx, client, base, password, http.MethodGet, "/session/status", nil, &status)
			cancelStatus()
			if statusErr == nil {
				if current, present := status[session]; present {
					if err := observeStatus(current); err != nil {
						return TurnOutput{}, err
					}
				}
				if entry, present := status[session]; !present || entry.Type == "idle" {
					answer, err := finishOpenCodeTurn(ctx, client, base, password, session, previous, tracker)
					if err == nil {
						completed = true
						return answer, nil
					}
					idlePolls++
					if idlePolls >= 3 {
						return TurnOutput{}, err
					}
				} else {
					idlePolls = 0
				}
			}
		case <-ctx.Done():
			return TurnOutput{}, ctx.Err()
		}
	}
}

func validOpenCodeSessionID(id string) bool {
	if id == "" || len(id) > 256 {
		return false
	}
	for _, c := range id {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' {
			continue
		}
		return false
	}
	return true
}

var errOpenCodeNoReply = errors.New("OpenCode ended without a written assistant reply or readable tool result. Check its native session before retrying; this does not necessarily mean the provider failed.")

func finishOpenCodeTurn(ctx context.Context, client *http.Client, base, password, session, previous string, tracker *openCodeTextTracker) (TurnOutput, error) {
	readFinal := false
	for attempt := 0; attempt < 3; attempt++ {
		messages, err := openCodeMessages(ctx, client, base, password, session)
		if err == nil {
			readFinal = true
			if err := newOpenCodeMessageError(messages, previous); err != nil {
				return TurnOutput{}, err
			}
			tracker.observeMessages(messages, previous)
			if text := tracker.answer(); text != "" {
				tracker.report(text, true)
				return TurnOutput{Text: text, NativeID: session}, nil
			}
		}
		if attempt < 2 {
			select {
			case <-time.After(150 * time.Millisecond):
			case <-ctx.Done():
				return TurnOutput{}, ctx.Err()
			}
		}
	}
	if text := strings.TrimSpace(tracker.text()); text != "" && tracker.complete {
		return TurnOutput{Text: text, NativeID: session}, nil
	}
	if summary := tracker.toolSummary(); summary != "" && readFinal {
		// Explicitly identify this as an execution record, not a model reply.
		// Saving it preserves the native session and changed files for follow-up.
		return TurnOutput{Text: summary, NativeID: session}, nil
	}
	return TurnOutput{}, errOpenCodeNoReply
}

type openCodeTextPart struct {
	message string
	text    string
	polled  string
	valid   bool
}

type openCodeTextTracker struct {
	session       string
	roles         map[string]string
	parts         map[string]*openCodeTextPart
	order         []string
	v2Parts       map[string]string
	v2Order       []string
	complete      bool
	reported      string
	last          time.Time
	callback      func(StreamProgress)
	ignore        map[string]bool
	activityParts map[string]openCodePart
	tools         map[string]openCodeActivity
	toolOrder     []string
	active        bool
	snapshotText  string
}

func newOpenCodeTextTracker(session string, callback func(StreamProgress)) *openCodeTextTracker {
	return &openCodeTextTracker{session: session, roles: make(map[string]string), parts: make(map[string]*openCodeTextPart),
		v2Parts: make(map[string]string), callback: callback, ignore: make(map[string]bool),
		activityParts: make(map[string]openCodePart), tools: make(map[string]openCodeActivity)}
}

func (t *openCodeTextTracker) part(id string) *openCodeTextPart {
	if id == "" {
		return nil
	}
	if part := t.parts[id]; part != nil {
		return part
	}
	part := &openCodeTextPart{}
	t.parts[id] = part
	t.order = append(t.order, id)
	return part
}

func (t *openCodeTextTracker) accept(event openCodeEvent) bool {
	switch event.Type {
	case "message.updated":
		var row struct {
			Info struct {
				ID        string `json:"id"`
				Role      string `json:"role"`
				SessionID string `json:"sessionID"`
			} `json:"info"`
		}
		if json.Unmarshal(event.Properties, &row) == nil && row.Info.SessionID == t.session {
			if t.ignore[row.Info.ID] {
				return false
			}
			t.roles[row.Info.ID] = row.Info.Role
			for _, part := range t.activityParts {
				if part.MessageID == row.Info.ID {
					t.observeActivity(part)
				}
			}
			t.report(t.text(), false)
		}
	case "message.part.updated":
		var row struct {
			Part openCodePart `json:"part"`
		}
		if json.Unmarshal(event.Properties, &row) == nil && row.Part.SessionID == t.session && !t.ignore[row.Part.MessageID] {
			t.observeActivity(row.Part)
			if row.Part.Type != "text" {
				return false
			}
			if part := t.part(row.Part.ID); part != nil {
				part.message, part.valid = row.Part.MessageID, true
				if len(row.Part.Text) >= len(part.text) {
					part.text = row.Part.Text
				}
				if row.Part.Text != "" {
					t.complete = true
				}
				t.report(t.text(), false)
			}
		}
	case "message.part.delta":
		var row struct {
			SessionID string `json:"sessionID"`
			MessageID string `json:"messageID"`
			PartID    string `json:"partID"`
			Field     string `json:"field"`
			Delta     string `json:"delta"`
		}
		if json.Unmarshal(event.Properties, &row) == nil && row.SessionID == t.session && row.Field == "text" && !t.ignore[row.MessageID] {
			if part := t.part(row.PartID); part != nil {
				part.message = row.MessageID
				part.text += row.Delta
				t.report(t.text(), false)
			}
		}
	case "session.next.text.started", "session.next.text.delta", "session.next.text.ended",
		"session.text.started", "session.text.delta", "session.text.ended":
		// Newer OpenCode runners publish their live text through the V2
		// session event family. Unlike V1 parts, these events are already
		// explicitly assistant text, not reasoning or tool input.
		var row struct {
			SessionID          string `json:"sessionID"`
			AssistantMessageID string `json:"assistantMessageID"`
			TextID             string `json:"textID"`
			Ordinal            *int   `json:"ordinal"`
			Delta              string `json:"delta"`
			Text               string `json:"text"`
		}
		if json.Unmarshal(event.Properties, &row) == nil && row.SessionID == t.session && row.AssistantMessageID != "" && !t.ignore[row.AssistantMessageID] {
			key := row.AssistantMessageID + ":" + row.TextID
			if row.TextID == "" && row.Ordinal != nil {
				key = fmt.Sprintf("%s:%d", row.AssistantMessageID, *row.Ordinal)
			}
			if _, exists := t.v2Parts[key]; !exists {
				t.v2Parts[key] = ""
				t.v2Order = append(t.v2Order, key)
			}
			switch event.Type {
			case "session.next.text.delta", "session.text.delta":
				t.v2Parts[key] += row.Delta
			case "session.next.text.ended", "session.text.ended":
				t.v2Parts[key] = row.Text
				t.complete = true
			}
			t.report(t.text(), false)
		}
	case "session.idle":
		var row struct {
			SessionID string `json:"sessionID"`
		}
		return json.Unmarshal(event.Properties, &row) == nil && row.SessionID == t.session
	case "session.status":
		var row struct {
			SessionID string `json:"sessionID"`
			Status    struct {
				Type string `json:"type"`
			} `json:"status"`
		}
		return json.Unmarshal(event.Properties, &row) == nil && row.SessionID == t.session && row.Status.Type == "idle"
	case "session.execution.succeeded":
		var row struct {
			SessionID string `json:"sessionID"`
		}
		return json.Unmarshal(event.Properties, &row) == nil && row.SessionID == t.session
	case "session.error":
		// The API status/final-message path below reports the outcome without
		// forwarding raw provider errors or tool arguments into chat text.
	}
	return false
}

func (t *openCodeTextTracker) text() string {
	if len(t.v2Order) > 0 {
		var chunks []string
		for _, id := range t.v2Order {
			if text := t.v2Parts[id]; strings.TrimSpace(text) != "" {
				chunks = append(chunks, text)
			}
		}
		return strings.Join(chunks, "\n\n")
	}
	var chunks []string
	for _, id := range t.order {
		part := t.parts[id]
		text := part.text
		if len(part.polled) >= len(text) {
			text = part.polled
		}
		if part.valid && !t.ignore[part.message] && t.roles[part.message] == "assistant" && strings.TrimSpace(text) != "" {
			chunks = append(chunks, text)
		}
	}
	return strings.Join(chunks, "\n\n")
}

func (t *openCodeTextTracker) report(text string, force bool) {
	if text != "" {
		t.active = true
	}
	if text == "" || text == t.reported {
		return
	}
	if !force && strings.HasPrefix(t.reported, text) {
		return
	} // Ignore a stale poll/SSE snapshot.
	if !force && time.Since(t.last) < 70*time.Millisecond {
		return
	}
	t.reported = text
	t.last = time.Now()
	if t.callback != nil {
		t.callback(StreamProgress{Kind: "text", Text: text})
	}
}
