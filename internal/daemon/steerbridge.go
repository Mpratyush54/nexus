// Steer runner bridge: the daemon side of the steering DaemonBridge seam
// (issue #42; master's internal/steering package).
//
// The steering package deliberately does NOT import this package. This file
// is the daemon side of the seam: SteerBridge implements
// steering.DaemonBridge by firing the run's in-process cancellation — the
// in-process equivalent of the SIGINT the daemon would send to an agent
// child process, observable by any runner selecting on the run context. A
// future daemon owner can add a real SIGINT/MCP-cancel push beside the
// cancel call without changing this file's API.
//
// Gate helper: GateBeforeToolCall is the one-line hook the tool-dispatch
// path calls before every tool call so a redirect always lands before new
// tool effects. daemon.go itself is NOT edited here; the exact hook line
// for the daemon owner is documented in ADR-042.
//
// All methods are nil-receiver safe: a nil *SteerBridge signals nothing
// (the InterruptManager already cancels the run context itself), and a nil
// manager makes the gate helper a pass-through.
package daemon

import (
	"context"
	"errors"
	"strings"
	"sync"

	"central-memory/internal/steering"
)

// Compile-time seam check: SteerBridge is usable as a steering DaemonBridge.
var _ steering.DaemonBridge = (*SteerBridge)(nil)

// SteerBridge signals the active runner out-of-band. The daemon registers
// each run's cancellation at dispatch start (TrackRun) and releases it at
// run end (UntrackRun); Signal fires the registered cancel. Safe for
// concurrent use: one mutex guards the registry.
type SteerBridge struct {
	mu      sync.Mutex
	cancels map[string]context.CancelFunc
}

// NewSteerBridge builds an empty SteerBridge.
func NewSteerBridge() *SteerBridge {
	return &SteerBridge{
		cancels: map[string]context.CancelFunc{},
	}
}

// TrackRun registers cancel as the out-of-band signal for runID. It is
// called when the daemon starts a run; a second TrackRun for the same run
// replaces the previous registration. Safe for concurrent use.
func (b *SteerBridge) TrackRun(runID string, cancel context.CancelFunc) {
	if b == nil {
		return
	}
	runID = strings.TrimSpace(runID)
	if runID == "" || cancel == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cancels[runID] = cancel
}

// UntrackRun releases runID's registration. Idempotent: unknown runs are a
// no-op, so dispatch cleanup paths can defer it unconditionally (mirrors
// InterruptManager.Unregister idempotence).
func (b *SteerBridge) UntrackRun(runID string) {
	if b == nil {
		return
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.cancels, runID)
}

// Signal fires runID's registered cancel (the in-process SIGINT). A nil
// bridge is a no-op success — interruption still propagates via the run
// context the InterruptManager cancels itself. Unknown runs fail
// explicitly so callers can distinguish "no such run" from "signalled".
// Cancellation is irreversible, so a duplicate interrupt still reports
// success once the run is known.
func (b *SteerBridge) Signal(runID string) error {
	if b == nil {
		return nil
	}
	runID = strings.TrimSpace(runID)
	b.mu.Lock()
	cancel, ok := b.cancels[runID]
	b.mu.Unlock()
	if !ok || cancel == nil {
		return errors.New("daemon: steer: no active run " + runID)
	}
	cancel()
	return nil
}

// GateBeforeToolCall is the daemon tool-dispatch hook: call it before every
// tool call. It delegates to the InterruptManager gate and unpacks the
// decision into plain values so dispatch code stays one line:
//
//	hold:   true while the run is not running — do not issue the tool call,
//	         wait for Resume.
//	prompt: set when a spectator redirect is pending — inject it with
//	         priority (e.g. as a system message) before the tool call.
//	         Delivery consumes it: the next gate sees ok=false.
//
// A nil manager is a pass-through (false, nil): local-only mode and tests
// run ungated.
func GateBeforeToolCall(mgr *steering.InterruptManager, runID string) (hold bool, prompt *steering.SteerPrompt) {
	if mgr == nil {
		return false, nil
	}
	if err := mgr.Gate(runID); err != nil {
		// Unknown runs run ungated (local-only mode registers nothing);
		// known-but-not-running runs hold for Resume.
		if errors.Is(err, steering.ErrNoRun) {
			return false, nil
		}
		return true, nil
	}
	p, ok := mgr.TakeNextPrompt(runID)
	if !ok {
		return false, nil
	}
	return false, &p
}
