package agentrun

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"central-memory/internal/checkpoint"
	"central-memory/internal/continuex"
	"central-memory/internal/usage"
)

// Statuses a chat can be in, as the sidebar shows them.
const (
	StatusIdle        = "idle"
	StatusQueued      = "queued"
	StatusRunning     = "running"
	StatusApproval    = "approval"    // waiting for the person to allow something
	StatusDone        = "done"        // finished; Unread until the chat is opened
	StatusError       = "error"       // the turn failed
	StatusInterrupted = "interrupted" // Nexus closed while the turn ran
)

// DefaultMaxRunning is how many turns run at once; more wait in a queue.
const DefaultMaxRunning = 6

// StartSpec is what a new agent process needs.
type StartSpec struct {
	Agent, ProjectID, Workspace, ResumeID, Model, Mode, Context string
}

// TurnRecord is a finished turn, handed to Config.Record to save.
type TurnRecord struct {
	SessionID, ProjectID, Agent, NativeID string
	Prompt, Answer                        string
	Files                                 []string
	Activities                            []continuex.ToolActivity
	Usage                                 []usage.Entry
	// Checkpoint is the snapshot taken before the turn ("" when none).
	Checkpoint string
}

// Config wires a Manager to the agents, the transcript store and the UI.
type Config struct {
	Start  func(ctx context.Context, spec StartSpec) (continuex.LiveSession, error)
	Record func(TurnRecord) (any, error)
	// Emit delivers one UI event. It is called with the manager's lock
	// held, so events stay in order; it must not call back into the Manager.
	Emit       func(map[string]any)
	MaxRunning int
	// MaxOpen bounds idle agent processes kept warm for follow-ups; the
	// least recently used idle ones are closed beyond it.
	MaxOpen int
	// StateDir holds approval-rules.json and running-turns.json.
	StateDir string
	// DeltaFlush batches text deltas (default 50 ms).
	DeltaFlush time.Duration
	Now        func() time.Time
	// Checkpoints, when set, snapshots the project folder before each
	// turn, so the chat can be restored to before any message and a
	// turn's changes reviewed hunk by hunk.
	Checkpoints *checkpoint.Store
}

// SendRequest is one message from the composer.
type SendRequest struct {
	SessionID string   `json:"session_id"`
	ProjectID string   `json:"project_id"`
	Agent     string   `json:"agent"`
	Workspace string   `json:"workspace_root"`
	ResumeID  string   `json:"resume_id"`
	Model     string   `json:"model"`
	Mode      string   `json:"mode"`
	Context   string   `json:"context"`
	Text      string   `json:"text"`
	Images    []string `json:"images"`
	Title     string   `json:"title"`
	// When is "" or "queue" (a follow-up waits for the running turn) or
	// "now" (it stops the running turn and goes next).
	When string `json:"when"`
}

// MaxFollowups is how many follow-ups a chat can queue behind its turn.
const MaxFollowups = 5

// Manager owns every live chat. It is safe for concurrent use.
type Manager struct {
	cfg   Config
	Rules *RuleStore

	mu       sync.Mutex
	sessions map[string]*session
	queue    []string
	seq      uint64
	closed   bool
}

type pendingApproval struct {
	RequestID string
	ToolID    string
	Action    Action
	At        time.Time
}

type session struct {
	id, projectID, agent, workspace, model, mode, title string
	startMode                                           string // the native mode the process was started with
	live                                                continuex.LiveSession
	liveID                                              string
	gen                                                 int
	status                                              string
	unread                                              bool
	lastError                                           string
	turn                                                *turn
	queued                                              *SendRequest
	followups                                           []SendRequest
	chatRules                                           []Rule
	pending                                             map[string]*pendingApproval
	pendingOrder                                        []string
	lastUsed                                            time.Time
	interrupted                                         *runningRecord
}

type turn struct {
	prompt     string
	text       string // what the person typed, for the replayed bubble
	images     []string
	startedAt  time.Time
	log        []map[string]any
	answer     strings.Builder
	streamed   strings.Builder
	activities []continuex.ToolActivity
	files      []string
	checkpoint string // snapshot taken before the agent got the message
}

// runningRecord is what survives a restart for a turn that was running.
type runningRecord struct {
	SessionID string    `json:"session_id"`
	ProjectID string    `json:"project_id"`
	Agent     string    `json:"agent"`
	Workspace string    `json:"workspace"`
	Model     string    `json:"model"`
	Mode      string    `json:"mode"`
	NativeID  string    `json:"native_id"`
	Prompt    string    `json:"prompt"`
	Title     string    `json:"title,omitempty"`
	StartedAt time.Time `json:"started_at"`
	// Interrupted is set once a later Nexus has shown the turn as stopped.
	Interrupted bool `json:"interrupted,omitempty"`
}

