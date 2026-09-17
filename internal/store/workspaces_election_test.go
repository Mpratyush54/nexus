package store

// Pure tests for the #36 election helpers: threshold predicates and
// app-side filtering. All DB-free with a fixed clock. The PostgresStore
// election writes (ElectDesignatedProcessor/ReassignStaleDesignated) need a
// live Postgres and are covered by integration runs gated on TEST_POSTGRES_DSN.

import (
	"testing"
	"time"
)

func TestOfflineThresholdIs90s(t *testing.T) {
	if OfflineThreshold != 90*time.Second {
		t.Fatalf("OfflineThreshold = %v, want 90s (plan: offline after 90s silence)", OfflineThreshold)
	}
}

func TestIsOnlineAt(t *testing.T) {
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
		{"zero time is stale", time.Time{}, false},
		{"future timestamp (clock skew) is online", now.Add(60 * time.Second), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsOnlineAt(tc.lastSeen, now); got != tc.online {
				t.Errorf("IsOnlineAt(%v) = %v, want %v", tc.lastSeen, got, tc.online)
			}
			if got := IsStaleAt(tc.lastSeen, now); got == tc.online {
				t.Errorf("IsStaleAt(%v) = %v, want negation of %v", tc.lastSeen, got, tc.online)
			}
		})
	}
}

func TestIsOnlineAtPtr(t *testing.T) {
	now := time.Now().UTC()
	if IsOnlineAtPtr(nil, now) {
		t.Error("nil last_seen must be offline")
	}
	ts := now.Add(-10 * time.Second)
	if !IsOnlineAtPtr(&ts, now) {
		t.Error("fresh timestamp must be online")
	}
	stale := now.Add(-5 * time.Minute)
	if IsOnlineAtPtr(&stale, now) {
		t.Error("stale timestamp must be offline")
	}
}

func TestFilterOnline(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	ws := []Workspace{
		{ID: "fresh", LastSeen: now.Add(-10 * time.Second)},
		{ID: "edge", LastSeen: now.Add(-90 * time.Second)},
		{ID: "stale", LastSeen: now.Add(-91 * time.Second)},
		{ID: "never"},
	}
	got := FilterOnline(ws, now)
	if len(got) != 2 || got[0].ID != "fresh" || got[1].ID != "edge" {
		t.Fatalf("FilterOnline = %+v, want [fresh edge]", got)
	}
}
