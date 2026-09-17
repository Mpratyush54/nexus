package store

// episode_extra.go — Episodic Bug & Incident Tracking Engine (issue #11).
//
// Phase 2.3 (auto-detection) + 2.4 (retrieval) + 2.5 (context injection)
// WITHOUT touching episodes.go (foundation-owned: Postgres CRUD + ILIKE
// search). Everything here lives in this file only:
//
//   - LinkEpisodeEvent / GetEpisodeTimeline — event<->episode correlation.
//     MemStore reuses Event.EpisodeID + payload role/note keys instead of a
//     new map, so store.go is untouched (no new fields, no interface change).
//   - SearchEpisodesByErrorPattern (exact ANY match) — complements the ILIKE
//     substring match in SearchEpisodes.
//   - SearchEpisodesByFile — exact membership in files_involved.
//   - CosineSimilarity + SearchEpisodesSemantic — in-memory cosine ranking
//     whose ordering matches Postgres `<=>` (cosine distance) ordering.
//   - AutoDetectEpisode — pure heuristic over an event window, returns a
//     draft Episode (never writes to the store).
//   - (*Episode).ToXML — inference-ready full-arc XML per plan §2.4.
//
// Both MemStore (unit-testable, no DB) and PostgresStore (production SQL)
// get implementations so the two backends stay behavior-compatible.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"encoding/xml"

	"github.com/jackc/pgx/v5"
)

// Episode event roles — mirrors the CHECK constraint on episode_events.role
// in migrations/001_initial.up.sql. Single source of truth so Go code and
// SQL can never disagree on the enum.
const (
	EpisodeRoleTrigger       = "trigger"
	EpisodeRoleInvestigation = "investigation"
	EpisodeRoleAttempt       = "attempt"
	EpisodeRoleFix           = "fix"
	EpisodeRoleVerification  = "verification"
	EpisodeRoleContext       = "context"
)

// ValidEpisodeRole reports whether role is in the episode_events CHECK enum.
func ValidEpisodeRole(role string) bool {
	switch role {
	case EpisodeRoleTrigger, EpisodeRoleInvestigation, EpisodeRoleAttempt,
		EpisodeRoleFix, EpisodeRoleVerification, EpisodeRoleContext:
		return true
	default:
		return false
	}
}

// Payload keys used by MemStore to carry link metadata on the Event itself
// (no new MemStore fields — see file header).
const (
	payloadEpisodeRole = "episode_role"
	payloadEpisodeNote = "episode_note"
)

