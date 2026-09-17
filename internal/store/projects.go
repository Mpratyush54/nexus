// Project identity and CRUD (issue #2, plan §1.2).
//
// The canonical resolver reuses the internal/project identity model:
// project.Fingerprint(dir) yields (origin, rootCommit); this file normalizes
// the origin into canonical_url and resolves against the projects table with
// the planned priority chain — canonical_url, then root_commit, then
// folder_name — as a deterministic upsert that never creates duplicates.
//
// The matching core (NormalizeRemoteURL, MatchProject) is pure and covered by
// table-driven unit tests. Only Resolve/Create/Get/Update/Delete/List touch
// the database (via DBTX) and are covered by TEST_POSTGRES_DSN-gated
// integration tests.
package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"central-memory/internal/project"

	"github.com/jackc/pgx/v5"
)

// Project mirrors a projects row (plan §1.1). Empty CanonicalURL/RootCommit/
// DisplayName mean SQL NULL; FolderName is always set.
type Project struct {
	ID           string
	CanonicalURL string
	RootCommit   string
	FolderName   string
	DisplayName  string
	CreatedAt    string // RFC3339; informational, set by the database
}

// ProjectParams carries project identity for Resolve/Create. Origin is the
// raw git remote URL (as returned by project.Fingerprint); it is normalized
// internally, so callers must not pre-normalize.
type ProjectParams struct {
	Origin      string
	RootCommit  string
	FolderName  string
	DisplayName string
	CreatedBy   string // optional user UUID; "" = NULL
}

// MatchStrategy names which identity signal resolved a project. The zero
// value reports no match.
type MatchStrategy string

// Resolution strategies in priority order (plan §1.2).
const (
	MatchNone         MatchStrategy = "none"
	MatchCanonicalURL MatchStrategy = "canonical_url"
	MatchRootCommit   MatchStrategy = "root_commit"
	MatchFolderName   MatchStrategy = "folder_name"
)

// NormalizeRemoteURL canonicalizes a git remote URL for identity comparison:
//
//	git@github.com:org/repo.git      → github.com/org/repo
//	https://github.com/org/repo(.git) → github.com/org/repo
//	ssh://git@github.com:22/org/repo → github.com/org/repo
//
// Rules: trim whitespace, lowercase (hosting providers compare
// case-insensitively; determinism wins over theoretical case-sensitive
// hosts), drop scheme/userinfo/port, unify scp-like and URL forms, strip one
// trailing ".git" and surrounding slashes. Empty input yields "".
func NormalizeRemoteURL(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return ""
	}
	// Windows drive-letter path remote ("c:\repos\foo", "c:/repos/foo").
	if len(s) >= 3 && s[1] == ':' && s[0] >= 'a' && s[0] <= 'z' {
		return trimRepoSuffix(strings.ReplaceAll(s, "\\", "/"))
	}
	if i := strings.Index(s, "://"); i >= 0 {
		rest := s[i+3:]
		// Strip userinfo ("user:pass@host").
		if at := strings.LastIndex(rest, "@"); at >= 0 {
			rest = rest[at+1:]
		}
		host, path := rest, ""
		if j := strings.Index(rest, "/"); j >= 0 {
			host, path = rest[:j], rest[j+1:]
		}
		// Strip numeric port ("host:22").
		if k := strings.LastIndex(host, ":"); k >= 0 {
			if _, err := strconv.Atoi(host[k+1:]); err == nil {
				host = host[:k]
			}
		}
		// Strip query/fragment from the path.
		if j := strings.IndexAny(path, "?#"); j >= 0 {
			path = path[:j]
		}
		if host == "" { // file://… — path-only canonical form.
			return trimRepoSuffix(path)
		}
		return trimRepoSuffix(host + "/" + path)
	}
	// scp-like "[user@]host:path".
	body := s
	if at := strings.LastIndex(body, "@"); at >= 0 {
		body = body[at+1:]
	}
	if i := strings.Index(body, ":"); i >= 0 && !strings.Contains(body[:i], "/") {
		return trimRepoSuffix(body[:i] + "/" + body[i+1:])
	}
	// Bare path remote ("/srv/git/foo.git", "../foo").
	return trimRepoSuffix(strings.ReplaceAll(body, "\\", "/"))
}

// trimRepoSuffix strips slashes, one ".git" suffix, and leftover slashes.
// The caller lowercases first so the suffix match is case-insensitive.
func trimRepoSuffix(s string) string {
	s = strings.Trim(s, "/")
	s = strings.TrimSuffix(s, ".git")
	return strings.Trim(s, "/")
}

