package store

// Regression tests for issue #131 P0s: Subscribe send-on-close race,
// AppendEvent validation, episode-type case normalization.

import (
	"context"
	"sync"
	"testing"
)

func TestSubscribeCancelRaceNoPanic(t *testing.T) {
	s := NewMemStore()
	ctx := context.Background()
	var wg sync.WaitGroup
	// Hammer appends while subscribers churn; -race must stay clean and
	// no send-on-closed-channel panic may occur.
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ch, cancel, err := s.Subscribe(ctx, "p1")
			if err != nil {
				t.Errorf("subscribe: %v", err)
				return
			}
			_ = ch
			for j := 0; j < 25; j++ {
				_ = s.AppendEvent(ctx, &Event{ProjectID: "p1", EventType: "T"})
			}
			cancel()
		}(i)
	}
	wg.Wait()
}

func TestAppendEventValidation(t *testing.T) {
	s := NewMemStore()
	ctx := context.Background()
	if err := s.AppendEvent(ctx, nil); err == nil {
		t.Error("nil event should fail")
	}
	if err := s.AppendEvent(ctx, &Event{EventType: "T"}); err == nil {
		t.Error("empty project_id should fail")
	}
	if err := s.AppendEvent(ctx, &Event{ProjectID: "p1"}); err == nil {
		t.Error("empty event_type should fail")
	}
	if err := s.AppendEvent(ctx, &Event{ProjectID: "p1", EventType: "T"}); err != nil {
		t.Errorf("valid event should append: %v", err)
	}
}

func TestCreateEpisodeNormalizesTypeCase(t *testing.T) {
	s := NewMemStore()
	ctx := context.Background()
	if err := s.CreateEpisode(ctx, &Episode{ProjectID: "p1", Title: "mixed case type here", EpisodeType: "Bug_Fix"}); err != nil {
		t.Fatalf("CreateEpisode: %v", err)
	}
	eps, err := s.SearchEpisodes(ctx, "p1", "", "", "", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(eps) != 1 || eps[0].EpisodeType != "bug_fix" {
		t.Fatalf("episode type = %+v, want normalized bug_fix", eps)
	}
}

func TestPromoteGuardsTerminalStates(t *testing.T) {
	s := NewMemStore()
	ctx := context.Background()
	if err := s.CreateMemoryItem(ctx, &MemoryItem{ProjectID: "p1", Key: "k1", Content: "session memory content here", Level: "session", SessionID: "s1"}); err != nil {
		t.Fatal(err)
	}
	items, err := s.SearchMemory(ctx, "p1", "k1", nil, 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("setup: %+v %v", items, err)
	}
	id := items[0].ID
	// Force REJECTED then attempt promote: must refuse.
	s.mu.Lock()
	s.memories[id].Status = StatusRejected
	s.mu.Unlock()
	if err := s.PromoteSessionMemory(ctx, id, "u1"); err == nil {
		t.Fatal("promoting REJECTED should fail")
	}
}
