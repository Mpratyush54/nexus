// Workspace registration and heartbeats (issue #2, plan §§1.2–1.3).
//
// The daemon registers on startup and POSTs a heartbeat every 30s; the
// server marks a workspace offline after 90s of silence (OfflineAfter).
// Expiry is evaluated in both places from the same constant: the pure
// predicates IsOnlineAt/IsStaleAt for app-side gating and cache filtering,
// and interval SQL in ListActive/MarkStaleOffline for authoritative queries.
// All predicates take an explicit now so tests use a fixed clock.
package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// OfflineAfter is the heartbeat silence budget: a workspace whose last_seen
// is older than now-OfflineAfter is offline. Mirrors the plan ("server marks
// offline after 90s silence") with heartbeats every 30s, i.e. three missed
// beats tolerate one slow interval plus jitter.
const OfflineAfter = 90 * time.Second

// OfflineAfterSeconds renders the threshold for make_interval SQL so queries
// share this single source of truth instead of a second literal.
func OfflineAfterSeconds() float64 {
	return OfflineAfter.Seconds()
}

// IsOnlineAt reports whether a workspace seen at lastSeen is online at now:
// online while silence has not exceeded OfflineAfter (the exact 90s boundary
// still counts as online; "after 90s" means strictly greater).
func IsOnlineAt(lastSeen, now time.Time) bool {
	return !now.After(lastSeen.Add(OfflineAfter))
}

// IsStaleAt is the negation of IsOnlineAt: silence exceeded OfflineAfter.
func IsStaleAt(lastSeen, now time.Time) bool {
	return !IsOnlineAt(lastSeen, now)
}

// IsOnlineAtPtr is the nil-safe variant: a workspace never seen (NULL
// last_seen) is offline.
func IsOnlineAtPtr(lastSeen *time.Time, now time.Time) bool {
	if lastSeen == nil {
		return false
	}
	return IsOnlineAt(*lastSeen, now)
}

// ExpiryAt returns the instant after which a lastSeen timestamp is stale.
func ExpiryAt(lastSeen time.Time) time.Time {
	return lastSeen.Add(OfflineAfter)
}

// Workspace mirrors a workspaces row (plan §1.1). Empty Branch/CommitSHA/
// DaemonURL mean SQL NULL; LastSeen is nil until the first heartbeat
// (Register always sets it, so nil only appears on legacy rows).
type Workspace struct {
	ID                    string
	ProjectID             string
	UserID                string
	MachineID             string
	Path                  string
	Branch                string
	CommitSHA             string
	IsDirty               bool
	IsOnline              bool
	IsDesignatedProcessor bool
	LastSeen              *time.Time
	DaemonURL             string
	CreatedAt             time.Time
}

// WorkspaceParams carries registration identity for Register.
type WorkspaceParams struct {
	ProjectID string
	UserID    string
	MachineID string // hostname or hardware UUID; required
	Path      string // absolute local path; required
	Branch    string
	CommitSHA string
	IsDirty   bool
	DaemonURL string // ws://localhost:PORT
}

// HeartbeatParams carries one daemon heartbeat (plan §1.3: every 30s).
type HeartbeatParams struct {
	Branch    string
	CommitSHA string
	IsDirty   bool
}

// workspaceColumns selects workspaces with NULLs coalesced (except last_seen,
// which stays nullable so "never seen" is representable).
const workspaceColumns = `id::TEXT AS id, ` +
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

// scanWorkspace scans a full workspaceColumns row.
func scanWorkspace(row pgx.Row) (*Workspace, error) {
	var w Workspace
	if err := row.Scan(
		&w.ID, &w.ProjectID, &w.UserID, &w.MachineID, &w.Path,
		&w.Branch, &w.CommitSHA, &w.IsDirty, &w.IsOnline,
		&w.IsDesignatedProcessor, &w.LastSeen, &w.DaemonURL, &w.CreatedAt,
	); err != nil {
		return nil, err
	}
	return &w, nil
}

// WorkspaceStore is workspace registration, heartbeat, and presence.
type WorkspaceStore struct {
	db DBTX
}

// NewWorkspaceStore wires a WorkspaceStore to any DBTX.
func NewWorkspaceStore(db DBTX) *WorkspaceStore {
	return &WorkspaceStore{db: db}
}

