// Package steer implements live agent steering, interruption and
// intervention (issue #22, plan Phase 3 WS protocol §3.2 extension).
//
// A watcher (dashboard spectator) can Interrupt/Redirect a running agent:
// the agent pauses its pending tool call, ingests the redirect prompt, and
// continues corrected. The four steering events are:
//
//	AGENT_INTERRUPT_REQUESTED   watcher asks the active run to stop
//	AGENT_STEER_PROMPT          watcher injects a redirect prompt (priority)
//	AGENT_PAUSED                runner acknowledges the pause
//	AGENT_RESUMED               watcher releases the run to continue
//
// State machine (per session run):
//
//	Running --RequestInterrupt--> InterruptRequested --AcknowledgePaused--> Paused --Resume--> Running
//
// Steering prompts are single-pending with priority injection: EnqueueSteer
// stores at most one pending prompt and the runner drains it via
// BeforeToolCall before its next tool call, so the redirect always lands
// before new tool effects. Concurrent spectators cannot send conflicting
// signals: the second concurrent interrupt fails with ErrAlreadyInterrupted
// and the second concurrent steer fails with ErrSteerPending (single-winner
// lock under one mutex).
//
// OWNERSHIP: this package owns ONLY internal/steer plus docs. It deliberately
// does NOT import internal/server, internal/daemon, internal/mcp or
// internal/store. Integration rides on two narrow local seams:
//
//	Emitter       — event fan-out (wire to store.AppendEvent + ws Hub publish)
//	RunnerBridge  — active-runner signalling (daemon wires: SIGINT to the
//	                child process and/or a cancellation token over MCP)
//
// Cancellation propagates via context: StartRun returns a run context the
// runner selects on; RequestInterrupt cancels it (the in-process equivalent
// of SIGINT), so a runner blocked in ctx-aware work observes the interrupt
// without any daemon import. See docs/decisions/ADR-022-* for the wiring map.
package steer

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

// Steering event types (extension of the plan §3.2 WS protocol vocabulary,
// which names only event/presence/memory_update/episode_update).
//
// NOTE: these intentionally duplicate no store constant: the store §2.1
// registry (store.ValidEventTypes) is owned by another issue and cannot be
// edited here. The follow-up that registers these four types with the store
// is documented in ADR-022; until then the Emitter seam carries them.
const (
	EventAgentInterruptRequested = "AGENT_INTERRUPT_REQUESTED"
	EventAgentSteerPrompt        = "AGENT_STEER_PROMPT"
	EventAgentPaused             = "AGENT_PAUSED"
	EventAgentResumed            = "AGENT_RESUMED"
)

// MaxSteerPromptChars caps a redirect prompt so one spectator paste cannot
// flood the event stream (cf. harvester's 8KB turn cap).
const MaxSteerPromptChars = 8000

// RunState is one node's view of a session's agent run.
type RunState string

// Run states. There is no "Resumed" state: Resume transitions Paused back
// to Running and the AGENT_RESUMED event records that the transition
// happened.
const (
	StateRunning            RunState = "running"
	StateInterruptRequested RunState = "interrupt_requested"
	StatePaused             RunState = "paused"
)

// Sentinel errors. All Controller methods return these (possibly wrapped)
// so callers — and competing spectators — can distinguish "lost the race"
// from "no such run".
var (
	// ErrNoActiveRun means the session has no run (StartRun was never
	// called or EndRun already ran).
	ErrNoActiveRun = errors.New("steer: no active run for session")
	// ErrRunActive means StartRun was called for a session that already
	// has a live run.
	ErrRunActive = errors.New("steer: session already has an active run")
	// ErrAlreadyInterrupted means RequestInterrupt lost the single-winner
	// race: the run is already interrupting or paused.
	ErrAlreadyInterrupted = errors.New("steer: interrupt already requested")
	// ErrNotAwaitingPause means AcknowledgePaused was called while the run
	// was not in InterruptRequested (never interrupted, or already paused).
	ErrNotAwaitingPause = errors.New("steer: run is not awaiting pause acknowledgement")
	// ErrNotPaused means Resume was called while the run was not Paused.
	ErrNotPaused = errors.New("steer: run is not paused")
	// ErrSteerPending means EnqueueSteer lost the single-winner race: a
	// redirect is already queued and undelivered.
	ErrSteerPending = errors.New("steer: a steer prompt is already pending")
	// ErrEmptyPrompt means the redirect prompt was blank.
	ErrEmptyPrompt = errors.New("steer: prompt must not be blank")
)

// Emitter fans steering events out. Production wires it to
// store.AppendEvent plus the ws Hub publish path (see ADR-022); a nil
// Emitter is a valid silent sink (tests, local-only mode).
type Emitter interface {
	Emit(eventType string, payload map[string]any)
}

// EmitFunc adapts a plain func to an Emitter.
type EmitFunc func(eventType string, payload map[string]any)

// Emit calls f unless f is nil.
func (f EmitFunc) Emit(eventType string, payload map[string]any) {
	if f != nil {
		f(eventType, payload)
	}
}

