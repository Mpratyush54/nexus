// WS event bridge: store.Subscribe (LISTEN/NOTIFY) → Hub fan-out (issue #40).
//
// PROBLEM: the store package streams committed events via Subscribe
// (events.go, one dedicated pooled connection per subscriber) and the hub
// fans them out via PublishEvent/PublishMemoryUpdate/PublishEpisodeUpdate
// (ws.go), but nothing connected the two — and the shipped container never
// called EnableWS/RegisterWebRoutes, so /ws and the dashboard were dead.
//
// OWNERSHIP: this file ONLY (plus wsbridge_test.go and the bootstrap
// entrypoint). It touches no other server/store file: the bridge consumes
// only the public Subscribe signature, EventStore.GetEventByID, and the
// Hub's Publish* methods. See ADR-040 for the why.
//
// Delivery contract (inherited from Subscribe): notifications are wake-ups,
// not payloads. Each notification hydrates the full row via GetEventByID
// and a broken stream ends the subscription — the caller (BridgeEvents)
// resubscribes with backoff. Missed commits while disconnected are NOT
// replayed here; consumers backfill via ListEvents (per ADR-009).
package server

import (
	"context"
	"errors"
	"log"
	"time"

	"central-memory/internal/store"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Reconnect backoff bounds for the subscribe loop. Base doubles per failed
// or empty subscription; a subscription that delivers ≥1 notification
// resets to base (proof the path is healthy — see bridgeLoop).
const (
	bridgeBackoffBase = 1 * time.Second
	bridgeBackoffMax  = 30 * time.Second
)

// eventPublisher is the Hub surface the bridge needs. *Hub satisfies it;
// tests substitute a recording fake, so mapping is unit-testable with no DB
// and no sockets.
type eventPublisher interface {
	PublishEvent(projectID, sessionID string, event any) (int, error)
	PublishMemoryUpdate(projectID, sessionID string, item any, action string) (int, error)
	PublishEpisodeUpdate(projectID, sessionID string, episode any, action string) (int, error)
}

// Compile-time proof the real hub satisfies the seam.
var _ eventPublisher = (*Hub)(nil)

// poolDBTX adapts a *pgxpool.Pool to store.DBTX for the hydration path.
// Subscribe needs the concrete pool (LISTEN is per-connection state), but
// EventStore needs only DBTX; the pool cannot satisfy it directly because
// DBTX.Query returns the narrow store.Rows while pgx returns pgx.Rows
// (which satisfies store.Rows method-for-method — see memory.go — so this
// adapter is pure type-level forwarding with no row wrapping).
type poolDBTX struct {
	pool *pgxpool.Pool
}

func (p poolDBTX) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return p.pool.Exec(ctx, sql, args...)
}

func (p poolDBTX) Query(ctx context.Context, sql string, args ...any) (store.Rows, error) {
	rows, err := p.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (p poolDBTX) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return p.pool.QueryRow(ctx, sql, args...)
}

// Compile-time proof the adapter satisfies the store seam.
var _ store.DBTX = poolDBTX{}

// eventFetcher hydrates one event row by id. *store.EventStore satisfies it;
// tests substitute a map-backed fake.
type eventFetcher interface {
	GetEventByID(ctx context.Context, id int64) (*store.Event, error)
}

// subscribeEvents is the Subscribe entrypoint, indirected through a package
// var so BridgeEvents stays a one-line production call while tests inject a
// channel-backed stub (no Postgres).
var subscribeEvents = store.Subscribe

// memoryActionForEventType maps memory lifecycle event types onto the §3.2
// memory_update vocabulary (proposed|confirmed|rejected). SUPERSEDED has no
// WS action — it returns ok=false so the caller falls back to a generic
// event frame rather than inventing protocol.
func memoryActionForEventType(t string) (action string, ok bool) {
	switch t {
	case store.EventMemoryProposed:
		return "proposed", true
	case store.EventMemoryConfirmed:
		return "confirmed", true
	case store.EventMemoryRejected:
		return "rejected", true
	default:
		return "", false
	}
}

// episodeActionForEventType maps episode lifecycle event types onto the §3.2
// episode_update vocabulary (opened|updated|resolved).
func episodeActionForEventType(t string) (action string, ok bool) {
	switch t {
	case store.EventEpisodeOpened:
		return "opened", true
	case store.EventEpisodeUpdated:
		return "updated", true
	case store.EventEpisodeResolved:
		return "resolved", true
	default:
		return "", false
	}
}

