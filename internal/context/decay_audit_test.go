package context

import (
	"math"
	"testing"
	"time"

	"central-memory/internal/store"
)

func TestAuditDecayConstants(t *testing.T) {
	if DecayPerMonth != 0.95 {
		t.Errorf("DecayPerMonth = %v, want 0.95", DecayPerMonth)
	}
	if DecayPeriodDays != 30.0 {
		t.Errorf("DecayPeriodDays = %v, want 30", DecayPeriodDays)
	}
	if StaleConfidenceThreshold != 0.2 {
		t.Errorf("StaleConfidenceThreshold = %v, want 0.2", StaleConfidenceThreshold)
	}
	if StaleAfterDays != 180.0 {
		t.Errorf("StaleAfterDays = %v, want 180", StaleAfterDays)
	}
}

func TestAuditDecayFormula(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	for _, days := range []float64{0, 15, 30, 45, 90, 180, 365} {
		last := now.Add(-time.Duration(days * 24 * float64(time.Hour)))
		got := EffectiveConfidence(1.0, last, now)
		want := math.Pow(0.95, days/30)
		if math.Abs(got-want) > 1e-9 {
			t.Errorf("days=%.0f: got %v want %v (0.95^(days/30))", days, got, want)
		}
	}
	// Fractional months interpolate continuously (no step quantization).
	got := EffectiveConfidence(1.0, now.Add(-15*24*time.Hour), now)
	if math.Abs(got-math.Pow(0.95, 0.5)) > 1e-9 {
		t.Errorf("15d = %v, want 0.95^0.5", got)
	}
}

func TestAuditDecayFallbackChain(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	old := now.Add(-60 * 24 * time.Hour)
	mid := now.Add(-30 * 24 * time.Hour)
	// LastUsedAt wins over UpdatedAt/CreatedAt.
	it := &store.MemoryItem{Confidence: 1.0, LastUsedAt: old, UpdatedAt: mid, CreatedAt: now}
	if got, want := EffectiveConfidenceForItem(it, now), math.Pow(0.95, 60.0/30); math.Abs(got-want) > 1e-9 {
		t.Errorf("LastUsedAt precedence: got %v want %v", got, want)
	}
	// UpdatedAt fallback.
	it2 := &store.MemoryItem{Confidence: 1.0, UpdatedAt: mid, CreatedAt: now}
	if got, want := EffectiveConfidenceForItem(it2, now), math.Pow(0.95, 1.0); math.Abs(got-want) > 1e-9 {
		t.Errorf("UpdatedAt fallback: got %v want %v", got, want)
	}
	// CreatedAt fallback.
	it3 := &store.MemoryItem{Confidence: 1.0, CreatedAt: mid}
	if got, want := EffectiveConfidenceForItem(it3, now), math.Pow(0.95, 1.0); math.Abs(got-want) > 1e-9 {
		t.Errorf("CreatedAt fallback: got %v want %v", got, want)
	}
	// Fully zero timestamps: no age info -> base undecayed.
	it4 := &store.MemoryItem{Confidence: 0.7}
	if got := EffectiveConfidenceForItem(it4, now); math.Abs(got-0.7) > 1e-6 {
		t.Errorf("zero timestamps: got %v want 0.7", got)
	}
	if got := EffectiveConfidenceForItem(nil, now); got != 0 {
		t.Errorf("nil item = %v, want 0", got)
	}
	if got := DaysSinceUse(nil, now); got != 0 {
		t.Errorf("DaysSinceUse(nil) = %v, want 0", got)
	}
	if got := DaysSinceUse(&store.MemoryItem{}, now); got != 0 {
		t.Errorf("DaysSinceUse(zero) = %v, want 0", got)
	}
}

func TestAuditStaleThresholdBoundary(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	// Exactly 180 idle days with sub-threshold confidence -> stale (>= boundary).
	at180 := &store.MemoryItem{Confidence: 0.2, LastUsedAt: now.Add(-180 * 24 * time.Hour)}
	// 0.2*0.95^6 ~= 0.147 < 0.2, idle 180 -> stale.
	if !FlagStale(at180, now) {
		t.Error("180d boundary should be stale (>= StaleAfterDays)")
	}
	justUnder := &store.MemoryItem{Confidence: 0.2, LastUsedAt: now.Add(-179 * 24 * time.Hour)}
	if FlagStale(justUnder, now) {
		t.Error("179d should not be stale (< 180d)")
	}
	// Either condition alone is insufficient.
	weakFresh := &store.MemoryItem{Confidence: 0.05, LastUsedAt: now}
	if FlagStale(weakFresh, now) {
		t.Error("weak but fresh must stay live")
	}
	strongOld := &store.MemoryItem{Confidence: 1.0, LastUsedAt: now.Add(-400 * 24 * time.Hour)}
	if FlagStale(strongOld, now) {
		t.Errorf("strong dormant (eff=%v) must stay live", EffectiveConfidenceForItem(strongOld, now))
	}
}

func TestAuditSessionExpiryAbsence(t *testing.T) {
	// PIN: decay.go has NO session-expiry concept — FlagStale treats session,
	// personal, project, organization levels identically, and there is no TTL
	// after which a session memory auto-expires. Sessions age only via the
	// generic confidence/idle rule.
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	old := now.Add(-400 * 24 * time.Hour)
	for _, lvl := range []string{"session", "personal", "project", "organization"} {
		a := &store.MemoryItem{Confidence: 1.0, Level: lvl, LastUsedAt: old}
		b := &store.MemoryItem{Confidence: 1.0, Level: lvl, LastUsedAt: now}
		if FlagStale(a, now) != FlagStale(b, now) && lvl == "session" {
			t.Errorf("session level unexpectedly special-cased")
		}
		_ = a
		_ = b
	}
	// A fresh session item is never stale regardless of level.
	fresh := &store.MemoryItem{Confidence: 0.1, Level: "session", LastUsedAt: now}
	if FlagStale(fresh, now) {
		t.Error("fresh low-confidence session item must not be stale (no session TTL)")
	}
}
