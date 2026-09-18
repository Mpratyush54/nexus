// Package steering — live agent steering, interruption & intervention
// protocol (nexus issue #22).
//
// Transport mapping (must match internal/server/ws.go): steering rides on
// the existing WS envelope, it does NOT add new envelope types. Clients send
// {type:"action", event_type:<steering event>, payload:{...}} and the daemon
// fans out {type:"event", event_type:<steering event>, payload:{...}} via
// Hub.PublishEvent / Hub.HandleClientMessage. The four event types below are
// the only new vocabulary.
//
// Runtime model (stdlib-only):
//   - InterruptManager tracks one Run per agent run (keyed by runID, usually
//     the session ID). Each run owns a cancellable context + a Done channel:
//     RequestInterrupt closes/cancels it so a pending tool call doing
//     `select { case <-mgr.Done(runID): ... }` or `<-ctx.Done()` aborts
//     promptly instead of running to completion.
//   - Priority injection: Steer() pushes a correction prompt onto a per-run
//     FIFO. The agent MUST call TakeNextPrompt (or Gate) before every tool
//     call, so the correction is ingested before the next tool executes.
//   - Single-steerer lock: one global mutex serializes all transitions, and
//     each run records an owner (the steerer who interrupted). Any
//     RequestInterrupt/Steer/Resume from a different user while the run is
//     held returns ErrSteerConflict. Same-owner prompts queue; other steerers
//     are rejected, never interleaved.
//   - DaemonBridge is the seam to the real daemon: production code signals
//     the active agent runner process (SIGINT) or sends a cancellation token
//     over MCP; here it is an interface stub so unit tests run without
//     processes or sockets.
package steering

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// --- wire protocol ----------------------------------------------------------

// Envelope type constants mirror internal/server/ws.go. Steering messages
// are carried as EventType inside these envelopes.
const (
	// WSMsgAction is the client → server envelope carrying a steering event.
	WSMsgAction = "action"
	// WSMsgEvent is the server → client fan-out envelope.
	WSMsgEvent = "event"
)

// Steering event types (issue #22 scope).
const (
	EventInterruptRequested = "AGENT_INTERRUPT_REQUESTED"
	EventSteerPrompt        = "AGENT_STEER_PROMPT"
	EventPaused             = "AGENT_PAUSED"
	EventResumed            = "AGENT_RESUMED"
)

// Payload keys used in the WS payload map.
const (
	PayloadRunID    = "run_id"
	PayloadSteerer  = "steerer_id"
	PayloadReason   = "reason"
	PayloadPrompt   = "prompt"
	PayloadQueueLen = "queue_len"
)

// IsSteeringEvent reports whether t is one of the four steering event types.
func IsSteeringEvent(t string) bool {
	switch t {
	case EventInterruptRequested, EventSteerPrompt, EventPaused, EventResumed:
		return true
	}
	return false
}

// Event is a steering transition ready to fan out through the WS hub via
// Hub.PublishEvent(projectID, sessionID, ev.EventType, ev.Payload, userID).
type Event struct {
	EventType string
	Payload   map[string]any
}

// --- daemon bridge ----------------------------------------------------------

// DaemonBridge signals the active agent runner when an interrupt is
// requested. Production implementations either send SIGINT to the runner
// process or deliver a cancellation token over MCP; both collapse to this
// single method so the manager stays transport-agnostic.
type DaemonBridge interface {
	// Signal asks the runner for runID to stop what it is doing and pause
	// before its next tool call.
	Signal(runID string) error
}

// NoopBridge is a DaemonBridge that does nothing (single-agent local runs,
// or callers that only need the state machine).
type NoopBridge struct{}

func (NoopBridge) Signal(string) error { return nil }

// FuncBridge adapts a function to a DaemonBridge (handy for tests/fakes).
type FuncBridge struct{ Fn func(runID string) error }

func (b FuncBridge) Signal(runID string) error {
	if b.Fn == nil {
		return nil
	}
	return b.Fn(runID)
}

// RecordingBridge is a test fake that records every Signal call.
type RecordingBridge struct {
	mu    sync.Mutex
	Calls []string
	Err   error
}

func (b *RecordingBridge) Signal(runID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.Calls = append(b.Calls, runID)
	return b.Err
}

// Count reports how many times Signal was called.
func (b *RecordingBridge) Count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.Calls)
}

// --- state machine ----------------------------------------------------------

// Run states.
const (
	StateRunning        = "running"
	StatePauseRequested = "pause_requested" // interrupt sent, daemon not yet acked
	StatePaused         = "paused"          // daemon acked; steering prompts accepted
	StateFinished       = "finished"        // run unregistered/completed
)

