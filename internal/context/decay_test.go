package context

import (
	"math"
	"testing"
	"time"

	"central-memory/internal/store"
)

func TestEffectiveConfidenceMath(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		idle time.Duration
		want float64
	}{
		{"fresh", 0, 1.0},
		{"one month", 30 * 24 * time.Hour, 0.95},
		{"three months", 90 * 24 * time.Hour, math.Pow(0.95, 3)},
		{"six months", 180 * 24 * time.Hour, math.Pow(0.95, 6)},
	}
	for _, c := range cases {
		got := EffectiveConfidence(1.0, now.Add(-c.idle), now)
		if math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
	// Zero lastUsed carries no age information: undecayed (float32 input,
	// so compare with float32-scale tolerance).
	if got := EffectiveConfidence(0.8, time.Time{}, now); math.Abs(got-0.8) > 1e-6 {
		t.Errorf("zero lastUsed: got %v, want 0.8", got)
	}
	// Future timestamps clamp to zero days.
	if got := EffectiveConfidence(0.8, now.Add(24*time.Hour), now); math.Abs(got-0.8) > 1e-6 {
		t.Errorf("future lastUsed: got %v, want 0.8", got)
	}
	// Base scales linearly.
	if got := EffectiveConfidence(0.5, now.Add(-30*24*time.Hour), now); math.Abs(got-0.475) > 1e-6 {
		t.Errorf("scaled base: got %v, want 0.475", got)
	}
}

func TestFlagStale(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	old := now.Add(-200 * 24 * time.Hour)
	recent := now.Add(-24 * time.Hour)

	stale := &store.MemoryItem{Confidence: 0.2, LastUsedAt: old}
	// 0.2 * 0.95^(200/30) ~ 0.142 < 0.2 and idle 200d -> stale.
	if !FlagStale(stale, now) {
		t.Error("weak long-unused item should be stale")
	}
	used := &store.MemoryItem{Confidence: 0.2, LastUsedAt: recent}
	if FlagStale(used, now) {
		t.Error("recently used item must not be stale")
	}
	strong := &store.MemoryItem{Confidence: 1.0, LastUsedAt: old}
	// 1.0 * 0.95^(200/30) ~ 0.71 > 0.2 -> not stale despite age.
	if FlagStale(strong, now) {
		t.Error("high-confidence item must not be stale")
	}
	if FlagStale(nil, now) {
		t.Error("nil item must not be stale")
	}
}
