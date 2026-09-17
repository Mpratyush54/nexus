package server

// wsbridge.go — store → WebSocket event bridge (issue #40).
//
// Master owns ws.go (Hub broadcast) and the store package owns persistence
// (PostgresStore.Subscribe delivers full *store.Event rows over LISTEN).
// This file is purely additive: it streams committed events into the hub
// with backoff resubscription. No master file is touched.
//
// Design (ported from the #40 branch onto master's API):
//   - subscribe delivers full events (no hydration round-trip: master's
//     Subscribe yields *store.Event, not bare notifications).
//   - publishBridgedEvent fans one event out through the Publish* method
//     matching its type. Memory/episode lifecycle rows become typed update
//     frames; everything else becomes a generic event frame. A nil hub or
//     empty project id is an error; zero receivers (nobody subscribed) is
//     not — the Hub methods are void and never fail.
//   - Event-type vocabulary is local (master uses raw strings, e.g.
//     "MEMORY_PROPOSED" in memstore_test.go): memory_* → memory_update
//     actions (proposed|confirmed|rejected), episode_* → episode_update
//     actions (opened|updated|resolved).
//   - bridgeLoop holds one subscription with exponential backoff
//     (base → max, reset on first delivery); BridgeEvents is the
//     production entrypoint — run it as a goroutine next to Serve:
//
//	go func() {
//		if err := server.BridgeEvents(ctx, srv.SubscribeFunc(), srv.Hub()); err != nil {
//			log.Printf("ws bridge: %v", err)
//		}
//	}()
//
//	where SubscribeFunc wraps PostgresStore.Subscribe for one project (or
//	all projects with "").

import (
	"context"
	"errors"
	"log"
	"time"

	"central-memory/internal/store"
)

// Reconnect backoff bounds for the subscribe loop. Base doubles per failed
// or empty subscription; a subscription that delivers ≥1 event resets to
// base (proof the path is healthy).
const (
	bridgeBackoffBase = 1 * time.Second
	bridgeBackoffMax  = 30 * time.Second
)

// Bridge event-type vocabulary (master stores raw strings).
const (
	bridgeMemoryProposed  = "MEMORY_PROPOSED"
	bridgeMemoryConfirmed = "MEMORY_CONFIRMED"
	bridgeMemoryRejected  = "MEMORY_REJECTED"
	bridgeEpisodeOpened   = "EPISODE_OPENED"
	bridgeEpisodeUpdated  = "EPISODE_UPDATED"
	bridgeEpisodeResolved = "EPISODE_RESOLVED"
)

// eventPublisher is the Hub surface the bridge needs. *Hub satisfies it;
// tests substitute a recording fake, so mapping is unit-testable with no DB
// and no sockets.
type eventPublisher interface {
	PublishEvent(projectID, sessionID, eventType string, payload map[string]any, userID string)
	PublishMemoryUpdate(projectID, sessionID string, item any, action string)
	PublishEpisodeUpdate(projectID, sessionID string, episode any, action string)
}

// Compile-time proof the real hub satisfies the seam.
var _ eventPublisher = (*Hub)(nil)

// memoryActionForEventType maps memory lifecycle event types onto the
// memory_update vocabulary (proposed|confirmed|rejected).
func memoryActionForEventType(t string) (action string, ok bool) {
	switch t {
	case bridgeMemoryProposed:
		return "proposed", true
	case bridgeMemoryConfirmed:
		return "confirmed", true
	case bridgeMemoryRejected:
		return "rejected", true
	default:
		return "", false
	}
}

// episodeActionForEventType maps episode lifecycle event types onto the
// episode_update vocabulary (opened|updated|resolved).
func episodeActionForEventType(t string) (action string, ok bool) {
	switch t {
	case bridgeEpisodeOpened:
		return "opened", true
	case bridgeEpisodeUpdated:
		return "updated", true
	case bridgeEpisodeResolved:
		return "resolved", true
	default:
		return "", false
	}
}

