// Steer emitter bridge: wires internal/steer Emitter to persistence +
// live fan-out (issue #42; wiring map from ADR-022 §"Wiring map" step 2).
//
// The steer package deliberately does NOT import this package (ownership:
// steer owns only internal/steer). This file is the server side of the
// Emitter seam: SteerEmitter implements steer.Emitter by appending the
// steering event to the event store and fanning the stored row out as a
// generic {"type":"event"} frame via the existing Hub.PublishEvent — no
// protocol change needed server-side (steering events ride the generic
// event frame, same precedent as #13's action envelopes).
//
// Delivery contract (mirrors the daemon interceptor Emit): best-effort and
// never panics. A nil receiver, nil Store, or nil Hub is a valid degraded
// sink (persist-only, fan-out-only, or silent). Store errors fall back to
// publishing the unenriched envelope so live watchers still see the signal
// even when persistence is down; a panicking Store/Hub is isolated via
// recover so a slow/broken sink can never break the steering transition it
// observes (same isolation the steer Controller already gives its Emitter).
package server

import (
	"context"
	"strings"

	"central-memory/internal/steer"
	"central-memory/internal/store"
)

// Compile-time seam check: SteerEmitter is usable as a steer Emitter.
var _ steer.Emitter = (*SteerEmitter)(nil)

// SteerEventStore is the narrow persistence seam SteerEmitter needs.
// *store.EventStore satisfies it method-for-method, so production wires it
// with no adapter; tests substitute a fake.
type SteerEventStore interface {
	AppendEvent(ctx context.Context, params store.AppendEventParams) (*store.Event, error)
}

// SteerEmitter persists steering events and fans them out live. ProjectID
// scopes every emission (steering is per-project); the session rides in the
// payload's session_id (the steer Controller always sets it).
type SteerEmitter struct {
	Store     SteerEventStore
	Hub       *Hub
	ProjectID string
}

// NewSteerEmitter builds a SteerEmitter over store + hub for one project.
// Either seam may be nil (persist-only, fan-out-only, or silent sink).
func NewSteerEmitter(eventStore SteerEventStore, hub *Hub, projectID string) *SteerEmitter {
	return &SteerEmitter{Store: eventStore, Hub: hub, ProjectID: strings.TrimSpace(projectID)}
}

// Emit persists eventType/payload and publishes the result. It never returns
// an error and never panics: failures are swallowed by design (see package
// doc). A copy of payload is stored so the caller's map is never mutated.
func (e *SteerEmitter) Emit(eventType string, payload map[string]any) {
	if e == nil {
		return
	}
	// Panic isolation mirrors the daemon interceptor Emit contract.
	defer func() { _ = recover() }()

	eventType = strings.TrimSpace(eventType)
	if eventType == "" || strings.TrimSpace(e.ProjectID) == "" {
		return
	}
	sessionID := ""
	if payload != nil {
		if s, ok := payload["session_id"].(string); ok {
			sessionID = strings.TrimSpace(s)
		}
	}
	stored := map[string]any{}
	for k, v := range payload {
		stored[k] = v
	}
	stored["event_type"] = eventType

	var event any = stored
	if e.Store != nil {
		if appended, err := e.Store.AppendEvent(context.Background(), store.AppendEventParams{
			ProjectID: e.ProjectID,
			SessionID: sessionID,
			EventType: eventType,
			Payload:   stored,
		}); err == nil && appended != nil {
			event = appended
		}
		// On append error event stays the envelope fallback (live
		// continuity beats strict consistency for steering signals).
	}
	if e.Hub != nil {
		_, _ = e.Hub.PublishEvent(e.ProjectID, sessionID, event)
	}
}