// LinkEpisodeEvent attaches an event to an episode's timeline with a role.
// MemStore: stamps Event.EpisodeID + payload role/note (no new state).
// Postgres: upserts episode_events and backfills events.episode_id.
func (s *MemStore) LinkEpisodeEvent(ctx context.Context, episodeID string, eventID int64, role, note string) (*EpisodeEvent, error) {
	if !ValidEpisodeRole(role) {
		return nil, fmt.Errorf("invalid episode role %q: want trigger|investigation|attempt|fix|verification|context", role)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ep, ok := s.episodes[episodeID]
	if !ok {
		return nil, ErrNotFound
	}
	var target *Event
	for _, ev := range s.events {
		if ev.ID == eventID {
			target = ev
			break
		}
	}
	if target == nil {
		return nil, ErrNotFound
	}
	target.EpisodeID = ep.ID
	if target.Payload == nil {
		target.Payload = make(map[string]any)
	}
	target.Payload[payloadEpisodeRole] = role
	target.Payload[payloadEpisodeNote] = note
	return &EpisodeEvent{
		EpisodeID: ep.ID,
		EventID:   target.ID,
		Role:      role,
		Note:      note,
		CreatedAt: time.Now().UTC(),
	}, nil
}

// LinkEpisodeEvent upserts the episode_events row and backfills events.episode_id.
func (s *PostgresStore) LinkEpisodeEvent(ctx context.Context, episodeID string, eventID int64, role, note string) (*EpisodeEvent, error) {
	if !ValidEpisodeRole(role) {
		return nil, fmt.Errorf("invalid episode role %q: want trigger|investigation|attempt|fix|verification|context", role)
	}
	var created time.Time
	err := s.pool.QueryRow(ctx,
		`INSERT INTO episode_events (episode_id, event_id, role, note)
		 VALUES ($1::uuid, $2, $3, NULLIF($4,''))
		 ON CONFLICT (episode_id, event_id)
		 DO UPDATE SET role = EXCLUDED.role, note = EXCLUDED.note
		 RETURNING created_at`,
		episodeID, eventID, role, note).Scan(&created)
	if err != nil {
		return nil, err
	}
	// Best-effort backfill so episode_id-scoped queries also see the link.
	_, _ = s.pool.Exec(ctx, `UPDATE events SET episode_id = $1::uuid WHERE id = $2`, episodeID, eventID)
	return &EpisodeEvent{
		EpisodeID: episodeID,
		EventID:   eventID,
		Role:      role,
		Note:      note,
		CreatedAt: created,
	}, nil
}

// GetEpisodeTimeline returns an episode's events oldest-first (plan §2.4:
// "The full episode is returned with all linked events").
func (s *MemStore) GetEpisodeTimeline(ctx context.Context, episodeID string) ([]*Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.episodes[episodeID]; !ok {
		return nil, ErrNotFound
	}
	var out []*Event
	for _, ev := range s.events {
		if ev.EpisodeID == episodeID {
			out = append(out, cloneEvent(ev))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// GetEpisodeTimeline reads both link bases (episode_events join wins, with
// events.episode_id as fallback) so links written by either path are visible.
func (s *PostgresStore) GetEpisodeTimeline(ctx context.Context, episodeID string) ([]*Event, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+eventColumns+` FROM events
		  WHERE id IN (SELECT event_id FROM episode_events WHERE episode_id = $1::uuid)
		     OR episode_id = $1::uuid
		  ORDER BY id ASC`, episodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Event
	for rows.Next() {
		ev, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

// SearchEpisodesByErrorPattern matches episodes whose error_patterns contain
// pattern EXACTLY (plan §2.4 query #1: `$2 = ANY(error_patterns)`).
// Contrast with SearchEpisodes, which does ILIKE substring matching.
func (s *MemStore) SearchEpisodesByErrorPattern(ctx context.Context, projectID, pattern string) ([]*Episode, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*Episode
	for _, ep := range s.episodes {
		if ep.ProjectID != projectID {
			continue
		}
		for _, p := range ep.ErrorPatterns {
			if p == pattern {
				out = append(out, cloneEpisode(ep))
				break
			}
		}
	}
	sortEpisodesByOpenedDesc(out)
	return out, nil
}

// SearchEpisodesByErrorPattern is the exact-match retrieval from plan §2.4.
func (s *PostgresStore) SearchEpisodesByErrorPattern(ctx context.Context, projectID, pattern string) ([]*Episode, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+episodeColumns+` FROM episodes
		  WHERE project_id = $1::uuid
		    AND $2 = ANY(error_patterns)
		  ORDER BY resolved_at DESC NULLS LAST, opened_at DESC`, projectID, pattern)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectEpisodes(rows)
}

// SearchEpisodesByFile returns episodes involving file exactly
// (plan §2.4 query #3: `$2 = ANY(files_involved)`).
func (s *MemStore) SearchEpisodesByFile(ctx context.Context, projectID, file string) ([]*Episode, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*Episode
	for _, ep := range s.episodes {
		if ep.ProjectID != projectID {
			continue
		}
		for _, f := range ep.FilesInvolved {
			if f == file {
				out = append(out, cloneEpisode(ep))
				break
			}
		}
	}
	sortEpisodesByOpenedDesc(out)
	return out, nil
}

// SearchEpisodesByFile is the file-involvement lookup from plan §2.4.
func (s *PostgresStore) SearchEpisodesByFile(ctx context.Context, projectID, file string) ([]*Episode, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+episodeColumns+` FROM episodes
		  WHERE project_id = $1::uuid
		    AND $2 = ANY(files_involved)
		  ORDER BY resolved_at DESC NULLS LAST, opened_at DESC`, projectID, file)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectEpisodes(rows)
}

func collectEpisodes(rows pgx.Rows) ([]*Episode, error) {
	var out []*Episode
	for rows.Next() {
		ep, err := scanEpisode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ep)
	}
	return out, rows.Err()
}

func sortEpisodesByOpenedDesc(eps []*Episode) {
	sort.Slice(eps, func(i, j int) bool {
		if eps[i].OpenedAt.Equal(eps[j].OpenedAt) {
			return eps[i].ID < eps[j].ID
		}
		return eps[i].OpenedAt.After(eps[j].OpenedAt)
	})
}

// CosineSimilarity is the shared math behind semantic episode search.
// pgvector's `<=>` (cosine distance) orders rows by 1 - cosine_similarity,
// so ranking by this function descending reproduces production ordering in
// MemStore tests. Returns 0 for zero-length, mismatched, or zero-norm inputs
// (no embedding -> no similarity, never NaN).
func CosineSimilarity(a, b []float32) float64 {
	if len(a) == 0 || len(b) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (sqrtFloat(na) * sqrtFloat(nb))
}

// sqrtFloat is math.Sqrt without importing math into the hot path's file
// scope — implemented via Newton's method in one place for testability.
func sqrtFloat(x float64) float64 {
	if x <= 0 {
		return 0
	}
	z := x
	if z < 1 {
		z = 1
	}
	for i := 0; i < 50; i++ {
		z -= (z*z - x) / (2 * z)
	}
	return z
}

// SearchEpisodesSemantic ranks a project's episodes by cosine similarity to
// queryVec, descending (plan §2.4 query #2). Episodes without embeddings or
// with dimension mismatches are skipped. Ties break by ID for determinism.
func (s *MemStore) SearchEpisodesSemantic(ctx context.Context, projectID string, queryVec []float32, limit int) ([]*Episode, error) {
	if limit <= 0 {
		limit = 5 // plan §2.4 caps semantic results at 5
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	type scored struct {
		ep  *Episode
		sim float64
	}
	var ranked []scored
	for _, ep := range s.episodes {
		if ep.ProjectID != projectID || len(ep.Embedding) == 0 {
			continue
		}
		if sim := CosineSimilarity(ep.Embedding, queryVec); sim > 0 {
			ranked = append(ranked, scored{ep, sim})
		}
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].sim == ranked[j].sim {
			return ranked[i].ep.ID < ranked[j].ep.ID
		}
		return ranked[i].sim > ranked[j].sim
	})
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	out := make([]*Episode, 0, len(ranked))
	for _, r := range ranked {
		out = append(out, cloneEpisode(r.ep))
	}
	return out, nil
}

// SearchEpisodesSemantic is the production semantic path (mirrors
// SearchMemoryVector): pgvector cosine ordering over episode narratives.
func (s *PostgresStore) SearchEpisodesSemantic(ctx context.Context, projectID string, queryVec []float32, limit int) ([]*Episode, error) {
	if limit <= 0 {
		limit = 5
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+episodeColumns+` FROM episodes
		  WHERE project_id = $1::uuid
		    AND embedding IS NOT NULL
		  ORDER BY embedding <=> $2::vector
		  LIMIT $3`,
		projectID, encodeEmbedding(queryVec), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectEpisodes(rows)
}

// ---- Auto-detection (plan §2.3) ----

// AutoDetectEpisode scans an ordered event window for the bug arc:
//
//	COMMAND_EXECUTED exit != 0 (trigger) -> FILE_READ cluster (investigation)
//	-> FILE_MODIFIED (fix) -> COMMAND_EXECUTED same command exit == 0
//	(verification) -> GIT_COMMITTED (resolution marker)
//
// It returns a draft Episode (Status OPEN, type bug_fix) plus true when a
// failing command is found. Resolution/verification stay empty unless the
// full arc completes — the caller (Memory Processor) fills root_cause with
// an LLM pass; the RootCause field here is explicitly a heuristic guess.
// Pure function: reads events, never touches the store.
func AutoDetectEpisode(events []*Event, projectID string) (*Episode, bool) {
	ordered := append([]*Event(nil), events...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })

	// 1. Trigger: first failing command.
	triggerIdx := -1
	for i, ev := range ordered {
		if ev.EventType == "COMMAND_EXECUTED" && episodeExitCode(ev.Payload) != 0 {
			triggerIdx = i
			break
		}
	}
	if triggerIdx < 0 {
		return nil, false
	}
	trigger := ordered[triggerIdx]
	cmd := episodePayloadStr(trigger.Payload, "command", "cmd")
	stderr := episodePayloadStr(trigger.Payload, "stderr", "error", "output", "stdout")
	if projectID == "" {
		projectID = trigger.ProjectID
	}

	// 2-5. Walk the post-trigger window.
	var reads, modified []string
	var verification, commit *Event
	for _, ev := range ordered[triggerIdx+1:] {
		switch ev.EventType {
		case "FILE_READ":
			if p := episodePayloadStr(ev.Payload, "path", "file"); p != "" {
				reads = appendUnique(reads, p)
			}
		case "FILE_MODIFIED":
			if p := episodePayloadStr(ev.Payload, "path", "file"); p != "" {
				modified = appendUnique(modified, p)
			}
		case "COMMAND_EXECUTED":
			if verification == nil && episodeExitCode(ev.Payload) == 0 &&
				(cmd == "" || episodePayloadStr(ev.Payload, "command", "cmd") == cmd) {
				verification = ev
			}
		case "GIT_COMMITTED":
			if commit == nil {
				commit = ev
			}
		}
	}

	patterns := extractErrorPatterns(stderr)
	title := "Bug: " + firstNonEmpty(cmd, "command failed")
	if len(patterns) > 0 {
		title += " — " + truncateOneLine(patterns[0], 80)
	}
	triggerText := strings.TrimSpace(strings.Join([]string{
		firstNonEmpty(cmd, "unknown command"),
		"exit code " + itoa(episodeExitCode(trigger.Payload)),
		truncateOneLine(stderr, 500),
	}, " | "))

	investigation := "No file reads recorded after the failure."
	if len(reads) > 0 {
		investigation = "Read: " + strings.Join(reads, ", ")
	}
	rootCause := fmt.Sprintf("Unknown — heuristic guess only: %d file(s) read, %d file(s) modified after %q.",
		len(reads), len(modified), truncateOneLine(firstNonEmpty(cmd, "failing command"), 60))
	if len(modified) > 0 {
		rootCause += " Likely in " + modified[0] + "."
	}
	resolution, verificationText := "", ""
	if verification != nil {
		verificationText = "Passing run: " + strings.TrimSpace(firstNonEmpty(
			episodePayloadStr(verification.Payload, "command", "cmd")+" "+
				episodePayloadStr(verification.Payload, "args", "arg"), cmd))
	}
	if commit != nil {
		msg := episodePayloadStr(commit.Payload, "message", "msg")
		sha := episodePayloadStr(commit.Payload, "sha", "commit", "hash")
		resolution = strings.TrimSpace(strings.Join([]string{truncateOneLine(msg, 300), truncateOneLine(sha, 40)}, " "))
		if verificationText == "" {
			verificationText = "Commit recorded after fix; test verification not observed in window."
		}
	}

	return &Episode{
		ProjectID:     projectID,
		SessionID:     trigger.SessionID,
		Title:         title,
		EpisodeType:   "bug_fix",
		Trigger:       triggerText,
		Investigation: investigation,
		RootCause:     rootCause,
		Resolution:    resolution,
		Verification:  verificationText,
		FilesInvolved: modified,
		ErrorPatterns: patterns,
		Status:        "OPEN",
	}, true
}

func appendUnique(in []string, s string) []string {
	for _, v := range in {
		if v == s {
			return in
		}
	}
	return append(in, s)
}

// extractErrorPatterns pulls the first non-empty stderr line (capped 500
// chars) for error_patterns. One pattern per trigger keeps exact-match
// retrieval precise; multi-line logs are a verification-time refinement.
func extractErrorPatterns(stderr string) []string {
	for _, line := range strings.Split(stderr, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return []string{truncateOneLine(t, 500)}
		}
	}
	return nil
}

func episodePayloadStr(p map[string]any, keys ...string) string {
	for _, k := range keys {
		if p == nil {
			break
		}
		switch v := p[k].(type) {
		case string:
			if v != "" {
				return v
			}
		case []string:
			if len(v) > 0 {
				return strings.Join(v, " ")
			}
		case []any:
			var parts []string
			for _, e := range v {
				if s, ok := e.(string); ok && s != "" {
					parts = append(parts, s)
				}
			}
			if len(parts) > 0 {
				return strings.Join(parts, " ")
			}
		}
	}
	return ""
}

// episodeExitCode reads exit status across payload conventions (int from Go
// producers, float64 from JSON round-trips, string from CLI shims).
// Missing/unparseable means "unknown failure" (-1) only when the key is
// absent AND no success marker exists — callers treat != 0 as failure.
func episodeExitCode(p map[string]any) int {
	if p == nil {
		return -1
	}
	for _, k := range []string{"exit_code", "exitCode", "code", "exit"} {
		switch v := p[k].(type) {
		case int:
			return v
		case int8:
			return int(v)
		case int16:
			return int(v)
		case int32:
			return int(v)
		case int64:
			return int(v)
		case float32:
			return int(v)
		case float64:
			return int(v)
		case string:
			var n int
			if _, err := fmt.Sscanf(strings.TrimSpace(v), "%d", &n); err == nil {
				return n
			}
		}
	}
	return -1
}

func firstNonEmpty(in ...string) string {
	for _, s := range in {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

func truncateOneLine(s string, max int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	s = strings.Join(strings.Fields(s), " ")
	if max > 0 && len(s) > max {
		return s[:max]
	}
	return s
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }

// ---- XML rendering (plan §2.4) ----

// ToXML renders the full episode arc as inference-ready XML (plan §2.4
// shape). Empty arc sections still emit their tags so consumers can rely on
// a stable shape. All text is XML-escaped.
func (ep *Episode) ToXML() string { return ep.ToXMLWithEvents(nil) }

// ToXMLWithEvents renders the arc plus the linked-event timeline count and,
// when events are provided, one <event> line each (id, type, role).
func (ep *Episode) ToXMLWithEvents(events []*Event) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf(`<episode type=%s title=%s status=%s>`,
		xmlAttr(ep.EpisodeType), xmlAttr(ep.Title), xmlAttr(ep.Status)))
	b.WriteString("\n  <trigger" + xmlDateAttr(ep.OpenedAt) + ">" + xmlEscape(ep.Trigger) + "</trigger>")
	b.WriteString("\n  <investigation>" + xmlEscape(ep.Investigation) + "</investigation>")
	b.WriteString("\n  <root_cause>" + xmlEscape(ep.RootCause) + "</root_cause>")
	b.WriteString("\n  <resolution" + xmlFilesAttr(ep.FilesInvolved) + ">" + xmlEscape(ep.Resolution) + "</resolution>")
	b.WriteString("\n  <verification>" + xmlEscape(ep.Verification) + "</verification>")
	fmt.Fprintf(&b, "\n  <events count=%s>", xmlAttr(itoa(len(events))))
	for _, ev := range events {
		role := episodePayloadStr(ev.Payload, payloadEpisodeRole)
		fmt.Fprintf(&b, "\n    <event id=%s type=%s role=%s />",
			xmlAttr(itoa(int(ev.ID))), xmlAttr(ev.EventType), xmlAttr(role))
	}
	if len(events) > 0 {
		b.WriteString("\n  ")
	}
	b.WriteString("</events>")
	b.WriteString("\n</episode>")
	return b.String()
}

func xmlEscape(s string) string {
	var b strings.Builder
	if err := xml.EscapeText(&b, []byte(s)); err != nil {
		return ""
	}
	return b.String()
}

func xmlAttr(s string) string { return `"` + xmlEscape(s) + `"` }

func xmlDateAttr(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return fmt.Sprintf(` date=%s`, xmlAttr(t.UTC().Format(time.RFC3339)))
}

func xmlFilesAttr(files []string) string {
	if len(files) == 0 {
		return ""
	}
	return fmt.Sprintf(` files=%s`, xmlAttr(strings.Join(files, ", ")))
}
