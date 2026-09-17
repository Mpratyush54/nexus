// Package store provides Postgres data access for central-memory.
//
// episodes.go implements issue #11 (plan §§2.3–2.5): the episodes engine —
// a pure, DB-free arc detector plus the retrieval SQL seam:
//
//	COMMAND_EXECUTED exit!=0 → FILE_READ cluster → FILE_MODIFIED →
//	COMMAND_EXECUTED exit==0 → GIT_COMMITTED
//
// DB OWNERSHIP NOTE (parallel-agent constraint): internal/store/db.go,
// projects.go and workspaces.go are owned by other issues and are NOT
// touched here. This file is stdlib-only and defines the narrow
// EpisodeQuerier interface it needs; *DB already satisfies it
// method-for-method (Query with the same signature), so the pool owner
// wires it with no adapter and no edits. Rows/FormatEmbedding are reused
// from memory.go (same package). See ADR-011.
package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Episode event types consumed by the detector (plan §2.1, Layer 1).
const (
	EventCommandExecuted = "COMMAND_EXECUTED"
	EventFileRead        = "FILE_READ"
	EventFileModified    = "FILE_MODIFIED"
	EventGitCommitted    = "GIT_COMMITTED"
)

// Episode types (CHECK constraint in migrations/001, plan §1.1).
const (
	EpisodeBugFix        = "bug_fix"
	EpisodeFeature       = "feature"
	EpisodeRefactor      = "refactor"
	EpisodeIncident      = "incident"
	EpisodeInvestigation = "investigation"
	EpisodeOnboarding    = "onboarding"
)

// Episode lifecycle states (CHECK constraint in migrations/001, plan §1.1).
const (
	EpisodeOpen          = "OPEN"
	EpisodeInvestigating = "INVESTIGATING"
	EpisodeResolved      = "RESOLVED"
	EpisodeWontFix       = "WONT_FIX"
)

// Episode-event link roles (CHECK constraint on episode_events, plan §1.1).
// The passing command is the verification; the resolving commit is linked
// as context (it confirms the resolution rather than proving it).
const (
	RoleTrigger       = "trigger"
	RoleInvestigation = "investigation"
	RoleFix           = "fix"
	RoleVerification  = "verification"
	RoleContext       = "context"
)

// Retrieval limits for episode search (plan §2.4 shows LIMIT 5).
const (
	DefaultEpisodeLimit = 5
	MaxEpisodeLimit     = 50
)

// EventView is the detector's narrow view of one event row. The Memory
// Processor (issue #10) maps the JSONB payload of each Layer-1 event into
// this shape; the detector itself never touches the database or JSON.
type EventView struct {
	ID        int64
	Type      string
	Command   string   // COMMAND_EXECUTED: binary, e.g. "go"
	Args      []string // COMMAND_EXECUTED: argv, e.g. ["test", "./..."]
	ExitCode  int      // COMMAND_EXECUTED only
	Stdout    string   // COMMAND_EXECUTED: capped 4KB by the interceptor
	Stderr    string   // COMMAND_EXECUTED: capped 4KB by the interceptor
	Path      string   // FILE_READ / FILE_MODIFIED: workspace-relative path
	Message   string   // GIT_COMMITTED: commit message
	CommitSHA string   // GIT_COMMITTED: commit SHA
}

// EpisodeLink binds one event to its role in the arc.
type EpisodeLink struct {
	EventID int64
	Role    string
	Note    string // optional annotation, e.g. "resolution commit"
}

// EpisodeDraft is the pure detector output: a synthesized story arc plus
// the search signals derived from it. NarrativeEmbedding is left nil by
// the detector — the Memory Processor fills it (embed the Narrative) and
// persists it via FormatEmbedding at write time.
type EpisodeDraft struct {
	Title              string
	EpisodeType        string
	Trigger            string
	Investigation      string
	RootCause          string
	Resolution         string
	Verification       string
	Narrative          string
	NarrativeEmbedding []float32
	ErrorPatterns      []string
	FilesInvolved      []string
	Links              []EpisodeLink
}

// Episode mirrors an episodes row for retrieval (plan §2.4).
type Episode struct {
	ID            string
	ProjectID     string
	Title         string
	EpisodeType   string
	Trigger       string
	Investigation string
	RootCause     string
	Resolution    string
	Verification  string
	Tags          []string
	FilesInvolved []string
	ErrorPatterns []string
	Status        string
}

