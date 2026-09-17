package store

// branches.go — Copy-on-Write memory branching (Phase 5, nexus issue #17).
//
// This is the ONLY file that implements branching. It was added without
// touching any existing store file (models.go, store.go, memory.go, …) so
// parallel Phase 5 work (diff/merge, security) cannot clash:
//
//   - Branch rows for the Postgres backend live in the `memory_branches`
//     table from migrations/005_branches.up.sql; all SQL here is local to
//     this file (column lists, scanners, INSERT/SELECT).
//   - Branch rows + overlays for the MemStore backend (tests/local dev)
//     live in a package-level registry keyed by *MemStore
//     (branchBucket), because MemStore's struct cannot gain a field without
//     editing store.go. Main-line memories stay in MemStore.memories —
//     branch writes go to the bucket overlay and never touch them.
//
// CoW contract (plan §5.2), enforced identically on both backends:
//
//   - Fork: inserts ONE branch row pointing at its parent. Zero memory rows
//     are copied.
//   - Read (ResolveRead): walks branch -> parent -> … -> main and returns
//     the first key match. A key written on a child shadows the same key on
//     every ancestor; keys never written on the branch resolve to the parent
//     value.
//   - Write (WriteToBranch): always inserts into the current branch. Parent
//     branches are never updated, so isolation holds by construction.
//   - Visibility (CheckBranchAccess): `shared` branches are readable by
//     anyone; `private` branches only by their owner. The empty requester id
//     means internal/system access and bypasses the check.
//   - Depth: chains longer than MaxBranchDepth levels are rejected at fork.
//   - Episodes are NOT branched (plan §5.3) — no episode code lives here.
//
// Note on MemoryItem: the struct (models.go) has no BranchID field, so the
// Postgres path tags rows via the branch_id column using the branch id
// passed to WriteToBranch, and ResolveRead returns the winning item fetched
// by id. The MemStore path keeps branch overlays physically separate from
// main-line memories. Both honor the contract above.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrForbidden is returned when a requester may not read a private branch.
var ErrForbidden = errors.New("forbidden: private branch")

// Branch naming, visibility, and depth limits.
const (
	// MainBranchName is auto-created per project (shared, no parent).
	MainBranchName = "main"
	// BranchVisibilityPrivate restricts reads to the branch owner.
	BranchVisibilityPrivate = "private"
	// BranchVisibilityShared allows reads by any project member.
	BranchVisibilityShared = "shared"
	// MaxBranchDepth caps the parent chain length (plan §5.4: 5 levels).
	MaxBranchDepth = 5
)

// MemoryBranch is one copy-on-write branch pointer. It carries no memory
// content — content stays in memory_items tagged with branch_id (Postgres)
// or in the MemStore overlay bucket (in-memory).
type MemoryBranch struct {
	ID              string     `json:"id"`
	ProjectID       string     `json:"project_id"`
	Name            string     `json:"name"`
	OwnerID         string     `json:"owner_id,omitempty"`
	ParentBranchID  string     `json:"parent_branch_id,omitempty"`
	ForkedAtEventID int64      `json:"forked_at_event_id,omitempty"`
	Visibility      string     `json:"visibility"` // private | shared
	CreatedAt       time.Time  `json:"created_at"`
	ArchivedAt      *time.Time `json:"archived_at,omitempty"`      // nil = active (migration 007)
	PotentiallyStale bool      `json:"potentially_stale,omitempty"` // persisted DetectStale signal (migration 007)
}

// BranchStore is the branching surface. Both MemStore (tests/local dev) and
// PostgresStore (production) implement it.
type BranchStore interface {
	// EnsureMainBranch returns the project's shared "main" branch,
	// creating it when missing (migration seeds pre-existing projects;
	// this covers projects created afterwards).
	EnsureMainBranch(ctx context.Context, projectID string) (*MemoryBranch, error)
	// CreateBranch creates a top-level branch forked from main.
	CreateBranch(ctx context.Context, projectID, name, ownerID, visibility string) (*MemoryBranch, error)
	// ForkBranch creates child branch `name` pointing at parentID.
	// forkEventID records the source event at fork time (0 = unknown).
	// Zero memory rows are copied.
	ForkBranch(ctx context.Context, parentID, name, ownerID, visibility string, forkEventID int64) (*MemoryBranch, error)
	// GetBranch fetches one branch by id.
	GetBranch(ctx context.Context, id string) (*MemoryBranch, error)
	// ListBranches lists all branches of a project, oldest first.
	ListBranches(ctx context.Context, projectID string) ([]*MemoryBranch, error)
	// WriteToBranch inserts item into the current branch only. The item's
	// project is forced to the branch's project; parents are never touched.
	WriteToBranch(ctx context.Context, branchID string, item *MemoryItem) error
	// ResolveRead walks branch -> parent -> main and returns the first
	// match for key (CONFIRMED/PROPOSED only, latest write wins per level).
	ResolveRead(ctx context.Context, branchID, key string) (*MemoryItem, error)
}

