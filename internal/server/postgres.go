// Postgres-backed Store adapter for issue #37 (plan §§1.5, 1.8).
//
// Deploy ships deploy/server-bootstrap/main.go with a stubStore that fails
// closed on every data route; GET /memory/search is text-only with no vector
// path. This file is the mechanical adapter that closes both gaps WITHOUT
// touching internal/store/* (parallel-agent ownership rule):
//
//   - PostgresStore wraps any store.DBTX (*store.DB satisfies it, so
//     production passes the pool-backed *DB and tests pass scripted fakes)
//     and implements all 9 Store methods (server.go, frozen):
//     Authenticate → UserStore.GetByUsername (issue #34; password
//     verification stays fail-closed until a password_hash column lands)
//     ResolveProject → ProjectStore.Resolve
//     RegisterWorkspace → WorkspaceStore.Register
//     HeartbeatWorkspace → WorkspaceStore.Heartbeat
//     ListWorkspaces → raw per-project SELECT (no store method returns an
//     unfiltered list; the SERVER applies the IsOnlineAt gate, same
//     contract the fakeStore in server_test.go proves)
//     CreateMemory → INSERT … RETURNING (no store create method exists)
//     SearchMemory → text/tag/key/level SELECT (ILIKE + @> + LIMIT clamp)
//     CreateEpisode → INSERT … RETURNING preserving status/type/tags/files/
//     patterns (EpisodeStore.Create forces RESOLVED and drops tags, so
//     direct SQL is required for fidelity)
//     SearchEpisodes → filtered SELECT (status/type/error_pattern/file/
//     ILIKE/LIMIT)
//   - VectorMemorySearcher (optional extension, lifecycle.go precedent:
//     server.go stays frozen) routes ?embedding= through store.Search —
//     the plan §1.5 pgvector cosine search via BuildSearchSQL plus the
//     0.7/0.2/0.1 rerank — so GET /memory/search gains a real vector path
//     while the text path keeps working.
//   - GetMemory/UpdateMemory implement the lifecycleStore extension that
//     lifecycle.go (issue #38) instructs "the future Postgres adapter" to
//     provide, with the exact SQL semantics quoted there (promote NULLs
//     session_id alongside the level flip).
//
// Mapping limits (see ADR-037): vector rows do not select status/source, so
// SearchMemoryVector defaults Status to CONFIRMED (the SQL guard) and Source
// to ""; text-path rows select both. Rationale for every choice lives in
// docs/decisions/ADR-037-server-postgres-adapter.md.
package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"central-memory/internal/store"
)

// errAuthPending fails login closed for KNOWN users: migration 001 carries
// no credential column (see UserStore, issue #34), so there is nothing to
// verify a password against. Unknown users map to ErrUnauthorized (401);
// known users surface this error (500 via storeError) instead of pretending
// a comparison happened. Landing a password_hash column + hashing flips this
// method to a real check (ADR-037 follow-up).
var errAuthPending = errors.New("server: password verification pending password_hash column")

// VectorMemorySearcher is the OPTIONAL vector path for GET /memory/search.
// It mirrors the lifecycleStore pattern (lifecycle.go): server.go stays
// frozen, handlers type-assert, and stores that predate issue #37 answer
// 400 instead of panicking. PostgresStore implements it via store.Search.
type VectorMemorySearcher interface {
	SearchMemoryVector(ctx context.Context, q MemoryFilter, embedding []float32) ([]Memory, error)
}

// PostgresStore adapts *store.DB (or any store.DBTX fake) to the narrow
// server.Store seam. It is safe for concurrent use when db is.
type PostgresStore struct {
	db         store.DBTX
	projects   *store.ProjectStore
	workspaces *store.WorkspaceStore
	episodes   *store.EpisodeStore
	users      *store.UserStore
}

// Compile-time proofs: the adapter satisfies the frozen Store seam plus the
// two optional extensions (vector search, issue #37; lifecycle, issue #38).
var (
	_ Store                = (*PostgresStore)(nil)
	_ VectorMemorySearcher = (*PostgresStore)(nil)
	_ lifecycleStore       = (*PostgresStore)(nil)
)