// RankedEpisode pairs an episode with its cosine similarity scalar.
type RankedEpisode struct {
	Episode    Episode
	Similarity float64
}

// commandLine renders "go test ./..." for human-readable arc text.
func (e EventView) commandLine() string {
	if len(e.Args) == 0 {
		return e.Command
	}
	return e.Command + " " + strings.Join(e.Args, " ")
}

// firstNonEmptyLine returns the first non-blank line of s.
func firstNonEmptyLine(s string) string {
	for _, ln := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(stripANSI(ln)); t != "" {
			return t
		}
	}
	return ""
}

// stripANSI removes simple "\x1b[...m" color escapes so error patterns
// match regardless of whether the command emitted color.
func stripANSI(s string) string {
	for {
		i := strings.Index(s, "\x1b[")
		if i < 0 {
			return s
		}
		j := strings.IndexByte(s[i:], 'm')
		if j < 0 {
			return s[:i]
		}
		s = s[:i] + s[i+j+1:]
	}
}

// ExtractErrorPatterns derives searchable error signals from a failed
// command's output: stderr first, stdout as fallback. It returns up to 5
// deduplicated non-empty lines, each truncated to 200 chars. Pure and
// DB-free.
func ExtractErrorPatterns(stderr, stdout string) []string {
	src := stderr
	if strings.TrimSpace(src) == "" {
		src = stdout
	}
	var out []string
	seen := map[string]struct{}{}
	for _, ln := range strings.Split(src, "\n") {
		t := strings.TrimSpace(stripANSI(ln))
		if t == "" {
			continue
		}
		if len(t) > 200 {
			t = t[:200]
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
		if len(out) == 5 {
			break
		}
	}
	return out
}

// dedupStrings preserves first-seen order.
func dedupStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// truncateShort caps s at n chars with an ellipsis marker.
func truncateShort(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// DetectEpisode recognizes the plan §2.3 bug/incident arc in an ordered
// event stream:
//
//	COMMAND_EXECUTED exit!=0 → FILE_READ cluster → FILE_MODIFIED →
//	COMMAND_EXECUTED exit==0 → GIT_COMMITTED
//
// Required for a draft: a failing command, at least one FILE_MODIFIED
// after it, and a passing command after the first fix (same command line
// preferred, any passing command accepted). The FILE_READ cluster and the
// closing GIT_COMMITTED are optional enrichers — an arc without reads
// ("fix from memory") or without a commit (uncommitted fix) is still a
// real episode; requiring them would trade recall for no precision gain.
// Clean runs (no failing command, or failure without fix+verification)
// yield nil — no false positives. Pure: no DB, no I/O.
func DetectEpisode(events []EventView) *EpisodeDraft {
	triggerIdx := -1
	for i, e := range events {
		if e.Type == EventCommandExecuted && e.ExitCode != 0 {
			triggerIdx = i
			break
		}
	}
	if triggerIdx < 0 {
		return nil
	}
	trigger := events[triggerIdx]

	var reads, mods []string
	var readIDs, modIDs []int64
	firstFixIdx := -1
	for i := triggerIdx + 1; i < len(events); i++ {
		e := events[i]
		switch e.Type {
		case EventFileRead:
			if e.Path != "" {
				reads = append(reads, e.Path)
				readIDs = append(readIDs, e.ID)
			}
		case EventFileModified:
			if e.Path != "" {
				mods = append(mods, e.Path)
				modIDs = append(modIDs, e.ID)
			}
			if firstFixIdx < 0 {
				firstFixIdx = i
			}
		}
	}
	if firstFixIdx < 0 {
		return nil // failure never acted on — not an episode
	}

	// Verification: prefer re-running the same command line (plan §2.3:
	// "COMMAND_EXECUTED same command, exit code = 0"), accept any passing
	// command after the first fix as fallback.
	verifyIdx, fallbackIdx := -1, -1
	for i := firstFixIdx + 1; i < len(events); i++ {
		e := events[i]
		if e.Type != EventCommandExecuted || e.ExitCode != 0 {
			continue
		}
		if fallbackIdx < 0 {
			fallbackIdx = i
		}
		if e.commandLine() == trigger.commandLine() {
			verifyIdx = i
			break
		}
	}
	if verifyIdx < 0 {
		verifyIdx = fallbackIdx
	}
	if verifyIdx < 0 {
		return nil // fix never verified — not an episode yet
	}
	verification := events[verifyIdx]

	// Resolution commit: first GIT_COMMITTED after verification, if any.
	commitIdx := -1
	for i := verifyIdx + 1; i < len(events); i++ {
		if events[i].Type == EventGitCommitted {
			commitIdx = i
			break
		}
	}

	patterns := ExtractErrorPatterns(trigger.Stderr, trigger.Stdout)
	files := dedupStrings(mods)
	uniqueReads := dedupStrings(reads)

	firstErr := firstNonEmptyLine(trigger.Stderr)
	if firstErr == "" {
		firstErr = firstNonEmptyLine(trigger.Stdout)
	}

	title := fmt.Sprintf("Bug fix: %s exited %d", trigger.commandLine(), trigger.ExitCode)
	if firstErr != "" {
		title = fmt.Sprintf("Bug fix: %s failed (%s)", trigger.Command, truncateShort(firstErr, 80))
	}

	triggerText := fmt.Sprintf("Command `%s` exited %d.", trigger.commandLine(), trigger.ExitCode)
	if firstErr != "" {
		triggerText += " Error: " + truncateShort(firstErr, 300)
	}

	var inv strings.Builder
	if len(uniqueReads) == 0 {
		fmt.Fprintf(&inv, "No file reads observed between failure and fix; fix applied from prior knowledge. %d file(s) modified.", len(files))
	} else {
		fmt.Fprintf(&inv, "After the failure, read %d file(s): %s. %d file(s) then modified.",
			len(uniqueReads), strings.Join(uniqueReads, ", "), len(files))
	}

	rootCause := fmt.Sprintf("Heuristic inference from failure of `%s` (exit %d)",
		trigger.commandLine(), trigger.ExitCode)
	if firstErr != "" {
		rootCause += ": " + truncateShort(firstErr, 200)
	}
	rootCause += fmt.Sprintf(". Fix touched: %s.", strings.Join(files, ", "))

	resolution := "Modified: " + strings.Join(files, ", ")
	if commitIdx >= 0 {
		c := events[commitIdx]
		resolution += fmt.Sprintf(". Committed %s: %s",
			truncateShort(c.CommitSHA, 12), truncateShort(strings.TrimSpace(c.Message), 200))
	} else {
		resolution += " (no linked commit observed)."
	}

	verifyText := fmt.Sprintf("Re-ran `%s`: exit 0.", verification.commandLine())

	narrative := strings.Join([]string{
		"Trigger: " + triggerText,
		"Investigation: " + inv.String(),
		"Root cause: " + rootCause,
		"Resolution: " + resolution,
		"Verification: " + verifyText,
	}, "\n")

	var links []EpisodeLink
	links = append(links, EpisodeLink{EventID: trigger.ID, Role: RoleTrigger})
	for _, id := range readIDs {
		links = append(links, EpisodeLink{EventID: id, Role: RoleInvestigation})
	}
	for _, id := range modIDs {
		links = append(links, EpisodeLink{EventID: id, Role: RoleFix})
	}
	links = append(links, EpisodeLink{EventID: verification.ID, Role: RoleVerification})
	if commitIdx >= 0 {
		links = append(links, EpisodeLink{
			EventID: events[commitIdx].ID,
			Role:    RoleContext,
			Note:    "resolution commit",
		})
	}

	return &EpisodeDraft{
		Title:         title,
		EpisodeType:   EpisodeBugFix,
		Trigger:       triggerText,
		Investigation: inv.String(),
		RootCause:     rootCause,
		Resolution:    resolution,
		Verification:  verifyText,
		Narrative:     narrative,
		ErrorPatterns: patterns,
		FilesInvolved: files,
		Links:         links,
	}
}

// ---------------------------------------------------------------------------
// Retrieval SQL builders (plan §2.4) — pure, DB-free, unit-tested.
// ---------------------------------------------------------------------------

// episodeColumns selects episodes with NULLs coalesced so rows scan into
// the plain-string Episode struct.
const episodeColumns = `id::TEXT AS id, ` +
	`title, episode_type, ` +
	`COALESCE(trigger, '') AS trigger, ` +
	`COALESCE(investigation, '') AS investigation, ` +
	`COALESCE(root_cause, '') AS root_cause, ` +
	`COALESCE(resolution, '') AS resolution, ` +
	`COALESCE(verification, '') AS verification, ` +
	`COALESCE(tags, '{}') AS tags, ` +
	`COALESCE(files_involved, '{}') AS files_involved, ` +
	`COALESCE(error_patterns, '{}') AS error_patterns, ` +
	`status`

// BuildEpisodeErrorSearchSQL renders the plan §2.4 exact error-pattern
// match: latest RESOLVED episode whose error_patterns contain the pattern.
func BuildEpisodeErrorSearchSQL(projectID, pattern string) (string, []any) {
	return `SELECT ` + episodeColumns + ` FROM episodes ` +
			`WHERE project_id = $1 AND $2 = ANY(error_patterns) ` +
			`AND status = 'RESOLVED' ORDER BY resolved_at DESC`,
		[]any{projectID, pattern}
}

// BuildEpisodeFileSearchSQL renders the plan §2.4 file-involvement lookup.
func BuildEpisodeFileSearchSQL(projectID, path string) (string, []any) {
	return `SELECT ` + episodeColumns + ` FROM episodes ` +
			`WHERE project_id = $1 AND $2 = ANY(files_involved) ` +
			`ORDER BY resolved_at DESC`,
		[]any{projectID, path}
}

// BuildEpisodeSimilaritySQL renders the plan §2.4 semantic search: cosine
// ordering via `embedding <=> $2` over RESOLVED episodes. The embedding arg
// is the FormatEmbedding text literal so no vector dependency is needed
// (same seam as memory.go BuildSearchSQL).
func BuildEpisodeSimilaritySQL(projectID string, embedding []float32, limit int) (string, []any) {
	if limit <= 0 {
		limit = DefaultEpisodeLimit
	}
	if limit > MaxEpisodeLimit {
		limit = MaxEpisodeLimit
	}
	return `SELECT ` + episodeColumns + `, 1 - (embedding <=> $2) AS similarity ` +
			`FROM episodes WHERE project_id = $1 AND status = 'RESOLVED' ` +
			`ORDER BY embedding <=> $2 LIMIT $3`,
		[]any{projectID, FormatEmbedding(embedding), limit}
}

// BuildCreateEpisodeSQL renders the INSERT for a detected draft. Embedding
// travels as a pgvector text literal (FormatEmbedding); a nil embedding
// stores NULL for the Memory Processor to fill once it embeds the
// narrative.
func BuildCreateEpisodeSQL(projectID string, d *EpisodeDraft) (string, []any) {
	var emb any
	if len(d.NarrativeEmbedding) > 0 {
		emb = FormatEmbedding(d.NarrativeEmbedding)
	}
	return `INSERT INTO episodes (project_id, title, episode_type, trigger, investigation, ` +
			`root_cause, resolution, verification, embedding, files_involved, error_patterns, status) ` +
			`VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, 'RESOLVED') ` +
			`RETURNING id::TEXT AS id`,
		[]any{projectID, d.Title, d.EpisodeType, d.Trigger, d.Investigation,
			d.RootCause, d.Resolution, d.Verification, emb, d.FilesInvolved, d.ErrorPatterns}
}

// OrderBySimilarity sorts episodes by cosine similarity to the query
// vector, descending. It is the DB-free ordering behind SimilaritySQL —
// ties break by Title for determinism (mirrors memory.go Rank semantics).
func OrderBySimilarity(episodes []Episode, vectors map[string][]float32, query []float32) []RankedEpisode {
	out := make([]RankedEpisode, len(episodes))
	for i, ep := range episodes {
		out[i] = RankedEpisode{Episode: ep, Similarity: CosineSimilarity(vectors[ep.ID], query)}
	}
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].Similarity == out[b].Similarity {
			return out[a].Episode.Title < out[b].Episode.Title
		}
		return out[a].Similarity > out[b].Similarity
	})
	return out
}