// Compile-time guarantees.
var _ BranchStore = (*MemStore)(nil)
var _ BranchStore = (*PostgresStore)(nil)

// CheckBranchAccess enforces branch visibility: shared branches are open,
// private branches require requesterID == owner. Empty requesterID means
// internal/system access and always passes.
func CheckBranchAccess(branch *MemoryBranch, requesterID string) error {
	if branch == nil {
		return ErrNotFound
	}
	if requesterID == "" {
		return nil
	}
	if branch.Visibility == BranchVisibilityShared {
		return nil
	}
	if branch.OwnerID != "" && branch.OwnerID == requesterID {
		return nil
	}
	return ErrForbidden
}

// normalizeVisibility defaults blank to private and rejects unknown values.
func normalizeVisibility(v string) (string, error) {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return BranchVisibilityPrivate, nil
	}
	switch v {
	case BranchVisibilityPrivate, BranchVisibilityShared:
		return v, nil
	default:
		return "", fmt.Errorf("invalid branch visibility %q: must be private or shared", v)
	}
}

// ---------------------------------------------------------------------------
// MemStore backend — branch rows + overlays in a sidecar bucket.
// ---------------------------------------------------------------------------

// memBranchBucket is the sidecar state for one *MemStore: branch rows by id
// plus per-branch overlays (key -> latest item). Main-line memories stay in
// MemStore.memories; overlays shadow them on ResolveRead.
type memBranchBucket struct {
	mu       sync.RWMutex
	branches map[string]*MemoryBranch
	overlays map[string]map[string]*MemoryItem
}

// memBranchBuckets keys sidecar state by store instance so tests using
// separate NewMemStore() values never see each other's branches.
var memBranchBuckets sync.Map // map[*MemStore]*memBranchBucket

func branchBucket(s *MemStore) *memBranchBucket {
	b, _ := memBranchBuckets.LoadOrStore(s, &memBranchBucket{
		branches: make(map[string]*MemoryBranch),
		overlays: make(map[string]map[string]*MemoryItem),
	})
	return b.(*memBranchBucket)
}

// memBranchChain returns the parent chain starting at id, child first.
// Caller must hold (at least) b.mu.RLock.
func memBranchChain(b *memBranchBucket, id string) ([]*MemoryBranch, error) {
	var chain []*MemoryBranch
	seen := make(map[string]bool)
	cur := id
	for cur != "" {
		if seen[cur] {
			return nil, fmt.Errorf("branch chain cycle at %q", cur)
		}
		seen[cur] = true
		br, ok := b.branches[cur]
		if !ok {
			return nil, ErrNotFound
		}
		chain = append(chain, br)
		cur = br.ParentBranchID
		if len(chain) > MaxBranchDepth+1 {
			return nil, fmt.Errorf("branch chain exceeds max depth %d", MaxBranchDepth)
		}
	}
	return chain, nil
}

// visibleStatus reports whether a memory row participates in branch reads,
// mirroring SearchMemory's CONFIRMED/PROPOSED visibility.
func visibleStatus(status string) bool {
	return status == "CONFIRMED" || status == "PROPOSED"
}

// EnsureMainBranch returns the project's shared "main", creating it lazily.
func (s *MemStore) EnsureMainBranch(_ context.Context, projectID string) (*MemoryBranch, error) {
	if strings.TrimSpace(projectID) == "" {
		return nil, fmt.Errorf("project id is required")
	}
	b := branchBucket(s)
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, br := range b.branches {
		if br.ProjectID == projectID && br.Name == MainBranchName {
			return br, nil
		}
	}
	main := &MemoryBranch{
		ID:         newID("br"),
		ProjectID:  projectID,
		Name:       MainBranchName,
		Visibility: BranchVisibilityShared,
		CreatedAt:  time.Now().UTC(),
	}
	b.branches[main.ID] = main
	return main, nil
}