// Register upserts a workspace on its natural key (machine_id, path):
// first registration inserts (online, last_seen = now()); restarts update
// the mutable columns and flip the row back online.
func (s *WorkspaceStore) Register(ctx context.Context, params WorkspaceParams) (*Workspace, error) {
	if strings.TrimSpace(params.ProjectID) == "" {
		return nil, errors.New("store: workspace project id is required")
	}
	if strings.TrimSpace(params.UserID) == "" {
		return nil, errors.New("store: workspace user id is required")
	}
	if strings.TrimSpace(params.MachineID) == "" {
		return nil, errors.New("store: workspace machine id is required")
	}
	if strings.TrimSpace(params.Path) == "" {
		return nil, errors.New("store: workspace path is required")
	}
	w, err := scanWorkspace(s.db.QueryRow(ctx,
		`INSERT INTO workspaces
		 (project_id, user_id, machine_id, path, branch, commit_sha, is_dirty, daemon_url, last_seen, is_online)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now(), true)
		 ON CONFLICT (machine_id, path) DO UPDATE SET
		   project_id = EXCLUDED.project_id,
		   user_id = EXCLUDED.user_id,
		   branch = EXCLUDED.branch,
		   commit_sha = EXCLUDED.commit_sha,
		   is_dirty = EXCLUDED.is_dirty,
		   daemon_url = EXCLUDED.daemon_url,
		   last_seen = now(),
		   is_online = true
		 RETURNING `+workspaceColumns,
		params.ProjectID, params.UserID, params.MachineID, params.Path,
		nullText(strings.TrimSpace(params.Branch)),
		nullText(strings.TrimSpace(params.CommitSHA)),
		params.IsDirty,
		nullText(strings.TrimSpace(params.DaemonURL))))
	if err != nil {
		return nil, fmt.Errorf("store: register workspace: %w", err)
	}
	return w, nil
}

// Heartbeat records one daemon pulse: last_seen = now(), fresh git state,
// and back online (a flapping daemon re-appears without re-registering).
func (s *WorkspaceStore) Heartbeat(ctx context.Context, id string, hb HeartbeatParams) (*Workspace, error) {
	w, err := scanWorkspace(s.db.QueryRow(ctx,
		`UPDATE workspaces
		 SET last_seen = now(), branch = $2, commit_sha = $3, is_dirty = $4, is_online = true
		 WHERE id = $1
		 RETURNING `+workspaceColumns,
		id,
		nullText(strings.TrimSpace(hb.Branch)),
		nullText(strings.TrimSpace(hb.CommitSHA)),
		hb.IsDirty))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("store: workspace %s: %w", id, ErrNotFound)
		}
		return nil, fmt.Errorf("store: workspace heartbeat: %w", err)
	}
	return w, nil
}

// GetByID fetches one workspace or a wrapped ErrNotFound.
func (s *WorkspaceStore) GetByID(ctx context.Context, id string) (*Workspace, error) {
	w, err := scanWorkspace(s.db.QueryRow(ctx,
		`SELECT `+workspaceColumns+` FROM workspaces WHERE id = $1`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("store: workspace %s: %w", id, ErrNotFound)
		}
		return nil, fmt.Errorf("store: get workspace: %w", err)
	}
	return w, nil
}

// ListActive returns the online workspaces of a project: is_online flag set
// AND last_seen within OfflineAfter. Both conditions are required so a missed
// sweeper run (MarkStaleOffline) cannot resurrect stale rows — the timestamp
// predicate is authoritative.
func (s *WorkspaceStore) ListActive(ctx context.Context, projectID string) ([]Workspace, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+workspaceColumns+` FROM workspaces
		 WHERE project_id = $1
		   AND is_online
		   AND last_seen > now() - make_interval(secs => $2)
		 ORDER BY last_seen DESC`,
		projectID, OfflineAfterSeconds())
	if err != nil {
		return nil, fmt.Errorf("store: list active workspaces: %w", err)
	}
	defer rows.Close()
	var out []Workspace
	for rows.Next() {
		var w Workspace
		if err := rows.Scan(
			&w.ID, &w.ProjectID, &w.UserID, &w.MachineID, &w.Path,
			&w.Branch, &w.CommitSHA, &w.IsDirty, &w.IsOnline,
			&w.IsDesignatedProcessor, &w.LastSeen, &w.DaemonURL, &w.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("store: list active workspaces scan: %w", err)
		}
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list active workspaces rows: %w", err)
	}
	return out, nil
}

// MarkStaleOffline flips every workspace silent past OfflineAfter (or never
// seen) to offline and returns the affected row count. Strict `<` matches
// IsStaleAt exactly: silence == 90s is still online on both sides.
func (s *WorkspaceStore) MarkStaleOffline(ctx context.Context) (int64, error) {
	tag, err := s.db.Exec(ctx,
		`UPDATE workspaces
		 SET is_online = false
		 WHERE is_online
		   AND (last_seen IS NULL OR last_seen < now() - make_interval(secs => $1))`,
		OfflineAfterSeconds())
	if err != nil {
		return 0, fmt.Errorf("store: mark stale workspaces offline: %w", err)
	}
	return tag.RowsAffected(), nil
}

