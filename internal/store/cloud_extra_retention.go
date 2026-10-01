package store

import (
	"time"

	"central-memory/internal/capture"
)

func (m *MemStore) pruneVersionsLocked(now time.Time) int {
	n := 0
	for sid, vers := range m.sessionVersions {
		stamps := make([]capture.VersionStamp, 0, len(vers))
		for _, v := range vers {
			stamps = append(stamps, capture.VersionStamp{ID: v.ID, At: v.CreatedAt})
		}
		dropIDs := ApplyRetention(stamps, now)
		if len(dropIDs) == 0 {
			continue
		}
		drop := make(map[string]bool, len(dropIDs))
		for _, id := range dropIDs {
			drop[id] = true
		}
		kept := make([]SessionVersion, 0, len(vers))
		for _, v := range vers {
			if drop[v.ID] {
				n++
				continue
			}
			kept = append(kept, v)
		}
		m.sessionVersions[sid] = kept
	}
	return n
}