// NewPostgresStore wires the adapter around db (production: *store.DB from
// store.Connect; tests: any scripted store.DBTX fake). It panics on nil —
// fail fast rather than nil-dereferencing per request.
func NewPostgresStore(db store.DBTX) *PostgresStore {
	if db == nil {
		panic("server: nil DBTX")
	}
	return &PostgresStore{
		db:         db,
		projects:   store.NewProjectStore(db),
		workspaces: store.NewWorkspaceStore(db),
		episodes:   store.NewEpisodeStore(db),
		users:      store.NewUserStore(db),
	}
}

// ---------------------------------------------------------------------------
// Column lists (NULL-coalesced so rows scan into plain server shapes)
// ---------------------------------------------------------------------------

// pgWorkspaceColumns scans positionally into store.Workspace:
// ID, ProjectID, UserID, MachineID, Path, Branch, CommitSHA, IsDirty,
// IsOnline, IsDesignatedProcessor, LastSeen (*time.Time, nullable),
// DaemonURL, CreatedAt.
const pgWorkspaceColumns = `id::TEXT AS id, ` +
	`project_id::TEXT AS project_id, ` +
	`user_id::TEXT AS user_id, ` +
	`machine_id, ` +
	`path, ` +
	`COALESCE(branch, '') AS branch, ` +
	`COALESCE(commit_sha, '') AS commit_sha, ` +
	`COALESCE(is_dirty, false) AS is_dirty, ` +
	`COALESCE(is_online, false) AS is_online, ` +
	`COALESCE(is_designated_processor, false) AS is_designated_processor, ` +
	`last_seen, ` +
	`COALESCE(daemon_url, '') AS daemon_url, ` +
	`created_at`

// pgMemoryColumns scans positionally into server.Memory plus a time.Time:
// ID, ProjectID, Key, Content, ContextSnippet, Level, Scope, Tags,
// Confidence, Status, Source, CreatedAt.
const pgMemoryColumns = `id::TEXT AS id, ` +
	`project_id::TEXT AS project_id, ` +
	`key, ` +
	`content, ` +
	`COALESCE(context_snippet, '') AS context_snippet, ` +
	`level, ` +
	`scope, ` +
	`COALESCE(tags, '{}') AS tags, ` +
	`confidence, ` +
	`status, ` +
	`COALESCE(source, '') AS source, ` +
	`created_at`

// pgEpisodeColumns scans positionally into server.Episode plus a time.Time:
// ID, Title, EpisodeType, Trigger, Investigation, RootCause, Resolution,
// Verification, Tags, FilesInvolved, ErrorPatterns, Status, OpenedAt.
const pgEpisodeColumns = `id::TEXT AS id, ` +
	`title, ` +
	`episode_type, ` +
	`COALESCE(trigger, '') AS trigger, ` +
	`COALESCE(investigation, '') AS investigation, ` +
	`COALESCE(root_cause, '') AS root_cause, ` +
	`COALESCE(resolution, '') AS resolution, ` +
	`COALESCE(verification, '') AS verification, ` +
	`COALESCE(tags, '{}') AS tags, ` +
	`COALESCE(files_involved, '{}') AS files_involved, ` +
	`COALESCE(error_patterns, '{}') AS error_patterns, ` +
	`status, ` +
	`opened_at`

// ---------------------------------------------------------------------------
// Small mapping helpers
// ---------------------------------------------------------------------------

// pgTime renders a DB timestamp as the server's RFC3339 JSON shape.
func pgTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

