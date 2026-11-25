package continuex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// One in-app turn at a time prevents simultaneous agents from editing the
// same workspace, but the wait must be visible and cancellable in the UI.
var headlessTurnSlot = make(chan struct{}, 1)

// TurnExecutor runs one non-interactive turn of a locally installed agent.
// It is separate from Runner: Runner starts an external session, whereas the
// desktop needs the answer back in its own chat view.
type TurnExecutor interface {
	Turn(ctx context.Context, plan LaunchPlan, workspace string) (string, error)
}

type TurnOutput struct {
	Text         string
	NativeID     string
	ChangedFiles []string
	FileDiffs    []FileDiff
}

type StreamProgress struct {
	Kind string // status, text, or activity (safe structured tool progress)
	Text string
}

type DetailedTurnExecutor interface {
	TurnDetailed(ctx context.Context, plan LaunchPlan, workspace string) (TurnOutput, error)
}

// StreamingTurnExecutor reports readable assistant text while a turn runs.
// The final TurnOutput remains authoritative for storage and display.
type StreamingTurnExecutor interface {
	TurnStreaming(ctx context.Context, plan LaunchPlan, workspace string, onProgress func(StreamProgress)) (TurnOutput, error)
}

// HeadlessExecutor runs supported native CLIs without opening a console.
// A command can take a while, but output is bounded and stdin is closed so an
// interactive prompt cannot leave an invisible process waiting forever.
type HeadlessExecutor struct{}

const maxHeadlessOutput = 2 << 20

