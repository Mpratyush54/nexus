// Unit tests for workspace heartbeat staleness: threshold predicates and
// app-side filtering. All DB-free with a fixed clock.
package store_test

import (
	"testing"
	"time"

	"central-memory/internal/store"
)

func TestStore_OfflineAfterIs90s(t *testing.T) {
	if store.OfflineAfter != 90*time.Second {
		t.Fatalf("OfflineAfter = %v, want 90s (plan: offline after 90s silence)", store.OfflineAfter)
	}
	if store.OfflineAfterSeconds() != 90 {
		t.Fatalf("OfflineAfterSeconds() = %v, want 90", store.OfflineAfterSeconds())
	}
}

func TestStore_IsOnlineAt(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		lastSeen time.Time
		online   bool
	}{
		{"just seen", now, true},
		{"one beat missed", now.Add(-30 * time.Second), true},
		{"two beats missed", now.Add(-60 * time.Second), true},
		{"89s silence", now.Add(-89 * time.Second), true},
		{"exactly 90s is still online", now.Add(-90 * time.Second), true},
		{"91s silence is offline", now.Add(-91 * time.Second), false},
		{"minutes of silence", now.Add(-5 * time.Minute), false},
		{"hours of silence", now.Add(-2 * time.Hour), false},
		{"future timestamp (clock skew) is online", now.Add(60 * time.Second), true},
		{"zero time is stale", time.Time{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := store.IsOnlineAt(tc.lastSeen, now); got != tc.online {
				t.Errorf("IsOnlineAt(%v) = %v, want %v", tc.lastSeen, got, tc.online)
			}
			if got := store.IsStaleAt(tc.lastSeen, now); got == tc.online {
				t.Errorf("IsStaleAt(%v) = %v, want negation of %v", tc.lastSeen, got, tc.online)
			}
		})
	}
}

func TestStore_IsOnlineAtPtr(t *testing.T) {
	now := time.Now().UTC()
	if store.IsOnlineAtPtr(nil, now) {
		t.Error("nil last_seen must be offline")
	}
	fresh := now.Add(-10 * time.Second)
	if !store.IsOnlineAtPtr(&fresh, now) {
		t.Error("fresh last_seen must be online")
	}
	stale := now.Add(-10 * time.Minute)
	if store.IsOnlineAtPtr(&stale, now) {
		t.Error("stale last_seen must be offline")
	}
}

func TestStore_ExpiryAt(t *testing.T) {
	seen := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	if got := store.ExpiryAt(seen); !got.Equal(seen.Add(90 * time.Second)) {
		t.Fatalf("ExpiryAt = %v, want +90s", got)
	}
}

func TestStore_FilterOnline(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-10 * time.Second)
	stale := now.Add(-10 * time.Minute)
	in := []store.Workspace{
		{ID: "fresh", LastSeen: &fresh},
		{ID: "stale", LastSeen: &stale},
		{ID: "never"},
	}
	got := store.FilterOnline(in, now)
	if len(got) != 1 || got[0].ID != "fresh" {
		t.Fatalf("FilterOnline = %+v, want only [fresh]", got)
	}
	if len(in) != 3 {
		t.Fatal("FilterOnline must not mutate its input slice")
	}
	if got := store.FilterOnline(nil, now); len(got) != 0 {
		t.Fatalf("FilterOnline(nil) = %v, want empty", got)
	}
}
