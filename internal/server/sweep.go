package server

// sweep.go — periodic lifecycle tick (issue #119 box 4, plan §§1.7/2.7/6.2).
//
// The server owns the durable store, so the server runs the schedule:
// auto-confirm tiers (§2.8), session expiry + 7d grace (§2.7), and stale
// triage (§1.7). The tick asserts narrow store seams optionally —
// capability-poor stores (stubStore, fakes, text-only) report idle instead
// of failing, so local-dev boots are unaffected. Branch auto-archive stays
// per-project on demand (store.SweepBranchesForProject): the tick has no
// project enumeration seam.

import (
	"context"
	"log"
	"sync"
	"time"

	"central-memory/internal/store"
)

// DefaultSweepInterval is the lifecycle tick period. Fifteen minutes keeps
// confirm latency reasonable without meaningful DB load (all three sweep
// queries are indexed; empty ticks are three cheap reads).
const DefaultSweepInterval = 15 * time.Minute

// recordMemoryUse bumps use_count/last_used_at on served rows (plan §1.7
// decay clock, issue #119). Best-effort: narrow stores skip it, errors are
// swallowed so serving never fails on bookkeeping.
func recordMemoryUse(ctx context.Context, st store.Store, items []*store.MemoryItem) {
	ru, ok := st.(interface {
		RecordMemoryUse(context.Context, string) error
	})
	if !ok {
		return
	}
	for _, it := range items {
		if it == nil || it.ID == "" {
			continue
		}
		_ = ru.RecordMemoryUse(ctx, it.ID)
	}
}

// StartLifecycleSweeper runs store.SweepLifecycle on a ticker until ctx ends
// or the returned stop func fires (idempotent). It sweeps immediately once
// at start — sweeps are idempotent — then on every interval. Stores without
// sweep support log once and stay idle.
func StartLifecycleSweeper(ctx context.Context, st store.Store, interval time.Duration) (stop func()) {
	if interval <= 0 {
		interval = DefaultSweepInterval
	}
	c, _ := st.(store.ConfirmSweeper)
	e, _ := st.(store.SessionSweeper)
	l, _ := st.(store.StaleMemoryLister)
	if c == nil && e == nil && l == nil {
		log.Print("server: lifecycle sweeper idle (store supports no sweep operations)")
		return func() {}
	}
	done := make(chan struct{})
	var once sync.Once
	stop = func() {
		once.Do(func() { close(done) })
	}
	sweepOnce := func() {
		if ctx.Err() != nil {
			return
		}
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		rep, err := store.SweepLifecycle(sctx, time.Now().UTC(), c, e, l)
		if err != nil {
			log.Printf("server: lifecycle sweep partial (confirmed=%d expired_sessions=%d stale_candidates=%d): %v",
				rep.Confirmed, rep.ExpiredSessions, rep.StaleCandidates, err)
			return
		}
		log.Printf("server: lifecycle sweep confirmed=%d expired_sessions=%d stale_candidates=%d",
			rep.Confirmed, rep.ExpiredSessions, rep.StaleCandidates)
	}
	go func() {
		sweepOnce()
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-t.C:
				sweepOnce()
			}
		}
	}()
	return stop
}