// New creates a Manager and loads turns a previous Nexus left running:
// those chats show as "stopped when Nexus closed" with a Resume action.
func New(cfg Config) *Manager {
	if cfg.MaxRunning <= 0 {
		cfg.MaxRunning = DefaultMaxRunning
	}
	if cfg.MaxOpen <= 0 {
		cfg.MaxOpen = 10
	}
	if cfg.DeltaFlush <= 0 {
		cfg.DeltaFlush = 50 * time.Millisecond
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Emit == nil {
		cfg.Emit = func(map[string]any) {}
	}
	rulesPath := ""
	if cfg.StateDir != "" {
		rulesPath = filepath.Join(cfg.StateDir, "approval-rules.json")
	}
	m := &Manager{cfg: cfg, Rules: OpenRuleStore(rulesPath), sessions: map[string]*session{}}
	for _, rec := range m.loadRunning() {
		rec := rec
		rec.Interrupted = true
		m.sessions[rec.SessionID] = &session{
			id: rec.SessionID, projectID: rec.ProjectID, agent: rec.Agent, workspace: rec.Workspace,
			model: rec.Model, mode: rec.Mode, title: rec.Title, status: StatusInterrupted, unread: true,
			pending: map[string]*pendingApproval{}, interrupted: &rec,
		}
	}
	m.mu.Lock()
	m.saveRunningLocked()
	m.mu.Unlock()
	return m
}

var errBusy = errors.New("This chat is still working. Wait for the reply, or press Stop")

// Send starts a turn, or queues it when MaxRunning turns already run.
func (m *Manager) Send(req SendRequest) (map[string]any, error) {
	req.SessionID = strings.TrimSpace(req.SessionID)
	if req.SessionID == "" {
		return nil, errors.New("Open a chat before sending")
	}
	explicitMode := strings.TrimSpace(req.Mode) != ""
	req.Mode = NormalizeMode(nonEmpty(req.Mode, m.Rules.DefaultMode()))
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, errors.New("Nexus is closing")
	}
	s := m.sessions[req.SessionID]
	if s == nil {
		s = &session{id: req.SessionID, pending: map[string]*pendingApproval{}, status: StatusIdle}
		m.sessions[req.SessionID] = s
	}
	if s.turn != nil || s.queued != nil {
		if len(s.followups) >= MaxFollowups {
			m.mu.Unlock()
			return nil, errBusy
		}
		if !explicitMode && s.mode != "" {
			req.Mode = s.mode
		}
		now := req.When == "now"
		pos := len(s.followups) + 1
		if now {
			s.followups = append([]SendRequest{req}, s.followups...)
			pos = 1
		} else {
			s.followups = append(s.followups, req)
		}
		m.emitFollowupsLocked(s)
		m.emitStatusLocked(s)
		live, running := s.live, s.turn != nil
		m.mu.Unlock()
		if now && running && live != nil {
			_ = live.Interrupt()
		}
		return map[string]any{"followup": true, "position": pos, "session_id": req.SessionID}, nil
	}
	s.projectID, s.mode = req.ProjectID, req.Mode
	if req.Title != "" {
		s.title = req.Title
	}
	s.interrupted = nil
	s.lastError = ""
	s.unread = false
	if m.runningLocked() >= m.cfg.MaxRunning {
		r := req
		s.queued = &r
		s.agent, s.workspace, s.model = req.Agent, req.Workspace, req.Model
		s.status = StatusQueued
		m.queue = append(m.queue, s.id)
		pos := len(m.queue)
		m.saveRunningLocked()
		m.emitStatusLocked(s)
		m.mu.Unlock()
		return map[string]any{"queued": true, "position": pos, "session_id": s.id}, nil
	}
	m.beginTurnLocked(s, req)
	m.mu.Unlock()
	if err := m.launch(s, req); err != nil {
		m.failTurn(s, err)
		return nil, err
	}
	m.mu.Lock()
	liveID := s.liveID
	m.mu.Unlock()
	return map[string]any{"queued": false, "live_id": liveID, "session_id": s.id}, nil
}

func (m *Manager) runningLocked() int {
	n := 0
	for _, s := range m.sessions {
		if s.turn != nil {
			n++
		}
	}
	return n
}

func (m *Manager) beginTurnLocked(s *session, req SendRequest) {
	s.queued = nil
	s.turn = &turn{prompt: continuex.ImageTags(strings.TrimSpace(req.Text), req.Images), text: strings.TrimSpace(req.Text), images: req.Images, startedAt: m.cfg.Now()}
	s.status = StatusRunning
	s.lastUsed = m.cfg.Now()
	m.saveRunningLocked()
	// Lets an open chat that did not send this turn itself (a follow-up,
	// a queued start, a resume) show it.
	m.emitLocked(map[string]any{"type": "live.turn_started", "session_id": s.id, "agent": req.Agent, "prompt": s.turn.text, "images": req.Images})
	m.emitStatusLocked(s)
}

