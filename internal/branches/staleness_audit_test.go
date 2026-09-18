package branches

import (
	"math"
	"testing"
	"time"
)

func auditTime(daysAgo int, now time.Time) time.Time {
	return now.Add(-time.Duration(daysAgo) * 24 * time.Hour)
}

func TestAuditStaleEffectiveConfidenceCurve(t *testing.T) {
	for _, days := range []float64{0, 30, 90, 180} {
		got := EffectiveConfidence(1.0, days)
		want := math.Pow(0.95, days/30)
		if math.Abs(got-want) > 1e-9 {
			t.Errorf("days=%.0f got %v want %v", days, got, want)
		}
	}
	if got := EffectiveConfidence(1.0, -5); got != 1.0 {
		t.Errorf("negative idle must clamp to 0: %v", got)
	}
	if got := EffectiveConfidence(-0.5, 10); got != 0 {
		t.Errorf("negative base must clamp to 0: %v", got)
	}
	if got := EffectiveConfidence(2.0, 30); math.Abs(got-0.95) > 1e-9 {
		t.Errorf("base>1 must clamp to 1 before decay: %v", got)
	}
	if StaleConfidenceThreshold != 0.2 || StaleIdleDays != 180.0 {
		t.Errorf("thresholds = %v/%v, want 0.2/180", StaleConfidenceThreshold, StaleIdleDays)
	}
}

func TestAuditStaleNeglectRequiresAllThree(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	mk := func(conf float64, idleDays int, uses int) StaleCandidate {
		return StaleCandidate{Key: "k", Confidence: conf, UseCount: uses, LastUsedAt: auditTime(idleDays, now), ForkedAt: auditTime(400, now)}
	}
	// All three true -> neglected.
	flags := DetectStaleness([]StaleCandidate{mk(0.1, 200, 0)}, now)
	if len(flags) != 1 || !flags[0].Stale || len(flags[0].Reasons) != 1 {
		t.Fatalf("neglect triple should flag: %+v", flags)
	}
	if flags[0].Reasons[0] != ReasonNeglected {
		t.Errorf("reason = %q, want neglected", flags[0].Reasons[0])
	}
	// Reused item (UseCount>0) never neglected even if weak+old.
	flags = DetectStaleness([]StaleCandidate{mk(0.1, 400, 3)}, now)
	if flags[0].Stale {
		t.Errorf("reused item must not be neglected: %+v", flags[0])
	}
	// Young item never neglected.
	flags = DetectStaleness([]StaleCandidate{mk(0.05, 10, 0)}, now)
	if flags[0].Stale {
		t.Errorf("young weak item must not be neglected: %+v", flags[0])
	}
	// Strong dormant item never neglected.
	flags = DetectStaleness([]StaleCandidate{mk(1.0, 400, 0)}, now)
	if flags[0].Stale {
		t.Errorf("strong dormant must not be neglected: %+v", flags[0])
	}
}

func TestAuditStaleParentAdvanced(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	fork := auditTime(30, now)
	after := auditTime(5, now)
	before := auditTime(60, now)
	fresh := StaleCandidate{Key: "k", Confidence: 1.0, UseCount: 5, LastUsedAt: now, ForkedAt: fork, ParentUpdatedAt: &after}
	flags := DetectStaleness([]StaleCandidate{fresh}, now)
	if len(flags) != 1 || !flags[0].Stale {
		t.Fatalf("parent-advanced must flag even fresh/reused items: %+v", flags)
	}
	found := false
	for _, r := range flags[0].Reasons {
		if r == ReasonParentAdvanced {
			found = true
		}
	}
	if !found {
		t.Errorf("reasons missing parent-advanced: %+v", flags[0].Reasons)
	}
	// Parent update before/at fork -> clean. Nil parent -> clean.
	stale := StaleCandidate{Key: "k", Confidence: 1.0, UseCount: 5, LastUsedAt: now, ForkedAt: fork, ParentUpdatedAt: &before}
	if flags := DetectStaleness([]StaleCandidate{stale}, now); flags[0].Stale {
		t.Errorf("pre-fork parent update must be clean: %+v", flags[0])
	}
	none := StaleCandidate{Key: "k", Confidence: 1.0, UseCount: 5, LastUsedAt: now, ForkedAt: fork}
	if flags := DetectStaleness([]StaleCandidate{none}, now); flags[0].Stale {
		t.Errorf("nil parent must be clean: %+v", flags[0])
	}
	// Both reasons together.
	both := StaleCandidate{Key: "k", Confidence: 0.05, UseCount: 0, LastUsedAt: auditTime(300, now), ForkedAt: fork, ParentUpdatedAt: &after}
	if flags := DetectStaleness([]StaleCandidate{both}, now); len(flags[0].Reasons) != 2 {
		t.Errorf("both reasons expected: %+v", flags[0])
	}
}

func TestAuditStaleLastTouchFallbackAndSort(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	// UpdatedAt used when LastUsedAt zero; ForkedAt when both zero.
	c := StaleCandidate{Key: "k", Confidence: 1.0, UpdatedAt: auditTime(60, now), ForkedAt: auditTime(300, now)}
	flags := DetectStaleness([]StaleCandidate{c}, now)
	if math.Abs(flags[0].IdleDays-60) > 0.01 {
		t.Errorf("UpdatedAt fallback idle = %v, want ~60", flags[0].IdleDays)
	}
	c2 := StaleCandidate{Key: "k", Confidence: 1.0, ForkedAt: auditTime(45, now)}
	flags = DetectStaleness([]StaleCandidate{c2}, now)
	if math.Abs(flags[0].IdleDays-45) > 0.01 {
		t.Errorf("ForkedAt fallback idle = %v, want ~45", flags[0].IdleDays)
	}
	// Future touch clamps to 0 idle.
	c3 := StaleCandidate{Key: "k", Confidence: 1.0, LastUsedAt: now.Add(24 * time.Hour), ForkedAt: auditTime(10, now)}
	if flags := DetectStaleness([]StaleCandidate{c3}, now); flags[0].IdleDays != 0 {
		t.Errorf("future touch must clamp to 0: %+v", flags[0])
	}
	// Sorted by key; nil/empty -> empty non-nil.
	in := []StaleCandidate{
		{Key: "z", Confidence: 1.0, LastUsedAt: now, ForkedAt: now},
		{Key: "a", Confidence: 1.0, LastUsedAt: now, ForkedAt: now},
	}
	got := DetectStaleness(in, now)
	if len(got) != 2 || got[0].Key != "a" || got[1].Key != "z" {
		t.Errorf("results must be sorted by key: %+v", got)
	}
	if got := DetectStaleness(nil, now); got == nil || len(got) != 0 {
		t.Errorf("nil input must return empty non-nil slice: %#v", got)
	}
}
