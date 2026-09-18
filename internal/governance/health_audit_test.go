package governance

import (
	"math"
	"testing"
	"time"
)

func TestAuditHealthDecayParity(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	for _, days := range []float64{0, 30, 90, 180} {
		last := now.Add(-time.Duration(days * 24 * float64(time.Hour)))
		if got, want := EffectiveConfidence(1.0, last, now), math.Pow(0.95, days/30); math.Abs(got-want) > 1e-9 {
			t.Errorf("days=%.0f got %v want %v", days, got, want)
		}
	}
	// Zero LastActive -> undecayed base.
	if got := EffectiveConfidence(0.6, time.Time{}, now); math.Abs(got-0.6) > 1e-9 {
		t.Errorf("zero LastActive = %v, want 0.6", got)
	}
	// Future clamps.
	if got := EffectiveConfidence(0.6, now.Add(time.Hour), now); math.Abs(got-0.6) > 1e-9 {
		t.Errorf("future = %v, want 0.6", got)
	}
	if DecayPerMonth != 0.95 || DecayPeriodDays != 30.0 || StaleConfidenceThreshold != 0.2 || StaleAfterDays != 180.0 {
		t.Error("health thresholds drifted from context/decay.go (must mirror 0.95/30/0.2/180)")
	}
}

func TestAuditIsStaleBothConditions(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	old := now.Add(-200 * 24 * time.Hour)
	if !IsStale(MemoryDTO{Key: "s", Confidence: 0.2, LastActive: old}, now) {
		t.Error("weak+old must be stale")
	}
	if IsStale(MemoryDTO{Key: "f", Confidence: 0.05, LastActive: now}, now) {
		t.Error("weak+fresh stays live")
	}
	if IsStale(MemoryDTO{Key: "d", Confidence: 1.0, LastActive: old}, now) {
		t.Error("strong+dormant stays live")
	}
	// IdleDays zero-time -> 0; future -> 0.
	if got := IdleDays(MemoryDTO{}, now); got != 0 {
		t.Errorf("zero LastActive idle = %v, want 0", got)
	}
	if got := IdleDays(MemoryDTO{LastActive: now.Add(time.Hour)}, now); got != 0 {
		t.Errorf("future idle = %v, want 0", got)
	}
	// Batch preserves input order.
	mems := []MemoryDTO{
		{Key: "b-stale", Confidence: 0.1, LastActive: old},
		{Key: "a-live", Confidence: 1.0, LastActive: now},
		{Key: "c-stale", Confidence: 0.1, LastActive: old},
	}
	got := FlagStaleBatch(mems, now)
	if len(got) != 2 || got[0].Key != "b-stale" || got[1].Key != "c-stale" {
		t.Errorf("batch order/content wrong: %+v", got)
	}
}

func TestAuditArchivalSelection(t *testing.T) {
	if !IsArchivable(MemoryDTO{Status: "SUPERSEDED"}) || !IsArchivable(MemoryDTO{Status: "superseded"}) {
		t.Error("SUPERSEDED (any case) must be archivable")
	}
	for _, s := range []string{"ARCHIVED", "CONFIRMED", "PROPOSED", "REJECTED", "", " superseded-extra "} {
		if s == "SUPERSEDED" {
			continue
		}
		if IsArchivable(MemoryDTO{Status: s}) {
			t.Errorf("status %q must not be archivable", s)
		}
	}
	in := []MemoryDTO{{Key: "a", Status: "SUPERSEDED"}, {Key: "b", Status: "CONFIRMED"}, {Key: "c", Status: "SUPERSEDED"}}
	got := SelectForArchive(in)
	if len(got) != 2 || got[0].Key != "a" || got[1].Key != "c" {
		t.Errorf("archive order wrong: %+v", got)
	}
}

func TestAuditAnalytics(t *testing.T) {
	d1 := time.Date(2026, 9, 15, 23, 0, 0, 0, time.UTC)
	d2 := time.Date(2026, 9, 16, 1, 0, 0, 0, time.UTC)
	counts := DailyEventCounts([]time.Time{d1, d1, d2})
	if counts["2026-09-15"] != 2 || counts["2026-09-16"] != 1 {
		t.Errorf("UTC day bucketing wrong: %+v", counts)
	}
	if got := GrowthPerDay([]int{10, 20}, 2); got != 5 {
		t.Errorf("GrowthPerDay = %v, want 5", got)
	}
	if GrowthPerDay([]int{5}, 1) != 0 || GrowthPerDay([]int{1, 2}, 0) != 0 {
		t.Error("GrowthPerDay degnerate inputs must be 0")
	}
	if got := RelativeGrowth(100, 130); math.Abs(got-0.3) > 1e-9 {
		t.Errorf("RelativeGrowth = %v, want 0.3", got)
	}
	if RelativeGrowth(0, 5) != 0 {
		t.Error("zero baseline must be 0")
	}
	if got := ExtractionAccuracy(3, 4); math.Abs(got-0.75) > 1e-9 {
		t.Errorf("accuracy = %v, want 0.75", got)
	}
	if ExtractionAccuracy(1, 0) != 0 {
		t.Error("zero proposed must be 0")
	}
	if got := ExtractionAccuracy(9, 4); got != 1.0 {
		t.Errorf("confirmed>proposed must clamp to 1: %v", got)
	}
	if got := ExtractionAccuracy(-2, 4); got != 0 {
		t.Errorf("negative confirmed must clamp to 0: %v", got)
	}
}