// emitFollowupsLocked tells the UI which follow-ups wait in a chat.
func (m *Manager) emitFollowupsLocked(s *session) {
	m.emitLocked(map[string]any{"type": "live.followups", "session_id": s.id, "followups": followupTexts(s.followups)})
}

func followupTexts(list []SendRequest) []map[string]any {
	out := make([]map[string]any, 0, len(list))
	for _, f := range list {
		out = append(out, map[string]any{"text": f.Text, "images": f.Images})
	}
	return out
}

// RemoveFollowup drops a waiting follow-up and returns it, so "Edit" can
// put its text back in the composer.
func (m *Manager) RemoveFollowup(sessionID string, index int) (map[string]any, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[sessionID]
	if s == nil || index < 0 || index >= len(s.followups) {
		return nil, errors.New("That follow-up was already sent")
	}
	f := s.followups[index]
	s.followups = append(s.followups[:index], s.followups[index+1:]...)
	m.emitFollowupsLocked(s)
	m.emitStatusLocked(s)
	return map[string]any{"text": f.Text, "images": f.Images}, nil
}

// nextFollowupLocked starts the chat's next follow-up once its turn ended.
func (m *Manager) nextFollowupLocked(s *session) {
	if len(s.followups) == 0 || s.turn != nil || s.queued != nil || m.closed {
		return
	}
	req := s.followups[0]
	s.followups = s.followups[1:]
	m.emitFollowupsLocked(s)
	s.mode = NormalizeMode(nonEmpty(req.Mode, s.mode))
	if m.runningLocked() >= m.cfg.MaxRunning {
		r := req
		s.queued = &r
		s.status = StatusQueued
		m.queue = append(m.queue, s.id)
		m.emitStatusLocked(s)
		return
	}
	m.beginTurnLocked(s, req)
	go func() {
		if err := m.launch(s, req); err != nil {
			m.failTurn(s, err)
		}
	}()
}

// launch makes sure the chat has an agent process with the right settings,
// then sends the message. A changed agent, model, folder or native mode
// restarts the process on the same agent session, so context carries over.
func (m *Manager) launch(s *session, req SendRequest) error {
	m.takeCheckpoint(s, req)
	nativeMode := NativeMode(req.Agent, req.Mode)
	m.mu.Lock()
	old := s.live
	restart := old != nil && (s.agent != req.Agent || s.model != req.Model || s.workspace != req.Workspace || s.startMode != nativeMode)
	resume := strings.TrimSpace(req.ResumeID)
	if old != nil && restart && s.agent == req.Agent {
		if n := old.NativeID(); n != "" {
			resume = n
		}
	}
	need := old == nil || restart
	if restart {
		s.live = nil
		s.gen++
	}
	s.agent, s.workspace, s.model = req.Agent, req.Workspace, req.Model
	m.mu.Unlock()
	if restart {
		_ = old.Close()
	}
	if need {
		live, err := m.cfg.Start(context.Background(), StartSpec{
			Agent: req.Agent, ProjectID: req.ProjectID, Workspace: req.Workspace, ResumeID: resume,
			Model: req.Model, Mode: nativeMode, Context: req.Context,
		})
		if err != nil {
			return err
		}
		m.mu.Lock()
		s.live = live
		s.liveID = newID("live_")
		s.startMode = nativeMode
		s.gen++
		gen := s.gen
		m.mu.Unlock()
		go m.pump(s, live, gen)
	}
	m.mu.Lock()
	live := s.live
	m.mu.Unlock()
	if live == nil {
		return errors.New("The agent stopped before it could take your message. Send again")
	}
	if len(req.Images) > 0 {
		sender, ok := live.(continuex.ImageSender)
		if !ok {
			return errors.New(continuex.AgentDisplayName(req.Agent) + " can't take images in Nexus yet. Remove the image, or pick another agent")
		}
		return sender.SendWithImages(context.Background(), strings.TrimSpace(req.Text), req.Images)
	}
	return live.Send(context.Background(), strings.TrimSpace(req.Text))
}

// failTurn ends a turn that could not start and tells the UI.
func (m *Manager) failTurn(s *session, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s.turn == nil {
		return
	}
	s.turn = nil
	s.status = StatusError
	s.lastError = err.Error()
	s.unread = true
	m.emitLocked(map[string]any{"type": "live.recorded", "session_id": s.id, "live_id": s.liveID, "error": err.Error()})
	m.saveRunningLocked()
	m.emitStatusLocked(s)
	m.nextFollowupLocked(s)
	m.startQueuedLocked()
}

