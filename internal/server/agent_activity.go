package server

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
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
//
// Modern messages are counted per day in SQL; only conversations that also
// have an archived transcript come back with fingerprints. Archives are read
// (gunzip + parse) for at most legacyInlineBudget per request: the rest are
// read in the background and the answer says archive_pending until then.
func (s *Server) activityCalendar(ctx context.Context, project, owner string, offset int, now time.Time) (map[string]any, error) {
	if offset < -840 || offset > 840 {
		offset = 0
	}
	zone := time.FixedZone("activity", offset*60)
	today := now.In(zone)
	end := time.Date(today.Year(), today.Month(), today.Day()+1, 0, 0, 0, 0, zone)
	start := end.AddDate(0, 0, -365)
	dayCounts := map[string]int{}
	counts := map[[3]string]map[uint64]struct{}{}
	undated := map[[2]string]int{}
	unknown := 0
	var rows []store.AgentActivityDay
	if ds, ok := s.Store.(store.DashboardStore); ok {
		roll, err := ds.AgentActivityRollup(ctx, project, owner, start, end, offset)
		if err != nil {
			return nil, err
		}
		for day, n := range roll.Days {
			dayCounts[day] += n
		}
		unknown += roll.Undated
		rows = roll.Twins
	} else if activity, ok := s.Store.(store.AgentActivityStore); ok {
		var err error
		if rows, err = activity.ListAgentActivity(ctx, project, owner, start, end, offset); err != nil {
			return nil, err
		}
	}
	for _, row := range rows {
		if row.Date == "" {
			undated[[2]string{row.Harness, row.NativeID}] += row.Count
			continue
		}
		key := [3]string{row.Harness, row.NativeID, row.Date}
		if counts[key] == nil {
			counts[key] = map[uint64]struct{}{}
		}
		for _, fingerprint := range row.Fingerprints {
			counts[key][fingerprintKey(fingerprint)] = struct{}{}
		}
	}
	partial, pending := false, false
	if ps := provenanceStore(s.Store); ps != nil {
		snapshots, err := ps.ListSnapshotsForProject(ctx, project, 10000)
		if err != nil {
			return nil, err
		}
		partial = len(snapshots) >= 10000
		inline := time.Now().Add(legacyInlineBudget)
		var later []store.SessionSnapshot
		for _, snap := range snapshots {
			if snap.OwnerUserID != owner {
				continue
			}
			archive, found := s.cachedLegacyActivity(snap)
			if !found {
				if !time.Now().Before(inline) || ctx.Err() != nil {
					later = append(later, snap)
					continue
				}
				archive, err = s.readLegacyActivity(ctx, snap)
				if err != nil {
					if ctx.Err() != nil {
						later = append(later, snap)
						continue
					}
					// One unreadable archive must not hide the calendar.
					s.Log.Printf("activity: archived transcript %s unreadable: %v", snap.SessionID, err)
					partial = true
					continue
				}
			}
			partial = partial || archive.Partial
			identity := [2]string{snap.Harness, snap.ConversationID}
			for _, source := range archive.Sources {
				if undated[identity] > 0 {
					undated[identity]--
				}
				at := time.UnixMicro(source.AtMicro)
				if at.Before(start) || !at.Before(end) {
					continue
				}
				bucket := [3]string{snap.Harness, snap.ConversationID, at.In(zone).Format("2006-01-02")}
				if counts[bucket] == nil {
					counts[bucket] = map[uint64]struct{}{}
				}
				counts[bucket][source.Fingerprint] = struct{}{}
			}
			if archive.Undated > undated[identity] {
				undated[identity] = archive.Undated
			}
		}
		if len(later) > 0 {
			pending, partial = true, true
			s.warmLegacyActivity(project, owner, later)
		}
	}
	for key, identities := range counts {
		dayCounts[key[2]] += len(identities)
	}
	days := make([]map[string]any, 0, 366)
	total, active := 0, 0
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
		"timestamp_basis": "agent_source_time", "scope": "your_project_messages", "utc_offset_minutes": offset,
		"partial": partial, "archive_pending": pending}, nil
}

// legacyInlineBudget bounds how long one dashboard request spends reading
// archived transcripts that are not cached yet.
const legacyInlineBudget = 1500 * time.Millisecond

