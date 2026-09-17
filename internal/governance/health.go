// Package governance health monitoring: confidence-decay auditing, archival
// selection, and dashboard analytics as pure funcs over DTOs (issue #26).
//
// Threshold alignment: StaleConfidenceThreshold (0.2) and StaleAfterDays (180)
// intentionally mirror internal/context/decay.go's FlagStale semantics —
// effective confidence below 0.2 AND 180+ idle days. The decay formula
// (base * 0.95^(days/30)) is duplicated here so this package stays stdlib-only
// and decoupled from the pgx-backed store.
package governance

import (
	"math"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Confidence auditing
// ---------------------------------------------------------------------------

// Staleness thresholds (mirror internal/context/decay.go).
const (
	// DecayPerMonth is the multiplicative retention per 30 idle days.
	DecayPerMonth = 0.95
	// DecayPeriodDays is the idle period one DecayPerMonth factor applies to.
	DecayPeriodDays = 30.0
	// StaleConfidenceThreshold: effective confidence below this flags review.
	StaleConfidenceThreshold = 0.2
	// StaleAfterDays: idle days required before a sub-threshold memory flags.
	StaleAfterDays = 180.0
)

// MemoryDTO is the minimal memory shape auditors/analytics need. Deliberately
// decoupled from internal/store.MemoryItem (which needs pgx/pgvector) so all
// funcs stay pure and stdlib-only.
type MemoryDTO struct {
	Key        string
	Confidence float64
	// LastActive is the idle clock (maps to LastUsedAt → UpdatedAt → CreatedAt).
	LastActive time.Time
	Status     string // PROPOSED | CONFIRMED | REJECTED | SUPERSEDED | ARCHIVED
	UseCount   int
}

// EffectiveConfidence applies geometric decay: base * 0.95^(days/30).
// Zero LastActive carries no age info → base returned undecayed.
func EffectiveConfidence(base float64, lastActive, now time.Time) float64 {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	var days float64
	if !lastActive.IsZero() {
		days = now.Sub(lastActive).Hours() / 24
		if days < 0 {
			days = 0
		}
	}
	return base * math.Pow(DecayPerMonth, days/DecayPeriodDays)
}

// IdleDays reports days since LastActive; zero LastActive reports 0.
func IdleDays(m MemoryDTO, now time.Time) float64 {
	if m.LastActive.IsZero() {
		return 0
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	d := now.Sub(m.LastActive).Hours() / 24
	if d < 0 {
		return 0
	}
	return d
}

// IsStale flags memories needing human review/archival: effective confidence
// < 0.2 AND idle 180+ days. Either condition alone is not enough — a weak but
// actively used memory, or a strong but dormant one, both stay live.
func IsStale(m MemoryDTO, now time.Time) bool {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return EffectiveConfidence(m.Confidence, m.LastActive, now) < StaleConfidenceThreshold &&
		IdleDays(m, now) >= StaleAfterDays
}

// FlagStaleBatch returns the subset needing review, preserving input order.
func FlagStaleBatch(mems []MemoryDTO, now time.Time) []MemoryDTO {
	var out []MemoryDTO
	for _, m := range mems {
		if IsStale(m, now) {
			out = append(out, m)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Archival job: superseded → archive list (never delete)
// ---------------------------------------------------------------------------

// IsArchivable reports whether a memory belongs in archive storage:
// status SUPERSEDED (case-insensitive). ARCHIVED items are already moved and
// never re-selected; all other statuses stay live.
func IsArchivable(m MemoryDTO) bool {
	s := strings.ToUpper(strings.TrimSpace(m.Status))
	return s == "SUPERSEDED"
}

// SelectForArchive filters the archive list, preserving input order.
func SelectForArchive(mems []MemoryDTO) []MemoryDTO {
	var out []MemoryDTO
	for _, m := range mems {
		if IsArchivable(m) {
			out = append(out, m)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Dashboard analytics (pure funcs)
// ---------------------------------------------------------------------------

// DailyEventCounts buckets event timestamps by UTC calendar day (YYYY-MM-DD).
func DailyEventCounts(stamps []time.Time) map[string]int {
	out := make(map[string]int, len(stamps))
	for _, t := range stamps {
		out[t.UTC().Format("2006-01-02")]++
	}
	return out
}

// GrowthPerDay is the linear growth rate (last-first)/days over a count
// series. days <= 0 or fewer than 2 points → 0.
func GrowthPerDay(counts []int, days float64) float64 {
	if len(counts) < 2 || days <= 0 {
		return 0
	}
	return float64(counts[len(counts)-1]-counts[0]) / days
}

// RelativeGrowth is (last-first)/first; first <= 0 → 0 (no baseline).
func RelativeGrowth(first, last int) float64 {
	if first <= 0 {
		return 0
	}
	return float64(last-first) / float64(first)
}

// ExtractionAccuracy is confirmed/proposed in [0,1]; proposed <= 0 → 0.
func ExtractionAccuracy(confirmed, proposed int) float64 {
	if proposed <= 0 {
		return 0
	}
	if confirmed < 0 {
		confirmed = 0
	}
	if confirmed > proposed {
		confirmed = proposed
	}
	return float64(confirmed) / float64(proposed)
}
