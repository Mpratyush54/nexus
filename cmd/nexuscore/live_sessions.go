//go:build nexuscorelib

package main

// Live chat sessions for the desktop composer.
//
// continue.here runs one CLI process per message with stdin closed, so the
// agent can't ask for permission, can't be interrupted and can't take a
// follow-up while it works. live.* keeps one agent process per open chat and
// streams its events to the UI as {"type":"live.event", ...}.
//
//	live.supported {agent}                                  -> {live}
//	live.start     {session_id, agent, workspace_root, resume_id?, model?,
//	                permission_mode?, context?}             -> {live_id}
//	live.send      {live_id, text}
//	live.approve   {live_id, request_id, allow, reason?}
//	live.interrupt {live_id}
//	live.close     {live_id}
//
// Each finished turn is recorded through core continue.record, exactly like
// a continue.here exchange, then announced as {"type":"live.recorded", ...}.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"central-memory/internal/continuex"
	"central-memory/internal/core"
	"central-memory/internal/daemon"
	"central-memory/internal/diagnostics"
)

// liveStarters maps agents that have a live driver. Others keep using
// continue.here (one process per message).
var liveStarters = map[string]func(context.Context, continuex.LiveOptions) (continuex.LiveSession, error){
	"claude":   continuex.StartClaudeLive,
	"codex":    continuex.StartCodexLive,
	"cursor":   acpStarter("cursor"),
	"opencode": acpStarter("opencode"),
	"gemini":   acpStarter("gemini"),
	"kimi":     acpStarter("kimi"),
}

// acpStarter binds an agent to the shared Agent Client Protocol driver.
func acpStarter(agent string) func(context.Context, continuex.LiveOptions) (continuex.LiveSession, error) {
	return func(ctx context.Context, opts continuex.LiveOptions) (continuex.LiveSession, error) {
		return continuex.StartACPLive(ctx, agent, opts)
	}
}

type liveEntry struct {
	id, sessionID, projectID, agent, workspace string
	session                                    continuex.LiveSession
	cancel                                     context.CancelFunc
	resumeID                                   string

	mu     sync.Mutex
	prompt strings.Builder
	answer strings.Builder
	// streamed holds text_delta text not yet confirmed by a full "text"
	// block. ACP agents (Cursor, OpenCode) only ever send deltas, so this is
	// their whole reply.
	streamed   strings.Builder
	activities []continuex.ToolActivity
	files      []string
}

var liveSessions sync.Map // live_id -> *liveEntry

func normalizeLiveAgent(agent string) string {
	a := strings.ToLower(strings.TrimSpace(agent))
	switch a {
	case "claude-code", "claude_code":
		return "claude"
	case "agent", "cursor-cli", "cursor_agent", "cursor-agent":
		return "cursor"
	}
	return a
}