// publishBridgedEvent fans one hydrated event row out through the Publish*
// method matching its type. Memory/episode lifecycle rows become typed
// update frames (payload forwarded as the item/episode body); everything
// else becomes a generic event frame carrying an explicit snake_case
// envelope (store.Event has no JSON tags, so it is never marshaled raw).
// A nil hub publish surface or empty project id is an error; zero receivers
// (nobody subscribed) is not.
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
		_, err := hub.PublishMemoryUpdate(ev.ProjectID, ev.SessionID, ev.Payload, action)
		return err
	}
	if action, ok := episodeActionForEventType(ev.EventType); ok {
		_, err := hub.PublishEpisodeUpdate(ev.ProjectID, ev.SessionID, ev.Payload, action)
		return err
	}
	envelope := map[string]any{
		"id":         ev.ID,
		"project_id": ev.ProjectID,
		"event_type": ev.EventType,
		"payload":    ev.Payload,
		"created_at": ev.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
	}
	if ev.SessionID != "" {
		envelope["session_id"] = ev.SessionID
	}
	if ev.UserID != "" {
		envelope["user_id"] = ev.UserID
	}
	if ev.EpisodeID != "" {
		envelope["episode_id"] = ev.EpisodeID
	}
	_, err := hub.PublishEvent(ev.ProjectID, ev.SessionID, envelope)
	return err
}

// bridgeSubscription drains one live subscription: hydrate each notification
// and fan out, until ctx ends or the channel closes (broken connection —
// the caller resubscribes). It returns the count of hydrated notifications.
// Hydration misses (including ErrNotFound for a concurrently compacted row)
// and publish errors are logged and skipped — one bad row must never kill
// the stream. Malformed payloads never reach here (Subscribe filters them).
func bridgeSubscription(ctx context.Context, sub <-chan store.EventNotification, fetch eventFetcher, hub eventPublisher) int {
	delivered := 0
	for {
		select {
		case <-ctx.Done():
			return delivered
		case n, ok := <-sub:
			if !ok {
				return delivered // connection lost; caller resubscribes
			}
			ev, err := fetch.GetEventByID(ctx, n.ID)
			if err != nil {
				log.Printf("server: ws bridge: hydrate event %d: %v", n.ID, err)
				continue
			}
			if err := publishBridgedEvent(hub, ev); err != nil {
				log.Printf("server: ws bridge: publish event %d: %v", n.ID, err)
				continue
			}
			delivered++
		}
	}
}

// bridgeLoop holds one subscription at a time and resubscribes with
// exponential backoff when the stream ends. Backoff resets on the first
// delivered notification (healthy path); consecutive failed or empty
// subscriptions back off base → max so a dead database does not hot-loop
// LISTEN attempts. Returns only when ctx is done (nil) or wiring is bad.
func bridgeLoop(ctx context.Context, subscribe func(context.Context) (<-chan store.EventNotification, error), fetch eventFetcher, hub eventPublisher) error {
	if subscribe == nil {
		return errors.New("server: ws bridge requires a subscribe func")
	}
	if fetch == nil {
		return errors.New("server: ws bridge requires an event fetcher")
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
		if n := bridgeSubscription(ctx, sub, fetch, hub); n > 0 {
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

// BridgeEvents streams every committed event (all projects) into hub until
// ctx is canceled, resubscribing with backoff across connection loss. It
// returns nil on clean shutdown (ctx done) and a non-nil error only for
// misconfiguration (nil pool/hub). Run it as a goroutine next to Serve:
//
//	go func() {
//		if err := server.BridgeEvents(ctx, pool, srv.Hub()); err != nil {
//			log.Printf("ws bridge: %v", err)
//		}
//	}()
//
// pool is *pgxpool.Pool because Subscribe LISTENs on a dedicated pooled
// connection (per-connection state — see events.go); hydration reuses the
// same pool through the poolDBTX adapter into EventStore.
func BridgeEvents(ctx context.Context, pool *pgxpool.Pool, hub *Hub) error {
	if pool == nil {
		return errors.New("server: ws bridge requires a non-nil pool")
	}
	if hub == nil {
		return errors.New("server: ws bridge requires a non-nil hub")
	}
	return bridgeLoop(ctx,
		func(ctx context.Context) (<-chan store.EventNotification, error) {
			return subscribeEvents(ctx, pool, "")
		},
		store.NewEventStore(poolDBTX{pool: pool}),
		hub,
	)
}
