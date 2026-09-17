// Steer runner bridge: the daemon side of the steer RunnerBridge seam
// (issue #42; wiring map from ADR-022 §"Wiring map" step 3).
//
// The steer package deliberately does NOT import this package (ownership:
// steer owns only internal/steer). This file is the daemon side of the
// RunnerBridge seam: SteerBridge implements steer.RunnerBridge by firing
// the run's in-process cancellation — the in-process equivalent of the
// SIGINT the daemon would send to an agent child process, observable by
// any runner selecting on the run context. A future daemon owner can add a
// real SIGINT/MCP-cancel push beside the cancel call without changing this
// file's API.
//
// Gate helper: GateBeforeToolCall is the one-line hook the tool-dispatch
// path calls before every tool call so a redirect always lands before new
// tool effects. daemon.go itself is NOT edited here (ownership); the exact
// hook line for the daemon owner is documented in ADR-042.
//
// All methods are nil-receiver safe: a nil *SteerBridge means context-only
// interruption (the steer Controller already cancels the run context
// itself), and a nil controller makes the gate helper a pass-through.
package daemon

import (
	"context"
	"errors"
	"strings"
	"sync"

	"central-memory/internal/steer"
)

// Compile-time seam check: SteerBridge is usable as a steer RunnerBridge.
var _ steer.RunnerBridge = (*SteerBridge)(nil)

// SteerBridge signals the active runner out-of-band. The daemon registers
// each run's cancellation at dispatch start (TrackRun) and releases it at
// run end (UntrackRun); SignalInterrupt fires the registered cancel. Safe
// for concurrent use: one mutex guards the registry.
type SteerBridge struct {
	mu      sync.Mutex
	cancels map[string]context.CancelFunc
	runIDs  map[string]string
}

// NewSteerBridge builds an empty SteerBridge.
func NewSteerBridge() *SteerBridge {
	return &SteerBridge{
		cancels: map[string]context.CancelFunc{},
		runIDs:  map[string]string{},
	}
}

// TrackRun registers cancel as the out-of-band signal for sessionID's run.
// It is called when the daemon starts a run; a second TrackRun for the same
// session replaces the previous registration (runs are sequential per
// session — the steer Controller rejects overlapping runs with ErrRunActive).
func (b *SteerBridge) TrackRun(sessionID, runID string, cancel context.CancelFunc) {
	if b == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || cancel == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cancels[sessionID] = cancel
	b.runIDs[sessionID] = strings.TrimSpace(runID)
}

// UntrackRun releases sessionID's registration. Idempotent: unknown
// sessions are a no-op, so dispatch cleanup paths can defer it
// unconditionally (mirrors steer.Controller.EndRun idempotence).
func (b *SteerBridge) UntrackRun(sessionID string) {
	if b == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.cancels, sessionID)
	delete(b.runIDs, sessionID)
}

// SignalInterrupt fires sessionID's registered cancel (the in-process
// SIGINT). A nil bridge is a no-op success — interruption still propagates
// via the run context the steer Controller cancels itself. Unknown sessions
// and stale run IDs fail explicitly so callers can distinguish "no such
// run" from "signalled".
func (b *SteerBridge) SignalInterrupt(_ context.Context, sessionID, runID string) error {
	if b == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	b.mu.Lock()
	cancel, ok := b.cancels[sessionID]
	storedRunID := b.runIDs[sessionID]
	b.mu.Unlock()
	if !ok || cancel == nil {
		return errors.New("daemon: steer: no active run for session " + sessionID)
	}
	if want, got := strings.TrimSpace(storedRunID), strings.TrimSpace(runID); want != "" && got != "" && want != got {
		return errors.New("daemon: steer: run id mismatch for session " + sessionID)
	}
	// Cancellation is irreversible (a context cannot be uncancelled), so a
	// duplicate interrupt still reports success once the run is known — the
	// runner observes exactly one interrupt via its run context.
	cancel()
	return nil
}

// GateBeforeToolCall is the daemon tool-dispatch hook: call it before every
// tool call. It delegates to the steer Controller's BeforeToolCall gate and
// unpacks the decision into plain values so dispatch code stays one line:
//
//	hold:   true while the run is not Running — do not issue the tool call,
//	         wait for Resume (or AcknowledgePaused first when interrupting).
//	prompt: non-nil when a spectator redirect is pending — inject
//	         prompt.Prompt with priority (e.g. as a system message) before
//	         the tool call. Delivery consumes it: the next gate sees nil.
//
// A nil controller is a pass-through (false, nil): local-only mode and
// tests run ungated. See ADR-042 for the exact hook placement.
func GateBeforeToolCall(ctrl *steer.Controller, sessionID string) (hold bool, prompt *steer.SteerPrompt) {
	if ctrl == nil {
		return false, nil
	}
	gate := ctrl.BeforeToolCall(sessionID)
	return gate.Paused, gate.Steer
}
