package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"central-memory/internal/store"
)

func validAgentSourceTime(at *time.Time) *time.Time {
	if at == nil || at.Year() < 2000 || at.After(time.Now().Add(5*time.Minute)) {
		return nil
	}
	utc := at.UTC().Truncate(time.Microsecond)
	return &utc
}

func transcriptSourceTime(entry map[string]any) *time.Time {
	for _, key := range []string{"timestamp", "ts", "at", "createdAt", "time"} {
		value := entry[key]
		if text, ok := value.(string); ok {
			if at, err := time.Parse(time.RFC3339Nano, text); err == nil {
				if valid := validAgentSourceTime(&at); valid != nil {
					return valid
				}
			}
		}
		if n, ok := value.(float64); ok {
			for n > 1e11 {
				n /= 1000
			}
			at := time.Unix(int64(n), int64((n-float64(int64(n)))*1e9))
			if valid := validAgentSourceTime(&at); valid != nil {
				return valid
			}
		}
		if nested, ok := value.(map[string]any); ok {
			if at := transcriptSourceTime(map[string]any{"timestamp": nested["created"]}); at != nil {
				return at
			}
		}
	}
	for _, key := range []string{"payload", "message", "info"} {
		if nested, ok := entry[key].(map[string]any); ok {
			if at := transcriptSourceTime(nested); at != nil {
				return at
			}
		}
	}
	return nil
}

// activityCalendar never uses upload or server event dates. Modern captures
// carry source_ts. Archived transcripts recover original dates read-only.
// Original message fingerprints deduplicate legacy/modern copies; uploads
// never manufacture extra activity, and new messages are still additive.
func (s *Server) activityCalendar(r *http.Request, project string, now time.Time) (map[string]any, error) {
	offset, _ := strconv.Atoi(r.URL.Query().Get("utc_offset_minutes"))
	if offset < -840 || offset > 840 {
		offset = 0
	}
	zone := time.FixedZone("activity", offset*60)
	today := now.In(zone)
	end := time.Date(today.Year(), today.Month(), today.Day()+1, 0, 0, 0, 0, zone)
	start := end.AddDate(0, 0, -365)
	owner := authSubject(r)
	counts := map[[3]string]map[string]bool{}
	undated := map[[2]string]int{}
	if activity, ok := s.Store.(store.AgentActivityStore); ok {
		rows, err := activity.ListAgentActivity(r.Context(), project, owner, start, end, offset)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if row.Date == "" {
				undated[[2]string{row.Harness, row.NativeID}] += row.Count
				continue
			}
			key := [3]string{row.Harness, row.NativeID, row.Date}
			if counts[key] == nil {
				counts[key] = map[string]bool{}
			}
			for _, fingerprint := range row.Fingerprints {
				counts[key][fingerprint] = true
			}
		}
	}
	partial := false
	if ps := provenanceStore(s.Store); ps != nil {
		snapshots, err := ps.ListSnapshotsForProject(r.Context(), project, 10000)
		if err != nil {
			return nil, err
		}
		partial = len(snapshots) >= 10000
		for _, snap := range snapshots {
			if snap.OwnerUserID != owner {
				continue
			}
			if err := r.Context().Err(); err != nil {
				return nil, err
			}
			archive, err := s.readLegacyActivity(r, snap)
			if err != nil {
				return nil, err
			}
			partial = partial || archive.Partial
			for _, source := range archive.Sources {
				at, key := source.At, source.Fingerprint
				identity := [2]string{snap.Harness, snap.ConversationID}
				if undated[identity] > 0 {
					undated[identity]--
				}
				if at.Before(start) || !at.Before(end) {
					continue
				}
				bucket := [3]string{snap.Harness, snap.ConversationID, at.In(zone).Format("2006-01-02")}
				if counts[bucket] == nil {
					counts[bucket] = map[string]bool{}
				}
				counts[bucket][key] = true
			}
			identity := [2]string{snap.Harness, snap.ConversationID}
			if archive.Undated > undated[identity] {
				undated[identity] = archive.Undated
			}
		}
	}
	dayCounts := map[string]int{}
	for key, identities := range counts {
		dayCounts[key[2]] += len(identities)
	}
	days := []map[string]any{}
	total, active, unknown := 0, 0, 0
	for day := start; day.Before(end); day = day.AddDate(0, 0, 1) {
		date := day.Format("2006-01-02")
		count := dayCounts[date]
		days = append(days, map[string]any{"date": date, "count": count})
		total += count
		if count > 0 {
			active++
		}
	}
	for _, count := range undated {
		unknown += count
	}
	return map[string]any{"days": days, "total": total, "active_days": active, "undated_messages": unknown,
		"timestamp_basis": "agent_source_time", "scope": "your_project_messages", "utc_offset_minutes": offset, "partial": partial}, nil
}

type activitySource struct {
	At          time.Time
	Fingerprint string
}
type cachedLegacyActivity struct {
	Sources []activitySource
	Undated int
	Partial bool
}

func (s *Server) readLegacyActivity(r *http.Request, snap store.SessionSnapshot) (cachedLegacyActivity, error) {
	key := fmt.Sprintf("%s|%s|%s|%d|%s", snap.ProjectID, snap.OwnerUserID, snap.SessionID, snap.SnapshotVersion, snap.UpdatedAt.UTC().Format(time.RFC3339Nano))
	s.activityMu.Lock()
	cached, found := s.activityCache[key]
	s.activityMu.Unlock()
	if found {
		return cached, nil
	}
	full, err := provenanceStore(s.Store).GetLatestSnapshot(r.Context(), snap.SessionID)
	if err != nil {
		return cached, err
	}
	if full == nil {
		return cached, nil
	}
	raw, err := gunzipSnapshot(full.TranscriptPayload)
	if err != nil {
		cached.Partial = true
		return cached, nil
	}
	cached.Partial = len(raw) >= 64<<20
	seen := map[string]bool{}
	for i, line := range strings.Split(string(raw), "\n") {
		if i%1024 == 0 {
			if err := r.Context().Err(); err != nil {
				return cached, err
			}
		}
		var entry map[string]any
		if json.Unmarshal([]byte(line), &entry) != nil {
			continue
		}
		turn, ok := transcriptTurn(entry)
		if !ok || (turn.Role != "user" && turn.Role != "assistant") {
			continue
		}
		at := transcriptSourceTime(entry)
		if at == nil {
			cached.Undated++
			continue
		}
		fingerprint := store.ActivityFingerprint(*at, turn.Role, turn.TextPreview)
		if seen[fingerprint] {
			continue
		}
		seen[fingerprint] = true
		cached.Sources = append(cached.Sources, activitySource{At: *at, Fingerprint: fingerprint})
	}
	s.activityMu.Lock()
	defer s.activityMu.Unlock()
	if len(s.activityCache) >= 1024 || s.activityCacheEntries+len(cached.Sources) > 200000 {
		s.activityCache = nil
		s.activityCacheEntries = 0
	}
	if len(cached.Sources) <= 200000 {
		if s.activityCache == nil {
			s.activityCache = map[string]cachedLegacyActivity{}
		}
		if _, exists := s.activityCache[key]; !exists {
			s.activityCache[key] = cached
			s.activityCacheEntries += len(cached.Sources)
		}
	}
	return cached, nil
}