// CreateBranch creates a top-level branch forked from main.
func (s *MemStore) CreateBranch(ctx context.Context, projectID, name, ownerID, visibility string) (*MemoryBranch, error) {
	main, err := s.EnsureMainBranch(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return s.ForkBranch(ctx, main.ID, name, ownerID, visibility, 0)
}

// ForkBranch inserts one child row pointing at its parent. Zero data copied.
func (s *MemStore) ForkBranch(_ context.Context, parentID, name, ownerID, visibility string, forkEventID int64) (*MemoryBranch, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("branch name is required")
	}
	vis, err := normalizeVisibility(visibility)
	if err != nil {
		return nil, err
	}
	b := branchBucket(s)
	b.mu.Lock()
	defer b.mu.Unlock()
	parent, ok := b.branches[parentID]
	if !ok {
		return nil, ErrNotFound
	}
	for _, br := range b.branches {
		if br.ProjectID == parent.ProjectID && br.Name == name {
			return nil, ErrConflict
		}
	}
	chain, err := memBranchChain(b, parentID)
	if err != nil {
		return nil, err
	}
	if len(chain) >= MaxBranchDepth {
		return nil, fmt.Errorf("max branch depth %d exceeded", MaxBranchDepth)
	}
	child := &MemoryBranch{
		ID:              newID("br"),
		ProjectID:       parent.ProjectID,
		Name:            name,
		OwnerID:         ownerID,
		ParentBranchID:  parentID,
		ForkedAtEventID: forkEventID,
		Visibility:      vis,
		CreatedAt:       time.Now().UTC(),
	}
	b.branches[child.ID] = child
	return child, nil
}

// GetBranch fetches one branch by id.
func (s *MemStore) GetBranch(_ context.Context, id string) (*MemoryBranch, error) {
	b := branchBucket(s)
	b.mu.RLock()
	defer b.mu.RUnlock()
	br, ok := b.branches[id]
	if !ok {
		return nil, ErrNotFound
	}
	return br, nil
}

