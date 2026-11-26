//go:build nexuscorelib

package main

// Live chat sessions for the desktop composer.
//
// continue.here runs one CLI process per message with stdin closed, so the
// agent can't ask for permission, can't be interrupted and can't take a
// follow-up while it works. live.* keeps one agent process per chat, owned
// by the core (internal/agentrun), not by the page that sent the message:
// closing the chat, opening another one or hiding the window never stops a
// turn. Only Stop, closing the chat's session or quitting Nexus do.
//
// Every call is keyed by the chat's session_id:
//
//	live.supported   {agent}                         -> {live}
//	live.send        {session_id, project_id, agent, workspace_root,
//	                  resume_id?, model?, mode?, context?, text, images?,
//	                  title?, when?}                 -> {live_id} | {queued} | {followup}
//	live.attach      {session_id}                    -> snapshot to replay
//	live.approve     {session_id, request_id, decision: once|chat|project|deny, reason?}
//	live.set_mode    {session_id, mode: ask|edits|full}
//	live.interrupt   {session_id}
//	live.close       {session_id}
//	live.seen / live.resume / live.dismiss {session_id}
//	live.followup_remove {session_id, index}         -> {text, images}
//	live.list                                        -> {sessions, running, queued, approval}
//	live.rules.list                                  -> {default_mode, projects}
//	live.rules.remove {project, tool, prefix?}
//	live.rules.default {mode}
//
// Checkpoints (internal/checkpoint): each turn snapshots the project folder
// first, so a chat can go back to before any message and a turn's changes
// can be reviewed hunk by hunk. message_id is the saved user message index
// (user_idx), a checkpoint id or an exchange id; "" means the latest.
//
//	live.checkpoints  {session_id}                   -> {checkpoints, rewinds}
//	live.review       {checkpoint_id}                -> {files, pending, added, removed, ...}
//	live.review.keep  {checkpoint_id, path?, hunk?, version?}  path "" = all
//	live.review.undo  {checkpoint_id, path?, hunk?, version?}  path "" = all
//	live.rewind.preview {session_id, message_id?}    -> {checkpoint_id, files, later}
//	live.rewind       {session_id, message_id?}      -> {rewind, restored, failed}
//	live.rewind.undo  {session_id, undo_id}          -> {restored}
//
// Events: {"type":"live.event", session_id, seq, kind, ...} while a turn
// streams, live.turn_started, live.recorded when the turn is saved,
// live.status when a chat's status changes, live.followups.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"central-memory/internal/agentrun"
	"central-memory/internal/chatstatus"
	"central-memory/internal/checkpoint"
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

var (
	liveMu  sync.Mutex
	liveMgr *agentrun.Manager
	// liveStatusStop ends the chat status publisher (see liveManager).
	liveStatusStop context.CancelFunc
)

// liveManager returns the process-wide session manager, created on first
// use so it picks up the diagnostics folder (its state lives next to logs).
func liveManager() *agentrun.Manager {
	liveMu.Lock()
	defer liveMu.Unlock()
	if liveMgr == nil {
		// Chat statuses also go to the server, so the web chat list shows
		// the sidebar's dots and can mark chats read.
		publisher := &chatstatus.Publisher{Cloud: liveStatusCloud}
		m := agentrun.New(agentrun.Config{
			StateDir: liveStateDir(),
			Start:    startLiveAgent,
			Record:   recordLiveTurn,
			Emit:     func(e map[string]any) { emitCoreEvent(e); publisher.Note(e) },
			// Under the profile's data folder (LOCALAPPDATA/Nexus follows
			// NEXUS_PROFILE), beside logs/.
			Checkpoints: checkpoint.Open(filepath.Join(liveDataDir(), "checkpoints"), checkpoint.Options{}),
		})
		publisher.List = func() []chatstatus.Report { return liveStatusReports(m.List()) }
		publisher.Read = m.Seen
		ctx, cancel := context.WithCancel(context.Background())
		liveStatusStop = cancel
		go func() { defer diagnostics.Guard("chat-status")(); publisher.Run(ctx) }()
		liveMgr = m
	}
	return liveMgr
}

// liveStatusCloud is the signed-in server, or nil.
func liveStatusCloud() chatstatus.Doer {
	nxState.mu.Lock()
	defer nxState.mu.Unlock()
	if cloud, ok := nxState.deps.Cloud.(*core.HTTPCloud); ok && cloud != nil && strings.TrimSpace(cloud.Token) != "" {
		return cloud
	}
	return nil
}

// liveStatusReports turns live.list into the publisher's reports.
func liveStatusReports(list map[string]any) []chatstatus.Report {
	items, _ := list["sessions"].([]map[string]any)
	out := make([]chatstatus.Report, 0, len(items))
	for _, item := range items {
		id, _ := item["session_id"].(string)
		status, _ := item["status"].(string)
		unread, _ := item["unread"].(bool)
		out = append(out, chatstatus.Report{SessionID: id, Status: status, Unread: unread})
	}
	return out
}