// pgNullText maps "" to SQL NULL for nullable text columns (mirrors the
// store package's nullText, which is unexported and therefore unreachable).
func pgNullText(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// pgLimit clamps a caller limit into [1, max], defaulting non-positive to
// def. Callers pass the routes.go defaultSearchLimit/maxSearchLimit consts
// so the adapter can never drift from the API boundary.
func pgLimit(lim, def, max int) int {
	if lim <= 0 {
		return def
	}
	if lim > max {
		return max
	}
	return lim
}

// escapeLikePattern escapes the LIKE wildcards so ?q= matches literally
// (a query for "100%" must not match "1000"). Backslash is the ESCAPE char.
func escapeLikePattern(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}

// scanWorkspaceRow scans one pgWorkspaceColumns row.
func scanWorkspaceRow(rows store.Rows) (store.Workspace, error) {
	var w store.Workspace
	if err := rows.Scan(
		&w.ID, &w.ProjectID, &w.UserID, &w.MachineID, &w.Path,
		&w.Branch, &w.CommitSHA, &w.IsDirty, &w.IsOnline,
		&w.IsDesignatedProcessor, &w.LastSeen, &w.DaemonURL, &w.CreatedAt,
	); err != nil {
		return store.Workspace{}, err
	}
	return w, nil
}

// scanMemoryRow scans one pgMemoryColumns row plus created_at into Memory.
func scanMemoryRow(rows store.Rows) (Memory, error) {
	var m Memory
	var created time.Time
	if err := rows.Scan(
		&m.ID, &m.ProjectID, &m.Key, &m.Content, &m.ContextSnippet,
		&m.Level, &m.Scope, &m.Tags, &m.Confidence, &m.Status, &m.Source,
		&created,
	); err != nil {
		return Memory{}, err
	}
	m.CreatedAt = pgTime(created)
	return m, nil
}

// scanEpisodeRow scans one pgEpisodeColumns row plus opened_at into Episode.
func scanEpisodeRow(rows store.Rows, projectID string) (Episode, error) {
	var e Episode
	var opened time.Time
	if err := rows.Scan(
		&e.ID, &e.Title, &e.EpisodeType,
		&e.Trigger, &e.Investigation, &e.RootCause,
		&e.Resolution, &e.Verification,
		&e.Tags, &e.FilesInvolved, &e.ErrorPatterns, &e.Status,
		&opened,
	); err != nil {
		return Episode{}, err
	}
	e.ProjectID = projectID
	e.OpenedAt = pgTime(opened)
	return e, nil
}

// ---------------------------------------------------------------------------
// Store: auth + projects + workspaces (pure delegation)
// ---------------------------------------------------------------------------

// Authenticate resolves the username via UserStore.GetByUsername. Unknown
// users → ErrUnauthorized (401). Known users → errAuthPending (500,
// fail-closed): no credential column exists to verify against, so success
// must be impossible until the password_hash follow-up lands (ADR-037).
func (p *PostgresStore) Authenticate(ctx context.Context, username, password string) (string, error) {
	if strings.TrimSpace(username) == "" || password == "" {
		return "", ErrUnauthorized
	}
	u, err := p.users.GetByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return "", ErrUnauthorized
		}
		return "", err
	}
	return "", fmt.Errorf("server: user %s: %w", u.ID, errAuthPending)
}

// ResolveProject delegates to ProjectStore.Resolve (plan §1.2 upsert).
func (p *PostgresStore) ResolveProject(ctx context.Context, params store.ProjectParams) (*store.Project, error) {
	return p.projects.Resolve(ctx, params)
}

// RegisterWorkspace delegates to WorkspaceStore.Register (upsert on
// machine_id + path).
func (p *PostgresStore) RegisterWorkspace(ctx context.Context, params store.WorkspaceParams) (*store.Workspace, error) {
	return p.workspaces.Register(ctx, params)
}

// HeartbeatWorkspace delegates to WorkspaceStore.Heartbeat.
func (p *PostgresStore) HeartbeatWorkspace(ctx context.Context, id string, hb store.HeartbeatParams) (*store.Workspace, error) {
	return p.workspaces.Heartbeat(ctx, id, hb)
}