// legacyCacheMaxSources caps the archived-date cache (16 bytes a message).
const legacyCacheMaxSources = 1_000_000

// fingerprintKey folds a hex ActivityFingerprint (md5) to its first 64 bits:
// the cache keeps 16 bytes per message instead of a 32-character string.
func fingerprintKey(hex string) uint64 {
	if len(hex) > 16 {
		hex = hex[:16]
	}
	v, err := strconv.ParseUint(hex, 16, 64)
	if err != nil {
		h := fnv.New64a()
		_, _ = h.Write([]byte(hex))
		return h.Sum64()
	}
	return v
}

type activitySource struct {
	AtMicro     int64
	Fingerprint uint64
}
type cachedLegacyActivity struct {
	Sources []activitySource
	Undated int
	Partial bool
}

func legacyActivityKey(snap store.SessionSnapshot) string {
	return fmt.Sprintf("%s|%s|%s|%d|%s", snap.ProjectID, snap.OwnerUserID, snap.SessionID, snap.SnapshotVersion, snap.UpdatedAt.UTC().Format(time.RFC3339Nano))
}

func (s *Server) cachedLegacyActivity(snap store.SessionSnapshot) (cachedLegacyActivity, bool) {
	s.activityMu.Lock()
	defer s.activityMu.Unlock()
	cached, found := s.activityCache[legacyActivityKey(snap)]
	return cached, found
}

// warmLegacyActivity reads archived transcripts a dashboard request had no
// time for, one project+owner at a time, then drops that project's cached
// dashboards so the next request shows the complete calendar.
func (s *Server) warmLegacyActivity(project, owner string, snaps []store.SessionSnapshot) {
	key := project + "|" + owner
	s.activityMu.Lock()
	if s.activityWarming == nil {
		s.activityWarming = map[string]bool{}
	}
	if s.activityWarming[key] {
		s.activityMu.Unlock()
		return
	}
	s.activityWarming[key] = true
	s.activityMu.Unlock()
	go func() {
		defer func() {
			if p := recover(); p != nil {
				s.Log.Printf("activity: reading archived transcripts panicked: %v", p)
			}
			s.activityMu.Lock()
			delete(s.activityWarming, key)
			s.activityMu.Unlock()
			s.invalidateDashboard(project)
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		for _, snap := range snaps {
			if _, err := s.readLegacyActivity(ctx, snap); err != nil && ctx.Err() != nil {
				return
			}
		}
	}()
}

func (s *Server) readLegacyActivity(ctx context.Context, snap store.SessionSnapshot) (cachedLegacyActivity, error) {
	if cached, found := s.cachedLegacyActivity(snap); found {
		return cached, nil
	}
	var cached cachedLegacyActivity
	var payload []byte
	if ts, ok := s.Store.(store.TranscriptStore); ok {
		// Only the transcript: the artifacts bundle and diff can be larger.
		raw, err := ts.LatestTranscript(ctx, snap.SessionID)
		if err != nil {
			return cached, err
		}
		payload = raw
	} else {
		full, err := provenanceStore(s.Store).GetLatestSnapshot(ctx, snap.SessionID)
		if err != nil {
			return cached, err
		}
		if full != nil {
			payload = full.TranscriptPayload
		}
	}
	if payload == nil {
		return cached, nil
	}
	raw, err := gunzipSnapshot(payload)
	if err != nil {
		cached.Partial = true
		return cached, nil
	}
	cached.Partial = len(raw) >= 64<<20
	seen := map[uint64]bool{}
	for i, line := range strings.Split(string(raw), "\n") {
		if i%1024 == 0 {
			if err := ctx.Err(); err != nil {
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
		fingerprint := fingerprintKey(store.ActivityFingerprint(*at, turn.Role, turn.TextPreview))
		if seen[fingerprint] {
			continue
		}
		seen[fingerprint] = true
		cached.Sources = append(cached.Sources, activitySource{AtMicro: at.UnixMicro(), Fingerprint: fingerprint})
	}
	key := legacyActivityKey(snap)
	s.activityMu.Lock()
	defer s.activityMu.Unlock()
	if len(s.activityCache) >= 4096 || s.activityCacheEntries+len(cached.Sources) > legacyCacheMaxSources {
		s.activityCache = nil
		s.activityCacheEntries = 0
	}
	if len(cached.Sources) <= legacyCacheMaxSources {
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
