// Steer emitter bridge: wires steering transitions to persistence +
// live fan-out (issue #42).
//
// The steering package deliberately does NOT import this package. This file
// is the server side: SteerEmitter appends the steering event to the event
// store and fans the stored row out as a generic event frame via the
// existing Hub.PublishEvent — no protocol change needed server-side
// (steering events ride the generic event frame, same precedent as the WS
// bridge in wsbridge.go).
//
// Delivery contract: best-effort and never panics. A nil receiver, empty
// project, nil Store, or nil Hub is a valid degraded sink. Store errors
// fall back to publishing the unenriched envelope so live watchers still
// see the signal even when persistence is down.
package server

import (
	"context"
	"strings"

	"central-memory/internal/store"
)

// SteerEmitter persists steering events and fans them out live. ProjectID
// scopes every emission (steering is per-project); the session rides in the
// payload's session_id (callers always set it).
type SteerEmitter struct {
	Store     store.Store
	Hub       *Hub
	ProjectID string
}

// NewSteerEmitter builds a SteerEmitter over store + hub for one project.
// Either seam may be nil (persist-only, fan-out-only, or silent sink).
func NewSteerEmitter(st store.Store, hub *Hub, projectID string) *SteerEmitter {
	return &SteerEmitter{Store: st, Hub: hub, ProjectID: strings.TrimSpace(projectID)}
}

// Emit persists eventType/payload and publishes the result. It never returns
// an error and never panics: failures are swallowed by design. A copy of
// payload is stored so the caller's map is never mutated.
func (e *SteerEmitter) Emit(eventType string, payload map[string]any) {
	if e == nil {
		return
	}
	eventType = strings.TrimSpace(eventType)
	if eventType == "" || e.ProjectID == "" {
		return
	}
	cp := make(map[string]any, len(payload)+1)
	for k, v := range payload {
		cp[k] = v
	}
	sessionID, _ := cp["session_id"].(string)

	if e.Store != nil {
		func() {
			defer func() { _ = recover() }()
			_ = appendSteerEvent(e.Store, e.ProjectID, sessionID, eventType, cp)
		}()
	}
	if e.Hub != nil {
		func() {
			defer func() { _ = recover() }()
			e.Hub.PublishEvent(e.ProjectID, sessionID, eventType, cp, "")
		}()
	}
}

// appendSteerEvent persists one steering event row.
func appendSteerEvent(st store.Store, projectID, sessionID, eventType string, payload map[string]any) error {
	return st.AppendEvent(context.Background(), &store.Event{
		ProjectID: projectID,
		SessionID: sessionID,
		EventType: eventType,
		Payload:   payload,
	})
}
