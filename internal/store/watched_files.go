// Watched-file hash persistence (issue #34, plan §§1.1, 1.3).
//
// The watched_files table shipped in migration 001 but had zero store
// coverage, and the daemon watcher (internal/daemon/watcher.go) kept
// last_hash in memory only — a daemon restart forgot every hash, so the
// first PollOnce after startup either missed edits made while down or
// re-emitted the full state as "changed". This file provides the server
// side: an upsert/get/list store over the DBTX seam (db.go) plus the pure
// staleness helper the daemon (or the Memory Processor) uses to compare
// stored hashes against a fresh scan.
//
// Conventions follow projects.go/workspaces.go: NULL-coalescing column
// lists, pgx.ErrNoRows mapped to wrapped ErrNotFound, validation before
// any statement. File-type constants transcribe the migration 001 CHECK
// constraint and mirror the daemon's InstructionFiles table.
package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Watched-file types (CHECK constraint in migration 001; the daemon maps
// each InstructionFiles entry to one of these).
const (
	WatchedClaudeMD            = "claude_md"
	WatchedCursorRules         = "cursorrules"
	WatchedCopilotInstructions = "copilot_instructions"
	WatchedWindsurfRules       = "windsurfrules"
	WatchedCustom              = "custom"
)

// IsValidWatchedFileType reports whether fileType is a known watched type.
func IsValidWatchedFileType(fileType string) bool {
	switch fileType {
	case WatchedClaudeMD, WatchedCursorRules, WatchedCopilotInstructions,
		WatchedWindsurfRules, WatchedCustom:
		return true
	default:
		return false
	}
}

// NormalizeWatchedFileType lower-cases and trims fileType. The second
// return is false when the type is unknown.
func NormalizeWatchedFileType(fileType string) (string, bool) {
	t := strings.ToLower(strings.TrimSpace(fileType))
	if !IsValidWatchedFileType(t) {
		return t, false
	}
	return t, true
}

// WatchedFile mirrors a watched_files row (migration 001). Path is
// workspace-relative; LastHash is "" until the first content scan.
type WatchedFile struct {
	ID          string
	WorkspaceID string
	Path        string
	LastHash    string
	FileType    string
	CreatedAt   time.Time
}

// WatchedFileParams carries the identity and hash for Upsert.
type WatchedFileParams struct {
	WorkspaceID string
	Path        string // workspace-relative; required
	LastHash    string // hex SHA256; "" = not yet scanned
	FileType    string // required; one of the Watched* constants
}

// watchedFileColumns selects watched files with NULLs coalesced and the
// UUID formatted as text.
const watchedFileColumns = `id::TEXT AS id, ` +
	`workspace_id::TEXT AS workspace_id, ` +
	`path, ` +
	`COALESCE(last_hash, '') AS last_hash, ` +
	`file_type, ` +
	`created_at`

// scanWatchedFile scans a full watchedFileColumns row.
func scanWatchedFile(row pgx.Row) (*WatchedFile, error) {
	var w WatchedFile
	if err := row.Scan(&w.ID, &w.WorkspaceID, &w.Path, &w.LastHash, &w.FileType, &w.CreatedAt); err != nil {
		return nil, err
	}
	return &w, nil
}

// WatchedFileStore is watched-file hash persistence over any DBTX.
type WatchedFileStore struct {
	db DBTX
}

// NewWatchedFileStore wires a WatchedFileStore to any DBTX.
func NewWatchedFileStore(db DBTX) *WatchedFileStore {
	return &WatchedFileStore{db: db}
}

// Upsert records the latest hash for (workspace, path): first sight
// inserts, later scans update last_hash (and file_type, so a reclassified
// path does not go stale). The natural key is the UNIQUE(workspace_id,
// path) constraint from migration 001.
func (s *WatchedFileStore) Upsert(ctx context.Context, params WatchedFileParams) (*WatchedFile, error) {
	if strings.TrimSpace(params.WorkspaceID) == "" {
		return nil, errors.New("store: watched file workspace id is required")
	}
	if strings.TrimSpace(params.Path) == "" {
		return nil, errors.New("store: watched file path is required")
	}
	fileType, ok := NormalizeWatchedFileType(params.FileType)
	if !ok {
		return nil, fmt.Errorf("store: unknown watched file type %q", params.FileType)
	}
	w, err := scanWatchedFile(s.db.QueryRow(ctx,
		`INSERT INTO watched_files (workspace_id, path, last_hash, file_type)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (workspace_id, path) DO UPDATE SET
		   last_hash = EXCLUDED.last_hash,
		   file_type = EXCLUDED.file_type
		 RETURNING `+watchedFileColumns,
		strings.TrimSpace(params.WorkspaceID),
		strings.TrimSpace(params.Path),
		nullText(strings.TrimSpace(params.LastHash)),
		fileType))
	if err != nil {
		return nil, fmt.Errorf("store: upsert watched file: %w", err)
	}
	return w, nil
}

// GetByWorkspacePath fetches one watched file or a wrapped ErrNotFound.
func (s *WatchedFileStore) GetByWorkspacePath(ctx context.Context, workspaceID, path string) (*WatchedFile, error) {
	w, err := scanWatchedFile(s.db.QueryRow(ctx,
		`SELECT `+watchedFileColumns+` FROM watched_files WHERE workspace_id = $1 AND path = $2`,
		workspaceID, path))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("store: watched file %s/%s: %w", workspaceID, path, ErrNotFound)
		}
		return nil, fmt.Errorf("store: get watched file: %w", err)
	}
	return w, nil
}

