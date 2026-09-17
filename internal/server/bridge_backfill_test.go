package server

// LISTEN/NOTIFY resubscribe + backfill tests (nexus issue #88).
//
// The bridge holds one subscription with exponential backoff and replays
// ListEvents after every (re)connect, so a disconnect window cannot silently
// lose events. Delivery is at-least-once: consumers dedupe on event ID.

import (
	"context"
	"testing"
	"time"

	"central-memory/internal/store"
)

// backfill replays rows after the cursor in ID order and advances it.
func TestBridgeBackfillReplaysMissed(t *testing.T) {
	ctx := context.Background()
	rows := []*store.Event{
		{ID: 6, ProjectID: "p1", EventType: "MESSAGE_SENT", Payload: map[string]any{}},
		{ID: 7, ProjectID: "p1", EventType: bridgeMemoryProposed, Payload: map[string]any{}},
		{ID: 8, ProjectID: "p1", EventType: bridgeEpisodeResolved, Payload: map[string]any{}},
	}
	list := func(_ context.Context, sinceID int64) ([]*store.Event, error) {
		var out []*store.Event
		for _, e := range rows {
			if e.ID > sinceID {
				out = append(out, e)
			}
		}
		return out, nil
	}
	fp := &fakePublisher{}
	cursor, err := backfill(ctx, list, fp, 5)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if cursor != 8 {
		t.Fatalf("cursor = %d, want 8", cursor)
	}
	if fp.count() != 3 {
		t.Fatalf("published = %d, want 3", fp.count())
	}

	// Nothing missed: cursor unchanged, no duplicate delivery.
	fp2 := &fakePublisher{}
	cursor2, err := backfill(ctx, list, fp2, cursor)
	if err != nil || cursor2 != 8 || fp2.count() != 0 {
		t.Fatalf("no-op backfill: cursor=%d err=%v published=%d", cursor2, err, fp2.count())
	}

	// Nil lister is a no-op (bridge without a store still streams live).
	fp3 := &fakePublisher{}
	if c, err := backfill(ctx, nil, fp3, 9); err != nil || c != 9 || fp3.count() != 0 {
		t.Fatalf("nil list backfill: cursor=%d err=%v published=%d", c, err, fp3.count())
	}
}

// The full loop replays missed rows on reconnect before draining live
// notifications: the boundary event may duplicate (at-least-once), but no
// event is lost across the drop.
func TestBridgeLoopBackfillsAcrossReconnect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	live := make(chan *store.Event, 2)
	live <- &store.Event{ID: 12, ProjectID: "p1", EventType: "AFTER_DROP", Payload: map[string]any{}}

	calls := 0
	subscribe := func(context.Context) (<-chan *store.Event, error) {
		calls++
		if calls == 1 {
			// First connection dies instantly with nothing delivered.
			empty := make(chan *store.Event)
			close(empty)
			return empty, nil
		}
		return live, nil
	}
	missed := []*store.Event{
		{ID: 11, ProjectID: "p1", EventType: "MISSED_WHILE_DOWN", Payload: map[string]any{}},
	}
	list := func(_ context.Context, sinceID int64) ([]*store.Event, error) {
		var out []*store.Event
		for _, e := range missed {
			if e.ID > sinceID {
				out = append(out, e)
			}
		}
		return out, nil
	}

	fp := &fakePublisher{}
	// End the test once both deliveries land; backstop never hangs it.
	go func() {
		timeout := time.After(10 * time.Second)
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-timeout:
				cancel()
				return
			case <-tick.C:
				if fp.count() >= 2 {
					cancel()
					return
				}
			}
		}
	}()
	if err := bridgeLoopWithBackfill(ctx, subscribe, fp, bridgeConfig{list: list}); err != nil {
		t.Fatalf("bridgeLoopWithBackfill: %v", err)
	}
	// Missed(11) replays on reconnect, then live(12) drains: none lost.
	if fp.count() != 2 {
		t.Fatalf("published = %d, want 2 (missed + live, none lost)", fp.count())
	}
	fp.mu.Lock()
	defer fp.mu.Unlock()
	first, _ := fp.calls[0].payload.(map[string]any)
	second, _ := fp.calls[1].payload.(map[string]any)
	if first["event_type"] != "MISSED_WHILE_DOWN" {
		t.Fatalf("first delivery = %v, want the backfilled miss first", first)
	}
	if second["event_type"] != "AFTER_DROP" {
		t.Fatalf("second delivery = %v, want the live event second", second)
	}
}