// ---------------------------------------------------------------------------
// Store methods behind the local narrow interface.
// ---------------------------------------------------------------------------

// EpisodeQuerier is the minimal query surface episode retrieval needs. It
// is defined locally (not reused from memory.go) so this file owns its
// seam; *DB satisfies it implicitly via its Query method, and INSERT…
// RETURNING goes through Query so creates stay stdlib-only (no pgx/pgconn
// imports, no driver coupling).
type EpisodeQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (Rows, error)
}

// EpisodeStore is episode persistence + retrieval over any EpisodeQuerier
// (pool, transaction, fake).
type EpisodeStore struct {
	db EpisodeQuerier
}

// NewEpisodeStore wires an EpisodeStore to any EpisodeQuerier.
func NewEpisodeStore(db EpisodeQuerier) *EpisodeStore {
	return &EpisodeStore{db: db}
}

// scanEpisode scans one episodeColumns row.
func scanEpisode(rows Rows, ep *Episode) error {
	return rows.Scan(
		&ep.ID, &ep.Title, &ep.EpisodeType,
		&ep.Trigger, &ep.Investigation, &ep.RootCause,
		&ep.Resolution, &ep.Verification,
		&ep.Tags, &ep.FilesInvolved, &ep.ErrorPatterns, &ep.Status,
	)
}

