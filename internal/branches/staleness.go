// Staleness detection for Phase 5 (implementation-plan §5.4, §1.7, §2.7).
//
// A child-branch memory goes stale for two independent reasons, and either
// one alone is sufficient to flag it:
//
//  1. Neglect: the item's effective confidence (plan §1.7 decay) has fallen
//     below 0.2 after 180+ days without reuse (plan §2.7 archival rule).
//  2. Parent advancement: the parent branch updated the same key after the
//     child's fork point, so the child's copy may no longer reflect team
//     consensus (plan §5.4 potentially_stale rule).
package branches

import (
	"math"
	"sort"
	"time"
)

const (
	// StaleConfidenceThreshold is the effective-confidence floor below which
	// a long-idle, never-reused item is flagged (plan §2.7).
	StaleConfidenceThreshold = 0.2
	// StaleIdleDays is the reuse-free idle period after which a
	// below-threshold item is flagged for review/archival (plan §1.7/§2.7).
	StaleIdleDays = 180.0
	// decayFactor and decayPeriodDays reproduce the plan §1.7 decay curve:
	// effective = base * 0.95^(idleDays/30).
	decayFactor     = 0.95
	decayPeriodDays = 30.0
)

const (
	// ReasonNeglected marks items that decayed below the confidence floor
	// after 180+ idle days with zero reuse.
	ReasonNeglected = "neglected: effective confidence below 0.2 after 180+ days idle with zero reuse"
	// ReasonParentAdvanced marks child items whose key the parent branch
	// updated after the fork point.
	ReasonParentAdvanced = "parent-advanced: parent branch updated this key after the fork point"
)

// EffectiveConfidence applies the plan §1.7 decay curve:
// base * 0.95^(idleDays/30). Negative idle clamps to 0; base clamps to
// [0, 1] so uncalibrated inputs cannot produce absurd outputs.
func EffectiveConfidence(baseConfidence, idleDays float64) float64 {
	if idleDays < 0 {
		idleDays = 0
	}
	if baseConfidence < 0 {
		baseConfidence = 0
	}
	if baseConfidence > 1 {
		baseConfidence = 1
	}
	return baseConfidence * math.Pow(decayFactor, idleDays/decayPeriodDays)
}

// StaleCandidate is one child-branch item evaluated for staleness.
// ParentUpdatedAt is nil when the parent holds no newer version of the key;
// ForkedAt is the child's fork point (forked_at_event time).
type StaleCandidate struct {
	Key             string
	Content         string
	Confidence      float64
	UseCount        int
	LastUsedAt      time.Time
	UpdatedAt       time.Time
	ForkedAt        time.Time
	ParentUpdatedAt *time.Time
}

// StaleFlag is the verdict for one candidate.
type StaleFlag struct {
	Key                 string
	Stale               bool
	Reasons             []string
	EffectiveConfidence float64
	IdleDays            float64
}

// DetectStaleness flags every candidate that is stale by neglect, by parent
// advancement, or both. Results are sorted by key for deterministic output.
// A nil or empty input returns an empty (non-nil) slice.
func DetectStaleness(cands []StaleCandidate, now time.Time) []StaleFlag {
	out := make([]StaleFlag, 0, len(cands))
	for _, c := range cands {
		// Last-touch ordering mirrors plan §1.7 ("days since last used"):
		// explicit reuse first, then row update, then the fork point itself.
		lastTouch := c.LastUsedAt
		if lastTouch.IsZero() {
			lastTouch = c.UpdatedAt
		}
		if lastTouch.IsZero() {
			lastTouch = c.ForkedAt
		}
		idle := now.Sub(lastTouch).Hours() / 24
		if idle < 0 {
			idle = 0
		}
		eff := EffectiveConfidence(c.Confidence, idle)

		var reasons []string
		if eff < StaleConfidenceThreshold && idle >= StaleIdleDays && c.UseCount == 0 {
			reasons = append(reasons, ReasonNeglected)
		}
		if c.ParentUpdatedAt != nil && !c.ParentUpdatedAt.IsZero() && c.ParentUpdatedAt.After(c.ForkedAt) {
			reasons = append(reasons, ReasonParentAdvanced)
		}

		out = append(out, StaleFlag{
			Key:                 c.Key,
			Stale:               len(reasons) > 0,
			Reasons:             reasons,
			EffectiveConfidence: eff,
			IdleDays:            idle,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}
