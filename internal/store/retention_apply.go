package store

import (
	"time"

	"central-memory/internal/capture"
)

// ApplyRetention returns the IDs of versions D10 would delete.
// capture.RetainVersions keeps every version from the last 90 days and,
// before that, the latest version on each UTC day. IDs not in that set
// are returned in input order. The version record is capture.VersionStamp.
func ApplyRetention(versions []capture.VersionStamp, now time.Time) []string {
	keep := capture.RetainVersions(versions, now)
	kept := make(map[string]bool, len(keep))
	for _, id := range keep {
		kept[id] = true
	}
	var drop []string
	seen := make(map[string]bool, len(versions))
	for _, v := range versions {
		if kept[v.ID] || seen[v.ID] {
			continue
		}
		seen[v.ID] = true
		drop = append(drop, v.ID)
	}
	return drop
}
