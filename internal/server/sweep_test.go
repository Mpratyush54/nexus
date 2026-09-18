package server

// Lifecycle sweeper tests (issue #119 box 4): the tick runs against capable
// stores, stays idle on narrow ones (stubStore), and search paths reset the
// decay clock.

import (
	"context"
	"net/http"
	"testing"
	"time"

	"central-memory/internal/store"
)

func TestSweeperRunsAgainstCapableStore(t *testing.T) {
	st := store.NewMemStore()
	s := NewServer(st)
	stop := StartLifecycleSweeper(context.Background(), s.Store, 20*time.Millisecond)
	defer stop()
	// Leading sweep + at least one tick must fire without hanging or panicking.
	time.Sleep(150 * time.Millisecond)
	stop()
	// Second stop is a safe no-op.
	stop()
}

func TestSweeperIdleOnNarrowStore(t *testing.T) {
	// struct{store.Store}{} has no sweep seams: idle path, no goroutine leak.
	stop := StartLifecycleSweeper(context.Background(), struct{ store.Store }{}, time.Millisecond)
	stop()
}

func TestSearchResetsDecayClock(t *testing.T) {
	s := newTestServer()
	ctx := context.Background()
	p, err := s.Store.ResolveProject(ctx, "", "", "sweepuse")
	if err != nil {
		t.Fatal(err)
	}
	item := store.MemoryItem{
		ProjectID: p.ID, Key: "ops/deploy",
		Content: "deploy with migrations applied before switching traffic over",
		Status:  "CONFIRMED",
	}
	if err := s.Store.CreateMemoryItem(ctx, &item); err != nil {
		t.Fatal(err)
	}
	tok := loginAs(t, s, "alice")
	rec := doJSON(t, s, http.MethodGet, "/memory/search?project_id="+p.ID+"&q=deploy+migrations", tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("search status = %d, body = %s", rec.Code, rec.Body.String())
	}
	got, err := s.Store.GetMemoryItem(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.UseCount < 1 {
		t.Errorf("UseCount = %d after serving, want >= 1 (decay clock must reset)", got.UseCount)
	}
	if got.LastUsedAt.IsZero() {
		t.Error("LastUsedAt not set after serving")
	}
}
