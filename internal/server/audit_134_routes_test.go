package server

// Regression tests for issue #134 P0s: cross-project merge guard,
// WS steering session binding, paginated backfill.

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"central-memory/internal/steering"
	"central-memory/internal/store"
)

func TestMergeRejectsCrossProject(t *testing.T) {
	s := newTestServer()
	token := loginAs(t, s, "alice")
	projA := resolveTestProject(t, s, token, "merge-proj-a")
	projB := resolveTestProject(t, s, token, "merge-proj-b")
	ctx := context.Background()
	ms := s.Store.(*store.MemStore)

	mainA, err := ms.EnsureMainBranch(ctx, projA)
	if err != nil {
		t.Fatal(err)
	}
	mainB, err := ms.EnsureMainBranch(ctx, projB)
	if err != nil {
		t.Fatal(err)
	}
	// Resolve the other project by raw ID: must 400, not merge across.
	rec := doJSON(t, s, http.MethodPost, "/branches/merge", token, map[string]any{
		"source": mainA.ID, "target": mainB.ID, "project_id": projA,
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("cross-project merge = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}

func TestSteerHookRejectsForeignRun(t *testing.T) {
	s, h, _, runID, _ := steerTestSetup(t)
	alice := newHubClient(h, "alice", "", "")
	raw, _ := json.Marshal(WSMessage{Type: WSMsgSubscribe, ProjectID: s.hubProjectOf(t, runID), SessionID: runID})
	if err := h.HandleClientMessage(alice.ID, raw); err != nil {
		t.Skipf("session subscribe unavailable: %v", err)
	}
	readMsg(t, alice)
	// Foreign run_id: rejected, manager untouched.
	handled, err := s.routeSteerAction(alice.ID, "alice", steering.EventInterruptRequested,
		map[string]any{"run_id": "other-session", "reason": "hijack"})
	if !handled || err == nil {
		t.Fatalf("foreign run steer: handled=%v err=%v, want handled + error", handled, err)
	}
	// Own session: accepted.
	handled, err = s.routeSteerAction(alice.ID, "alice", steering.EventInterruptRequested,
		map[string]any{"run_id": runID, "reason": "legit"})
	if !handled || err != nil {
		t.Fatalf("own session steer: handled=%v err=%v, want handled + nil", handled, err)
	}
}

func TestBackfillPaginates(t *testing.T) {
	h := NewHub()
	mk := func(id int64) *store.Event {
		return &store.Event{ID: id, ProjectID: "p", EventType: "T"}
	}
	// Two capped pages then empty: backfill must advance past both.
	pages := [][]*store.Event{{mk(1), mk(2)}, {mk(3)}, {}}
	var calls int
	list := func(_ context.Context, sinceID int64) ([]*store.Event, error) {
		var out []*store.Event
		for _, p := range pages {
			for _, ev := range p {
				if ev.ID > sinceID {
					out = append(out, ev)
				}
			}
			if len(out) > 0 {
				break // one page per call
			}
		}
		calls++
		return out, nil
	}
	last, err := backfill(context.Background(), list, h, 0)
	if err != nil {
		t.Fatal(err)
	}
	if last != 3 {
		t.Fatalf("backfill cursor = %d, want 3", last)
	}
	if calls < 3 {
		t.Fatalf("list calls = %d, want >= 3 (paginated)", calls)
	}
}