// ListBranches lists a project's branches, oldest first.
func (s *MemStore) ListBranches(_ context.Context, projectID string) ([]*MemoryBranch, error) {
	b := branchBucket(s)
	b.mu.RLock()
	defer b.mu.RUnlock()
	var out []*MemoryBranch
	for _, br := range b.branches {
		if br.ProjectID == projectID {
			out = append(out, br)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// WriteToBranch inserts item into the branch overlay only. Main-line
// memories (s.memories) and parent overlays are never touched, so write
// isolation holds by construction.
func (s *MemStore) WriteToBranch(_ context.Context, branchID string, item *MemoryItem) error {
	if item == nil {
		return fmt.Errorf("memory item is required")
	}
	if strings.TrimSpace(item.Key) == "" {
		return fmt.Errorf("memory key is required")
	}
	b := branchBucket(s)
	b.mu.Lock()
	defer b.mu.Unlock()
	br, ok := b.branches[branchID]
	if !ok {
		return ErrNotFound
	}
	if item.ID == "" {
		item.ID = newID("mem")
	}
	if item.Confidence == 0 {
		item.Confidence = 1.0
	}
	if item.Status == "" {
		item.Status = "PROPOSED"
	}
	if item.Level == "" {
		item.Level = "project"
	}
	item.ProjectID = br.ProjectID // a branch write always belongs to its project
	item.CreatedAt = time.Now().UTC()
	item.UpdatedAt = item.CreatedAt
	ov, ok := b.overlays[branchID]
	if !ok {
		ov = make(map[string]*MemoryItem)
		b.overlays[branchID] = ov
	}
	ov[item.Key] = item
	return nil
}

// ResolveRead walks branch -> parent -> main, first key match wins. The
// latest write on each branch level shadows ancestors; when no level has
// the key, main-line MemStore memories (project-scoped + org-level,
// CONFIRMED/PROPOSED, latest first) are the final fallback.
func (s *MemStore) ResolveRead(_ context.Context, branchID, key string) (*MemoryItem, error) {
	b := branchBucket(s)
	b.mu.RLock()
	chain, err := memBranchChain(b, branchID)
	if err != nil {
		b.mu.RUnlock()
		return nil, err
	}
	for _, br := range chain {
		if m, ok := b.overlays[br.ID][key]; ok {
			b.mu.RUnlock()
			return m, nil
		}
	}
	var projectID string
	if len(chain) > 0 {
		projectID = chain[0].ProjectID
	}
	b.mu.RUnlock()

	s.mu.RLock()
	defer s.mu.RUnlock()
	var best *MemoryItem
	for _, m := range s.memories {
		if m.Key != key || !visibleStatus(m.Status) {
			continue
		}
		if m.ProjectID != "" && m.ProjectID != projectID {
			continue
		}
		if best == nil || m.UpdatedAt.After(best.UpdatedAt) {
			best = m
		}
	}
	if best == nil {
		return nil, ErrNotFound
	}
	return best, nil
}

// ---------------------------------------------------------------------------
// PostgresStore backend — memory_branches table + branch_id column.
// ---------------------------------------------------------------------------

const branchColumns = `id, project_id, name, owner_id, parent_branch_id,
	forked_at_event_id, visibility, created_at, archived_at,
	COALESCE(potentially_stale, false) AS potentially_stale`

func scanBranch(row pgx.Row) (*MemoryBranch, error) {
	var b MemoryBranch
	var projectID string
	var ownerID, parentID *string
	var forkEventID *int64
	if err := row.Scan(&b.ID, &projectID, &b.Name, &ownerID, &parentID,
		&forkEventID, &b.Visibility, &b.CreatedAt, &b.ArchivedAt,
		&b.PotentiallyStale); err != nil {
		return nil, err
	}
	b.ProjectID = projectID
	if ownerID != nil {
		b.OwnerID = *ownerID
	}
	if parentID != nil {
		b.ParentBranchID = *parentID
	}
	if forkEventID != nil {
		b.ForkedAtEventID = *forkEventID
	}
	return &b, nil
}

// pgBranchChain walks the parent chain in Go (chains are <= MaxBranchDepth,
// so one row fetch per level is cheap and keeps the SQL simple).
func (s *PostgresStore) pgBranchChain(ctx context.Context, id string) ([]*MemoryBranch, error) {
	var chain []*MemoryBranch
	seen := make(map[string]bool)
	cur := id
	for cur != "" {
		if seen[cur] {
			return nil, fmt.Errorf("branch chain cycle at %q", cur)
		}
		seen[cur] = true
		br, err := scanBranch(s.pool.QueryRow(ctx,
			`SELECT `+branchColumns+` FROM memory_branches WHERE id = $1::uuid`, cur))
		if err == pgx.ErrNoRows {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		chain = append(chain, br)
		cur = br.ParentBranchID
		if len(chain) > MaxBranchDepth+1 {
			return nil, fmt.Errorf("branch chain exceeds max depth %d", MaxBranchDepth)
		}
	}
	if len(chain) == 0 {
		return nil, ErrNotFound
	}
	return chain, nil
}

// EnsureMainBranch get-or-creates the project's shared "main" branch.
func (s *PostgresStore) EnsureMainBranch(ctx context.Context, projectID string) (*MemoryBranch, error) {
	if strings.TrimSpace(projectID) == "" {
		return nil, fmt.Errorf("project id is required")
	}
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO memory_branches (project_id, name, visibility)
		 VALUES ($1::uuid, 'main', 'shared')
		 ON CONFLICT (project_id, name) DO NOTHING`, projectID); err != nil {
		return nil, err
	}
	br, err := scanBranch(s.pool.QueryRow(ctx,
		`SELECT `+branchColumns+` FROM memory_branches
		  WHERE project_id = $1::uuid AND name = 'main'`, projectID))
	if err == pgx.ErrNoRows {
		return nil, ErrNotFound
	}
	return br, err
}

// CreateBranch creates a top-level branch forked from main.
func (s *PostgresStore) CreateBranch(ctx context.Context, projectID, name, ownerID, visibility string) (*MemoryBranch, error) {
	main, err := s.EnsureMainBranch(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return s.ForkBranch(ctx, main.ID, name, ownerID, visibility, 0)
}

// ForkBranch inserts one child row pointing at its parent. Zero data copied.
func (s *PostgresStore) ForkBranch(ctx context.Context, parentID, name, ownerID, visibility string, forkEventID int64) (*MemoryBranch, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("branch name is required")
	}
	vis, err := normalizeVisibility(visibility)
	if err != nil {
		return nil, err
	}
	parentChain, err := s.pgBranchChain(ctx, parentID)
	if err != nil {
		return nil, err
	}
	parent := parentChain[0]
	if len(parentChain) >= MaxBranchDepth {
		return nil, fmt.Errorf("max branch depth %d exceeded", MaxBranchDepth)
	}
	var exists bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM memory_branches
		  WHERE project_id = $1::uuid AND name = $2)`,
		parent.ProjectID, name).Scan(&exists); err != nil {
		return nil, err
	}
	if exists {
		return nil, ErrConflict
	}
	br, err := scanBranch(s.pool.QueryRow(ctx,
		`INSERT INTO memory_branches
			(project_id, name, owner_id, parent_branch_id, forked_at_event_id, visibility)
		 VALUES ($1::uuid, $2, $3::uuid, $4::uuid, $5, $6)
		 RETURNING `+branchColumns,
		parent.ProjectID, name, nullText(ownerID), parent.ID,
		nullEventID(forkEventID), vis))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, ErrConflict
		}
		return nil, err
	}
	return br, nil
}

// GetBranch fetches one branch by id.
func (s *PostgresStore) GetBranch(ctx context.Context, id string) (*MemoryBranch, error) {
	br, err := scanBranch(s.pool.QueryRow(ctx,
		`SELECT `+branchColumns+` FROM memory_branches WHERE id = $1::uuid`, id))
	if err == pgx.ErrNoRows {
		return nil, ErrNotFound
	}
	return br, err
}

// ListBranches lists a project's branches, oldest first.
func (s *PostgresStore) ListBranches(ctx context.Context, projectID string) ([]*MemoryBranch, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+branchColumns+` FROM memory_branches
		  WHERE project_id = $1::uuid ORDER BY created_at`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*MemoryBranch
	for rows.Next() {
		br, err := scanBranch(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, br)
	}
	return out, rows.Err()
}

// WriteToBranch inserts into the current branch only — a bare INSERT with
// branch_id set. No UPDATE exists on this path, so parents cannot change.
func (s *PostgresStore) WriteToBranch(ctx context.Context, branchID string, item *MemoryItem) error {
	if item == nil {
		return fmt.Errorf("memory item is required")
	}
	if strings.TrimSpace(item.Key) == "" {
		return fmt.Errorf("memory key is required")
	}
	chain, err := s.pgBranchChain(ctx, branchID)
	if err != nil {
		return err
	}
	if item.Confidence == 0 {
		item.Confidence = 1.0
	}
	if item.Status == "" {
		item.Status = "PROPOSED"
	}
	if item.Level == "" {
		item.Level = "project"
	}
	if item.Scope == "" {
		item.Scope = "fact"
	}
	if len(item.Tags) == 0 {
		item.Tags = []string{}
	}
	row := s.pool.QueryRow(ctx,
		`INSERT INTO memory_items
			(project_id, user_id, session_id, org_id, "key", content,
			 context_snippet, level, scope, embedding, tags, confidence,
			 status, source, source_event_id, proposed_by, branch_id)
		 VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5, $6,
		         NULLIF($7,''), $8, $9, $10::vector, $11, $12,
		         $13, NULLIF($14,''), $15, $16::uuid, $17::uuid)
		 RETURNING id, created_at, updated_at`,
		chain[0].ProjectID, nullText(item.UserID),
		nullText(item.SessionID), nullText(item.OrgID),
		item.Key, item.Content, item.ContextSnippet,
		item.Level, item.Scope, encodeEmbedding(item.Embedding),
		item.Tags, item.Confidence, item.Status, item.Source,
		nullEventID(item.SourceEventID), nullText(item.ProposedBy), branchID)
	return row.Scan(&item.ID, &item.CreatedAt, &item.UpdatedAt)
}

// ResolveRead walks branch -> parent -> main, first key match wins, then
// falls back to untagged main-line rows (project-scoped + org-level).
func (s *PostgresStore) ResolveRead(ctx context.Context, branchID, key string) (*MemoryItem, error) {
	chain, err := s.pgBranchChain(ctx, branchID)
	if err != nil {
		return nil, err
	}
	for _, br := range chain {
		var itemID string
		err := s.pool.QueryRow(ctx,
			`SELECT id FROM memory_items
			  WHERE branch_id = $1::uuid AND "key" = $2
			    AND status IN ('CONFIRMED','PROPOSED')
			  ORDER BY updated_at DESC LIMIT 1`, br.ID, key).Scan(&itemID)
		if err == nil {
			return s.GetMemoryItem(ctx, itemID)
		}
		if err != pgx.ErrNoRows {
			return nil, err
		}
	}
	var itemID string
	err = s.pool.QueryRow(ctx,
		`SELECT id FROM memory_items
		  WHERE branch_id IS NULL AND "key" = $1
		    AND (project_id = $2::uuid OR project_id IS NULL)
		    AND status IN ('CONFIRMED','PROPOSED')
		  ORDER BY updated_at DESC LIMIT 1`, key, chain[0].ProjectID).Scan(&itemID)
	if err == pgx.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return s.GetMemoryItem(ctx, itemID)
}
