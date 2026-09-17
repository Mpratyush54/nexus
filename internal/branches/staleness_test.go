package branches

import (
	"testing"
	"time"
)

func TestEffectiveConfidenceDecayCurve(t *testing.T) {
	if got := EffectiveConfidence(1.0, 0); got != 1.0 {
		t.Fatalf("day 0 = %v, want 1.0", got)
	}
	// Plan §1.7: ~86% at 90 days, ~74% at 180 days.
	if got := EffectiveConfidence(1.0, 90); got < 0.85 || got > 0.87 {
		t.Fatalf("day 90 = %v, want ~0.86", got)
	}
	if got := EffectiveConfidence(1.0, 180); got < 0.73 || got > 0.75 {
		t.Fatalf("day 180 = %v, want ~0.74", got)
	}
}

func TestDetectStalenessFlagsNeglected(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	cands := []StaleCandidate{{
		Key:        "testing/framework",
		Content:    "we use pytest with fixtures",
		Confidence: 0.25, // 0.25 * 0.95^(200/30) ≈ 0.177 < 0.2
		UseCount:   0,
		LastUsedAt: now.AddDate(0, 0, -200),
		ForkedAt:   now.AddDate(0, 0, -400),
	}}
	flags := DetectStaleness(cands, now)
	if len(flags) != 1 || !flags[0].Stale {
		t.Fatalf("neglected item not flagged: %+v", flags)
	}
	if len(flags[0].Reasons) != 1 || flags[0].Reasons[0] != ReasonNeglected {
		t.Fatalf("reasons = %v, want [%q]", flags[0].Reasons, ReasonNeglected)
	}
}

func TestDetectStalenessFreshItemClean(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	cands := []StaleCandidate{{
		Key:        "testing/framework",
		Content:    "we use pytest with fixtures",
		Confidence: 0.95,
		UseCount:   12,
		LastUsedAt: now.AddDate(0, 0, -1),
		ForkedAt:   now.AddDate(0, 0, -400),
	}}
	flags := DetectStaleness(cands, now)
	if len(flags) != 1 || flags[0].Stale {
		t.Fatalf("fresh item flagged: %+v", flags)
	}
}

func TestDetectStalenessParentAdvanced(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	fork := now.AddDate(0, 0, -30)
	parentUpdate := now.AddDate(0, 0, -5)
	cands := []StaleCandidate{{
		Key:             "security/auth",
		Content:         "all APIs must use JWT",
		Confidence:      1.0, // high confidence must NOT suppress the flag
		UseCount:        40,
		LastUsedAt:      now.Add(-time.Hour),
		ForkedAt:        fork,
		ParentUpdatedAt: &parentUpdate,
	}}
	flags := DetectStaleness(cands, now)
	if len(flags) != 1 || !flags[0].Stale {
		t.Fatalf("parent-advanced item not flagged: %+v", flags)
	}
	found := false
	for _, r := range flags[0].Reasons {
		if r == ReasonParentAdvanced {
			found = true
		}
	}
	if !found {
		t.Fatalf("reasons = %v, want parent-advanced", flags[0].Reasons)
	}
}

func TestDetectStalenessParentUpdateBeforeForkClean(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	fork := now.AddDate(0, 0, -30)
	parentUpdate := now.AddDate(0, 0, -60) // before fork: already incorporated
	cands := []StaleCandidate{{
		Key:             "security/auth",
		Content:         "all APIs must use JWT",
		Confidence:      1.0,
		UseCount:        40,
		LastUsedAt:      now.Add(-time.Hour),
		ForkedAt:        fork,
		ParentUpdatedAt: &parentUpdate,
	}}
	flags := DetectStaleness(cands, now)
	if len(flags) != 1 || flags[0].Stale {
		t.Fatalf("pre-fork parent update flagged: %+v", flags)
	}
}
