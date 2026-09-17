package store

// lifecycle.go — memory use tracking, session expiry, archival triage, and
// event-retention tiers (issues #30, #119).
//
// Additive only: no existing symbol is renamed or removed. These methods are
// defined on the concrete stores (NOT on the Store interface) so existing
// Store implementations outside this package (e.g. test stubs, server fakes)
// keep compiling.
//
// Scheduler note: the background loops that call these sweepers on a timer
// belong to the daemon (plan §§1.7/2.7/6.2), not to the store — the store
// exposes idempotent, bounded sweep primitives the daemon can invoke from
// its existing tick. Wiring the tick is an explicit follow-up (ADR
// deferral); everything here is safe to call from any goroutine.

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// MaxEventsLimit caps ListEvents reads (issue #119). limit <= 0 used to mean
// "no cap" (Postgres LIMIT NULL, MemStore uncapped loop), which lets a
// backfill read the whole event log into memory. Both backends now clamp to
// this bound.
const MaxEventsLimit = 1000

// ClampEventsLimit normalizes a ListEvents limit: non-positive becomes the
// default page (20, matching SearchMemory/SearchEpisodes), oversized values
// clamp to MaxEventsLimit.
func ClampEventsLimit(limit int) int {
	if limit <= 0 {
		return 20
	}
	if limit > MaxEventsLimit {
		return MaxEventsLimit
	}
	return limit
}

// DefaultSessionTTL bounds session lifetime for the expiry sweeper (plan
// §2.7 grace model): sessions that are still active past this age are ended
// automatically. Per-session ExpiresAt (migration 010) overrides it.
const DefaultSessionTTL = 7 * 24 * time.Hour

// SessionExpiryAt returns when a session expires: its explicit ExpiresAt
// when set, otherwise CreatedAt + ttl (ttl <= 0 falls back to
// DefaultSessionTTL).
func SessionExpiryAt(sess *Session, ttl time.Duration) time.Time {
	if sess == nil {
		return time.Time{}
	}
	if !sess.ExpiresAt.IsZero() {
		return sess.ExpiresAt
	}
	if ttl <= 0 {
		ttl = DefaultSessionTTL
	}
	if sess.CreatedAt.IsZero() {
		return time.Time{}
	}
	return sess.CreatedAt.Add(ttl)
}

// IsSessionExpired reports whether sess should be swept by the expiry job:
// still active and past its expiry instant. Zero expiries never expire.
func IsSessionExpired(sess *Session, now time.Time, ttl time.Duration) bool {
	if sess == nil || !sess.IsActive {
		return false
	}
	exp := SessionExpiryAt(sess, ttl)
	if exp.IsZero() {
		return false
	}
	return !now.Before(exp)
}

// StaleMemoryThresholds tunes the archival triage (plan §1.7): memories at
// or below MaxConfidence with no recorded use are archival candidates.
type StaleMemoryThresholds struct {
	MaxConfidence float32
}

// DefaultStaleMemoryThresholds mirrors the plan: conf <= 0.2 with
// use_count == 0 is cold.
var DefaultStaleMemoryThresholds = StaleMemoryThresholds{MaxConfidence: 0.2}

// IsMemoryArchivable is the pure archival predicate: a CONFIRMED memory with
// confidence at or below threshold and zero recorded uses is cold. PROPOSED
// rows are owned by the confirm sweeper, terminal states stay untouched.
func IsMemoryArchivable(m *MemoryItem, th StaleMemoryThresholds) bool {
	if m == nil {
		return false
	}
	if m.Status != StatusConfirmed {
		return false
	}
	if m.UseCount != 0 {
		return false
	}
	return m.Confidence <= th.MaxConfidence
}