// collectEpisodes drains an episodeColumns result set.
func (s *EpisodeStore) collectEpisodes(ctx context.Context, query string, args ...any) ([]Episode, error) {
	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: episode query: %w", err)
	}
	defer rows.Close()
	var out []Episode
	for rows.Next() {
		var ep Episode
		if err := scanEpisode(rows, &ep); err != nil {
			return nil, fmt.Errorf("store: episode scan: %w", err)
		}
		out = append(out, ep)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: episode rows: %w", err)
	}
	return out, nil
}

// Create persists a detected draft as a RESOLVED episode and returns its ID.
// Event linking (episode_events) and embedding backfill are follow-ups for
// the Memory Processor (issue #10); Links on the draft name the events.
func (s *EpisodeStore) Create(ctx context.Context, projectID string, d *EpisodeDraft) (*Episode, error) {
	if d == nil {
		return nil, fmt.Errorf("store: create episode requires a non-nil draft")
	}
	query, args := BuildCreateEpisodeSQL(projectID, d)
	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: create episode: %w", err)
	}
	defer rows.Close()
	ep := &Episode{
		ProjectID:     projectID,
		Title:         d.Title,
		EpisodeType:   d.EpisodeType,
		Trigger:       d.Trigger,
		Investigation: d.Investigation,
		RootCause:     d.RootCause,
		Resolution:    d.Resolution,
		Verification:  d.Verification,
		FilesInvolved: append([]string(nil), d.FilesInvolved...),
		ErrorPatterns: append([]string(nil), d.ErrorPatterns...),
		Status:        EpisodeResolved,
	}
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("store: create episode rows: %w", err)
		}
		return nil, fmt.Errorf("store: create episode returned no id")
	}
	if err := rows.Scan(&ep.ID); err != nil {
		return nil, fmt.Errorf("store: create episode scan: %w", err)
	}
	return ep, nil
}

