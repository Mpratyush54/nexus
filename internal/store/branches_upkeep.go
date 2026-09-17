package store

// branches_upkeep.go — branch archive + persisted staleness (issue #44,
// plan §5.4).
//
// Master owns branches.go (CoW branching on MemoryBranch) and migrations
// 001–006. This file is additive except for the two 007 columns surfaced
// in MemoryBranch/scanBranch (archived_at, potentially_stale, added by
// migrations/007_branch_upkeep): 30-day auto-archive and the persisted
// DetectStale signal.
//
// Rules (ported from the #44 branch onto master's MemoryBranch):
//   - Main never auto-archives (it is the project root every chain
//     resolves against); IsArchivable is the pure predicate (main,
//     already-archived, or younger than BranchArchiveTTL → false,
//     boundary inclusive).
//   - ArchiveBranch validates eligibility before writing (main/young/
//     archived fail with a descriptive error, unknown id → ErrNotFound).
//   - MarkStale sets potentially_stale (the DetectStale persistence
//     primitive; DetectStale itself stays pure in internal/branches).

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// BranchArchiveTTL is the plan §5.4 auto-archive age: a non-main branch
// becomes eligible once now - created_at >= 30 days.
const BranchArchiveTTL = 30 * 24 * time.Hour

// IsArchived reports whether the 30-day auto-archive has fired.
func (b *MemoryBranch) IsArchived() bool {
	return b != nil && b.ArchivedAt != nil
}

// IsArchivable encodes the plan §5.4 30-day rule as a pure predicate.
func IsArchivable(b *MemoryBranch, now time.Time) bool {
	if b == nil || b.Name == MainBranchName || b.IsArchived() {
		return false
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return !now.Before(b.CreatedAt.Add(BranchArchiveTTL))
}

// archiveEligibility loads the branch and rejects ineligible archives with
// a descriptive error (unknown id → ErrNotFound).
func archiveEligibilityMem(b *MemoryBranch, now time.Time) error {
	if !IsArchivable(b, now) {
		return fmt.Errorf("store: branch %s is not archivable (main, already archived, or younger than %s)",
			b.ID, BranchArchiveTTL)
	}
	return nil
}

// ArchiveBranch fires the 30-day auto-archive for one branch (MemStore).
func (s *MemStore) ArchiveBranch(_ context.Context, branchID string, now time.Time) (*MemoryBranch, error) {
	if strings.TrimSpace(branchID) == "" {
		return nil, fmt.Errorf("store: archive branch requires a branch id")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	b := branchBucket(s)
	b.mu.Lock()
	defer b.mu.Unlock()
	br, ok := b.branches[branchID]
	if !ok {
		return nil, ErrNotFound
	}
	if err := archiveEligibilityMem(br, now); err != nil {
		return nil, err
	}
	br.ArchivedAt = &now
	return br, nil
}

// MarkStale flags one branch as potentially stale (MemStore).
func (s *MemStore) MarkStale(_ context.Context, branchID string) (*MemoryBranch, error) {
	if strings.TrimSpace(branchID) == "" {
		return nil, fmt.Errorf("store: mark stale requires a branch id")
	}
	b := branchBucket(s)
	b.mu.Lock()
	defer b.mu.Unlock()
	br, ok := b.branches[branchID]
	if !ok {
		return nil, ErrNotFound
	}
	br.PotentiallyStale = true
	return br, nil
}

// ArchiveBranch fires the 30-day auto-archive for one branch (Postgres).
func (s *PostgresStore) ArchiveBranch(ctx context.Context, branchID string, now time.Time) (*MemoryBranch, error) {
	if strings.TrimSpace(branchID) == "" {
		return nil, fmt.Errorf("store: archive branch requires a branch id")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	head, err := s.GetBranch(ctx, branchID)
	if err != nil {
		return nil, err
	}
	if !IsArchivable(head, now) {
		return nil, fmt.Errorf("store: branch %s is not archivable (main, already archived, or younger than %s)",
			branchID, BranchArchiveTTL)
	}
	br, err := scanBranch(s.pool.QueryRow(ctx,
		`UPDATE memory_branches SET archived_at = $2 WHERE id = $1::uuid
		 RETURNING `+branchColumns, branchID, now))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return br, nil
}

// MarkStale flags one branch as potentially stale (Postgres).
func (s *PostgresStore) MarkStale(ctx context.Context, branchID string) (*MemoryBranch, error) {
	if strings.TrimSpace(branchID) == "" {
		return nil, fmt.Errorf("store: mark stale requires a branch id")
	}
	br, err := scanBranch(s.pool.QueryRow(ctx,
		`UPDATE memory_branches SET potentially_stale = true WHERE id = $1::uuid
		 RETURNING `+branchColumns, branchID))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return br, nil
}
