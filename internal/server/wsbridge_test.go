// Tests for the WS event bridge (issue #40). All DB-free, no sockets:
// mapping is driven through a recording fake publisher, hydration through a
// map-backed fake fetcher, and Subscribe through stubbed channels.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"central-memory/internal/store"
)

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

// publishCall records one Publish* invocation.
type publishCall struct {
	method    string // "event" | "memory" | "episode"
	projectID string
	sessionID string
	action    string
	payload   any
}

// fakePublisher records Publish* calls for mapping assertions.
type fakePublisher struct {
	mu    sync.Mutex
	calls []publishCall
}

func (f *fakePublisher) PublishEvent(projectID, sessionID string, event any) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, publishCall{method: "event", projectID: projectID, sessionID: sessionID, payload: event})
	return 1, nil
}

func (f *fakePublisher) PublishMemoryUpdate(projectID, sessionID string, item any, action string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, publishCall{method: "memory", projectID: projectID, sessionID: sessionID, action: action, payload: item})
	return 1, nil
}

func (f *fakePublisher) PublishEpisodeUpdate(projectID, sessionID string, episode any, action string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, publishCall{method: "episode", projectID: projectID, sessionID: sessionID, action: action, payload: episode})
	return 1, nil
}

func (f *fakePublisher) last() publishCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[len(f.calls)-1]
}

func (f *fakePublisher) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// fakeFetcher serves canned rows (or errors) by id.
type fakeFetcher struct {
	rows map[int64]*store.Event
	err  map[int64]error
}

func (f *fakeFetcher) GetEventByID(_ context.Context, id int64) (*store.Event, error) {
	if err, ok := f.err[id]; ok {
		return nil, err
	}
	if e, ok := f.rows[id]; ok {
		return e, nil
	}
	return nil, errors.New("store: event: not found")
}

func memEvent(t, project, session string) *store.Event {
	return &store.Event{
		ID:        7,
		ProjectID: project,
		SessionID: session,
		EventType: t,
		Payload:   json.RawMessage(`{"key":"k"}`),
		CreatedAt: time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC),
	}
}

// ---------------------------------------------------------------------------
// Mapping
// ---------------------------------------------------------------------------

func TestBridgeMemoryMapping(t *testing.T) {
	cases := map[string]string{
		store.EventMemoryProposed:  "proposed",
		store.EventMemoryConfirmed: "confirmed",
		store.EventMemoryRejected:  "rejected",
	}
	for eventType, want := range cases {
		fp := &fakePublisher{}
		if err := publishBridgedEvent(fp, memEvent(eventType, "p1", "s1")); err != nil {
			t.Fatalf("%s: %v", eventType, err)
		}
		if fp.count() != 1 {
			t.Fatalf("%s: got %d calls, want 1", eventType, fp.count())
		}
		got := fp.last()
		if got.method != "memory" || got.action != want || got.projectID != "p1" || got.sessionID != "s1" {
			t.Fatalf("%s: got %+v, want memory/%s p1/s1", eventType, got, want)
		}
	}
}

func TestBridgeEpisodeMapping(t *testing.T) {
	cases := map[string]string{
		store.EventEpisodeOpened:   "opened",
		store.EventEpisodeUpdated:   "updated",
		store.EventEpisodeResolved:  "resolved",
	}
	for eventType, want := range cases {
		fp := &fakePublisher{}
		if err := publishBridgedEvent(fp, memEvent(eventType, "p1", "s1")); err != nil {
			t.Fatalf("%s: %v", eventType, err)
		}
		got := fp.last()
		if got.method != "episode" || got.action != want {
			t.Fatalf("%s: got %+v, want episode/%s", eventType, got, want)
		}
	}
}

func TestBridgeGenericEventEnvelope(t *testing.T) {
	fp := &fakePublisher{}
	ev := memEvent(store.EventMessageSent, "p1", "s1")
	ev.UserID = "u9"
	if err := publishBridgedEvent(fp, ev); err != nil {
		t.Fatal(err)
	}
	got := fp.last()
	if got.method != "event" || got.projectID != "p1" || got.sessionID != "s1" {
		t.Fatalf("got %+v, want generic event p1/s1", got)
	}
	env, ok := got.payload.(map[string]any)
	if !ok {
		t.Fatalf("envelope type %T, want map[string]any", got.payload)
	}
	if env["event_type"] != store.EventMessageSent || env["user_id"] != "u9" || env["project_id"] != "p1" {
		t.Fatalf("envelope fields wrong: %v", env)
	}
}

func TestBridgeSupersededFallsBackToGeneric(t *testing.T) {
	// MEMORY_SUPERSEDED has no §3.2 memory_update action — it must ride a
	// generic event frame, never an invented action string.
	fp := &fakePublisher{}
	if err := publishBridgedEvent(fp, memEvent(store.EventMemorySuperseded, "p1", "")); err != nil {
		t.Fatal(err)
	}
	if got := fp.last(); got.method != "event" {
		t.Fatalf("got method %q, want generic event fallback", got.method)
	}
}

func TestBridgePublishValidation(t *testing.T) {
	fp := &fakePublisher{}
	if err := publishBridgedEvent(nil, memEvent(store.EventMessageSent, "p", "")); err == nil {
		t.Fatal("nil hub: want error")
	}
	if err := publishBridgedEvent(fp, nil); err == nil {
		t.Fatal("nil event: want error")
	}
	if err := publishBridgedEvent(fp, memEvent(store.EventMessageSent, "", "")); err == nil {
		t.Fatal("empty project: want error")
	}
	if fp.count() != 0 {
		t.Fatalf("no successful publish expected, got %d", fp.count())
	}
}