// RunnerBridge signals the active runner out-of-band. The daemon implements
// this by sending SIGINT to the agent child process and/or pushing a
// cancellation token over MCP to the active runner (see ADR-022). A nil
// bridge means in-process context cancellation only — sufficient for tests
// and for runners that select on the run context.
type RunnerBridge interface {
	SignalInterrupt(ctx context.Context, sessionID, runID string) error
}

// BridgeFunc adapts a plain func to a RunnerBridge.
type BridgeFunc func(ctx context.Context, sessionID, runID string) error

// SignalInterrupt calls f unless f is nil.
func (f BridgeFunc) SignalInterrupt(ctx context.Context, sessionID, runID string) error {
	if f == nil {
		return nil
	}
	return f(ctx, sessionID, runID)
}

// SteerPrompt is one queued spectator redirect.
type SteerPrompt struct {
	Prompt string
	From   string
	At     time.Time
}

// GateResult is the runner's pre-tool-call decision. The runner MUST call
// BeforeToolCall before every tool call:
//
//   - Paused == true: do not issue the tool call; wait for Resume.
//   - Steer != nil: inject Steer.Prompt with priority (e.g. as a system
//     message) before the tool call. The prompt is consumed on handoff, so
//     a second gate check will not redeliver it.
type GateResult struct {
	Paused bool
	Steer  *SteerPrompt
}

// run is the per-session mutable state. All fields are guarded by the
// Controller mutex.
type run struct {
	runID   string
	state   RunState
	cancel  context.CancelFunc
	pending *SteerPrompt
}

// Controller coordinates steering for any number of session runs.
// The zero value is not usable; build with NewController. Safe for
// concurrent use: one mutex serializes every transition, which is exactly
// what makes the losers of interrupt/steer races observable as errors.
type Controller struct {
	mu     sync.Mutex
	runs   map[string]*run
	emit   Emitter
	bridge RunnerBridge
}

// NewController builds a Controller. Either seam may be nil (silent
// emitter, context-only interruption).
func NewController(emit Emitter, bridge RunnerBridge) *Controller {
	return &Controller{runs: map[string]*run{}, emit: emit, bridge: bridge}
}

// StartRun begins tracking sessionID under runID and returns a run context
// derived from ctx. The runner must select on the returned context: it is
// cancelled by RequestInterrupt (and by parent cancellation), which is the
// in-process propagation of the daemon's SIGINT/MCP cancel.
func (c *Controller) StartRun(ctx context.Context, sessionID, runID string) (context.Context, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, errors.New("steer: session id is required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.runs[sessionID]; ok {
		return nil, ErrRunActive
	}
	runCtx, cancel := context.WithCancel(ctx)
	c.runs[sessionID] = &run{
		runID:  strings.TrimSpace(runID),
		state:  StateRunning,
		cancel: cancel,
	}
	return runCtx, nil
}

// RequestInterrupt moves a Running run to InterruptRequested, cancels its
// run context (SIGINT equivalent), emits AGENT_INTERRUPT_REQUESTED, and
// signals the RunnerBridge. Only the first caller wins: concurrent or
// repeated interrupts fail with ErrAlreadyInterrupted.
//
// Bridge errors are returned but do NOT roll the transition back —
// cancellation is irreversible (a context cannot be uncancelled), so the
// runner will still observe the interrupt via its run context.
func (c *Controller) RequestInterrupt(sessionID, requestedBy, reason string) error {
	sessionID = strings.TrimSpace(sessionID)
	c.mu.Lock()
	r, ok := c.runs[sessionID]
	if !ok {
		c.mu.Unlock()
		return ErrNoActiveRun
	}
	if r.state != StateRunning {
		c.mu.Unlock()
		return ErrAlreadyInterrupted
	}
	r.state = StateInterruptRequested
	cancel, runID := r.cancel, r.runID
	c.mu.Unlock()

	cancel()
	c.emitEvent(EventAgentInterruptRequested, map[string]any{
		"session_id":   sessionID,
		"run_id":       runID,
		"requested_by": strings.TrimSpace(requestedBy),
		"reason":       strings.TrimSpace(reason),
	})
	if c.bridge == nil {
		return nil
	}
	if err := c.bridge.SignalInterrupt(context.Background(), sessionID, runID); err != nil {
		return errors.New("steer: runner bridge signal failed: " + err.Error())
	}
	return nil
}

// AcknowledgePaused records that the runner honored the interrupt and is
// now holding its pending tool call. Only valid from InterruptRequested;
// emits AGENT_PAUSED.
func (c *Controller) AcknowledgePaused(sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	c.mu.Lock()
	r, ok := c.runs[sessionID]
	if !ok {
		c.mu.Unlock()
		return ErrNoActiveRun
	}
	if r.state != StateInterruptRequested {
		c.mu.Unlock()
		return ErrNotAwaitingPause
	}
	r.state = StatePaused
	runID := r.runID
	c.mu.Unlock()

	c.emitEvent(EventAgentPaused, map[string]any{
		"session_id": sessionID,
		"run_id":     runID,
	})
	return nil
}