// publishBridgedEvent fans one event row out through the Publish* method
// matching its type. A nil hub or empty project id is an error.
func publishBridgedEvent(hub eventPublisher, ev *store.Event) error {
	if hub == nil {
		return errors.New("server: ws bridge requires a non-nil hub")
	}
	if ev == nil {
		return errors.New("server: ws bridge requires a non-nil event")
	}
	if ev.ProjectID == "" {
		return errors.New("server: ws bridge event requires a project id")
	}
	if action, ok := memoryActionForEventType(ev.EventType); ok {
		hub.PublishMemoryUpdate(ev.ProjectID, ev.SessionID, ev.Payload, action)
		return nil
	}
	if action, ok := episodeActionForEventType(ev.EventType); ok {
		hub.PublishEpisodeUpdate(ev.ProjectID, ev.SessionID, ev.Payload, action)
		return nil
	}
	payload := map[string]any{
		"id":         ev.ID,
		"project_id": ev.ProjectID,
		"event_type": ev.EventType,
		"payload":    ev.Payload,
		"created_at": ev.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
	}
	if ev.SessionID != "" {
		payload["session_id"] = ev.SessionID
	}
	if ev.UserID != "" {
		payload["user_id"] = ev.UserID
	}
	if ev.EpisodeID != "" {
		payload["episode_id"] = ev.EpisodeID
	}
	hub.PublishEvent(ev.ProjectID, ev.SessionID, ev.EventType, payload, ev.UserID)
	return nil
}

// bridgeSubscription drains one live subscription, fanning each event out
// until ctx ends or the channel closes (broken connection — the caller
// resubscribes). It returns the count of delivered events. Publish errors
// cannot happen (void Hub); a bad row is logged and skipped — one bad row
// must never kill the stream.
func bridgeSubscription(ctx context.Context, sub <-chan *store.Event, hub eventPublisher) int {
	delivered := 0
	for {
		select {
		case <-ctx.Done():
			return delivered
		case ev, ok := <-sub:
			if !ok {
				return delivered // connection lost; caller resubscribes
			}
			if err := publishBridgedEvent(hub, ev); err != nil {
				log.Printf("server: ws bridge: publish event: %v", err)
				continue
			}
			delivered++
		}
	}
}

// bridgeLoop holds one subscription at a time and resubscribes with
// exponential backoff when the stream ends. Returns only when ctx is done
// (nil) or wiring is bad.
func bridgeLoop(ctx context.Context, subscribe func(context.Context) (<-chan *store.Event, error), hub eventPublisher) error {
	if subscribe == nil {
		return errors.New("server: ws bridge requires a subscribe func")
	}
	if hub == nil {
		return errors.New("server: ws bridge requires a non-nil hub")
	}
	backoff := bridgeBackoffBase
	for {
		if ctx.Err() != nil {
			return nil
		}
		sub, err := subscribe(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			log.Printf("server: ws bridge: subscribe: %v (retry in %s)", err, backoff)
			if !sleepOrDone(ctx, backoff) {
				return nil
			}
			backoff = nextBackoff(backoff)
			continue
		}
		if n := bridgeSubscription(ctx, sub, hub); n > 0 {
			backoff = bridgeBackoffBase
		} else if ctx.Err() == nil {
			log.Printf("server: ws bridge: subscription ended without deliveries (retry in %s)", backoff)
			if !sleepOrDone(ctx, backoff) {
				return nil
			}
			backoff = nextBackoff(backoff)
		}
	}
}

// nextBackoff doubles b up to the max.
func nextBackoff(b time.Duration) time.Duration {
	b *= 2
	if b > bridgeBackoffMax {
		return bridgeBackoffMax
	}
	return b
}

// sleepOrDone waits d or until ctx ends; false means ctx ended.
func sleepOrDone(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// BridgeEvents streams committed events into hub until ctx is canceled,
// resubscribing with backoff across connection loss. It returns nil on clean
// shutdown (ctx done) and a non-nil error only for misconfiguration (nil
// subscribe/hub).
func BridgeEvents(ctx context.Context, subscribe func(context.Context) (<-chan *store.Event, error), hub *Hub) error {
	if hub == nil {
		return errors.New("server: ws bridge requires a non-nil hub")
	}
	return bridgeLoop(ctx, subscribe, hub)
}