// Boundedness contracts (nexus issue #113): the manager is a long-lived
// process singleton, so every per-run and global growth vector is capped.
const (
	// MaxRunHistory caps retained outbound events per run (oldest dropped).
	MaxRunHistory = 128
	// MaxPromptQueue caps pending (un-ingested) prompts per run.
	MaxPromptQueue = 16
	// MaxRuns caps tracked runs; Register evicts finished runs first.
	MaxRuns = 1024
)

var (
	// ErrNoRun is returned when the runID is unknown.
	ErrNoRun = errors.New("steering: unknown run")
	// ErrSteerConflict is returned when a second steerer tries to grab a run
	// already held by someone else.
	ErrSteerConflict = errors.New("steering: run already held by another steerer")
	// ErrInvalidState is returned when the transition is illegal for the
	// current state (e.g. Steer while running, Resume while running).
	ErrInvalidState = errors.New("steering: invalid state for transition")
	// ErrEmptyPrompt is returned when a steer prompt is blank.
	ErrEmptyPrompt = errors.New("steering: prompt must not be empty")
	// ErrQueueFull is returned when a run's pending prompt queue is at
	// MaxPromptQueue: the steerer must wait for ingestion, not pile on.
	ErrQueueFull = errors.New("steering: prompt queue full")
	// ErrTooManyRuns is returned when the manager is at MaxRuns with no
	// finished run to evict.
	ErrTooManyRuns = errors.New("steering: too many tracked runs")
	// ErrPaused is returned by Gate while the run is paused: the agent must
	// not execute tool calls until Resume.
	ErrPaused = errors.New("steering: agent paused")
)

// SteerPrompt is one injected correction awaiting ingestion.
type SteerPrompt struct {
	SteererID string
	Prompt    string
}

// run is the per-agent-run state. All fields are guarded by the manager mu;
// done is closed on interrupt and replaced on resume, so ASAP tool loops can
// select on a stable channel without racing the mutex. queue is a FIFO with
// a head offset: pops advance the head (O(1)) and compact when the dead
// prefix grows, instead of reallocating the backing array per pop.
type run struct {
	id      string
	state   string
	owner   string // steerer holding the single-steerer lock (" " when free)
	queue   []SteerPrompt
	qhead   int
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}
	bridge  DaemonBridge
	history []Event // outbound events in order (fan out via WS hub)
}

// queueDepth is the pending (un-ingested) prompt count. Caller holds m.mu.
func (r *run) queueDepth() int {
	if r.qhead >= len(r.queue) {
		return 0
	}
	return len(r.queue) - r.qhead
}

// pushPrompt appends one prompt. Caller holds m.mu and has checked the cap.
func (r *run) pushPrompt(p SteerPrompt) {
	r.queue = append(r.queue, p)
}

// popPrompt removes the oldest prompt (FIFO, O(1)). Caller holds m.mu.
func (r *run) popPrompt() (SteerPrompt, bool) {
	if r.qhead >= len(r.queue) {
		r.queue = nil
		r.qhead = 0
		return SteerPrompt{}, false
	}
	next := r.queue[r.qhead]
	r.qhead++
	if r.qhead >= len(r.queue) {
		r.queue = nil
		r.qhead = 0
	} else if r.qhead > 64 && r.qhead*2 > len(r.queue) {
		r.queue = append([]SteerPrompt(nil), r.queue[r.qhead:]...)
		r.qhead = 0
	}
	return next, true
}

func (r *run) emit(t string, payload map[string]any) {
	r.history = append(r.history, Event{EventType: t, Payload: payload})
	if len(r.history) > MaxRunHistory {
		r.history = append([]Event(nil), r.history[len(r.history)-MaxRunHistory:]...)
	}
}

// InterruptManager coordinates steering across runs. The zero value is not
// usable; construct with NewInterruptManager. All methods are safe for
// concurrent use; a single mutex serializes transitions (the single-steerer
// lock), while per-run ownership rejects conflicting steerers.
type InterruptManager struct {
	mu   sync.Mutex
	runs map[string]*run
	// finishedOrder is the FIFO of runIDs in Unregister order. Eviction
	// pops from the front so the least-recently-finished run goes first
	// (LRU); entries for re-registered or already-evicted runs are
	// skipped lazily at eviction time.
	finishedOrder []string
}

// NewInterruptManager returns an empty manager.
func NewInterruptManager() *InterruptManager {
	return &InterruptManager{runs: make(map[string]*run)}
}