// AllowedImage validates local files before passing them to a harness CLI.
func AllowedImage(path string) bool {
	if !imageExtensionAllowed(path) {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Size() > 0 && info.Size() <= MaxLocalImageBytes
}

func (e HeadlessExecutor) Turn(ctx context.Context, plan LaunchPlan, workspace string) (string, error) {
	result, err := e.TurnDetailed(ctx, plan, workspace)
	return result.Text, err
}

func (HeadlessExecutor) TurnDetailed(ctx context.Context, plan LaunchPlan, workspace string) (TurnOutput, error) {
	return (HeadlessExecutor{}).TurnStreaming(ctx, plan, workspace, nil)
}

func (HeadlessExecutor) TurnStreaming(ctx context.Context, plan LaunchPlan, workspace string, onProgress func(StreamProgress)) (result TurnOutput, turnErr error) {
	select {
	case headlessTurnSlot <- struct{}{}:
	default:
		if onProgress != nil {
			onProgress(StreamProgress{Kind: "status", Text: "Waiting for another local agent to finish…"})
		}
		select {
		case headlessTurnSlot <- struct{}{}:
		case <-ctx.Done():
			return TurnOutput{}, ctx.Err()
		}
	}
	defer func() { <-headlessTurnSlot }()
	if plan.Mode != "here" || len(plan.Argv) == 0 {
		return TurnOutput{}, fmt.Errorf("%s can't reply inside Nexus yet", AgentDisplayName(plan.Agent))
	}
	if !SupportsHere(plan.Agent) {
		return TurnOutput{}, fmt.Errorf("%s can't reply inside Nexus yet", AgentDisplayName(plan.Agent))
	}
	if strings.TrimSpace(workspace) == "" {
		return TurnOutput{}, errors.New("Choose this chat's project folder before sending")
	}
	info, err := os.Stat(workspace)
	if err != nil || !info.IsDir() {
		return TurnOutput{}, errors.New("This chat's project folder isn't on this computer. Choose a folder that is, then send again")
	}
	if _, err := lookAgent(plan.Argv[0]); err != nil {
		return TurnOutput{}, agentErrorf(plan.Agent, "not_installed", "%s is not installed or is not on PATH", plan.Argv[0])
	}
	if onProgress != nil {
		onProgress(StreamProgress{Kind: "status", Text: "Preparing workspace and agent…"})
	}
	beforeFiles := workspaceSnapshot(workspace)
	baseline := captureWorkspaceBaseline(ctx, workspace)
	defer func() {
		previewCtx, stop := context.WithTimeout(context.Background(), 6*time.Second)
		defer stop()
		result.FileDiffs = baseline.changes(captureWorkspaceBaseline(previewCtx, workspace))
		paths := []string{}
		for _, change := range result.FileDiffs {
			paths = append(paths, change.Path)
		}
		result.ChangedFiles = mergeChangedPaths(result.ChangedFiles, paths)
		if onProgress != nil && len(result.FileDiffs) > 0 {
			data, _ := json.Marshal(map[string]any{"file_changes": result.ChangedFiles, "file_diffs": result.FileDiffs})
			onProgress(StreamProgress{Kind: "files", Text: string(data)})
		}
	}()
	turnCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if normalizeAgent(plan.Agent) == "opencode" {
		result, streamErr := openCodeServerTurn(turnCtx, plan, workspace, onProgress)
		if streamErr == nil {
			result.ChangedFiles = workspaceChanges(beforeFiles, workspaceSnapshot(workspace))
		}
		if isOpenCodePreflightError(streamErr) {
			var ae *AgentError
			if !errors.As(streamErr, &ae) {
				ae = &AgentError{Agent: normalizeAgent(plan.Agent), Kind: "cannot_start", Detail: streamErr.Error()}
			}
			return result, ae
		}
		return result, AsAgentError(plan.Agent, "failed", streamErr)
	}
	workerCtx, stopWorker := context.WithCancelCause(turnCtx)
	defer stopWorker(nil)
	cmd, err := commandForInAppTurn(workerCtx, plan, lookAgent)
	if err != nil {
		return TurnOutput{}, err
	}
	cmd.Dir = workspace
	cmd.Stdin = nil
	if len(plan.Stdin) > 0 {
		// Claude reads image content blocks as one stream-json message; EOF
		// right after it ends the turn, so nothing can wait for more input.
		cmd.Stdin = bytes.NewReader(plan.Stdin)
	}
	cmd.WaitDelay = 3 * time.Second
	var stderr cappedOutput
	var progressLock sync.Mutex
	progress := func(update StreamProgress) {
		if onProgress != nil {
			progressLock.Lock()
			defer progressLock.Unlock()
			onProgress(update)
		}
	}
	fail := func(err error) { stopWorker(err) }
	stdout := newStreamingOutput(progress, plan.Agent)
	stdout.cursor.workspace = workspace
	cmd.Stdout = &harnessDiagnosticOutput{destination: stdout, agent: plan.Agent, onFailure: fail, onProgress: progress}
	cmd.Stderr = &harnessDiagnosticOutput{destination: &stderr, agent: plan.Agent, stderr: true, onFailure: fail, onProgress: progress}
	if onProgress != nil {
		progress(StreamProgress{Kind: "status", Text: "Starting " + AgentDisplayName(plan.Agent) + "…"})
	}
	if err := cmd.Start(); err != nil {
		return TurnOutput{}, agentErrorf(plan.Agent, "cannot_start", "could not start %s: %v", plan.Agent, err)
	}
	if onProgress != nil {
		progress(StreamProgress{Kind: "status", Text: AgentDisplayName(plan.Agent) + " is working on a reply…"})
	}
	waitErr := cmd.Wait()
	if cause, ok := context.Cause(workerCtx).(*harnessFailure); ok {
		return TurnOutput{}, cause
	}
	if waitErr != nil {
		if turnCtx.Err() != nil {
			return TurnOutput{}, agentErrorf(plan.Agent, "timeout", "agent timed out: %v", turnCtx.Err())
		}
		problem := activityDetail(stderr.String(), 500)
		if problem == "" {
			problem = waitErr.Error()
		}
		return TurnOutput{}, agentErrorf(plan.Agent, "failed", "agent could not continue: %s", problem)
	}
	stdout.Flush()
	if stdout.truncated {
		return TurnOutput{}, fmt.Errorf("%s's reply is too long to show here. Open the chat in %s itself to read all of it", AgentDisplayName(plan.Agent), AgentDisplayName(plan.Agent))
	}
	if answer := readableAgentOutput(stdout.String()); answer != "" {
		return TurnOutput{Text: answer, NativeID: nativeIDFromAgentOutput(stdout.String()),
			ChangedFiles: mergeChangedPaths(workspaceChanges(beforeFiles, workspaceSnapshot(workspace)), stdout.cursor.changedPaths())}, nil
	}
	return TurnOutput{}, agentErrorf(plan.Agent, "empty", "agent finished without a readable reply")
}

// streamingOutput scans only complete JSONL records. Tool details use a
// separate redacted activity channel, never the assistant text channel.
type streamingOutput struct {
	cappedOutput
	pending    string
	shown      string
	onProgress func(StreamProgress)
	lastReport time.Time
	lastStatus string
	agent      string
	cursor     cursorActivityTracker
}

// os/exec copies pipe output with io.Copy. The embedded bytes.Buffer exposes
// ReaderFrom; without this override io.Copy skips Write and no progress event
// reaches the UI until the process exits.
func (w *streamingOutput) ReadFrom(r io.Reader) (int64, error) {
	return copyOutputChunks(r, w.Write)
}

func (w *streamingOutput) WriteString(s string) (int, error) {
	return w.Write([]byte(s))
}

func newStreamingOutput(onProgress func(StreamProgress), agent ...string) *streamingOutput {
	output := &streamingOutput{onProgress: onProgress}
	if len(agent) > 0 {
		output.agent = agent[0]
	}
	return output
}

func (w *streamingOutput) Write(p []byte) (int, error) {
	n, err := w.cappedOutput.Write(p)
	if n == 0 {
		return n, err
	}
	w.pending += string(p[:n])
	for {
		end := strings.IndexByte(w.pending, '\n')
		if end < 0 {
			break
		}
		w.accept(w.pending[:end])
		w.pending = w.pending[end+1:]
	}
	if len(w.pending) > maxHeadlessOutput {
		w.pending = ""
	}
	return n, err
}

func (w *streamingOutput) Flush() {
	if w.pending != "" {
		w.accept(w.pending)
		w.pending = ""
	}
	if w.onProgress != nil && w.shown != "" {
		w.onProgress(StreamProgress{Kind: "text", Text: w.shown})
	}
}

func (w *streamingOutput) accept(line string) {
	if streamHarnessProblem(w.agent, line, false) != nil {
		return
	}
	if normalizeAgent(w.agent) == "cursor" || w.agent == "agent" {
		if activity, ok := w.cursor.observe(line); ok && w.onProgress != nil {
			encoded, _ := json.Marshal(activity)
			w.onProgress(StreamProgress{Kind: "activity", Text: string(encoded)})
		}
	}
	part, delta := streamAssistantText(line)
	if part == "" {
		if status := streamAgentStatus(line); status != "" && status != w.lastStatus && w.onProgress != nil {
			w.lastStatus = status
			w.onProgress(StreamProgress{Kind: "status", Text: status})
		}
		return
	}
	if delta {
		w.shown += part
	} else if strings.HasPrefix(part, w.shown) {
		w.shown = part
	} else if !strings.HasSuffix(w.shown, part) {
		if w.shown != "" {
			w.shown += "\n\n"
		}
		w.shown += part
	}
	if len(w.shown) > 120000 {
		w.shown = w.shown[:120000]
	}
	if w.onProgress != nil && time.Since(w.lastReport) >= 70*time.Millisecond {
		w.onProgress(StreamProgress{Kind: "text", Text: w.shown})
		w.lastReport = time.Now()
	}
}

func streamAgentStatus(line string) string {
	var row map[string]any
	if json.Unmarshal([]byte(line), &row) != nil {
		return ""
	}
	switch row["type"] {
	case "system":
		return "Connected · waiting for reply…"
	case "step_start":
		return "Agent is generating a response…"
	case "step_finish":
		return "Agent finished · saving reply…"
	case "tool_call":
		if row["subtype"] == "started" {
			return "Agent is using workspace tools…"
		}
	}
	return ""
}

func streamAssistantText(line string) (string, bool) {
	var row map[string]any
	if json.Unmarshal([]byte(line), &row) != nil {
		return "", false
	}
	typeName, _ := row["type"].(string)
	switch typeName {
	case "item.updated", "item.completed":
		item, _ := row["item"].(map[string]any)
		if item["type"] == "agent_message" {
			text, _ := item["text"].(string)
			return text, false
		}
	case "text", "message":
		part, _ := row["part"].(map[string]any)
		text, _ := part["text"].(string)
		return text, false
	case "stream_event":
		event, _ := row["event"].(map[string]any)
		if event["type"] == "content_block_delta" {
			delta, _ := event["delta"].(map[string]any)
			if delta["type"] == "text_delta" {
				text, _ := delta["text"].(string)
				return text, true
			}
		}
	case "assistant":
		message, _ := row["message"].(map[string]any)
		if text, ok := message["content"].(string); ok {
			return text, false
		}
		if blocks, ok := message["content"].([]any); ok {
			var chunks []string
			for _, block := range blocks {
				part, _ := block.(map[string]any)
				if part["type"] == "text" {
					if text, ok := part["text"].(string); ok {
						chunks = append(chunks, text)
					}
				}
			}
			return strings.Join(chunks, ""), true
		}
	case "result":
		text, _ := row["result"].(string)
		return text, false
	}
	return "", false
}

func nativeIDFromAgentOutput(raw string) string {
	for _, line := range strings.Split(raw, "\n") {
		var row map[string]any
		if json.Unmarshal([]byte(line), &row) != nil {
			continue
		}
		for _, key := range []string{"session_id", "sessionID", "thread_id"} {
			if id, ok := row[key].(string); ok && len(id) > 0 && len(id) <= 256 && !strings.ContainsAny(id, " \t\r\n") {
				return id
			}
		}
		if part, ok := row["part"].(map[string]any); ok {
			for _, key := range []string{"sessionID", "session_id"} {
				if id, ok := part[key].(string); ok && len(id) > 0 && len(id) <= 256 && !strings.ContainsAny(id, " \t\r\n") {
					return id
				}
			}
		}
	}
	return ""
}

// npm .cmd launchers can mangle arguments containing spaces and may expose a
// visible cmd.exe. Resolve the installed native executable instead. Never
// interpolate user prompts into a command shell.
func directExecutable(path, agent string) (string, []string, error) {
	if runtime.GOOS != "windows" {
		return path, nil, nil
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".cmd", ".bat", ".ps1", "":
	case ".exe":
		return resolveShim(path, agentBinary(agent)) // scoop .shim targets
	default:
		return path, nil, nil
	}
	dir := filepath.Dir(path)
	base := strings.TrimSuffix(path, filepath.Ext(path))
	switch normalizeAgent(agent) {
	case "codex":
		for _, rel := range []string{
			filepath.Join("node_modules", "@openai", "codex", "vendor", "x86_64-pc-windows-msvc", "codex", "codex.exe"),
			filepath.Join("node_modules", "@openai", "codex-win32-x64", "vendor", "x86_64-pc-windows-msvc", "codex", "codex.exe"),
			filepath.Join("node_modules", "@openai", "codex", "node_modules", "@openai", "codex-win32-x64", "vendor", "x86_64-pc-windows-msvc", "codex", "codex.exe"),
			filepath.Join("node_modules", "@openai", "codex-win32-arm64", "vendor", "aarch64-pc-windows-msvc", "codex", "codex.exe"),
			filepath.Join("node_modules", "@openai", "codex", "node_modules", "@openai", "codex-win32-arm64", "vendor", "aarch64-pc-windows-msvc", "codex", "codex.exe"),
		} {
			candidate := filepath.Join(dir, rel)
			if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
				return candidate, nil, nil
			}
		}
		// Newer packages ship vendor/<triple>/bin/codex.exe inside a nested
		// platform package; match any of those layouts.
		for _, pattern := range []string{
			filepath.Join(dir, "node_modules", "@openai", "codex", "node_modules", "@openai", "codex-win32-*", "vendor", "*", "bin", "codex.exe"),
			filepath.Join(dir, "node_modules", "@openai", "codex-win32-*", "vendor", "*", "bin", "codex.exe"),
			filepath.Join(dir, "node_modules", "@openai", "codex", "vendor", "*", "bin", "codex.exe"),
		} {
			if matches, _ := filepath.Glob(pattern); len(matches) > 0 {
				return matches[0], nil, nil
			}
		}
	case "gemini":
		// npm installs Gemini CLI as a .cmd shim around a Node bundle; run
		// Node directly so no console window appears.
		for _, rel := range []string{
			filepath.Join("node_modules", "@google", "gemini-cli", "bundle", "gemini.js"),
			filepath.Join("node_modules", "@google", "gemini-cli", "dist", "index.js"),
		} {
			entry := filepath.Join(dir, rel)
			if info, err := os.Stat(entry); err != nil || !info.Mode().IsRegular() {
				continue
			}
			node := filepath.Join(dir, "node.exe")
			if _, err := os.Stat(node); err != nil {
				if found, lookErr := exec.LookPath("node"); lookErr == nil {
					node = found
				} else {
					break
				}
			}
			return node, []string{entry}, nil
		}
	case "claude":
		// npm's claude.cmd shim wraps the native binary shipped in the package.
		// Without this, an npm install earlier on PATH than the native installer
		// made Claude look "not installed" (no models, install dialog on send).
		native := filepath.Join(dir, "node_modules", "@anthropic-ai", "claude-code", "bin", "claude.exe")
		if info, err := os.Stat(native); err == nil && info.Mode().IsRegular() {
			return native, nil, nil
		}
	case "opencode":
		native := filepath.Join(dir, "node_modules", "opencode-ai", "bin", "opencode.exe")
		if _, err := os.Stat(native); err == nil {
			return native, nil, nil
		}
	case "cursor", "agent":
		if exe := base + ".exe"; exe != path {
			if _, err := os.Stat(exe); err == nil {
				return exe, nil, nil
			}
		}
		if home, err := os.UserHomeDir(); err == nil {
			for _, rel := range []string{
				filepath.Join("AppData", "Local", "Programs", "cursor", "resources", "app", "bin", "cursor-agent.exe"),
				filepath.Join("AppData", "Local", "Programs", "Cursor", "resources", "app", "bin", "cursor-agent.exe"),
			} {
				candidate := filepath.Join(home, rel)
				if _, err := os.Stat(candidate); err == nil {
					return candidate, nil, nil
				}
			}
		}
		candidates, _ := filepath.Glob(filepath.Join(dir, "versions", "*", "node.exe"))
		sort.Sort(sort.Reverse(sort.StringSlice(candidates)))
		for _, node := range candidates {
			entry := filepath.Join(filepath.Dir(node), "index.js")
			if _, err := os.Stat(entry); err == nil {
				return node, []string{entry}, nil
			}
		}
	}
	// Any other launcher (npm, pnpm, yarn, scoop, ps1/sh scripts): read it
	// to find the program behind it, or the Node entry point to run.
	if exe, prefix, err := resolveShim(path, agentBinary(agent)); err == nil {
		return exe, prefix, nil
	}
	if exe := base + ".exe"; exe != path {
		if _, err := os.Stat(exe); err == nil {
			return exe, nil, nil
		}
	}
	if strings.EqualFold(filepath.Ext(path), ".bat") {
		// A hand-written batch file is the program itself; Windows runs it.
		return path, nil, nil
	}
	return "", nil, &launcherError{agent: normalizeAgent(agent), path: path}
}