// SetDesignatedProcessor assigns or revokes the Memory Processor role
// (plan: the project owner's daemon processes; failover is a follow-up).
//
// NOTE (issue #36): this stays a bare per-row flip for explicit admin
// assignment. Uniqueness is now enforced by the database — migration
// 008_processor_election adds partial unique index
// uq_workspaces_designated_processor_online (one designated+online workspace
// per project) — so a conflicting second designation fails here instead of
// silently forking extraction. Prefer ElectDesignatedProcessor for automatic
// failover.
func (s *WorkspaceStore) SetDesignatedProcessor(ctx context.Context, id string, designated bool) error {
	tag, err := s.db.Exec(ctx,
		`UPDATE workspaces SET is_designated_processor = $2 WHERE id = $1`,
		id, designated)
	if err != nil {
		return fmt.Errorf("store: set designated processor: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("store: workspace %s: %w", id, ErrNotFound)
	}
	return nil
}

// ElectDesignatedProcessor elects the project's Memory Processor (issue #36):
// the online workspace with the most-recent heartbeat (last_seen DESC, id
// tie-break so concurrent electors converge on the same winner) becomes the
// single designated processor and every other flag in the project is cleared.
//
// It is a transactional compare-and-set in two ordered steps, each one atomic
// statement sharing the OfflineAfter source of truth:
//  1. Revoke every designated flag in the project.
//  2. Designate the freshest online workspace (is_online AND last_seen within
//     OfflineAfter, the same authoritative predicate as ListActive).
//
// Step order matters for the 008 partial unique index
// (uq_workspaces_designated_processor_online): revoke-before-grant can never
// transiently hold two designated+online rows, while grant-before-revoke
// could. Concurrent electors pick the same deterministic winner, making the
// second election an idempotent no-op; a divergent winner fails on the index
// and the caller retries the election.
// With no online workspace the grant matches nothing and the election reports
// ErrNotFound (the revoke still stands — a project with nobody online must
// not keep a corpse designatee).
func (s *WorkspaceStore) ElectDesignatedProcessor(ctx context.Context, projectID string) (*Workspace, error) {
	if strings.TrimSpace(projectID) == "" {
		return nil, errors.New("store: elect designated processor requires a project id")
	}
	if _, err := s.db.Exec(ctx,
		`UPDATE workspaces
		 SET is_designated_processor = false
		 WHERE project_id = $1 AND is_designated_processor`,
		projectID); err != nil {
		return nil, fmt.Errorf("store: elect designated processor revoke: %w", err)
	}
	w, err := scanWorkspace(s.db.QueryRow(ctx,
		`UPDATE workspaces
		 SET is_designated_processor = true
		 WHERE id = (
		   SELECT id FROM workspaces
		   WHERE project_id = $1
		     AND is_online
		     AND last_seen > now() - make_interval(secs => $2)
		   ORDER BY last_seen DESC, id
		   LIMIT 1
		 )
		 RETURNING `+workspaceColumns,
		projectID, OfflineAfterSeconds()))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("store: elect designated processor for project %s: %w", projectID, ErrNotFound)
		}
		return nil, fmt.Errorf("store: elect designated processor: %w", err)
	}
	return w, nil
}

// ReassignStaleDesignated revokes the Memory Processor role (and the online
// flag) from every designated workspace silent past OfflineAfter or never
// seen (issue #36: a dead owner must not keep the flag while extraction
// silently stops). It returns the swept row count, like MarkStaleOffline,
// and shares its exact boundary semantics: strict `<` plus NULL last_seen, so
// silence of exactly 90s still counts as online on both the Go (IsStaleAt)
// and SQL sides. Electing a successor is a separate step — call
// ElectDesignatedProcessor for the affected projects afterwards; the swept
// rows are offline and therefore outside the 008 partial unique index, so the
// successor grant never conflicts with them.
func (s *WorkspaceStore) ReassignStaleDesignated(ctx context.Context) (int64, error) {
	tag, err := s.db.Exec(ctx,
		`UPDATE workspaces
		 SET is_online = false, is_designated_processor = false
		 WHERE is_designated_processor
		   AND (last_seen IS NULL OR last_seen < now() - make_interval(secs => $1))`,
		OfflineAfterSeconds())
	if err != nil {
		return 0, fmt.Errorf("store: reassign stale designated: %w", err)
	}
	return tag.RowsAffected(), nil
}

// Delete removes a workspace registration.
func (s *WorkspaceStore) Delete(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM workspaces WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("store: delete workspace: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("store: workspace %s: %w", id, ErrNotFound)
	}
	return nil
}

// FilterOnline is the app-side counterpart of ListActive: it keeps workspaces
// whose LastSeen is within OfflineAfter of now (nil LastSeen is dropped), so
// cached lists can be filtered without a query. Deterministic and pure.
func FilterOnline(workspaces []Workspace, now time.Time) []Workspace {
	out := workspaces[:0:0]
	for _, w := range workspaces {
		if IsOnlineAtPtr(w.LastSeen, now) {
			out = append(out, w)
		}
	}
	return out
}
