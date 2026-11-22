// memory_stale.go — file-based staleness and file lookup for memories
// (migration 042).
//
// The desktop reports changed files; CONFIRMED memories whose
// files_affected name one of them and that were written before the change
// get stale_since set. Agents see them flagged so they verify before relying
// on them. Re-confirming or editing a memory clears the flag (ConfirmMemory,
// UpdateMemory, RevertMemory).
package store

import (
	"context"
	"strings"
	"time"
)

// MaxStalePaths caps how many paths one files-changed report may carry.
const MaxStalePaths = 500

// NormalizeFilePath makes file paths comparable: forward slashes, no "./"
// prefix, no trailing slash, lower case (Windows paths are case-insensitive
// and the desktop is Windows-first).
func NormalizeFilePath(p string) string {
	p = strings.TrimSpace(strings.ReplaceAll(p, `\`, "/"))
	for strings.HasPrefix(p, "./") {
		p = p[2:]
	}
	for strings.Contains(p, "//") {
		p = strings.ReplaceAll(p, "//", "/")
	}
	p = strings.TrimSuffix(p, "/")
	return strings.ToLower(p)
}

// FilePathMatches reports whether two paths name the same file: after
// normalising, one must equal the other or be a suffix of it that starts at
// a path-segment boundary. So "internal/mcp/tools.go" matches
// "D:\repo\internal\mcp\tools.go" and "mcp/tools.go", but "ools.go" matches
// neither.
func FilePathMatches(a, b string) bool {
	a, b = NormalizeFilePath(a), NormalizeFilePath(b)
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	if len(a) < len(b) {
		a, b = b, a
	}
	return strings.HasSuffix(a, "/"+b)
}

// MemoryTouchesFile reports whether any of m.FilesAffected matches path.
func MemoryTouchesFile(m *MemoryItem, path string) bool {
	if m == nil {
		return false
	}
	for _, f := range m.FilesAffected {
		if FilePathMatches(f, path) {
			return true
		}
	}
	return false
}

func memoryTouchesAny(m *MemoryItem, paths []string) bool {
	for _, p := range paths {
		if MemoryTouchesFile(m, p) {
			return true
		}
	}
	return false
}

// cleanStalePaths trims, drops empties and duplicates, and caps the list.
func cleanStalePaths(paths []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		n := NormalizeFilePath(p)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
		if len(out) >= MaxStalePaths {
			break
		}
	}
	return out
}

// fileBaseName is the last path segment of a normalised path.
func fileBaseName(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// ---- MemStore ----

// MarkMemoriesStaleForFiles flags CONFIRMED memories of projectID whose
// files_affected match any of paths and that were created before at. Rows
// already stale keep their first stamp. Returns the IDs it flagged.
func (s *MemStore) MarkMemoriesStaleForFiles(ctx context.Context, projectID string, paths []string, at time.Time) ([]string, error) {
	_ = ctx
	paths = cleanStalePaths(paths)
	if strings.TrimSpace(projectID) == "" || len(paths) == 0 {
		return []string{}, nil
	}
	if at.IsZero() {
		at = time.Now()
	}
	at = at.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	marked := []string{}
	for _, m := range s.memories {
		if m.ProjectID != projectID || m.Status != StatusConfirmed || m.StaleSince != nil {
			continue
		}
		if !m.CreatedAt.Before(at) || !memoryTouchesAny(m, paths) {
			continue
		}
		stamp := at
		m.StaleSince = &stamp
		marked = append(marked, m.ID)
	}
	return marked, nil
}

// SearchMemoryByFile returns active memories (visible like SearchMemory)
// whose files_affected match path, highest confidence first.
func (s *MemStore) SearchMemoryByFile(ctx context.Context, projectID, path string, limit int) ([]*MemoryItem, error) {
	if NormalizeFilePath(path) == "" {
		return []*MemoryItem{}, nil
	}
	if limit <= 0 {
		limit = 20
	}
	all, err := s.SearchMemory(ctx, projectID, "", nil, 1<<30)
	if err != nil {
		return nil, err
	}
	out := make([]*MemoryItem, 0, limit)
	for _, m := range all {
		if MemoryTouchesFile(m, path) {
			out = append(out, m)
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

// ---- PostgresStore ----

// MarkMemoriesStaleForFiles is the Postgres form: a basename LIKE prefilter
// in SQL, the exact segment-suffix rule in Go, then one UPDATE.
func (s *PostgresStore) MarkMemoriesStaleForFiles(ctx context.Context, projectID string, paths []string, at time.Time) ([]string, error) {
	paths = cleanStalePaths(paths)
	if strings.TrimSpace(projectID) == "" || len(paths) == 0 {
		return []string{}, nil
	}
	if at.IsZero() {
		at = time.Now()
	}
	at = at.UTC()
	bases := make([]string, 0, len(paths))
	for _, p := range paths {
		bases = append(bases, fileBaseName(p))
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id::text, COALESCE(files_affected, '{}') FROM memory_items m
		  WHERE project_id = $1::uuid
		    AND status = 'CONFIRMED'
		    AND superseded_by IS NULL
		    AND stale_since IS NULL
		    AND created_at < $2
		    AND EXISTS (SELECT 1 FROM unnest(m.files_affected) f, unnest($3::text[]) b
		                 WHERE lower(replace(f, '\', '/')) LIKE '%' || b)`,
		projectID, at, bases)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var m MemoryItem
		if err := rows.Scan(&m.ID, &m.FilesAffected); err != nil {
			rows.Close()
			return nil, err
		}
		if memoryTouchesAny(&m, paths) {
			ids = append(ids, m.ID)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return []string{}, nil
	}
	// stale_since IS NULL again: a concurrent report keeps the first stamp.
	marked := []string{}
	out, err := s.pool.Query(ctx,
		`UPDATE memory_items SET stale_since = $2
		  WHERE id = ANY($1::uuid[]) AND stale_since IS NULL AND status = 'CONFIRMED'
		  RETURNING id::text`, ids, at)
	if err != nil {
		return nil, err
	}
	defer out.Close()
	for out.Next() {
		var id string
		if err := out.Scan(&id); err != nil {
			return nil, err
		}
		marked = append(marked, id)
	}
	return marked, out.Err()
}

// SearchMemoryByFile returns active memories whose files_affected match
// path. Visibility follows SearchMemoryPage (WithViewer).
func (s *PostgresStore) SearchMemoryByFile(ctx context.Context, projectID, path string, limit int) ([]*MemoryItem, error) {
	norm := NormalizeFilePath(path)
	if norm == "" {
		return []*MemoryItem{}, nil
	}
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+memoryColumns+` FROM memory_items
		  WHERE (project_id = $1::uuid OR (project_id IS NULL AND level = 'organization'))
		    AND status IN ('CONFIRMED','PROPOSED')
		    AND superseded_by IS NULL
		    AND EXISTS (SELECT 1 FROM unnest(files_affected) f
		                 WHERE lower(replace(f, '\', '/')) LIKE '%' || $2)
		  ORDER BY confidence DESC, created_at DESC
		  LIMIT $3`,
		nullText(projectID), fileBaseName(norm), limit*8)
	if err != nil {
		return nil, err
	}
	var candidates []*MemoryItem
	for rows.Next() {
		m, err := scanMemoryItem(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		if MemoryTouchesFile(m, path) {
			candidates = append(candidates, m)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	viewerID := ViewerFrom(ctx)
	isMember := false
	if viewerID != "" {
		ok, merr := s.IsProjectMember(ctx, viewerID, projectID)
		if merr != nil {
			return nil, merr
		}
		isMember = ok
	}
	out := make([]*MemoryItem, 0, limit)
	for _, m := range candidates {
		if len(out) >= limit {
			break
		}
		vis := NormalizeVisibility(m.Visibility)
		if viewerID == "" {
			if vis != VisibilityProject && vis != VisibilityPublic {
				continue
			}
		} else {
			hasShare := false
			if vis == VisibilityShared {
				hasShare, err = s.hasMemoryShare(ctx, m.ID, viewerID, m.ProjectID)
				if err != nil {
					return nil, err
				}
			}
			if !CanViewMemory(m, viewerID, isMember, hasShare) {
				continue
			}
		}
		out = append(out, m)
	}
	return out, nil
}
