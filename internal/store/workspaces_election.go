package store

// workspaces_election.go — designated-processor election + failover
// (issue #36, plan §6.3).
//
// Master owns workspaces.go (PostgresStore Register/Heartbeat over s.pool)
// and models.go (canonical Workspace). This file is purely additive: it
// ports the #36 election unique fix onto PostgresStore without touching
// master's files.
//
// Design (mirrors the #36 branch, adapted to master's conventions):
//   - MarkStaleOffline sweeps silent workspaces offline (strict `<`
//     boundary shared with IsStaleAt: silence == 90s is still online).
//   - SetDesignatedProcessor is the bare per-row flip for explicit admin
//     assignment; the 008 partial unique index
//     (uq_workspaces_designated_processor_online) makes a conflicting
//     second designation fail instead of forking extraction.
//   - ElectDesignatedProcessor is the transactional compare-and-set:
//     revoke-before-grant (never transiently two designated+online rows
//     under the 008 index), grant to freshest online workspace
//     (last_seen DESC, id tie-break so concurrent electors converge).
//     No online workspace -> ErrNotFound (revoke still stands: a project
//     with nobody online must not keep a corpse designatee).
//   - ReassignStaleDesignated clears both flags on dead designatees so the
//     successor grant never conflicts with the corpse row under 008.
//   - Pure helpers (IsOnlineAt/IsStaleAt/IsOnlineAtPtr/ExpiryAt,
//     FilterOnline) share OfflineThreshold (store.go) as the single source
//     of truth with the SQL predicates.

import (
	"context"
	"time"
)

// IsOnlineAt reports whether a workspace seen at lastSeen is online at now:
// online while silence has not exceeded OfflineThreshold (the exact 90s
// boundary still counts as online; "after 90s" means strictly greater).
func IsOnlineAt(lastSeen, now time.Time) bool {
	return !now.After(lastSeen.Add(OfflineThreshold))
}

// IsStaleAt is the negation of IsOnlineAt: silence exceeded OfflineThreshold.
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
	return lastSeen.Add(OfflineThreshold)
}

// FilterOnline keeps workspaces whose LastSeen is within OfflineThreshold
// of now (zero LastSeen is dropped), so cached lists can be filtered
// without a query. Deterministic and pure.
func FilterOnline(workspaces []Workspace, now time.Time) []Workspace {
	out := workspaces[:0:0]
	for _, w := range workspaces {
		if w.LastSeen.IsZero() {
			continue
		}
		if IsOnlineAt(w.LastSeen, now) {
			out = append(out, w)
		}
	}
	return out
}

// offlineSeconds renders OfflineThreshold for make_interval SQL so queries
// share the single source of truth instead of a second literal.
func offlineSeconds() float64 {
	return OfflineThreshold.Seconds()
}

// MarkStaleOffline flips every workspace silent past OfflineThreshold (or
// never seen) to offline and returns the affected row count.
func (s *PostgresStore) MarkStaleOffline(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE workspaces
		 SET is_online = false
		 WHERE is_online
		   AND (last_seen IS NULL OR last_seen < now() - make_interval(secs => $1))`,
		offlineSeconds())
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// SetDesignatedProcessor assigns or revokes the Memory Processor role.
// See the file header: prefer ElectDesignatedProcessor for failover.
func (s *PostgresStore) SetDesignatedProcessor(ctx context.Context, id string, designated bool) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE workspaces SET is_designated_processor = $2 WHERE id = $1::uuid`,
		id, designated)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ElectDesignatedProcessor elects the project's Memory Processor: the
// online workspace with the most-recent heartbeat becomes the single
// designated processor and every other flag in the project is cleared.
//
// Atomicity (issue #142): revoke + grant run as ONE UPDATE statement, so
// concurrent electors cannot interleave a clear between another's
// clear-and-grant (the old two-statement form left a zero-designatee
// window on crash and let two electors crown different winners). The
// winner subselect is deterministic (last_seen DESC, id tie-break), so
// concurrent electors converge on the same row. No online workspace ->
// ErrNotFound (stale designations are still revoked: a project with nobody
// online must not keep a corpse designatee).
func (s *PostgresStore) ElectDesignatedProcessor(ctx context.Context, projectID string) (*Workspace, error) {
	if projectID == "" {
		return nil, ErrNotFound
	}
	rows, err := s.pool.Query(ctx,
		`UPDATE workspaces AS w
		 SET is_designated_processor = (w.id = sel.id)
		 FROM (
		   SELECT id FROM workspaces
		   WHERE project_id = $1::uuid
		     AND is_online
		     AND last_seen > now() - make_interval(secs => $2)
		   ORDER BY last_seen DESC, id
		   LIMIT 1
		 ) AS sel
		 WHERE w.project_id = $1::uuid
		   AND (w.is_designated_processor OR w.id = sel.id)
		 RETURNING w.id, w.project_id, w.user_id, w.machine_id, w.path,
			w.branch, w.commit_sha, w.is_dirty, w.is_online, w.is_designated_processor,
			w.last_seen, w.daemon_url, w.created_at`,
		projectID, offlineSeconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var winner *Workspace
	for rows.Next() {
		w, err := scanWorkspace(rows)
		if err != nil {
			return nil, err
		}
		if w.IsDesignatedProcessor {
			winner = w
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if winner == nil {
		return nil, ErrNotFound
	}
	return winner, nil
}

// ReassignStaleDesignated revokes the Memory Processor role (and the online
// flag) from every designated workspace silent past OfflineThreshold or
// never seen, and returns the swept row count. Electing a successor is a
// separate step — call ElectDesignatedProcessor afterwards.
func (s *PostgresStore) ReassignStaleDesignated(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE workspaces
		 SET is_online = false, is_designated_processor = false
		 WHERE is_designated_processor
		   AND (last_seen IS NULL OR last_seen < now() - make_interval(secs => $1))`,
		offlineSeconds())
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