func liveCall(name string, args json.RawMessage) (any, error) {
	var body struct {
		LiveID         string `json:"live_id"`
		SessionID      string `json:"session_id"`
		ProjectID      string `json:"project_id"`
		Agent          string `json:"agent"`
		WorkspaceRoot  string `json:"workspace_root"`
		ResumeID       string `json:"resume_id"`
		Model          string `json:"model"`
		PermissionMode string `json:"permission_mode"`
		Context        string `json:"context"`
		Text           string `json:"text"`
		RequestID      string `json:"request_id"`
		Allow          bool   `json:"allow"`
		Reason         string `json:"reason"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &body); err != nil {
			return nil, err
		}
	}
	switch name {
	case "live.supported":
		_, ok := liveStarters[normalizeLiveAgent(body.Agent)]
		return map[string]any{"live": ok}, nil
	case "live.start":
		return startLive(body.SessionID, body.ProjectID, normalizeLiveAgent(body.Agent), body.WorkspaceRoot, body.ResumeID, body.Model, body.PermissionMode, body.Context)
	}
	value, ok := liveSessions.Load(body.LiveID)
	if !ok {
		return nil, errors.New("this chat is no longer running; send again to restart it")
	}
	entry := value.(*liveEntry)
	switch name {
	case "live.send":
		text := strings.TrimSpace(body.Text)
		if text == "" || len(text) > 16000 {
			return nil, errors.New("enter a message of up to 16,000 characters")
		}
		entry.mu.Lock()
		if entry.prompt.Len() > 0 {
			entry.prompt.WriteString("\n\n")
		}
		entry.prompt.WriteString(text)
		entry.mu.Unlock()
		// Background: drivers queue the message and must not tie the turn
		// to this call's lifetime.
		return map[string]any{"sent": true}, entry.session.Send(context.Background(), text)
	case "live.approve":
		reason := strings.TrimSpace(body.Reason)
		if !body.Allow && reason == "" {
			reason = "The user declined this action in Nexus."
		}
		return map[string]any{"answered": true}, entry.session.Approve(body.RequestID, body.Allow, reason)
	case "live.interrupt":
		return map[string]any{"interrupted": true}, entry.session.Interrupt()
	case "live.close":
		closeLive(entry)
		return map[string]any{"closed": true}, nil
	}
	return nil, errors.New("unknown live method " + name)
}

// nexusMemoryReadTools are memory lookups agents may call without asking;
// memory_write still goes through the agent's normal approval.
var nexusMemoryReadTools = []string{"memory_search", "project_knowledge", "session_summary_get", "session_fetch", "episode_search"}

// nexusMemoryServer gives the agent the signed-in account's Nexus memory as a
// remote MCP server, scoped to the chat's project. nil when signed out.
func nexusMemoryServer(projectID string) []continuex.MCPServer {
	nxState.mu.Lock()
	cloud, _ := nxState.deps.Cloud.(*core.HTTPCloud)
	nxState.mu.Unlock()
	if cloud == nil || strings.TrimSpace(cloud.Token) == "" || strings.TrimSpace(cloud.BaseURL) == "" {
		return nil
	}
	headers := map[string]string{"Authorization": "Bearer " + strings.TrimSpace(cloud.Token)}
	if p := strings.TrimSpace(projectID); p != "" {
		headers["X-Nexus-Project"] = p
	}
	return []continuex.MCPServer{{
		Name: "nexus", URL: strings.TrimRight(cloud.BaseURL, "/") + "/v1/agent/mcp",
		Headers: headers, AutoAllow: nexusMemoryReadTools,
	}}
}

func startLive(sessionID, projectID, agent, workspace, resumeID, model, permissionMode, seed string) (any, error) {
	start, ok := liveStarters[agent]
	if !ok {
		return nil, errors.New(agent + " does not support live chat yet")
	}
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return nil, errors.New("select the project folder before sending")
	}
	abs, err := filepath.Abs(workspace)
	if err != nil {
		return nil, err
	}
	if st, err := os.Stat(abs); err != nil || !st.IsDir() {
		return nil, errors.New("the project folder does not exist on this machine")
	}
	ctx, cancel := context.WithCancel(context.Background())
	session, err := start(ctx, continuex.LiveOptions{
		Workspace: abs, ResumeID: strings.TrimSpace(resumeID), Model: model,
		PermissionMode: permissionMode, SystemContext: continuex.SafeContinuationContext(seed),
		MCPServers: nexusMemoryServer(projectID),
	})
	if err != nil {
		cancel()
		return nil, err
	}
	entry := &liveEntry{id: newLiveID(), sessionID: sessionID, projectID: projectID, agent: agent, workspace: abs, session: session, cancel: cancel, resumeID: strings.TrimSpace(resumeID)}
	entry.markOwned(session.NativeID())
	liveSessions.Store(entry.id, entry)
	diagnostics.Go("live", func() { pumpLive(entry) })
	return map[string]any{"live_id": entry.id, "agent": agent}, nil
}

func newLiveID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "live_" + hex.EncodeToString(b[:])
}

func closeLive(entry *liveEntry) {
	liveSessions.Delete(entry.id)
	_ = entry.session.Close()
	entry.cancel()
}

// closeAllLive is called on core shutdown.
func closeAllLive() {
	liveSessions.Range(func(_, value any) bool {
		closeLive(value.(*liveEntry))
		return true
	})
}

// fileTools write files; their file_path input becomes a changed file.
var fileTools = map[string]bool{"Write": true, "Edit": true, "MultiEdit": true, "NotebookEdit": true}

// liveDeltaFlush batches text deltas: agents send dozens per second (Codex
// and Cursor sent 239 deltas for a 600-character reply), and forwarding each
// one made the desktop re-render the whole message thousands of times on long
// replies.
const liveDeltaFlush = 50 * time.Millisecond

func pumpLive(entry *liveEntry) {
	defer liveSessions.Delete(entry.id)
	send := func(ev continuex.LiveEvent) {
		event := map[string]any{
			"type": "live.event", "live_id": entry.id, "session_id": entry.sessionID, "agent": entry.agent,
			"kind": ev.Kind, "text": ev.Text, "tool_id": ev.ToolID, "tool_name": ev.ToolName,
			"tool_input": string(ev.ToolInput), "tool_output": ev.ToolOutput, "is_error": ev.IsError,
			"request_id": ev.RequestID, "native_id": ev.NativeID, "cost_usd": ev.CostUSD,
			"parent_tool_id": ev.ParentToolID,
		}
		if len(ev.Usage) > 0 {
			// Per model; the turn so far on "usage", the whole turn on turn_done.
			event["usage"] = ev.Usage
		}
		emitCoreEvent(event)
	}
	var pending strings.Builder
	flush := func() {
		if pending.Len() > 0 {
			send(continuex.LiveEvent{Kind: "text_delta", Text: pending.String()})
			pending.Reset()
		}
	}
	ticker := time.NewTicker(liveDeltaFlush)
	defer ticker.Stop()
	events := entry.session.Events()
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				flush()
				return
			}
			entry.track(ev)
			if ev.Kind == "text_delta" {
				pending.WriteString(ev.Text)
				continue
			}
			flush() // keep text ahead of the tool/turn event that follows it
			send(ev)
			if ev.Kind == "turn_done" {
				entry.record(ev)
			}
		case <-ticker.C:
			flush()
		}
	}
}

// markOwned tells capture that this agent session is saved by Nexus, so its
// transcript is not imported as a second copy of the chat. A resumed session
// that kept its id was captured before Nexus continued it; capture keeps it.
func (e *liveEntry) markOwned(nativeID string) {
	if nativeID != "" && nativeID != e.resumeID {
		daemon.MarkNexusOwned(nativeID)
	}
}

func (e *liveEntry) track(ev continuex.LiveEvent) {
	if ev.NativeID != "" {
		e.markOwned(ev.NativeID)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	switch ev.Kind {
	case "text_delta":
		e.streamed.WriteString(ev.Text)
	case "text":
		// A full block supersedes the deltas that built it.
		e.streamed.Reset()
		if e.answer.Len() > 0 {
			e.answer.WriteString("\n\n")
		}
		e.answer.WriteString(ev.Text)
	case "tool_start":
		e.flushStreamedLocked()
		activity := continuex.ToolActivity{ID: ev.ToolID, Tool: ev.ToolName, Status: "running", Input: string(ev.ToolInput), Parent: ev.ParentToolID}
		if fileTools[ev.ToolName] {
			var in struct {
				FilePath     string `json:"file_path"`
				NotebookPath string `json:"notebook_path"`
			}
			if json.Unmarshal(ev.ToolInput, &in) == nil {
				path := in.FilePath
				if path == "" {
					path = in.NotebookPath
				}
				if path != "" {
					if rel, err := filepath.Rel(e.workspace, path); err == nil && !strings.HasPrefix(rel, "..") {
						path = filepath.ToSlash(rel)
					}
					activity.Files = []string{path}
					e.files = appendUnique(e.files, path)
				}
			}
		}
		if len(e.activities) < 100 {
			e.activities = append(e.activities, activity)
		}
	case "tool_end":
		for i := range e.activities {
			if e.activities[i].ID == ev.ToolID {
				e.activities[i].Output = ev.ToolOutput
				e.activities[i].Status = "completed"
				if ev.IsError {
					e.activities[i].Status = "error"
					e.activities[i].Error = ev.ToolOutput
				}
			}
		}
	}
}

func (e *liveEntry) record(done continuex.LiveEvent) {
	e.mu.Lock()
	e.flushStreamedLocked()
	prompt, answer := e.prompt.String(), e.answer.String()
	if strings.TrimSpace(answer) == "" && !done.IsError {
		answer = done.Text
	}
	activities, files := e.activities, e.files
	e.prompt.Reset()
	e.answer.Reset()
	e.activities, e.files = nil, nil
	e.mu.Unlock()
	if strings.TrimSpace(answer) == "" {
		// The UI waits for live.recorded to finish the turn; always send one.
		problem := "The agent finished without a reply."
		if done.IsError && strings.TrimSpace(done.Text) != "" {
			problem = done.Text
		}
		emitCoreEvent(map[string]any{"type": "live.recorded", "live_id": e.id, "session_id": e.sessionID, "error": problem})
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"session_id": e.sessionID, "project_id": e.projectID, "agent": e.agent, "native_id": e.session.NativeID(),
		"prompt": prompt, "answer": answer, "file_changes": files, "activities": activities,
		"usage": done.Usage,
	})
	nxState.mu.Lock()
	deps := nxState.deps
	nxState.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := core.Call(ctx, "continue.record", payload, deps)
	event := map[string]any{"type": "live.recorded", "live_id": e.id, "session_id": e.sessionID, "result": result}
	if err != nil {
		event["error"] = err.Error()
	}
	emitCoreEvent(event)
}

// flushStreamedLocked moves delta-only text into the answer (caller holds mu).
func (e *liveEntry) flushStreamedLocked() {
	text := strings.TrimSpace(e.streamed.String())
	e.streamed.Reset()
	if text == "" {
		return
	}
	if e.answer.Len() > 0 {
		e.answer.WriteString("\n\n")
	}
	e.answer.WriteString(text)
}

func appendUnique(list []string, value string) []string {
	for _, existing := range list {
		if existing == value {
			return list
		}
	}
	return append(list, value)
}
