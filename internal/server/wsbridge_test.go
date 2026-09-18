// Tests for the WS event bridge (issue #40). All DB-free, no sockets:
// mapping is driven through a recording fake publisher, Subscribe through
// stubbed *store.Event channels.
package server

import (
	"context"
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

func (f *fakePublisher) PublishEvent(projectID, sessionID, eventType string, payload map[string]any, userID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, publishCall{method: "event", projectID: projectID, sessionID: sessionID, payload: payload})
}

func (f *fakePublisher) PublishMemoryUpdate(projectID, sessionID string, item any, action string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, publishCall{method: "memory", projectID: projectID, sessionID: sessionID, action: action, payload: item})
}

func (f *fakePublisher) PublishEpisodeUpdate(projectID, sessionID string, episode any, action string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, publishCall{method: "episode", projectID: projectID, sessionID: sessionID, action: action, payload: episode})
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

func memEvent(t, project, session string) *store.Event {
	return &store.Event{
		ID:        7,
		ProjectID: project,
		SessionID: session,
		EventType: t,
		Payload:   map[string]any{"key": "k"},
		CreatedAt: time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC),
	}
}

// ---------------------------------------------------------------------------
// Mapping
// ---------------------------------------------------------------------------

func TestBridgeMemoryMapping(t *testing.T) {
	cases := map[string]string{
		bridgeMemoryProposed:   "proposed",
		bridgeMemoryConfirmed:  "confirmed",
		bridgeMemoryRejected:   "rejected",
		bridgeMemoryUpdated:    "updated",
		bridgeMemorySuperseded: "superseded",
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
		bridgeEpisodeOpened:   "opened",
		bridgeEpisodeUpdated:  "updated",
		bridgeEpisodeResolved: "resolved",
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
	ev := memEvent("MESSAGE_SENT", "p1", "s1")
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
	if env["event_type"] != "MESSAGE_SENT" || env["user_id"] != "u9" || env["project_id"] != "p1" {
		t.Fatalf("envelope fields wrong: %v", env)
	}
}

func TestBridgeUnknownTypeFallsBackToGeneric(t *testing.T) {
	// Unmapped types ride a generic event frame, never an invented action.
	fp := &fakePublisher{}
	if err := publishBridgedEvent(fp, memEvent("FILE_MODIFIED", "p1", "")); err != nil {
		t.Fatal(err)
	}
	if got := fp.last(); got.method != "event" {
		t.Fatalf("got method %q, want generic event fallback", got.method)
	}
}

func TestBridgePublishValidation(t *testing.T) {
	fp := &fakePublisher{}
	if err := publishBridgedEvent(nil, memEvent("MESSAGE_SENT", "p", "")); err == nil {
		t.Fatal("nil hub: want error")
	}
	if err := publishBridgedEvent(fp, nil); err == nil {
		t.Fatal("nil event: want error")
	}
	if err := publishBridgedEvent(fp, memEvent("MESSAGE_SENT", "", "")); err == nil {
		t.Fatal("empty project: want error")
	}
	if fp.count() != 0 {
		t.Fatalf("no successful publish expected, got %d", fp.count())
	}
}

// ---------------------------------------------------------------------------
// Subscription drain
// ---------------------------------------------------------------------------

func TestBridgeSubscriptionDrains(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub := make(chan *store.Event, 3)
	sub <- memEvent("MESSAGE_SENT", "p1", "s1")
	sub <- memEvent(bridgeMemoryProposed, "p1", "s1")
	sub <- memEvent(bridgeEpisodeResolved, "p1", "")
	close(sub)

	fp := &fakePublisher{}
	if n := bridgeSubscription(ctx, sub, fp); n != 3 {
		t.Fatalf("delivered=%d, want 3", n)
	}
	if fp.count() != 3 {
		t.Fatalf("publish calls=%d, want 3", fp.count())
	}
}

func TestBridgeSubscriptionSkipsBadRows(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub := make(chan *store.Event, 3)
	sub <- memEvent("MESSAGE_SENT", "p1", "s1")
	sub <- nil // bad row: logged and skipped, never kills the stream
	sub <- memEvent(bridgeMemoryProposed, "p1", "s1")
	close(sub)

	fp := &fakePublisher{}
	if n := bridgeSubscription(ctx, sub, fp); n != 2 {
		t.Fatalf("delivered=%d, want 2 (bad row skipped)", n)
	}
	if fp.count() != 2 {
		t.Fatalf("publish calls=%d, want 2", fp.count())
	}
}

func TestBridgeSubscriptionEndsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	sub := make(chan *store.Event) // never delivers, never closes
	done := make(chan int, 1)
	go func() { done <- bridgeSubscription(ctx, sub, &fakePublisher{}) }()
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
	first := make(chan *store.Event, 1)
	first <- memEvent("MESSAGE_SENT", "p1", "")
	close(first) // broken connection right after one delivery
	second := make(chan *store.Event, 1)
	second <- memEvent("MESSAGE_SENT", "p1", "")

	calls := 0
	subscribe := func(context.Context) (<-chan *store.Event, error) {
		calls++
		if calls == 1 {
			return first, nil
		}
		cancel() // end the test once the second subscription is handed out
		return second, nil
	}
	go func() {
		time.Sleep(5 * time.Second)
		cancel() // backstop: never hang the suite
	}()
	if err := bridgeLoop(ctx, subscribe, &fakePublisher{}); err != nil {
		t.Fatalf("bridgeLoop: %v", err)
	}
	if calls < 2 {
		t.Fatalf("subscribe calls=%d, want ≥2 (resubscribe after drop)", calls)
	}
}

func TestBridgeLoopSubscribeErrorThenRecover(t *testing.T) {
	// A failing Subscribe must back off and retry, not return: fail once,
	// then hand over a channel that delivers one event and closes
	// (broken connection), then cancel on the third subscribe.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	good := make(chan *store.Event, 1)
	good <- memEvent("MESSAGE_SENT", "p1", "")
	close(good)
	attempts := 0
	subscribe := func(context.Context) (<-chan *store.Event, error) {
		attempts++
		switch attempts {
		case 1:
			return nil, errors.New("connection refused")
		case 2:
			return good, nil
		default:
			cancel()
			empty := make(chan *store.Event)
			return empty, nil
		}
	}
	if err := bridgeLoop(ctx, subscribe, &fakePublisher{}); err != nil {
		t.Fatalf("bridgeLoop: %v", err)
	}
	if attempts < 3 {
		t.Fatalf("subscribe attempts=%d, want ≥3 (error → recover → shutdown)", attempts)
	}
}

func TestBridgeEventsNilWiring(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := BridgeEvents(ctx, nil, NewHub()); err == nil {
		t.Fatal("nil subscribe: want error")
	}
	if err := BridgeEvents(ctx, nil, nil); err == nil {
		t.Fatal("nil subscribe+hub: want error")
	}
	okSub := func(context.Context) (<-chan *store.Event, error) {
		cancel()
		empty := make(chan *store.Event)
		return empty, nil
	}
	if err := BridgeEvents(ctx, okSub, nil); err == nil {
		t.Fatal("nil hub: want error")
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