// EnqueueSteer queues a spectator redirect for priority injection before
// the next tool call and emits AGENT_STEER_PROMPT. At most one prompt may
// be pending: concurrent spectators race and all but the winner receive
// ErrSteerPending, so conflicting redirects can never interleave.
//
// Steering is accepted while Running, InterruptRequested, or Paused — the
// typical flow interrupts first, but a steer issued against a running agent
// is equally held at the next BeforeToolCall gate.
func (c *Controller) EnqueueSteer(sessionID, prompt, from string) error {
	sessionID = strings.TrimSpace(sessionID)
	if strings.TrimSpace(prompt) == "" {
		return ErrEmptyPrompt
	}
	c.mu.Lock()
	r, ok := c.runs[sessionID]
	if !ok {
		c.mu.Unlock()
		return ErrNoActiveRun
	}
	if r.pending != nil {
		c.mu.Unlock()
		return ErrSteerPending
	}
	capped, truncated := capPrompt(prompt)
	r.pending = &SteerPrompt{Prompt: capped, From: strings.TrimSpace(from), At: time.Now().UTC()}
	runID := r.runID
	c.mu.Unlock()

	payload := map[string]any{
		"session_id": sessionID,
		"run_id":     runID,
		"prompt":     capped,
		"from":       strings.TrimSpace(from),
	}
	if truncated {
		payload["truncated"] = true
	}
	c.emitEvent(EventAgentSteerPrompt, payload)
	return nil
}

// BeforeToolCall is the runner's gate: call it before every tool call. A
// consumed pending steer is handed over for priority injection and
// Paused reports whether the run is currently holding tool calls
// (InterruptRequested or Paused both hold).
func (c *Controller) BeforeToolCall(sessionID string) GateResult {
	sessionID = strings.TrimSpace(sessionID)
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.runs[sessionID]
	if !ok {
		return GateResult{}
	}
	out := GateResult{Paused: r.state != StateRunning}
	if r.pending != nil {
		out.Steer = r.pending
		r.pending = nil
	}
	return out
}

// Pending reports whether a steer is queued without consuming it (for
// dashboards and tests; the runner itself uses BeforeToolCall).
func (c *Controller) Pending(sessionID string) bool {
	sessionID = strings.TrimSpace(sessionID)
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.runs[sessionID]
	return ok && r.pending != nil
}

// Resume releases a Paused run back to Running and emits AGENT_RESUMED. A
// steer drained earlier via BeforeToolCall has already been injected; one
// still pending stays queued and is delivered at the next gate — resuming
// never drops a redirect.
func (c *Controller) Resume(sessionID, resumedBy string) error {
	sessionID = strings.TrimSpace(sessionID)
	c.mu.Lock()
	r, ok := c.runs[sessionID]
	if !ok {
		c.mu.Unlock()
		return ErrNoActiveRun
	}
	if r.state != StatePaused {
		c.mu.Unlock()
		return ErrNotPaused
	}
	r.state = StateRunning
	runID := r.runID
	c.mu.Unlock()

	c.emitEvent(EventAgentResumed, map[string]any{
		"session_id": sessionID,
		"run_id":     runID,
		"resumed_by": strings.TrimSpace(resumedBy),
	})
	return nil
}

// EndRun stops tracking sessionID and cancels its run context. Idempotent:
// ending an unknown session is a no-op returning nil, so daemon cleanup
// paths can defer it unconditionally.
func (c *Controller) EndRun(sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	c.mu.Lock()
	r, ok := c.runs[sessionID]
	if ok {
		delete(c.runs, sessionID)
	}
	c.mu.Unlock()
	if ok && r.cancel != nil {
		r.cancel()
	}
}

// State reports the run state for sessionID (false when no run is active).
func (c *Controller) State(sessionID string) (RunState, bool) {
	sessionID = strings.TrimSpace(sessionID)
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.runs[sessionID]
	if !ok {
		return "", false
	}
	return r.state, true
}

// emitEvent delivers one event; nil-emitter safe. Emitters run outside the
// Controller lock (callers unlock before invoking this) so a slow sink can
// never stall a steering race.
func (c *Controller) emitEvent(eventType string, payload map[string]any) {
	if c.emit == nil {
		return
	}
	payload["event_type"] = eventType
	payload["at"] = time.Now().UTC().Format(time.RFC3339)
	// Emitter isolation mirrors the daemon interceptor contract: a
	// panicking sink must never break the steering transition it observes.
	defer func() { _ = recover() }()
	c.emit.Emit(eventType, payload)
}

// capPrompt bounds a redirect prompt at MaxSteerPromptChars, reporting
// whether truncation happened.
func capPrompt(s string) (string, bool) {
	if len(s) <= MaxSteerPromptChars {
		return s, false
	}
	return s[:MaxSteerPromptChars] + "\n... [truncated]", true
}
