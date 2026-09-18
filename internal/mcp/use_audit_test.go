package mcp

// Use-tracking test (issue #119 box 3): serving a memory via memory_search
// bumps its decay clock. Best-effort: narrow stores skip it, errors never
// fail the search.

import (
	"context"
	"sync"
	"testing"
)

// useCountingStore is a fakeStore that records RecordMemoryUse calls.
type useCountingStore struct {
	*fakeStore
	mu   sync.Mutex
	used []string
}

func (u *useCountingStore) RecordMemoryUse(_ context.Context, id string) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.used = append(u.used, id)
	return nil
}

func TestAuditMemorySearchRecordsUse(t *testing.T) {
	s, ms := newTestServerWithT(t)
	counter := &useCountingStore{fakeStore: ms}
	s.store = counter
	if err := ms.CreateMemoryItem(context.Background(), &MemoryItem{
		ProjectID: "proj_test", Key: "ops/retry",
		Content: "retry with backoff on connection refused errors here",
		Status:  "CONFIRMED",
	}); err != nil {
		t.Fatal(err)
	}
	r := callTool(t, s, "memory_search", map[string]any{"query": "retry backoff"})
	if r.Error != nil {
		t.Fatalf("search failed: %+v", r.Error)
	}
	counter.mu.Lock()
	defer counter.mu.Unlock()
	if len(counter.used) == 0 {
		t.Error("memory_search served items but recorded no use (decay clock never resets)")
	}
}

func TestAuditRecordUseSkipsNarrowStores(t *testing.T) {
	// fakeStore has no RecordMemoryUse: must be a silent no-op, never a panic.
	recordUse(context.Background(), newFakeStore(), []*MemoryItem{{ID: "x"}})
	recordUse(context.Background(), newFakeStore(), nil)
}