// ListByWorkspace returns every watched file of a workspace ordered by
// path (deterministic for scan comparison with StaleWatchedFiles).
func (s *WatchedFileStore) ListByWorkspace(ctx context.Context, workspaceID string) ([]WatchedFile, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+watchedFileColumns+` FROM watched_files WHERE workspace_id = $1 ORDER BY path ASC`,
		workspaceID)
	if err != nil {
		return nil, fmt.Errorf("store: list watched files: %w", err)
	}
	defer rows.Close()
	var out []WatchedFile
	for rows.Next() {
		var w WatchedFile
		if err := rows.Scan(&w.ID, &w.WorkspaceID, &w.Path, &w.LastHash, &w.FileType, &w.CreatedAt); err != nil {
			return nil, fmt.Errorf("store: list watched files scan: %w", err)
		}
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list watched files rows: %w", err)
	}
	return out, nil
}

// Delete removes one watched-file row (e.g. the instruction file left the
// watch list). Missing rows map to a wrapped ErrNotFound.
func (s *WatchedFileStore) Delete(ctx context.Context, workspaceID, path string) error {
	tag, err := s.db.Exec(ctx,
		`DELETE FROM watched_files WHERE workspace_id = $1 AND path = $2`,
		workspaceID, path)
	if err != nil {
		return fmt.Errorf("store: delete watched file: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("store: watched file %s/%s: %w", workspaceID, path, ErrNotFound)
	}
	return nil
}

// StaleWatchedFiles is the pure staleness check: given the stored rows and
// a fresh scan (path → current SHA256 hex), it returns the stored rows
// whose content changed (hash mismatch, including "" = never scanned) plus
// the scanned paths with no stored row at all (synthesized entries with
// empty LastHash, so the caller can upsert-then-diff them). Output is
// sorted by path for determinism. No database access.
func StaleWatchedFiles(stored []WatchedFile, current map[string]string) []WatchedFile {
	byPath := make(map[string]WatchedFile, len(stored))
	for _, w := range stored {
		byPath[w.Path] = w
	}
	var out []WatchedFile
	for path, hash := range current {
		if s, ok := byPath[path]; ok {
			if s.LastHash != hash {
				out = append(out, s)
			}
		} else {
			out = append(out, WatchedFile{Path: path, LastHash: ""})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}