// ByErrorPattern returns RESOLVED episodes with an exact error-pattern
// match, newest first (plan §2.4).
func (s *EpisodeStore) ByErrorPattern(ctx context.Context, projectID, pattern string) ([]Episode, error) {
	query, args := BuildEpisodeErrorSearchSQL(projectID, pattern)
	eps, err := s.collectEpisodes(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	for i := range eps {
		eps[i].ProjectID = projectID
	}
	return eps, nil
}

// ByFile returns episodes that touched the given file, newest first
// (plan §2.4).
func (s *EpisodeStore) ByFile(ctx context.Context, projectID, path string) ([]Episode, error) {
	query, args := BuildEpisodeFileSearchSQL(projectID, path)
	eps, err := s.collectEpisodes(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	for i := range eps {
		eps[i].ProjectID = projectID
	}
	return eps, nil
}

// Similar returns RESOLVED episodes ordered by cosine similarity to the
// query embedding (plan §2.4).
func (s *EpisodeStore) Similar(ctx context.Context, projectID string, embedding []float32, limit int) ([]RankedEpisode, error) {
	query, args := BuildEpisodeSimilaritySQL(projectID, embedding, limit)
	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: episode similarity query: %w", err)
	}
	defer rows.Close()
	var out []RankedEpisode
	for rows.Next() {
		var ep RankedEpisode
		if err := rows.Scan(
			&ep.Episode.ID, &ep.Episode.Title, &ep.Episode.EpisodeType,
			&ep.Episode.Trigger, &ep.Episode.Investigation, &ep.Episode.RootCause,
			&ep.Episode.Resolution, &ep.Episode.Verification,
			&ep.Episode.Tags, &ep.Episode.FilesInvolved, &ep.Episode.ErrorPatterns,
			&ep.Episode.Status, &ep.Similarity,
		); err != nil {
			return nil, fmt.Errorf("store: episode similarity scan: %w", err)
		}
		ep.Episode.ProjectID = projectID
		out = append(out, ep)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: episode similarity rows: %w", err)
	}
	return out, nil
}