func liveStateDir() string {
	base := liveDataDir()
	if base == "" {
		return ""
	}
	return filepath.Join(base, "agent-sessions")
}

func liveDataDir() string {
	base := strings.TrimSpace(diagBase)
	if base == "" {
		base = diagnostics.DefaultDir()
	}
	return base
}

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
		agentrun.SendRequest
		PermissionMode string `json:"permission_mode"` // older desktops
		RequestID      string `json:"request_id"`
		Decision       string `json:"decision"`
		Allow          *bool  `json:"allow"`
		Reason         string `json:"reason"`
		Index          int    `json:"index"`
		Project        string `json:"project"`
		Tool           string `json:"tool"`
		Prefix         string `json:"prefix"`
		CheckpointID   string `json:"checkpoint_id"`
		MessageID      string `json:"message_id"`
		Path           string `json:"path"`
		Hunk           *int   `json:"hunk"`
		Version        string `json:"version"`
		UndoID         string `json:"undo_id"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &body); err != nil {
			return nil, err
		}
	}
	if name == "live.supported" {
		_, ok := liveStarters[normalizeLiveAgent(body.Agent)]
		return map[string]any{"live": ok}, nil
	}
	m := liveManager()
	id := strings.TrimSpace(body.SessionID)
	switch name {
	case "live.send":
		req := body.SendRequest
		if req.Mode == "" {
			req.Mode = body.PermissionMode
		}
		if err := validateLiveSend(&req); err != nil {
			return nil, err
		}
		return m.Send(req)
	case "live.attach":
		return m.Attach(id), nil
	case "live.approve":
		decision := body.Decision
		if decision == "" && body.Allow != nil {
			decision = agentrun.Deny
			if *body.Allow {
				decision = agentrun.AllowOnce
			}
		}
		switch decision {
		case agentrun.AllowOnce, agentrun.AllowChat, agentrun.AllowProject, agentrun.Deny:
		default:
			return nil, errors.New("Choose Allow or Deny")
		}
		return map[string]any{"answered": true}, m.Approve(id, body.RequestID, decision, strings.TrimSpace(body.Reason))
	case "live.set_mode":
		return map[string]any{"mode": agentrun.NormalizeMode(body.Mode)}, m.SetMode(id, body.Mode)
	case "live.interrupt":
		return map[string]any{"interrupted": true}, m.Interrupt(id)
	case "live.close":
		m.Close(id)
		return map[string]any{"closed": true}, nil
	case "live.seen":
		m.Seen(id)
		return map[string]any{"seen": true}, nil
	case "live.resume":
		return m.Resume(id)
	case "live.dismiss":
		m.Dismiss(id)
		return map[string]any{"dismissed": true}, nil
	case "live.followup_remove":
		return m.RemoveFollowup(id, body.Index)
	case "live.list":
		return m.List(), nil
	case "live.rules.list":
		return map[string]any{"default_mode": m.Rules.DefaultMode(), "projects": m.Rules.All()}, nil
	case "live.rules.remove":
		return map[string]any{"removed": true}, m.Rules.Remove(body.Project, agentrun.Rule{Tool: body.Tool, Prefix: body.Prefix})
	case "live.checkpoints":
		return liveCheckpoints(m, id)
	case "live.review", "live.review.keep", "live.review.undo":
		store := m.Checkpoints()
		if store == nil {
			return nil, errors.New("Checkpoints are off in this copy of Nexus")
		}
		hunk := -1
		if body.Hunk != nil {
			hunk = *body.Hunk
		}
		switch name {
		case "live.review.keep":
			return store.Keep(body.CheckpointID, body.Path, hunk, body.Version)
		case "live.review.undo":
			return store.Undo(body.CheckpointID, body.Path, hunk, body.Version)
		}
		return store.Review(body.CheckpointID)
	case "live.rewind.preview":
		return m.PreviewRewind(id, body.MessageID)
	case "live.rewind":
		return m.RewindTo(id, body.MessageID)
	case "live.rewind.undo":
		put, err := m.UndoRewind(id, body.UndoID)
		return map[string]any{"restored": put}, err
	case "live.rules.default":
		return map[string]any{"default_mode": agentrun.NormalizeMode(body.Mode)}, m.Rules.SetDefaultMode(body.Mode)
	}
	return nil, errors.New("unknown live method " + name)
}

// liveCheckpoints lists a chat's restorable messages and its rewinds.
func liveCheckpoints(m *agentrun.Manager, sessionID string) (any, error) {
	store := m.Checkpoints()
	if store == nil || sessionID == "" {
		return map[string]any{"checkpoints": []any{}, "rewinds": []any{}}, nil
	}
	list := []map[string]any{}
	for _, cp := range store.List(sessionID) {
		list = append(list, map[string]any{
			"id": cp.ID, "user_idx": cp.UserIdx, "exchange_id": cp.ExchangeID, "created_at": cp.CreatedAt,
			"done": cp.Done, "changed": len(cp.Changed), "rewound": cp.Rewound,
		})
	}
	rewinds := store.Rewinds(sessionID)
	if rewinds == nil {
		rewinds = []checkpoint.Rewind{}
	}
	return map[string]any{"checkpoints": list, "rewinds": rewinds}, nil
}

// validateLiveSend checks a message before the manager takes it.
func validateLiveSend(req *agentrun.SendRequest) error {
	req.Agent = normalizeLiveAgent(req.Agent)
	if _, ok := liveStarters[req.Agent]; !ok {
		return errors.New(continuex.AgentDisplayName(req.Agent) + " can't chat inside Nexus yet")
	}
	text := strings.TrimSpace(req.Text)
	if text == "" || len(text) > 16000 {
		return errors.New("Write a message of up to 16,000 characters")
	}
	if len(req.Images) > continuex.MaxImages {
		return errors.New("Attach up to four images at a time")
	}
	for _, path := range req.Images {
		if !continuex.AllowedImage(path) {
			return errors.New("Attach a PNG, JPEG, GIF or WebP image under 20 MB")
		}
	}
	workspace := strings.TrimSpace(req.Workspace)
	if workspace == "" {
		return errors.New("Choose a project folder on this computer before sending")
	}
	abs, err := filepath.Abs(workspace)
	if err != nil {
		return err
	}
	if st, err := os.Stat(abs); err != nil || !st.IsDir() {
		return errors.New("That project folder isn't on this computer. Choose one that is, then send again")
	}
	req.Workspace = abs
	return nil
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

// startLiveAgent starts the agent process for a chat.
func startLiveAgent(ctx context.Context, spec agentrun.StartSpec) (continuex.LiveSession, error) {
	start, ok := liveStarters[spec.Agent]
	if !ok {
		return nil, errors.New(continuex.AgentDisplayName(spec.Agent) + " can't chat inside Nexus yet")
	}
	session, err := start(ctx, continuex.LiveOptions{
		Workspace: spec.Workspace, ResumeID: strings.TrimSpace(spec.ResumeID), Model: spec.Model,
		PermissionMode: spec.Mode, SystemContext: continuex.SafeContinuationContext(spec.Context),
		MCPServers: nexusMemoryServer(spec.ProjectID),
	})
	if err != nil {
		return nil, continuex.AsAgentError(spec.Agent, "cannot_start", err)
	}
	return ownLiveSession(session, spec.ResumeID), nil
}

// ownedSession tells capture that the agent session is saved by Nexus, so
// its transcript is not imported as a second copy of the chat. A resumed
// session that kept its id was captured before Nexus continued it.
type ownedSession struct {
	continuex.LiveSession
	resumeID string
	once     sync.Once
	events   chan continuex.LiveEvent
}

func ownLiveSession(session continuex.LiveSession, resumeID string) continuex.LiveSession {
	o := &ownedSession{LiveSession: session, resumeID: strings.TrimSpace(resumeID)}
	o.mark(session.NativeID())
	if _, ok := session.(continuex.ImageSender); ok {
		return &ownedImageSession{o}
	}
	return o
}

func (o *ownedSession) mark(nativeID string) {
	if nativeID != "" && nativeID != o.resumeID {
		daemon.MarkNexusOwned(nativeID)
	}
}

func (o *ownedSession) Events() <-chan continuex.LiveEvent {
	o.once.Do(func() {
		o.events = make(chan continuex.LiveEvent, 256)
		in := o.LiveSession.Events()
		diagnostics.Go("live", func() {
			defer close(o.events)
			for ev := range in {
				if ev.NativeID != "" {
					o.mark(ev.NativeID)
				}
				o.events <- ev
			}
		})
	})
	return o.events
}

type ownedImageSession struct{ *ownedSession }

func (o *ownedImageSession) SendWithImages(ctx context.Context, text string, images []string) error {
	return o.LiveSession.(continuex.ImageSender).SendWithImages(ctx, text, images)
}

// recordLiveTurn saves a finished turn through core continue.record,
// exactly like a continue.here exchange.
func recordLiveTurn(t agentrun.TurnRecord) (any, error) {
	payload, _ := json.Marshal(map[string]any{
		"session_id": t.SessionID, "project_id": t.ProjectID, "agent": t.Agent, "native_id": t.NativeID,
		"prompt": t.Prompt, "answer": t.Answer, "file_changes": t.Files, "activities": t.Activities,
		"usage": t.Usage,
	})
	nxState.mu.Lock()
	deps := nxState.deps
	nxState.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return core.Call(ctx, "continue.record", payload, deps)
}

// closeAllLive is called on core shutdown: every agent process ends, and
// turns still running are remembered so the next start offers Resume.
func closeAllLive() {
	liveMu.Lock()
	m := liveMgr
	liveMgr = nil
	stop := liveStatusStop
	liveStatusStop = nil
	liveMu.Unlock()
	if stop != nil {
		stop()
	}
	if m != nil {
		m.Shutdown()
	}
}