// MatchProject resolves identity against candidate rows with the planned
// priority: canonical_url, then root_commit, then folder_name. Empty signals
// never match. Ties within one strategy resolve to the first candidate, so
// callers must pass candidates in a deterministic order (Resolve orders by
// created_at, id) — resolution is then a pure function of (signals, rows).
func MatchProject(candidates []Project, canonicalURL, rootCommit, folderName string) (*Project, MatchStrategy) {
	if canonicalURL != "" {
		for i := range candidates {
			if candidates[i].CanonicalURL != "" && candidates[i].CanonicalURL == canonicalURL {
				return &candidates[i], MatchCanonicalURL
			}
		}
	}
	if rootCommit != "" {
		for i := range candidates {
			if candidates[i].RootCommit != "" && candidates[i].RootCommit == rootCommit {
				return &candidates[i], MatchRootCommit
			}
		}
	}
	if folderName != "" {
		for i := range candidates {
			if candidates[i].FolderName == folderName {
				return &candidates[i], MatchFolderName
			}
		}
	}
	return nil, MatchNone
}

// projectColumns selects projects with NULLs coalesced to "" so rows scan
// into the plain-string Project struct.
const projectColumns = `id::TEXT AS id, ` +
	`COALESCE(canonical_url, '') AS canonical_url, ` +
	`COALESCE(root_commit, '') AS root_commit, ` +
	`folder_name, ` +
	`COALESCE(display_name, '') AS display_name, ` +
	`created_at::TEXT AS created_at`

// projectLookupQuery builds the candidate-fetch query: one OR-arm per
// non-empty identity signal, deterministic ORDER BY for tie-breaking.
func projectLookupQuery(canonicalURL, rootCommit, folderName string) (string, []any) {
	var conds []string
	var args []any
	if canonicalURL != "" {
		args = append(args, canonicalURL)
		conds = append(conds, fmt.Sprintf("canonical_url = $%d", len(args)))
	}
	if rootCommit != "" {
		args = append(args, rootCommit)
		conds = append(conds, fmt.Sprintf("root_commit = $%d", len(args)))
	}
	if folderName != "" {
		args = append(args, folderName)
		conds = append(conds, fmt.Sprintf("folder_name = $%d", len(args)))
	}
	return `SELECT ` + projectColumns + ` FROM projects WHERE ` +
		strings.Join(conds, " OR ") + ` ORDER BY created_at ASC, id ASC`, args
}

// scanProject scans a full projectColumns row.
func scanProject(row pgx.Row) (*Project, error) {
	var p Project
	if err := row.Scan(&p.ID, &p.CanonicalURL, &p.RootCommit, &p.FolderName, &p.DisplayName, &p.CreatedAt); err != nil {
		return nil, err
	}
	return &p, nil
}

// nullText maps "" to SQL NULL for nullable text columns.
func nullText(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nullUUID maps "" to SQL NULL for nullable UUID columns.
func nullUUID(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// ProjectStore is project CRUD plus the canonical resolver.
type ProjectStore struct {
	db DBTX
}

// NewProjectStore wires a ProjectStore to any DBTX (pool, transaction, fake).
func NewProjectStore(db DBTX) *ProjectStore {
	return &ProjectStore{db: db}
}

// Resolve implements the plan §1.2 upsert: normalize the origin, fetch
// candidates, match by priority, insert when nothing matches. Concurrent
// inserts for the same identity collapse via ON CONFLICT DO NOTHING plus a
// re-resolve, so no duplicates are ever created.
func (s *ProjectStore) Resolve(ctx context.Context, params ProjectParams) (*Project, error) {
	canonical := NormalizeRemoteURL(params.Origin)
	root := strings.TrimSpace(params.RootCommit)
	folder := strings.TrimSpace(params.FolderName)
	display := strings.TrimSpace(params.DisplayName)
	if folder == "" {
		return nil, errors.New("store: project folder name is required for resolution")
	}
	if m, _, err := s.lookup(ctx, canonical, root, folder); err != nil {
		return nil, err
	} else if m != nil {
		return m, nil
	}
	p, err := s.insert(ctx, canonical, root, folder, display, strings.TrimSpace(params.CreatedBy))
	if err == nil {
		return p, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	// Lost a concurrent insert race: the winner is now resolvable.
	if m, _, err := s.lookup(ctx, canonical, root, folder); err != nil {
		return nil, err
	} else if m != nil {
		return m, nil
	}
	return nil, errors.New("store: project insert lost a conflict race and re-resolve found nothing")
}

// lookup fetches candidates and applies the pure priority matcher.
func (s *ProjectStore) lookup(ctx context.Context, canonical, root, folder string) (*Project, MatchStrategy, error) {
	query, args := projectLookupQuery(canonical, root, folder)
	if len(args) == 0 {
		return nil, MatchNone, nil
	}
	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, MatchNone, fmt.Errorf("store: lookup project: %w", err)
	}
	defer rows.Close()
	var candidates []Project
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.CanonicalURL, &p.RootCommit, &p.FolderName, &p.DisplayName, &p.CreatedAt); err != nil {
			return nil, MatchNone, fmt.Errorf("store: lookup project scan: %w", err)
		}
		candidates = append(candidates, p)
	}
	if err := rows.Err(); err != nil {
		return nil, MatchNone, fmt.Errorf("store: lookup project rows: %w", err)
	}
	m, strategy := MatchProject(candidates, canonical, root, folder)
	return m, strategy, nil
}