// ---------------------------------------------------------------------------
// Subscription drain
// ---------------------------------------------------------------------------

func TestBridgeSubscriptionDrainsAndSkipsBadRows(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub := make(chan store.EventNotification, 4)
	sub <- store.EventNotification{ID: 1, ProjectID: "p1", EventType: store.EventMessageSent}
	sub <- store.EventNotification{ID: 2, ProjectID: "p1", EventType: store.EventMessageSent} // missing row
	sub <- store.EventNotification{ID: 3, ProjectID: "p1", EventType: store.EventMemoryProposed}
	close(sub)

	fetch := &fakeFetcher{
		rows: map[int64]*store.Event{
			1: memEvent(store.EventMessageSent, "p1", "s1"),
			3: memEvent(store.EventMemoryProposed, "p1", "s1"),
		},
		err: map[int64]error{2: store.ErrNotFound},
	}
	fp := &fakePublisher{}
	if n := bridgeSubscription(ctx, sub, fetch, fp); n != 2 {
		t.Fatalf("delivered=%d, want 2 (bad row skipped)", n)
	}
	if fp.count() != 2 {
		t.Fatalf("publish calls=%d, want 2", fp.count())
	}
}

func TestBridgeSubscriptionEndsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	sub := make(chan store.EventNotification) // never delivers, never closes
	done := make(chan int, 1)
	go func() { done <- bridgeSubscription(ctx, sub, &fakeFetcher{}, &fakePublisher{}) }()
	cancel()
	select {
	case n := <-done:
		if n != 0 {
			t.Fatalf("delivered=%d, want 0", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("bridgeSubscription did not exit on ctx cancel")
	}
}

// ---------------------------------------------------------------------------
// Reconnect loop
// ---------------------------------------------------------------------------

func TestBridgeLoopResubscribesAfterDrop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := make(chan store.EventNotification, 1)
	first <- store.EventNotification{ID: 1, ProjectID: "p1", EventType: store.EventMessageSent}
	close(first) // broken connection right after one delivery
	second := make(chan store.EventNotification, 1)
	second <- store.EventNotification{ID: 2, ProjectID: "p1", EventType: store.EventMessageSent}

	calls := 0
	subscribe := func(context.Context) (<-chan store.EventNotification, error) {
		calls++
		if calls == 1 {
			return first, nil
		}
		cancel() // end the test once the second subscription is handed out
		return second, nil
	}
	fetch := &fakeFetcher{
		rows: map[int64]*store.Event{
			1: memEvent(store.EventMessageSent, "p1", ""),
			2: memEvent(store.EventMessageSent, "p1", ""),
		},
	}
	go func() {
		time.Sleep(5 * time.Second)
		cancel() // backstop: never hang the suite
	}()
	if err := bridgeLoop(ctx, subscribe, fetch, &fakePublisher{}); err != nil {
		t.Fatalf("bridgeLoop: %v", err)
	}
	if calls < 2 {
		t.Fatalf("subscribe calls=%d, want ≥2 (resubscribe after drop)", calls)
	}
}

func TestBridgeLoopSubscribeErrorThenRecover(t *testing.T) {
	// A failing Subscribe must back off and retry, not return: fail once,
	// then hand over a channel that delivers one notification and closes
	// (broken connection), then cancel on the third subscribe.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	good := make(chan store.EventNotification, 1)
	good <- store.EventNotification{ID: 1, ProjectID: "p1", EventType: store.EventMessageSent}
	close(good)
	attempts := 0
	subscribe := func(context.Context) (<-chan store.EventNotification, error) {
		attempts++
		switch attempts {
		case 1:
			return nil, errors.New("connection refused")
		case 2:
			return good, nil
		default:
			cancel()
			empty := make(chan store.EventNotification)
			return empty, nil
		}
	}
	fetch := &fakeFetcher{rows: map[int64]*store.Event{
		1: memEvent(store.EventMessageSent, "p1", ""),
	}}
	if err := bridgeLoop(ctx, subscribe, fetch, &fakePublisher{}); err != nil {
		t.Fatalf("bridgeLoop: %v", err)
	}
	if attempts < 3 {
		t.Fatalf("subscribe attempts=%d, want ≥3 (error → recover → shutdown)", attempts)
	}
}

func TestBridgeEventsNilPool(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := BridgeEvents(ctx, nil, NewHub(HubOptions{})); err == nil {
		t.Fatal("nil pool: want error")
	}
	if err := BridgeEvents(ctx, nil, nil); err == nil {
		t.Fatal("nil pool+hub: want error")
	}
}

func TestNextBackoffCaps(t *testing.T) {
	if got := nextBackoff(bridgeBackoffMax); got != bridgeBackoffMax {
		t.Fatalf("at max: got %s", got)
	}
	if got := nextBackoff(20 * time.Second); got != bridgeBackoffMax {
		t.Fatalf("over max: got %s", got)
	}
	if got := nextBackoff(bridgeBackoffBase); got != 2*bridgeBackoffBase {
		t.Fatalf("base: got %s", got)
	}
}