// Register starts tracking runID. Re-registering a live run is an error;
// re-registering after Unregister re-arms fresh state. At MaxRuns, finished
// runs are evicted first; a full live set fails with ErrTooManyRuns.
func (m *InterruptManager) Register(runID string, bridge DaemonBridge) error {
	if strings.TrimSpace(runID) == "" {
		return errors.New("steering: run_id is required")
	}
	if bridge == nil {
		bridge = NoopBridge{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.runs == nil {
		m.runs = make(map[string]*run)
	}
	if r, ok := m.runs[runID]; ok && r.state != StateFinished {
		return fmt.Errorf("steering: run %q already registered", runID)
	} else if !ok && len(m.runs) >= MaxRuns {
		// Evict exactly one finished run, least-recently-finished first,
		// so post-mortem reads for the rest survive within the cap.
		for len(m.finishedOrder) > 0 {
			oldest := m.finishedOrder[0]
			m.finishedOrder = m.finishedOrder[1:]
			if r, ok := m.runs[oldest]; ok && r.state == StateFinished {
				delete(m.runs, oldest)
				break
			}
		}
		if len(m.runs) >= MaxRuns {
			return ErrTooManyRuns
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.runs[runID] = &run{id: runID, state: StateRunning, ctx: ctx, cancel: cancel,
		done: make(chan struct{}), bridge: bridge}
	return nil
}

// Unregister marks runID finished, releases the steerer lock, cancels the
// context, and drops queued prompts. Unknown runs return ErrNoRun.
func (m *InterruptManager) Unregister(runID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.runs[runID]
	if !ok {
		return ErrNoRun
	}
	r.cancel()
	r.state = StateFinished
	r.owner = ""
	r.queue = nil
	r.qhead = 0
	m.finishedOrder = append(m.finishedOrder, runID)
	// Bound the order queue itself: re-registers leave stale entries that
	// eviction skips lazily, so compact once it doubles the cap.
	if len(m.finishedOrder) > 2*MaxRuns {
		kept := m.finishedOrder[:0]
		for _, id := range m.finishedOrder {
			if r, ok := m.runs[id]; ok && r.state == StateFinished {
				kept = append(kept, id)
			}
		}
		// Clear the vacated tail so dropped IDs are not retained.
		for i := len(kept); i < len(m.finishedOrder); i++ {
			m.finishedOrder[i] = ""
		}
		m.finishedOrder = kept
	}
	// Avoid closing done twice if RequestInterrupt already closed it.
	select {
	case <-r.done:
	default:
		close(r.done)
	}
	return nil
}

// RequestInterrupt moves a running run to pause_requested, fires the
// DaemonBridge signal (SIGINT / MCP cancel token in production), and cancels
// the run context + closes Done so a pending tool call aborts. It records
// AGENT_INTERRUPT_REQUESTED. Only one steerer may hold a run: a second
// steerer gets ErrSteerConflict; interrupting a non-running run gets
// ErrInvalidState.
//
// The bridge Signal runs WITHOUT holding the manager lock (issue #113): a
// slow or re-entrant bridge must never wedge the subsystem. The run is
// re-validated under the lock after the signal (same pointer, still
// running) so an interleaved Unregister/re-Register cannot cause a
// double-close or a mutation on a recycled run.
func (m *InterruptManager) RequestInterrupt(runID, steererID, reason string) error {
	if strings.TrimSpace(steererID) == "" {
		return errors.New("steering: steerer_id is required")
	}
	m.mu.Lock()
	r, ok := m.runs[runID]
	if !ok {
		m.mu.Unlock()
		return ErrNoRun
	}
	if r.state != StateRunning {
		conflict := r.owner != "" && r.owner != steererID
		m.mu.Unlock()
		if conflict {
			return ErrSteerConflict
		}
		return ErrInvalidState
	}
	bridge := r.bridge
	m.mu.Unlock()

	if err := bridge.Signal(runID); err != nil {
		return fmt.Errorf("steering: daemon signal: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.runs[runID]
	if !ok || cur != r {
		return ErrNoRun // recycled while signaling: never touch the new run
	}
	if r.state != StateRunning {
		if r.owner != "" && r.owner != steererID {
			return ErrSteerConflict
		}
		return ErrInvalidState
	}
	r.state = StatePauseRequested
	r.owner = steererID
	r.cancel() // abort in-flight tool call observing ctx
	close(r.done)
	r.emit(EventInterruptRequested, map[string]any{
		PayloadRunID: runID, PayloadSteerer: steererID, PayloadReason: reason,
	})
	return nil
}

// AcknowledgePaused confirms the daemon actually paused (it stopped before
// its next tool call). Moves pause_requested → paused and records
// AGENT_PAUSED. Called by the daemon side, not by spectators.
func (m *InterruptManager) AcknowledgePaused(runID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.runs[runID]
	if !ok {
		return ErrNoRun
	}
	if r.state != StatePauseRequested {
		return ErrInvalidState
	}
	r.state = StatePaused
	r.emit(EventPaused, map[string]any{PayloadRunID: runID, PayloadSteerer: r.owner})
	return nil
}

// Steer injects a high-priority correction prompt. Valid only while paused
// (or pause_requested, for zero-latency steering that lands before the ack),
// and only from the lock owner — anyone else gets ErrSteerConflict. Prompts
// queue FIFO (capped at MaxPromptQueue: overflow yields ErrQueueFull) and
// are consumed via TakeNextPrompt before the next tool call.
func (m *InterruptManager) Steer(runID, steererID, prompt string) error {
	if strings.TrimSpace(prompt) == "" {
		return ErrEmptyPrompt
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.runs[runID]
	if !ok {
		return ErrNoRun
	}
	if r.state != StatePaused && r.state != StatePauseRequested {
		return ErrInvalidState
	}
	if r.owner != steererID {
		return ErrSteerConflict
	}
	if r.queueDepth() >= MaxPromptQueue {
		return ErrQueueFull
	}
	r.pushPrompt(SteerPrompt{SteererID: steererID, Prompt: prompt})
	r.emit(EventSteerPrompt, map[string]any{
		PayloadRunID: runID, PayloadSteerer: steererID,
		PayloadPrompt: prompt, PayloadQueueLen: r.queueDepth(),
	})
	return nil
}

// Resume releases the single-steerer lock, re-arms the cancel context + Done
// channel, moves the run back to running, and records AGENT_RESUMED. Only
// the owner may resume; queued (already-ingested or still-pending) prompts
// stay queued — ingestion happens via TakeNextPrompt, resume only unblocks
// tool calls.
func (m *InterruptManager) Resume(runID, steererID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.runs[runID]
	if !ok {
		return ErrNoRun
	}
	if r.state != StatePaused && r.state != StatePauseRequested {
		return ErrInvalidState
	}
	if r.owner != steererID {
		return ErrSteerConflict
	}
	r.state = StateRunning
	r.owner = ""
	r.ctx, r.cancel = context.WithCancel(context.Background())
	r.done = make(chan struct{})
	r.emit(EventResumed, map[string]any{PayloadRunID: runID, PayloadSteerer: steererID})
	return nil
}

// Gate is the agent's pre-tool-call hook: it returns ErrPaused while the run
// is pause_requested/paused (do not execute tools), nil once running. It
// does NOT consume prompts — call TakeNextPrompt first so a correction is
// ingested before the tool call it should redirect.
func (m *InterruptManager) Gate(runID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.runs[runID]
	if !ok {
		return ErrNoRun
	}
	if r.state == StatePaused || r.state == StatePauseRequested {
		return ErrPaused
	}
	if r.state != StateRunning {
		return ErrInvalidState
	}
	return nil
}

// TakeNextPrompt pops the oldest queued steering prompt (FIFO, O(1) via the
// head offset). The agent must call this before every tool call: a returned
// prompt (ok==true) is prepended to the model context as a high-priority
// system instruction, ahead of whatever the next tool call would have been.
func (m *InterruptManager) TakeNextPrompt(runID string) (SteerPrompt, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.runs[runID]
	if !ok {
		return SteerPrompt{}, false
	}
	return r.popPrompt()
}

// Done returns the run's cancel channel: closed when RequestInterrupt fires,
// replaced (open) on Resume. Pending tool calls should select on it to abort
// promptly. Returns nil for unknown runs.
func (m *InterruptManager) Done(runID string) <-chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.runs[runID]; ok {
		return r.done
	}
	return nil
}

// Context returns the run's cancellable context: cancelled on
// RequestInterrupt, re-armed on Resume. Returns nil for unknown runs.
func (m *InterruptManager) Context(runID string) context.Context {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.runs[runID]; ok {
		return r.ctx
	}
	return nil
}

// State reports the run's current state, or "" for unknown runs.
func (m *InterruptManager) State(runID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.runs[runID]; ok {
		return r.state
	}
	return ""
}

// QueueDepth reports pending (un-ingested) steering prompts for runID.
func (m *InterruptManager) QueueDepth(runID string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.runs[runID]; ok {
		return r.queueDepth()
	}
	return 0
}

// Events returns the ordered outbound steering events for runID (fan these
// out via Hub.PublishEvent with matching EventType). Unknown runs yield nil.
func (m *InterruptManager) Events(runID string) []Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.runs[runID]; ok {
		return append([]Event(nil), r.history...)
	}
	return nil
}