// startQueuedLocked starts queued turns while there is room.
func (m *Manager) startQueuedLocked() {
	for len(m.queue) > 0 && m.runningLocked() < m.cfg.MaxRunning && !m.closed {
		id := m.queue[0]
		m.queue = m.queue[1:]
		s := m.sessions[id]
		if s == nil || s.queued == nil {
			continue
		}
		req := *s.queued
		m.beginTurnLocked(s, req)
		go func() {
			if err := m.launch(s, req); err != nil {
				m.failTurn(s, err)
			}
		}()
	}
	for _, id := range m.queue {
		if s := m.sessions[id]; s != nil {
			m.emitStatusLocked(s)
		}
	}
}

func (m *Manager) pump(s *session, live continuex.LiveSession, gen int) {
	events := live.Events()
	var pending, thinking strings.Builder
	ticker := time.NewTicker(m.cfg.DeltaFlush)
	defer ticker.Stop()
	flush := func() {
		if thinking.Len() > 0 {
			m.handle(s, live, gen, continuex.LiveEvent{Kind: "thinking_delta", Text: thinking.String()})
			thinking.Reset()
		}
		if pending.Len() > 0 {
			m.handle(s, live, gen, continuex.LiveEvent{Kind: "text_delta", Text: pending.String()})
			pending.Reset()
		}
	}
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				flush()
				m.processGone(s, gen)
				return
			}
			if ev.Kind == "text_delta" {
				if thinking.Len() > 0 {
					flush()
				}
				pending.WriteString(ev.Text)
				continue
			}
			if ev.Kind == "thinking_delta" {
				if pending.Len() > 0 {
					flush()
				}
				thinking.WriteString(ev.Text)
				continue
			}
			flush()
			m.handle(s, live, gen, ev)
		case <-ticker.C:
			flush()
		}
	}
}

func eventMap(s *session, ev continuex.LiveEvent) map[string]any {
	e := map[string]any{
		"type": "live.event", "live_id": s.liveID, "session_id": s.id, "agent": s.agent,
		"kind": ev.Kind, "text": ev.Text, "tool_id": ev.ToolID, "tool_name": ev.ToolName,
		"tool_input": string(ev.ToolInput), "tool_output": ev.ToolOutput, "is_error": ev.IsError,
		"request_id": ev.RequestID, "native_id": ev.NativeID, "cost_usd": ev.CostUSD,
		"parent_tool_id": ev.ParentToolID,
	}
	if len(ev.Usage) > 0 {
		e["usage"] = ev.Usage
	}
	return e
}

