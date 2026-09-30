package server

import (
	"context"
	"log"
	"sync"
	"time"

	"central-memory/internal/store"
)

const defaultSnapshotPruneInterval = 6 * time.Hour

// StartSnapshotRetention prunes old session snapshots every 6h (keep latest 5).
func StartSnapshotRetention(ctx context.Context, st store.Store, interval time.Duration) (stop func()) {
	if interval <= 0 {
		interval = defaultSnapshotPruneInterval
	}
	done := make(chan struct{})
	var once sync.Once
	stop = func() { once.Do(func() { close(done) }) }

	run := func() {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer cancel()
		switch x := st.(type) {
		case *store.PostgresStore:
			if err := x.PruneAllSnapshotsKeepN(sctx, 5); err != nil {
				log.Printf("server: snapshot prune: %v", err)
			} else {
				log.Printf("server: snapshot prune keepN=5 ok")
			}
			if _, err := x.PruneSessionVersions(sctx, time.Now()); err != nil {
				log.Printf("server: version retention: %v", err)
			}
		case *store.MemStore:
			for _, sid := range x.ListSnapshotSessionIDs() {
				_ = x.PruneOldSnapshots(sctx, sid, 5)
			}
			if _, err := x.PruneSessionVersions(sctx, time.Now()); err != nil {
				log.Printf("server: version retention: %v", err)
			}
		default:
			if ps, ok := st.(store.ProvenanceStore); ok {
				_ = ps
			}
		}
	}

	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-t.C:
				run()
			}
		}
	}()
	return stop
}