// ListStaleMemoryCandidates returns CONFIRMED memories matching the archival
// predicate, oldest first, bounded by limit (<= 0 clamps to 20). It only
// triages — the daemon decides the disposition (supersede vs cold-store),
// so no history is destroyed here.
func (s *MemStore) ListStaleMemoryCandidates(ctx context.Context, limit int) ([]*MemoryItem, error) {
	if limit <= 0 {
		limit = 20
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*MemoryItem
	for _, m := range s.memories {
		if IsMemoryArchivable(m, DefaultStaleMemoryThresholds) {
			out = append(out, cloneMemoryItem(m))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// ListStaleMemoryCandidates is the Postgres triage read (same predicate as
// MemStore.IsMemoryArchivable, oldest first, bounded).
func (s *PostgresStore) ListStaleMemoryCandidates(ctx context.Context, limit int) ([]*MemoryItem, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+memoryColumns+` FROM memory_items
		  WHERE status = 'CONFIRMED' AND use_count = 0 AND confidence <= $1
		  ORDER BY created_at ASC, id ASC
		  LIMIT $2`,
		DefaultStaleMemoryThresholds.MaxConfidence, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*MemoryItem
	for rows.Next() {
		m, err := scanMemoryItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// EventRetentionTier names the plan §6.2 retention tiers by event age.
type EventRetentionTier string

const (
	// RetentionHot covers 0-30d: full payload, indexed, fan-out eligible.
	RetentionHot EventRetentionTier = "hot"
	// RetentionWarm covers 30-180d: full payload, queryable, no fan-out.
	RetentionWarm EventRetentionTier = "warm"
	// RetentionCold covers 180d+: payload-drop / Glacier candidate.
	RetentionCold EventRetentionTier = "cold"
)

// RetentionTierForAge maps an event age to its plan §6.2 tier. Boundaries
// are inclusive at the bottom (exactly 30d reads warm, exactly 180d cold).
func RetentionTierForAge(age time.Duration) EventRetentionTier {
	if age < 0 {
		age = 0
	}
	switch {
	case age < 30*24*time.Hour:
		return RetentionHot
	case age < 180*24*time.Hour:
		return RetentionWarm
	default:
		return RetentionCold
	}
}

// CountEventsByRetentionTier tallies MemStore events per retention tier at
// now. The Postgres path can GROUP BY age inline; this helper covers tests,
// local dev, and capacity planning without a database.
func (s *MemStore) CountEventsByRetentionTier(_ context.Context, now time.Time) map[EventRetentionTier]int {
	out := map[EventRetentionTier]int{
		RetentionHot: 0, RetentionWarm: 0, RetentionCold: 0,
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, ev := range s.events {
		age := now.Sub(ev.CreatedAt)
		out[RetentionTierForAge(age)]++
	}
	return out
}

// ---- memory use tracking (issue #119: the missing writer) ----

// RecordMemoryUse bumps use_count and stamps last_used_at for one memory so
// the decay clock resets on serve/search hits. Unknown id -> ErrNotFound.
func (s *MemStore) RecordMemoryUse(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.memories[id]
	if !ok {
		return ErrNotFound
	}
	m.UseCount++
	m.LastUsedAt = time.Now().UTC()
	return nil
}

// RecordMemoryUse is the Postgres writer for serve/search hits.
func (s *PostgresStore) RecordMemoryUse(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE memory_items SET use_count = use_count + 1,
			last_used_at = now(), updated_at = now()
		  WHERE id = $1::uuid`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- session expiry sweeper (issue #119: SESSION TTL) ----

// ExpireStaleSessions ends active sessions past their expiry instant and
// returns the swept count. Idempotent: already-ended sessions are skipped.
// ttl <= 0 uses DefaultSessionTTL for sessions without explicit ExpiresAt.
func (s *MemStore) ExpireStaleSessions(ctx context.Context, now time.Time, ttl time.Duration) (int64, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	b := memSessionsOf(s)
	b.mu.Lock()
	defer b.mu.Unlock()
	var n int64
	for _, sess := range b.sessions {
		if !sess.IsActive {
			continue
		}
		cp := *sess
		if !IsSessionExpired(&cp, now, ttl) {
			continue
		}
		sess.IsActive = false
		sess.EndedAt = now
		for _, p := range b.participants[sess.ID] {
			if p.LeftAt.IsZero() {
				p.LeftAt = now
			}
		}
		n++
	}
	return n, nil
}

// ExpireStaleSessions is the Postgres TTL sweeper: ends active sessions
// whose explicit expires_at passed, or — when expires_at IS NULL — older
// than ttl. ttl <= 0 uses DefaultSessionTTL.
func (s *PostgresStore) ExpireStaleSessions(ctx context.Context, now time.Time, ttl time.Duration) (int64, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if ttl <= 0 {
		ttl = DefaultSessionTTL
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE sessions SET is_active = false, ended_at = now()
		  WHERE is_active
		    AND (expires_at IS NOT NULL AND expires_at <= $1
		         OR expires_at IS NULL AND created_at < $1 - make_interval(secs => $2))`,
		now, ttl.Seconds())
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// ---- memory rejection (issue #98: the missing persist) ----

// RejectMemory flips PROPOSED -> REJECTED (issue #89 DAG). Terminal states,
// CONFIRMED rows, and unknown ids fail: unknown id -> ErrNotFound, illegal
// edge -> wrapped ErrConflict. rejectedBy is accepted for handler parity
// (the memory_items table has no rejected_by column; only the transition is
// recorded).
func (s *MemStore) RejectMemory(ctx context.Context, id, rejectedBy string) error {
	_ = rejectedBy
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.memories[id]
	if !ok {
		return ErrNotFound
	}
	if m.Status != StatusProposed {
		return fmt.Errorf("store: illegal status transition %s -> %s: %w", m.Status, StatusRejected, ErrConflict)
	}
	m.Status = StatusRejected
	m.UpdatedAt = time.Now().UTC()
	return nil
}

// RejectMemory persists PROPOSED -> REJECTED with a conditional write so a
// concurrent confirm wins instead of being silently overwritten: zero
// touched rows distinguish unknown ids (ErrNotFound) from lost races and
// terminal states (ErrConflict).
func (s *PostgresStore) RejectMemory(ctx context.Context, id, rejectedBy string) error {
	_ = rejectedBy
	tag, err := s.pool.Exec(ctx,
		`UPDATE memory_items SET status = 'REJECTED', updated_at = now()
		  WHERE id = $1::uuid AND status = 'PROPOSED'`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		if _, gerr := s.GetMemoryItem(ctx, id); gerr != nil {
			return gerr
		}
		return fmt.Errorf("store: memory %s is not PROPOSED: %w", id, ErrConflict)
	}
	return nil
}

// errMemoryConflict formats an illegal lifecycle edge wrapping ErrConflict.
func errMemoryConflict(cur, next string) error {
	return fmt.Errorf("store: illegal status transition %s -> %s: %w", cur, next, ErrConflict)
}