// agentBinary is the command an agent is started with.
func agentBinary(agent string) string {
	switch a := normalizeAgent(agent); a {
	case "cursor", "agent":
		return "cursor-agent"
	default:
		return a
	}
}

// SupportsHere is deliberately narrower than native resume. Some vendors
// expose resume-by-ID for an interactive terminal but not a prompt/reply
// command that Nexus can safely host without opening a console.
func SupportsHere(agent string) bool {
	switch normalizeAgent(agent) {
	case "claude", "codex", "gemini", "opencode", "kimi":
		return true
	case "cursor", "agent":
		return true
	default:
		return false
	}
}

type cappedOutput struct {
	bytes.Buffer
	truncated bool
}

func (w *cappedOutput) ReadFrom(r io.Reader) (int64, error) {
	return copyOutputChunks(r, w.Write)
}

func (w *cappedOutput) WriteString(s string) (int, error) {
	return w.Write([]byte(s))
}

func copyOutputChunks(r io.Reader, write func([]byte) (int, error)) (int64, error) {
	var total int64
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			written, writeErr := write(buf[:n])
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			if written != n {
				return total, io.ErrShortWrite
			}
		}
		if err == io.EOF {
			return total, nil
		}
		if err != nil {
			return total, err
		}
	}
}

func (w *cappedOutput) Write(p []byte) (int, error) {
	n := len(p)
	if w.Len() < maxHeadlessOutput {
		remaining := maxHeadlessOutput - w.Len()
		if remaining > n {
			remaining = n
		}
		_, _ = w.Buffer.Write(p[:remaining])
		if remaining < n {
			w.truncated = true
		}
	} else if n > 0 {
		w.truncated = true
	}
	return n, nil
}

// The native CLIs use different JSONL envelopes. Prefer assistant text and
// never expose tool arguments or raw event JSON as a chat answer.
func readableAgentOutput(raw string) string {
	var messages []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if streamHarnessProblem("", line, false) != nil {
			continue
		}
		if _, _, retry := streamRetryHealth("", line, false); retry {
			continue
		}
		var row map[string]any
		if json.Unmarshal([]byte(line), &row) != nil {
			messages = append(messages, line)
			continue
		}
		typeName, _ := row["type"].(string)
		switch typeName {
		case "item.completed": // Codex --json
			item, _ := row["item"].(map[string]any)
			if itemType, _ := item["type"].(string); itemType == "agent_message" {
				if text, _ := item["text"].(string); strings.TrimSpace(text) != "" {
					messages = append(messages, text)
				}
			}
		case "result": // Claude stream-json
			if text, _ := row["result"].(string); strings.TrimSpace(text) != "" {
				messages = append(messages, text)
			}
		case "text", "message": // OpenCode JSON events
			part, _ := row["part"].(map[string]any)
			if text, _ := part["text"].(string); strings.TrimSpace(text) != "" {
				messages = append(messages, text)
			}
		}
	}
	return strings.TrimSpace(strings.Join(messages, "\n\n"))
}