func (m *Manager) handle(s *session, live continuex.LiveSession, gen int, ev continuex.LiveEvent) {
	m.mu.Lock()
	if s.gen != gen {
		m.mu.Unlock()
		return // a replaced process
	}
	if ev.NativeID != "" && s.turn != nil {
		m.saveRunningLocked()
	}
	if s.turn != nil {
		s.turn.track(ev, s.workspace)
	}
	switch ev.Kind {
	case "approval":
		action := Classify(ev.ToolName, ev.ToolKind, ev.ToolInput, s.workspace)
		rules := append(append([]Rule(nil), s.chatRules...), m.Rules.Rules(ProjectKey(s.projectID, s.workspace))...)
		if AutoApprove(s.mode, action, s.workspace, rules) {
			e := eventMap(s, ev)
			e["kind"] = "auto_approved"
			e["summary"] = action.Summary
			m.emitLocked(e)
			m.mu.Unlock()
			_ = live.Approve(ev.RequestID, true, "")
			return
		}
		s.pending[ev.RequestID] = &pendingApproval{RequestID: ev.RequestID, ToolID: ev.ToolID, Action: action, At: m.cfg.Now()}
		s.pendingOrder = append(s.pendingOrder, ev.RequestID)
		s.status = StatusApproval
		e := eventMap(s, ev)
		e["action"] = action
		m.emitLocked(e)
		m.emitStatusLocked(s)
		m.mu.Unlock()
		return
	case "turn_done":
		m.emitLocked(eventMap(s, ev))
		t := s.turn
		s.turn = nil
		s.clearPending()
		rec, problem := t.finish(s, live, ev)
		m.mu.Unlock()
		changed := m.finishCheckpoint(t)
		var result any
		var err error
		if problem == "" && t != nil {
			rec.Checkpoint = t.checkpoint
			result, err = m.cfg.Record(rec)
			if err != nil {
				problem = err.Error()
			} else {
				result = m.linkCheckpoint(t, result, changed)
			}
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		e := map[string]any{"type": "live.recorded", "live_id": s.liveID, "session_id": s.id}
		if problem != "" {
			e["error"] = problem
			s.status, s.lastError = StatusError, problem
		} else {
			e["result"] = result
			s.status = StatusDone
		}
		s.unread = true
		s.lastUsed = m.cfg.Now()
		m.emitLocked(e)
		m.saveRunningLocked()
		m.emitStatusLocked(s)
		m.nextFollowupLocked(s)
		m.startQueuedLocked()
		m.evictIdleLocked()
		return
	}
	m.emitLocked(eventMap(s, ev))
	m.mu.Unlock()
}

// processGone handles an agent process that exited.
func (m *Manager) processGone(s *session, gen int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s.gen != gen {
		return
	}
	s.live = nil
	s.clearPending()
	if s.turn != nil {
		s.turn = nil
		s.status = StatusError
		s.lastError = "The agent stopped. Send again to restart it."
		s.unread = true
		m.emitLocked(map[string]any{"type": "live.event", "live_id": s.liveID, "session_id": s.id, "agent": s.agent, "kind": "exited"})
		m.saveRunningLocked()
		m.emitStatusLocked(s)
		// A waiting follow-up restarts the agent on its own session.
		m.nextFollowupLocked(s)
		m.startQueuedLocked()
	}
}

func (s *session) clearPending() {
	s.pending = map[string]*pendingApproval{}
	s.pendingOrder = nil
}

// Approval decisions.
const (
	AllowOnce    = "once"
	AllowChat    = "chat"
	AllowProject = "project"
	Deny         = "deny"
)

// Approve answers a permission request. "chat" and "project" also remember
// the rule, and answer other waiting requests the rule now covers.
func (m *Manager) Approve(sessionID, requestID, decision, reason string) error {
	m.mu.Lock()
	s := m.sessions[sessionID]
	if s == nil || s.pending[requestID] == nil {
		m.mu.Unlock()
		return errors.New("That request was already answered")
	}
	p := s.pending[requestID]
	type answer struct {
		live      continuex.LiveSession
		id        string
		allow     bool
		reasonTxt string
	}
	answers := []answer{{s.live, requestID, decision != Deny, reason}}
	s.removePending(requestID)
	m.emitLocked(map[string]any{"type": "live.event", "kind": "approval_resolved", "session_id": s.id, "live_id": s.liveID, "request_id": requestID, "decision": decision})
	switch decision {
	case AllowChat:
		s.chatRules = append(s.chatRules, p.Action.Rule)
	case AllowProject:
		if err := m.Rules.Add(ProjectKey(s.projectID, s.workspace), p.Action.Rule); err != nil {
			m.mu.Unlock()
			return err
		}
	}
	if decision == AllowChat || decision == AllowProject {
		for _, other := range m.sessions {
			if other != s && (decision == AllowChat || ProjectKey(other.projectID, other.workspace) != ProjectKey(s.projectID, s.workspace)) {
				continue
			}
			for _, id := range append([]string(nil), other.pendingOrder...) {
				q := other.pending[id]
				rules := append(append([]Rule(nil), other.chatRules...), m.Rules.Rules(ProjectKey(other.projectID, other.workspace))...)
				if q != nil && AutoApprove(other.mode, q.Action, other.workspace, rules) {
					other.removePending(id)
					answers = append(answers, answer{other.live, id, true, ""})
					m.emitLocked(map[string]any{"type": "live.event", "kind": "approval_resolved", "session_id": other.id, "live_id": other.liveID, "request_id": id, "decision": "rule"})
					m.refreshStatusLocked(other)
				}
			}
		}
	}
	m.refreshStatusLocked(s)
	m.mu.Unlock()
	var first error
	for _, a := range answers {
		if a.live == nil {
			continue
		}
		reason := a.reasonTxt
		if !a.allow && strings.TrimSpace(reason) == "" {
			reason = "The user declined this action in Nexus."
		}
		if err := a.live.Approve(a.id, a.allow, reason); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (s *session) removePending(id string) {
	delete(s.pending, id)
	for i, v := range s.pendingOrder {
		if v == id {
			s.pendingOrder = append(s.pendingOrder[:i], s.pendingOrder[i+1:]...)
			break
		}
	}
}

func (m *Manager) refreshStatusLocked(s *session) {
	if s.turn == nil {
		return
	}
	want := StatusRunning
	if len(s.pending) > 0 {
		want = StatusApproval
	}
	if s.status != want {
		s.status = want
		m.emitStatusLocked(s)
	}
}

// SetMode changes a chat's permission mode now; waiting requests the new
// mode allows are answered. A native flag change applies from the next
// message (the agent restarts on the same session).
func (m *Manager) SetMode(sessionID, mode string) error {
	mode = NormalizeMode(mode)
	m.mu.Lock()
	s := m.sessions[sessionID]
	if s == nil {
		m.mu.Unlock()
		return nil
	}
	s.mode = mode
	var allow []string
	for _, id := range append([]string(nil), s.pendingOrder...) {
		q := s.pending[id]
		rules := append(append([]Rule(nil), s.chatRules...), m.Rules.Rules(ProjectKey(s.projectID, s.workspace))...)
		if q != nil && AutoApprove(mode, q.Action, s.workspace, rules) {
			s.removePending(id)
			allow = append(allow, id)
			m.emitLocked(map[string]any{"type": "live.event", "kind": "approval_resolved", "session_id": s.id, "live_id": s.liveID, "request_id": id, "decision": "mode"})
		}
	}
	m.refreshStatusLocked(s)
	live := s.live
	m.mu.Unlock()
	for _, id := range allow {
		if live != nil {
			_ = live.Approve(id, true, "")
		}
	}
	return nil
}

// Interrupt stops the chat's current turn (or drops it from the queue).
// The agent process stays open for the next message.
func (m *Manager) Interrupt(sessionID string) error {
	m.mu.Lock()
	s := m.sessions[sessionID]
	if s == nil {
		m.mu.Unlock()
		return nil
	}
	if s.queued != nil {
		s.queued = nil
		for i, id := range m.queue {
			if id == sessionID {
				m.queue = append(m.queue[:i], m.queue[i+1:]...)
				break
			}
		}
		s.status = StatusIdle
		m.emitLocked(map[string]any{"type": "live.recorded", "session_id": s.id, "live_id": s.liveID, "error": "Stopped before it started."})
		m.saveRunningLocked()
		m.emitStatusLocked(s)
		m.mu.Unlock()
		return nil
	}
	live := s.live
	s.clearPending()
	m.refreshStatusLocked(s)
	m.mu.Unlock()
	if live == nil {
		return nil
	}
	return live.Interrupt()
}

// Close ends a chat's agent process (the chat was deleted, or Nexus quits).
func (m *Manager) Close(sessionID string) {
	m.mu.Lock()
	s := m.sessions[sessionID]
	if s == nil {
		m.mu.Unlock()
		return
	}
	live := s.live
	s.live = nil
	s.gen++
	s.turn, s.queued, s.followups = nil, nil, nil
	s.clearPending()
	s.status = StatusIdle
	m.saveRunningLocked()
	m.emitStatusLocked(s)
	m.startQueuedLocked()
	m.mu.Unlock()
	if live != nil {
		_ = live.Close()
	}
}

// Shutdown closes every agent process. Turns still running stay in
// running-turns.json so the next Nexus shows them with a Resume action.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	m.closed = true
	m.saveRunningLocked()
	var lives []continuex.LiveSession
	for _, s := range m.sessions {
		if s.live != nil {
			lives = append(lives, s.live)
			s.live = nil
			s.gen++
		}
	}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, l := range lives {
		wg.Add(1)
		go func(l continuex.LiveSession) { defer wg.Done(); _ = l.Close() }(l)
	}
	wg.Wait()
}

// Snapshot is what a reopened chat needs to show its live turn.
func (m *Manager) Attach(sessionID string) map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[sessionID]
	out := map[string]any{"session_id": sessionID, "status": StatusIdle, "seq": m.seq, "events": []map[string]any{}}
	if s == nil {
		return out
	}
	out["status"] = s.status
	out["agent"] = s.agent
	out["live_id"] = s.liveID
	out["mode"] = s.mode
	out["last_error"] = s.lastError
	out["unread"] = s.unread
	if s.turn != nil {
		out["prompt"] = s.turn.text
		out["images"] = s.turn.images
		out["started_at"] = s.turn.startedAt
		out["events"] = append([]map[string]any(nil), s.turn.log...)
		pending := make([]map[string]any, 0, len(s.pendingOrder))
		for _, id := range s.pendingOrder {
			if p := s.pending[id]; p != nil {
				pending = append(pending, map[string]any{"request_id": p.RequestID, "tool_id": p.ToolID, "action": p.Action})
			}
		}
		out["pending"] = pending
	} else if s.queued != nil {
		out["prompt"] = s.queued.Text
		out["images"] = s.queued.Images
	}
	out["followups"] = followupTexts(s.followups)
	if s.interrupted != nil {
		out["interrupted"] = s.interrupted
	}
	if s.unread && s.turn == nil {
		s.unread = false
		m.emitStatusLocked(s)
	}
	return out
}

// Seen clears a chat's unread mark.
func (m *Manager) Seen(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.sessions[sessionID]; s != nil && s.unread {
		s.unread = false
		if s.status == StatusDone {
			s.status = StatusIdle
		}
		m.emitStatusLocked(s)
	}
}

// Resume continues a turn that was running when Nexus closed, on the
// agent's own session.
func (m *Manager) Resume(sessionID string) (map[string]any, error) {
	m.mu.Lock()
	s := m.sessions[sessionID]
	if s == nil || s.interrupted == nil {
		m.mu.Unlock()
		return nil, errors.New("Nothing to resume in this chat")
	}
	rec := *s.interrupted
	m.mu.Unlock()
	return m.Send(SendRequest{
		SessionID: rec.SessionID, ProjectID: rec.ProjectID, Agent: rec.Agent, Workspace: rec.Workspace,
		ResumeID: rec.NativeID, Model: rec.Model, Mode: rec.Mode, Title: rec.Title,
		Text: "Nexus closed while you were working on my last request. Please continue where you left off.",
	})
}

// Dismiss forgets a turn that was stopped when Nexus closed.
func (m *Manager) Dismiss(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.sessions[sessionID]; s != nil && s.interrupted != nil {
		s.interrupted = nil
		s.status = StatusIdle
		s.unread = false
		m.saveRunningLocked()
		m.emitStatusLocked(s)
	}
}

// List returns every chat with a status worth showing, for the sidebar and
// the tray, plus the counts.
func (m *Manager) List() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	items := []map[string]any{}
	for _, s := range m.sessions {
		if s.status == StatusIdle && !s.unread {
			continue
		}
		items = append(items, m.statusLocked(s))
	}
	sort.Slice(items, func(i, j int) bool { return items[i]["session_id"].(string) < items[j]["session_id"].(string) })
	running, queued, approval := m.countsLocked()
	return map[string]any{"sessions": items, "running": running, "queued": queued, "approval": approval, "max_running": m.cfg.MaxRunning}
}

