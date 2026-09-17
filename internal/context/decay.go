// Confidence decay for Central Memory (implementation-plan.md Phase 1.7).
//
// A memory's served confidence decays geometrically with disuse:
//
//	effective = base * 0.95^(daysSinceLastUse / 30)
//
// i.e. a 5% haircut per idle month. Items whose effective confidence drops
// below 0.2 after 180+ idle days are flagged stale for review/archival.
package context

import (
	"math"
	"time"

	"central-memory/internal/store"
)

// DecayPerMonth is the multiplicative retention per 30 idle days.
const DecayPerMonth = 0.95

// DecayPeriodDays is the idle period one DecayPerMonth factor applies to.
const DecayPeriodDays = 30.0

// StaleConfidenceThreshold marks the effective confidence below which an
// long-unused memory is considered stale.
const StaleConfidenceThreshold = 0.2

// StaleAfterDays is the idle time after which a sub-threshold memory is
// flagged for review/archival.
const StaleAfterDays = 180.0

// EffectiveConfidence applies geometric decay to a base confidence:
// base * 0.95^(days/30), where days is now-lastUsed in days. A zero
// lastUsed carries no disuse information, so the base is returned
// undecayed; future timestamps clamp to zero days.
func EffectiveConfidence(base float32, lastUsed, now time.Time) float64 {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	var days float64
	if !lastUsed.IsZero() {
		days = now.Sub(lastUsed).Hours() / 24
		if days < 0 {
			days = 0
		}
	}
	return float64(base) * math.Pow(DecayPerMonth, days/DecayPeriodDays)
}

// EffectiveConfidenceForItem decays item.Confidence by its idle time. The
// idle clock reads LastUsedAt, falling back to UpdatedAt then CreatedAt so
// never-touched items still age; a fully zero timestamp means "no age
// information" and returns the base confidence.
func EffectiveConfidenceForItem(item *store.MemoryItem, now time.Time) float64 {
	if item == nil {
		return 0
	}
	last := item.LastUsedAt
	if last.IsZero() {
		last = item.UpdatedAt
	}
	if last.IsZero() {
		last = item.CreatedAt
	}
	return EffectiveConfidence(item.Confidence, last, now)
}

// DaysSinceUse reports idle days from LastUsedAt (falling back to
// UpdatedAt, then CreatedAt). Zero timestamps report 0.
func DaysSinceUse(item *store.MemoryItem, now time.Time) float64 {
	if item == nil {
		return 0
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	last := item.LastUsedAt
	if last.IsZero() {
		last = item.UpdatedAt
	}
	if last.IsZero() {
		last = item.CreatedAt
	}
	if last.IsZero() {
		return 0
	}
	days := now.Sub(last).Hours() / 24
	if days < 0 {
		return 0
	}
	return days
}

// FlagStale reports whether an item needs review/archival: effective
// confidence below 0.2 AND idle for 180+ days. Either condition alone is
// not enough — a weak but actively used memory, or a strong but dormant
// one, both stay live.
func FlagStale(item *store.MemoryItem, now time.Time) bool {
	if item == nil {
		return false
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return EffectiveConfidenceForItem(item, now) < StaleConfidenceThreshold &&
		DaysSinceUse(item, now) >= StaleAfterDays
}