// insert creates the project row; a conflicting concurrent insert yields
// pgx.ErrNoRows (ON CONFLICT DO NOTHING returns nothing) for the re-resolve.
func (s *ProjectStore) insert(ctx context.Context, canonical, root, folder, display, createdBy string) (*Project, error) {
	row := s.db.QueryRow(ctx,
		`INSERT INTO projects (canonical_url, root_commit, folder_name, display_name, created_by)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT DO NOTHING
		 RETURNING `+projectColumns,
		nullText(canonical), nullText(root), folder, nullText(display), nullUUID(createdBy))
	p, err := scanProject(row)
	if err != nil {
		return nil, fmt.Errorf("store: insert project: %w", err)
	}
	return p, nil
}

// ResolveLocal fingerprints a workspace directory with project.Fingerprint
// and resolves it — the daemon/server path from a local path to project_id.
func (s *ProjectStore) ResolveLocal(ctx context.Context, dir, displayName string) (*Project, error) {
	origin, root := project.Fingerprint(dir)
	return s.Resolve(ctx, ProjectParams{
		Origin:      origin,
		RootCommit:  root,
		FolderName:  filepath.Base(filepath.Clean(dir)),
		DisplayName: displayName,
	})
}

// Create inserts a project without resolving (administrative path).
func (s *ProjectStore) Create(ctx context.Context, params ProjectParams) (*Project, error) {
	folder := strings.TrimSpace(params.FolderName)
	if folder == "" {
		return nil, errors.New("store: project folder name is required")
	}
	row := s.db.QueryRow(ctx,
		`INSERT INTO projects (canonical_url, root_commit, folder_name, display_name, created_by)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING `+projectColumns,
		nullText(NormalizeRemoteURL(params.Origin)),
		nullText(strings.TrimSpace(params.RootCommit)),
		folder,
		nullText(strings.TrimSpace(params.DisplayName)),
		nullUUID(strings.TrimSpace(params.CreatedBy)))
	p, err := scanProject(row)
	if err != nil {
		return nil, fmt.Errorf("store: create project: %w", err)
	}
	return p, nil
}

// GetByID fetches one project or a wrapped ErrNotFound.
func (s *ProjectStore) GetByID(ctx context.Context, id string) (*Project, error) {
	p, err := scanProject(s.db.QueryRow(ctx,
		`SELECT `+projectColumns+` FROM projects WHERE id = $1`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("store: project %s: %w", id, ErrNotFound)
		}
		return nil, fmt.Errorf("store: get project: %w", err)
	}
	return p, nil
}

// UpdateDisplayName renames a project for UI purposes (identity untouched).
func (s *ProjectStore) UpdateDisplayName(ctx context.Context, id, displayName string) (*Project, error) {
	p, err := scanProject(s.db.QueryRow(ctx,
		`UPDATE projects SET display_name = $2 WHERE id = $1 RETURNING `+projectColumns,
		id, nullText(strings.TrimSpace(displayName))))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("store: project %s: %w", id, ErrNotFound)
		}
		return nil, fmt.Errorf("store: update project: %w", err)
	}
	return p, nil
}

// Delete removes a project. Referenced rows (workspaces, memories) block
// deletion with a foreign-key error — reassignment is a follow-up.
func (s *ProjectStore) Delete(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM projects WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("store: delete project: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("store: project %s: %w", id, ErrNotFound)
	}
	return nil
}

// List returns projects newest-first with a clamped limit.
func (s *ProjectStore) List(ctx context.Context, limit, offset int) ([]Project, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.Query(ctx,
		`SELECT `+projectColumns+` FROM projects ORDER BY created_at DESC, id DESC LIMIT $1 OFFSET $2`,
		limit, offset)
	if err != nil {
		return nil, fmt.Errorf("store: list projects: %w", err)
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.CanonicalURL, &p.RootCommit, &p.FolderName, &p.DisplayName, &p.CreatedAt); err != nil {
			return nil, fmt.Errorf("store: list projects scan: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list projects rows: %w", err)
	}
	return out, nil
}