func (m *Manager) countsLocked() (running, queued, approval int) {
	for _, s := range m.sessions {
		switch {
		case s.turn != nil && len(s.pending) > 0:
			approval++
			running++
		case s.turn != nil:
			running++
		case s.queued != nil:
			queued++
		}
	}
	return
}

func (m *Manager) statusLocked(s *session) map[string]any {
	e := map[string]any{
		"session_id": s.id, "status": s.status, "unread": s.unread, "agent": s.agent,
		"project_id": s.projectID, "title": s.title, "pending": len(s.pending), "error": s.lastError,
		"followups": len(s.followups),
	}
	if len(s.pendingOrder) > 0 {
		if p := s.pending[s.pendingOrder[0]]; p != nil {
			e["approval_summary"] = p.Action.Summary
		}
	}
	return e
}

func (m *Manager) emitStatusLocked(s *session) {
	e := m.statusLocked(s)
	e["type"] = "live.status"
	running, queued, approval := m.countsLocked()
	e["running"], e["queued"], e["approval_count"] = running, queued, approval
	m.emitLocked(e)
}

// emitLocked numbers an event, keeps it in the turn's replay log, and
// sends it to the UI.
func (m *Manager) emitLocked(e map[string]any) {
	m.seq++
	e["seq"] = m.seq
	if t, _ := e["type"].(string); t == "live.event" {
		// When it happened (Unix ms), so a replay can tell how long the
		// agent thought.
		e["at"] = m.cfg.Now().UnixMilli()
		if s := m.sessions[strOf(e["session_id"])]; s != nil && s.turn != nil {
			s.turn.remember(e)
		}
	}
	m.cfg.Emit(e)
}