// ListWorkspaces returns the RAW per-project list (no staleness filter).
// There is deliberately no store method for this: WorkspaceStore.ListActive
// pre-filters by is_online + last_seen, which would hide offline rows the
// server's IsOnlineAt gate (and its 90s-boundary semantics) must see. The
// server filters, exactly as the fakeStore contract in server_test.go does.
func (p *PostgresStore) ListWorkspaces(ctx context.Context, projectID string) ([]store.Workspace, error) {
	rows, err := p.db.Query(ctx,
		`SELECT `+pgWorkspaceColumns+` FROM workspaces WHERE project_id = $1 ORDER BY last_seen DESC NULLS LAST`,
		projectID)
	if err != nil {
		return nil, fmt.Errorf("server: list workspaces: %w", err)
	}
	defer rows.Close()
	var out []store.Workspace
	for rows.Next() {
		w, err := scanWorkspaceRow(rows)
		if err != nil {
			return nil, fmt.Errorf("server: list workspaces scan: %w", err)
		}
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("server: list workspaces rows: %w", err)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Store: memories
// ---------------------------------------------------------------------------

// CreateMemory inserts one memory_items row. No store create method exists,
// so the SQL lives here (never in internal/store/* per the ownership rule).
func (p *PostgresStore) CreateMemory(ctx context.Context, m Memory) (*Memory, error) {
	rows, err := p.db.Query(ctx,
		`INSERT INTO memory_items (project_id, key, content, context_snippet, level, scope, tags, confidence, status, source)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		 RETURNING id::TEXT AS id, created_at`,
		m.ProjectID, m.Key, m.Content, pgNullText(m.ContextSnippet),
		m.Level, m.Scope, m.Tags, m.Confidence, m.Status, pgNullText(m.Source))
	if err != nil {
		return nil, fmt.Errorf("server: create memory: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("server: create memory rows: %w", err)
		}
		return nil, fmt.Errorf("server: create memory returned no id")
	}
	var id string
	var created time.Time
	if err := rows.Scan(&id, &created); err != nil {
		return nil, fmt.Errorf("server: create memory scan: %w", err)
	}
	out := m
	out.ID = id
	out.CreatedAt = pgTime(created)
	return &out, nil
}

// SearchMemory is the TEXT path: substring over key/content/context_snippet
// (ILIKE, literally escaped) plus exact tag-containment (@>, matching the
// fakeStore hasAllTags semantics), key, level, and a clamped LIMIT. Empty
// fields are ignored except ProjectID. The vector path is SearchMemoryVector
// (?embedding=); ?q= is never stub-embedded (ADR-037: deterministic fake
// vectors would corrupt cosine ranking while looking authoritative).
func (p *PostgresStore) SearchMemory(ctx context.Context, q MemoryFilter) ([]Memory, error) {
	args := []any{q.ProjectID}
	var b strings.Builder
	b.WriteString(`SELECT ` + pgMemoryColumns + ` FROM memory_items WHERE project_id = $1`)
	next := 2
	if q.Query != "" {
		fmt.Fprintf(&b, ` AND (key ILIKE $%d ESCAPE '\' OR content ILIKE $%d ESCAPE '\' OR COALESCE(context_snippet, '') ILIKE $%d ESCAPE '\'')`, next, next, next)
		args = append(args, "%"+escapeLikePattern(q.Query)+"%")
		next++
	}
	if len(q.Tags) > 0 {
		fmt.Fprintf(&b, ` AND tags @> $%d`, next)
		args = append(args, q.Tags)
		next++
	}
	if q.Key != "" {
		fmt.Fprintf(&b, ` AND key = $%d`, next)
		args = append(args, q.Key)
		next++
	}
	if q.Level != "" {
		fmt.Fprintf(&b, ` AND level = $%d`, next)
		args = append(args, q.Level)
		next++
	}
	fmt.Fprintf(&b, ` ORDER BY created_at DESC LIMIT $%d`, next)
	args = append(args, pgLimit(q.Limit, defaultSearchLimit, maxSearchLimit))

	rows, err := p.db.Query(ctx, b.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("server: search memory: %w", err)
	}
	defer rows.Close()
	var out []Memory
	for rows.Next() {
		m, err := scanMemoryRow(rows)
		if err != nil {
			return nil, fmt.Errorf("server: search memory scan: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("server: search memory rows: %w", err)
	}
	return out, nil
}

// SearchMemoryVector is the VECTOR path (VectorMemorySearcher extension):
// plan §1.5 cosine search through store.Search (BuildSearchSQL guard rails +
// 0.7/0.2/0.1 rerank). Status defaults to CONFIRMED (the SQL guard, which is
// not selected back) and Source to "" (embedding-adjacent columns are not
// selected by BuildSearchSQL); the text path selects both exactly.
func (p *PostgresStore) SearchMemoryVector(ctx context.Context, q MemoryFilter, embedding []float32) ([]Memory, error) {
	if len(embedding) == 0 {
		return nil, fmt.Errorf("server: search memory vector requires a non-empty embedding")
	}
	ranked, err := store.Search(ctx, p.db, store.SearchQuery{
		ProjectID:      q.ProjectID,
		QueryEmbedding: embedding,
		Tags:           q.Tags,
		Key:            q.Key,
		Level:          q.Level,
		Limit:          pgLimit(q.Limit, defaultSearchLimit, maxSearchLimit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]Memory, 0, len(ranked))
	for _, r := range ranked {
		out = append(out, Memory{
			ID:             r.Item.ID,
			ProjectID:      r.Item.ProjectID,
			Key:            r.Item.Key,
			Content:        r.Item.Content,
			ContextSnippet: r.Item.ContextSnippet,
			Level:          r.Item.Level,
			Scope:          r.Item.Scope,
			Tags:           append([]string(nil), r.Item.Tags...),
			Confidence:     r.Item.Confidence,
			Status:         store.StatusConfirmed,
			CreatedAt:      pgTime(r.Item.CreatedAt),
		})
	}
	return out, nil
}

// GetMemory fetches one memory by id (lifecycleStore extension, issue #38).
func (p *PostgresStore) GetMemory(ctx context.Context, id string) (*Memory, error) {
	rows, err := p.db.Query(ctx,
		`SELECT `+pgMemoryColumns+` FROM memory_items WHERE id = $1`, id)
	if err != nil {
		return nil, fmt.Errorf("server: get memory: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("server: get memory rows: %w", err)
		}
		return nil, fmt.Errorf("server: memory %s: %w", id, store.ErrNotFound)
	}
	m, err := scanMemoryRow(rows)
	if err != nil {
		return nil, fmt.Errorf("server: get memory scan: %w", err)
	}
	return &m, nil
}

// UpdateMemory persists a lifecycle mutation (lifecycleStore extension).
// Promote MUST NULL session_id in the same update (sessions.go promotion
// rule, ADR-012); every other lifecycle write leaves ownership alone, so
// session_id is cleared exactly when the level flips to project — the same
// condition the promote handler enforces.
func (p *PostgresStore) UpdateMemory(ctx context.Context, m Memory) (*Memory, error) {
	clearSession := m.Level == store.LevelProject
	rows, err := p.db.Query(ctx,
		`UPDATE memory_items SET key = $2, content = $3, context_snippet = $4,
		 level = $5, scope = $6, tags = $7, confidence = $8, status = $9, source = $10,
		 session_id = CASE WHEN $11 THEN NULL ELSE session_id END, updated_at = now()
		 WHERE id = $1 RETURNING `+pgMemoryColumns,
		m.ID, m.Key, m.Content, pgNullText(m.ContextSnippet),
		m.Level, m.Scope, m.Tags, m.Confidence, m.Status, pgNullText(m.Source),
		clearSession)
	if err != nil {
		return nil, fmt.Errorf("server: update memory: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("server: update memory rows: %w", err)
		}
		return nil, fmt.Errorf("server: memory %s: %w", m.ID, store.ErrNotFound)
	}
	updated, err := scanMemoryRow(rows)
	if err != nil {
		return nil, fmt.Errorf("server: update memory scan: %w", err)
	}
	return &updated, nil
}

// ---------------------------------------------------------------------------
// Store: episodes
// ---------------------------------------------------------------------------

// CreateEpisode inserts one episodes row preserving every server field.
// EpisodeStore.Create is deliberately NOT used: it forces status RESOLVED
// and drops tags, while the API defaults to OPEN and round-trips tags
// (see TestEpisodeRoundtrip). Direct SQL is the only fidelity-preserving
// route without editing internal/store/*.
func (p *PostgresStore) CreateEpisode(ctx context.Context, e Episode) (*Episode, error) {
	rows, err := p.db.Query(ctx,
		`INSERT INTO episodes (project_id, title, episode_type, trigger, investigation,
		 root_cause, resolution, verification, tags, files_involved, error_patterns, status)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		 RETURNING id::TEXT AS id, opened_at`,
		e.ProjectID, e.Title, e.EpisodeType,
		pgNullText(e.Trigger), pgNullText(e.Investigation),
		pgNullText(e.RootCause), pgNullText(e.Resolution),
		pgNullText(e.Verification),
		e.Tags, e.FilesInvolved, e.ErrorPatterns, e.Status)
	if err != nil {
		return nil, fmt.Errorf("server: create episode: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("server: create episode rows: %w", err)
		}
		return nil, fmt.Errorf("server: create episode returned no id")
	}
	var id string
	var opened time.Time
	if err := rows.Scan(&id, &opened); err != nil {
		return nil, fmt.Errorf("server: create episode scan: %w", err)
	}
	out := e
	out.ID = id
	out.OpenedAt = pgTime(opened)
	return &out, nil
}

// SearchEpisodes filters episodes: exact status/episode_type, exact
// error_pattern (= ANY) and file (= ANY) membership, substring ?q= over
// title/trigger/root_cause/resolution (ILIKE, literally escaped), newest
// first with a clamped LIMIT. No single EpisodeStore method covers this
// combination (ByErrorPattern/ByFile/Similar are single-purpose), so the
// combined SELECT lives here.
func (p *PostgresStore) SearchEpisodes(ctx context.Context, q EpisodeFilter) ([]Episode, error) {
	args := []any{q.ProjectID}
	var b strings.Builder
	b.WriteString(`SELECT ` + pgEpisodeColumns + ` FROM episodes WHERE project_id = $1`)
	next := 2
	if q.Status != "" {
		fmt.Fprintf(&b, ` AND status = $%d`, next)
		args = append(args, q.Status)
		next++
	}
	if q.EpisodeType != "" {
		fmt.Fprintf(&b, ` AND episode_type = $%d`, next)
		args = append(args, q.EpisodeType)
		next++
	}
	if q.ErrorPattern != "" {
		fmt.Fprintf(&b, ` AND $%d = ANY(error_patterns)`, next)
		args = append(args, q.ErrorPattern)
		next++
	}
	if q.File != "" {
		fmt.Fprintf(&b, ` AND $%d = ANY(files_involved)`, next)
		args = append(args, q.File)
		next++
	}
	if q.Query != "" {
		fmt.Fprintf(&b, ` AND (title ILIKE $%d ESCAPE '\' OR COALESCE(trigger, '') ILIKE $%d ESCAPE '\' OR COALESCE(root_cause, '') ILIKE $%d ESCAPE '\' OR COALESCE(resolution, '') ILIKE $%d ESCAPE '\'')`, next, next, next, next)
		args = append(args, "%"+escapeLikePattern(q.Query)+"%")
		next++
	}
	fmt.Fprintf(&b, ` ORDER BY opened_at DESC LIMIT $%d`, next)
	args = append(args, pgLimit(q.Limit, defaultSearchLimit, maxSearchLimit))

	rows, err := p.db.Query(ctx, b.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("server: search episodes: %w", err)
	}
	defer rows.Close()
	var out []Episode
	for rows.Next() {
		e, err := scanEpisodeRow(rows, q.ProjectID)
		if err != nil {
			return nil, fmt.Errorf("server: search episodes scan: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("server: search episodes rows: %w", err)
	}
	return out, nil
}