const maxReplayEvents = 4000

// remember appends an event to the replay log; consecutive text deltas are
// merged so a long reply stays one entry.
func (t *turn) remember(e map[string]any) {
	kind := strOf(e["kind"])
	if (kind == "text_delta" || kind == "thinking_delta") && len(t.log) > 0 {
		last := t.log[len(t.log)-1]
		if strOf(last["kind"]) == kind {
			merged := make(map[string]any, len(last))
			for k, v := range last {
				merged[k] = v
			}
			merged["text"] = strOf(last["text"]) + strOf(e["text"])
			merged["seq"] = e["seq"]
			// "at" stays the first delta's time; "until" is the last one's,
			// so the replayed thinking keeps its real length.
			if at, ok := e["at"]; ok {
				merged["until"] = at
			}
			t.log[len(t.log)-1] = merged
			return
		}
	}
	if len(t.log) >= maxReplayEvents && kind != "text" && kind != "text_delta" && kind != "approval" {
		return
	}
	t.log = append(t.log, e)
}

func strOf(v any) string {
	s, _ := v.(string)
	return s
}

// evictIdleLocked closes the least recently used idle processes beyond MaxOpen.
func (m *Manager) evictIdleLocked() {
	var idle []*session
	open := 0
	for _, s := range m.sessions {
		if s.live == nil {
			continue
		}
		open++
		if s.turn == nil && s.queued == nil && len(s.pending) == 0 {
			idle = append(idle, s)
		}
	}
	if open <= m.cfg.MaxOpen {
		return
	}
	sort.Slice(idle, func(i, j int) bool { return idle[i].lastUsed.Before(idle[j].lastUsed) })
	for _, s := range idle {
		if open <= m.cfg.MaxOpen {
			break
		}
		live := s.live
		s.live = nil
		s.gen++
		open--
		go func() { _ = live.Close() }()
	}
}

// ---- turn recording ---------------------------------------------------------

// fileTools write files; their file_path input becomes a changed file.
var fileTools = map[string]bool{"Write": true, "Edit": true, "MultiEdit": true, "NotebookEdit": true}

func (t *turn) track(ev continuex.LiveEvent, workspace string) {
	switch ev.Kind {
	case "text_delta":
		t.streamed.WriteString(ev.Text)
	case "text":
		t.streamed.Reset()
		if t.answer.Len() > 0 {
			t.answer.WriteString("\n\n")
		}
		t.answer.WriteString(ev.Text)
	case "tool_start":
		t.flushStreamed()
		activity := continuex.ToolActivity{ID: ev.ToolID, Tool: ev.ToolName, Status: "running", Input: string(ev.ToolInput), Parent: ev.ParentToolID}
		if fileTools[ev.ToolName] {
			var in struct {
				FilePath     string `json:"file_path"`
				NotebookPath string `json:"notebook_path"`
			}
			if json.Unmarshal(ev.ToolInput, &in) == nil {
				path := nonEmpty(in.FilePath, in.NotebookPath)
				if strings.TrimSpace(path) != "" {
					if rel, err := filepath.Rel(workspace, path); err == nil && !strings.HasPrefix(rel, "..") {
						path = filepath.ToSlash(rel)
					}
					activity.Files = []string{path}
					if !contains(t.files, path) {
						t.files = append(t.files, path)
					}
				}
			}
		}
		if len(t.activities) < 100 {
			t.activities = append(t.activities, activity)
		}
	case "tool_end":
		for i := range t.activities {
			if t.activities[i].ID == ev.ToolID {
				t.activities[i].Output = ev.ToolOutput
				t.activities[i].Status = "completed"
				if ev.IsError {
					t.activities[i].Status = "error"
					t.activities[i].Error = ev.ToolOutput
				}
			}
		}
	}
}

func (t *turn) flushStreamed() {
	text := strings.TrimSpace(t.streamed.String())
	t.streamed.Reset()
	if text == "" {
		return
	}
	if t.answer.Len() > 0 {
		t.answer.WriteString("\n\n")
	}
	t.answer.WriteString(text)
}

// finish builds the record for a finished turn, or the problem to show.
func (t *turn) finish(s *session, live continuex.LiveSession, done continuex.LiveEvent) (TurnRecord, string) {
	if t == nil {
		return TurnRecord{}, "The agent finished without a reply."
	}
	t.flushStreamed()
	answer := t.answer.String()
	if strings.TrimSpace(answer) == "" && !done.IsError {
		answer = done.Text
	}
	if strings.TrimSpace(answer) == "" {
		problem := "The agent finished without a reply."
		if done.IsError && strings.TrimSpace(done.Text) != "" {
			problem = done.Text
		}
		return TurnRecord{}, problem
	}
	native := ""
	if live != nil {
		native = live.NativeID()
	}
	return TurnRecord{
		SessionID: s.id, ProjectID: s.projectID, Agent: s.agent, NativeID: native,
		Prompt: t.prompt, Answer: answer, Files: t.files, Activities: t.activities, Usage: done.Usage,
	}, ""
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// ---- running-turns.json -----------------------------------------------------

func (m *Manager) runningPath() string {
	if m.cfg.StateDir == "" {
		return ""
	}
	return filepath.Join(m.cfg.StateDir, "running-turns.json")
}

func (m *Manager) loadRunning() []runningRecord {
	path := m.runningPath()
	if path == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var recs []runningRecord
	if json.Unmarshal(b, &recs) != nil {
		return nil
	}
	return recs
}

func (m *Manager) saveRunningLocked() {
	path := m.runningPath()
	if path == "" {
		return
	}
	recs := []runningRecord{}
	for _, s := range m.sessions {
		switch {
		case s.interrupted != nil:
			recs = append(recs, *s.interrupted)
		case s.turn != nil:
			native := ""
			if s.live != nil {
				native = s.live.NativeID()
			}
			recs = append(recs, runningRecord{SessionID: s.id, ProjectID: s.projectID, Agent: s.agent, Workspace: s.workspace,
				Model: s.model, Mode: s.mode, NativeID: native, Prompt: s.turn.text, Title: s.title, StartedAt: s.turn.startedAt})
		}
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].SessionID < recs[j].SessionID })
	b, _ := json.MarshalIndent(recs, "", "  ")
	_ = writeFileAtomic(path, b)
}

func newID(prefix string) string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}

// NativeMode maps a Nexus permission mode to the driver's PermissionMode:
// Claude's --permission-mode, Codex's approval policy and sandbox, and an
// ACP agent's session mode.
func NativeMode(agent, mode string) string {
	switch NormalizeMode(mode) {
	case ModeEdits:
		return "acceptEdits"
	case ModeFull:
		return "bypassPermissions"
	}
	return ""
}
