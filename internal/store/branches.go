// Memory branching, copy-on-write (issue #17, plan §§5.1–5.2).
//
// A branch is a lightweight head row: Fork inserts one memory_branches row
// pointing at its parent and copies ZERO memory_items rows. Reads resolve a
// key by walking child → parent → … → main and returning the first match;
// writes always INSERT into the current branch and never touch parents.
//
// Ownership: this file owns ONLY branch heads and branch-scoped reads/writes.
// MemoryItem (memory.go, issue #6) has no BranchID field, so this file
// defines its own BranchMemory view struct rather than editing another
// owner's type; a follow-up may unify them. Diff/merge (plan §5.2) are owned
// by issue #18 — this file exposes the seam #18 builds on (AncestorIDs,
// AncestorChain, FirstMatch) and nothing more.
//
// Testability: BranchStore depends on the DBTX interface (db.go), and chain
// walking plus first-match resolution are pure, so fork-zero-copy, read
// precedence, and write isolation are all covered DB-free; live-DB behaviour
// is covered by TEST_POSTGRES_DSN-gated tests (follow-up).
package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Branch visibility values (CHECK constraint in migrations/005, plan §5.1).
const (
	VisibilityPrivate = "private"
	VisibilityShared  = "shared"
)

// DefaultBranchVisibility applies when ForkParams leaves Visibility empty:
// branches start private; sharing is an explicit act.
const DefaultBranchVisibility = VisibilityPrivate

// MainBranchName is the auto-created root branch of every project (plan §5.1:
// "Every project gets a \"main\" branch automatically"). Creation is
// application-layer via EnsureMainBranch, stored 'shared' so every project
// member resolves it.
const MainBranchName = "main"

// MaxBranchDepth caps the parent chain (plan §5.4: 5 levels). Fork refuses a
// child that would exceed it; AncestorIDs refuses to walk past it (cycle
// backstop as well).
const MaxBranchDepth = 5

// BranchArchiveTTL is the plan §5.4 auto-archive age: a non-main branch with
// no archive timestamp becomes eligible once now - created_at >= 30 days.
// Main never auto-archives (it is the project root every chain resolves
// against); see IsArchivable.
const BranchArchiveTTL = 30 * 24 * time.Hour

// Branch mirrors a memory_branches row (plan §5.1, plus 007 upkeep columns).
// Empty OwnerID / ParentBranchID mean SQL NULL (root branch has no parent).
// ForkedAtEventID 0 means NULL — safe because events(id) is BIGSERIAL, never
// 0. ArchivedAt nil means NULL (branch active); PotentiallyStale persists
// the DetectStale signal (branch_diff.go, issue #18) via MarkStale /
// SurfaceStaleness.
type Branch struct {
	ID               string
	ProjectID        string
	Name             string
	OwnerID          string
	ParentBranchID   string
	ForkedAtEventID  int64
	Visibility       string
	CreatedAt        time.Time
	PotentiallyStale bool
	ArchivedAt       *time.Time
}

// IsMain reports whether this is the project root branch.
func (b Branch) IsMain() bool {
	return strings.TrimSpace(b.Name) == MainBranchName && strings.TrimSpace(b.ParentBranchID) == ""
}

// IsRoot reports whether this branch has no parent.
func (b Branch) IsRoot() bool {
	return strings.TrimSpace(b.ParentBranchID) == ""
}

// IsArchived reports whether the 30-day auto-archive has fired (archived_at
// IS NOT NULL, migration 007).
func (b Branch) IsArchived() bool {
	return b.ArchivedAt != nil
}

// IsArchivable encodes the plan §5.4 30-day rule as a pure predicate: a
// branch is eligible when it is not main, not already archived, has a known
// creation time, and now-created_at >= BranchArchiveTTL (boundary inclusive).
// Zero now never archives (fail closed — callers pass time.Now().UTC()).
func IsArchivable(b Branch, now time.Time) bool {
	if b.IsMain() || b.IsArchived() {
		return false
	}
	if b.CreatedAt.IsZero() || now.IsZero() {
		return false
	}
	return !now.Before(b.CreatedAt.Add(BranchArchiveTTL))
}

// HasForkPoint reports whether the branch records the event it forked at.
func (b Branch) HasForkPoint() bool {
	return b.ForkedAtEventID > 0
}

// NormalizeVisibility lower-cases and trims v, defaulting "" to private. The
// second return is false for values outside the plan §5.1 CHECK set.
func NormalizeVisibility(v string) (string, bool) {
	n := strings.ToLower(strings.TrimSpace(v))
	if n == "" {
		return DefaultBranchVisibility, true
	}
	switch n {
	case VisibilityPrivate, VisibilityShared:
		return n, true
	default:
		return n, false
	}
}

// ValidateBranchName rejects empty names (UNIQUE(project_id, name) would
// otherwise collide every unnamed branch on ("<project>", "")).
func ValidateBranchName(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("store: branch name is required")
	}
	return nil
}

// IsVisibleToUser encodes branch visibility: shared branches resolve for any
// project member; private branches resolve only for their owner. A private
// branch with no recorded owner resolves for nobody (fail closed).
func IsVisibleToUser(branch Branch, userID string) bool {
	if branch.Visibility == VisibilityShared {
		return true
	}
	return strings.TrimSpace(branch.OwnerID) != "" &&
		strings.TrimSpace(branch.OwnerID) == strings.TrimSpace(userID)
}

// BranchMemory is the branch-scoped view of one memory_items row. It embeds
// the issue-#6 MemoryItem untouched and adds the 005 branch_id: empty
// BranchID means a legacy pre-005 row (NULL branch_id), which resolution
// treats as main-level — visible at the end of every chain.
type BranchMemory struct {
	MemoryItem
	BranchID string
}

// IsLegacy reports whether this row predates branching (NULL branch_id).
func (m BranchMemory) IsLegacy() bool {
	return strings.TrimSpace(m.BranchID) == ""
}

// ForkParams carries one fork request. Visibility "" defaults to private;
// ForkedAtEventID 0 stores NULL.
type ForkParams struct {
	ProjectID       string
	ParentBranchID  string
	Name            string
	OwnerID         string // "" = NULL (e.g. system-created main)
	Visibility      string // "" = private
	ForkedAtEventID int64  // 0 = NULL
}

// BranchWriteParams carries one branch-scoped write. Level/Scope/Status
// default to 'project'/'fact'/'PROPOSED' when empty; anything else passes
// through to the DB CHECK constraints in migrations/001.
type BranchWriteParams struct {
	ProjectID string
	BranchID  string
	Key       string
	Content   string
	Level     string
	Scope     string
	Status    string
}

// normalizeWriteDefaults fills Level/Scope/Status blanks.
func (p *BranchWriteParams) normalizeWriteDefaults() {
	if strings.TrimSpace(p.Level) == "" {
		p.Level = LevelProject
	}
	if strings.TrimSpace(p.Scope) == "" {
		p.Scope = "fact"
	}
	if strings.TrimSpace(p.Status) == "" {
		p.Status = StatusProposed
	}
}

// Validate rejects writes missing an address (project/branch/key/content).
// It does not touch the database.
func (p BranchWriteParams) Validate() error {
	if strings.TrimSpace(p.ProjectID) == "" {
		return errors.New("store: branch write requires a project id")
	}
	if strings.TrimSpace(p.BranchID) == "" {
		return errors.New("store: branch write requires a branch id")
	}
	if strings.TrimSpace(p.Key) == "" {
		return errors.New("store: branch write requires a key")
	}
	if strings.TrimSpace(p.Content) == "" {
		return errors.New("store: branch write requires content")
	}
	return nil
}

// branchColumns selects branches with NULLs coalesced (except created_at
// and archived_at, which stay nullable). forked_at_event_id COALESCEs to 0
// ("no fork point"); callers read 0 as NULL. potentially_stale COALESCEs to
// false so pre-007 rows scan cleanly.
const branchColumns = `id::TEXT AS id, ` +
	`project_id::TEXT AS project_id, ` +
	`COALESCE(name, '') AS name, ` +
	`COALESCE(owner_id::TEXT, '') AS owner_id, ` +
	`COALESCE(parent_branch_id::TEXT, '') AS parent_branch_id, ` +
	`COALESCE(forked_at_event_id, 0) AS forked_at_event_id, ` +
	`COALESCE(visibility, 'private') AS visibility, ` +
	`created_at, ` +
	`COALESCE(potentially_stale, false) AS potentially_stale, ` +
	`archived_at`

// branchMemoryColumns selects one memory_items row plus its branch head.
// branch_id COALESCEs to ” so legacy pre-005 rows scan cleanly.
const branchMemoryColumns = `id::TEXT AS id, ` +
	`COALESCE(project_id::TEXT, '') AS project_id, ` +
	`key, ` +
	`content, ` +
	`COALESCE(level, 'project') AS level, ` +
	`COALESCE(scope, 'fact') AS scope, ` +
	`COALESCE(status, 'PROPOSED') AS status, ` +
	`COALESCE(branch_id::TEXT, '') AS branch_id`

// scanBranch scans a full branchColumns row.
func scanBranch(row pgx.Row) (*Branch, error) {
	var b Branch
	if err := row.Scan(
		&b.ID, &b.ProjectID, &b.Name, &b.OwnerID,
		&b.ParentBranchID, &b.ForkedAtEventID, &b.Visibility,
		&b.CreatedAt, &b.PotentiallyStale, &b.ArchivedAt,
	); err != nil {
		return nil, err
	}
	return &b, nil
}

// scanBranchMemory scans a full branchMemoryColumns row.
func scanBranchMemory(row pgx.Row) (*BranchMemory, error) {
	var m BranchMemory
	if err := row.Scan(
		&m.ID, &m.ProjectID, &m.Key, &m.Content,
		&m.Level, &m.Scope, &m.Status, &m.BranchID,
	); err != nil {
		return nil, err
	}
	return &m, nil
}

// nullEventID maps <= 0 to SQL NULL for the nullable forked_at_event_id.
func nullEventID(id int64) any {
	if id <= 0 {
		return nil
	}
	return id
}

// BranchStore is branch heads plus copy-on-write resolution.
type BranchStore struct {
	db DBTX
}

// NewBranchStore wires a BranchStore to any DBTX (pool, transaction, fake).
func NewBranchStore(db DBTX) *BranchStore {
	return &BranchStore{db: db}
}

// EnsureMainBranch returns the project's "main" branch, creating it (shared,
// owned by ownerID, no parent) when absent. The INSERT is ON CONFLICT
// DO NOTHING on UNIQUE(project_id, name), so concurrent first-writes race
// safely and the following SELECT always finds the row.
func (s *BranchStore) EnsureMainBranch(ctx context.Context, projectID, ownerID string) (*Branch, error) {
	if strings.TrimSpace(projectID) == "" {
		return nil, errors.New("store: main branch requires a project id")
	}
	if _, err := s.db.Exec(ctx,
		`INSERT INTO memory_branches (project_id, name, owner_id, visibility)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (project_id, name) DO NOTHING`,
		strings.TrimSpace(projectID), MainBranchName,
		nullUUID(strings.TrimSpace(ownerID)), VisibilityShared); err != nil {
		return nil, fmt.Errorf("store: ensure main branch: %w", err)
	}
	return s.GetBranchByName(ctx, strings.TrimSpace(projectID), MainBranchName)
}

// GetBranchByID fetches one branch or a wrapped ErrNotFound.
func (s *BranchStore) GetBranchByID(ctx context.Context, id string) (*Branch, error) {
	b, err := scanBranch(s.db.QueryRow(ctx,
		`SELECT `+branchColumns+` FROM memory_branches WHERE id = $1`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("store: branch %s: %w", id, ErrNotFound)
		}
		return nil, fmt.Errorf("store: get branch: %w", err)
	}
	return b, nil
}

// GetBranchByName fetches one project branch by name or a wrapped ErrNotFound.
func (s *BranchStore) GetBranchByName(ctx context.Context, projectID, name string) (*Branch, error) {
	b, err := scanBranch(s.db.QueryRow(ctx,
		`SELECT `+branchColumns+` FROM memory_branches WHERE project_id = $1 AND name = $2`,
		projectID, name))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("store: branch %q: %w", name, ErrNotFound)
		}
		return nil, fmt.Errorf("store: get branch by name: %w", err)
	}
	return b, nil
}

// Fork creates a child branch head pointing at parentBranchID. It copies ZERO
// memory_items rows — the child resolves parent content through the chain
// until it writes its own shadowing copies. Cross-project forks and chains
// that would exceed MaxBranchDepth are rejected.
func (s *BranchStore) Fork(ctx context.Context, params ForkParams) (*Branch, error) {
	projectID := strings.TrimSpace(params.ProjectID)
	parentID := strings.TrimSpace(params.ParentBranchID)
	name := strings.TrimSpace(params.Name)
	if projectID == "" {
		return nil, errors.New("store: fork requires a project id")
	}
	if parentID == "" {
		return nil, errors.New("store: fork requires a parent branch id")
	}
	if err := ValidateBranchName(name); err != nil {
		return nil, err
	}
	visibility, ok := NormalizeVisibility(params.Visibility)
	if !ok {
		return nil, fmt.Errorf("store: unknown branch visibility %q", params.Visibility)
	}
	parent, err := s.GetBranchByID(ctx, parentID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(parent.ProjectID) != projectID {
		return nil, fmt.Errorf("store: fork parent belongs to project %q, not %q",
			parent.ProjectID, projectID)
	}
	chain, err := s.AncestorIDs(ctx, parent.ID)
	if err != nil {
		return nil, err
	}
	if len(chain) >= MaxBranchDepth {
		return nil, fmt.Errorf("store: fork would exceed max branch depth %d", MaxBranchDepth)
	}
	b, err := scanBranch(s.db.QueryRow(ctx,
		`INSERT INTO memory_branches
		     (project_id, name, owner_id, parent_branch_id, forked_at_event_id, visibility)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING `+branchColumns,
		projectID, name,
		nullUUID(strings.TrimSpace(params.OwnerID)),
		parent.ID, nullEventID(params.ForkedAtEventID), visibility))
	if err != nil {
		return nil, fmt.Errorf("store: fork branch: %w", err)
	}
	return b, nil
}

// AncestorIDs walks branchID → parent → … → root in the database and returns
// the chain child-first (chain[0] is the branch itself). The walk is capped
// at MaxBranchDepth and cycle-guarded, so a corrupted parent loop surfaces as
// an error instead of an unbounded query sequence.
func (s *BranchStore) AncestorIDs(ctx context.Context, branchID string) ([]string, error) {
	cur := strings.TrimSpace(branchID)
	if cur == "" {
		return nil, errors.New("store: branch id is required")
	}
	var chain []string
	seen := make(map[string]struct{})
	for cur != "" {
		if _, dup := seen[cur]; dup {
			return nil, fmt.Errorf("store: branch parent cycle at %s", cur)
		}
		seen[cur] = struct{}{}
		chain = append(chain, cur)
		if len(chain) > MaxBranchDepth {
			return nil, fmt.Errorf("store: branch chain exceeds max depth %d", MaxBranchDepth)
		}
		var parent string
		if err := s.db.QueryRow(ctx,
			`SELECT COALESCE(parent_branch_id::TEXT, '') FROM memory_branches WHERE id = $1`,
			cur).Scan(&parent); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, fmt.Errorf("store: branch %s: %w", cur, ErrNotFound)
			}
			return nil, fmt.Errorf("store: walk branch chain: %w", err)
		}
		cur = strings.TrimSpace(parent)
	}
	return chain, nil
}

// AncestorChain is the pure, DB-free chain walk over an in-memory head map
// (id → branch): child-first, cycle-guarded, capped at MaxBranchDepth. IDs
// missing from heads still appear in the chain (their parent is unknown, so
// the walk stops there) — callers resolving against partial maps keep the
// known prefix instead of failing outright.
func AncestorChain(heads map[string]Branch, startID string) []string {
	var chain []string
	seen := make(map[string]struct{})
	cur := strings.TrimSpace(startID)
	for cur != "" {
		if _, dup := seen[cur]; dup {
			return chain
		}
		seen[cur] = struct{}{}
		chain = append(chain, cur)
		if len(chain) >= MaxBranchDepth {
			return chain
		}
		next, ok := heads[cur]
		if !ok {
			return chain
		}
		cur = strings.TrimSpace(next.ParentBranchID)
	}
	return chain
}

// FirstMatch resolves one key along a child-first chain: the nearest branch
// holding the key wins; legacy (NULL-branch) rows count as main-level and
// lose to every real branch row. Second return is false when no row matches.
func FirstMatch(items []BranchMemory, chain []string) (*BranchMemory, bool) {
	if len(items) == 0 || len(chain) == 0 {
		return nil, false
	}
	byBranch := make(map[string]*BranchMemory, len(items))
	var legacy *BranchMemory
	for i := range items {
		if items[i].IsLegacy() {
			if legacy == nil {
				legacy = &items[i]
			}
			continue
		}
		if _, taken := byBranch[items[i].BranchID]; !taken {
			byBranch[items[i].BranchID] = &items[i]
		}
	}
	for _, id := range chain {
		if m, ok := byBranch[id]; ok {
			return m, true
		}
	}
	if legacy != nil {
		return legacy, true
	}
	return nil, false
}

// BuildBranchReadSQL renders the branch-scoped item lookup: one row set per
// key across the whole chain (`IN` over child-first IDs) plus legacy
// NULL-branch rows, with first-match precedence applied in Go by FirstMatch.
// Comparing branch_id::TEXT keeps the driver surface to plain strings (same
// convention as the session store's UUID-carrying predicates).
func BuildBranchReadSQL(chainLen int) string {
	base := `SELECT ` + branchMemoryColumns + ` FROM memory_items ` +
		`WHERE project_id = $1 AND key = $2 AND (branch_id IS NULL`
	if chainLen > 0 {
		base += ` OR branch_id::TEXT IN (`
		for i := range chainLen {
			if i > 0 {
				base += `, `
			}
			base += fmt.Sprintf(`$%d`, 3+i)
		}
		base += `)`
	}
	return base + `)`
}

// Read resolves one key from branchID with copy-on-write precedence (plan
// §5.2: walk branch → parent → … → main, first match wins). A wrapped
// ErrNotFound means no row on the whole chain — not just the current branch.
func (s *BranchStore) Read(ctx context.Context, projectID, branchID, key string) (*BranchMemory, error) {
	projectID = strings.TrimSpace(projectID)
	branchID = strings.TrimSpace(branchID)
	key = strings.TrimSpace(key)
	if projectID == "" {
		return nil, errors.New("store: branch read requires a project id")
	}
	if branchID == "" {
		return nil, errors.New("store: branch read requires a branch id")
	}
	if key == "" {
		return nil, errors.New("store: branch read requires a key")
	}
	head, err := s.GetBranchByID(ctx, branchID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(head.ProjectID) != projectID {
		return nil, fmt.Errorf("store: branch %s belongs to project %q, not %q",
			branchID, head.ProjectID, projectID)
	}
	chain, err := s.AncestorIDs(ctx, head.ID)
	if err != nil {
		return nil, err
	}
	args := make([]any, 0, 2+len(chain))
	args = append(args, projectID, key)
	for _, id := range chain {
		args = append(args, id)
	}
	rows, err := s.db.Query(ctx, BuildBranchReadSQL(len(chain)), args...)
	if err != nil {
		return nil, fmt.Errorf("store: branch read query: %w", err)
	}
	defer rows.Close()
	var items []BranchMemory
	for rows.Next() {
		var m BranchMemory
		if err := rows.Scan(
			&m.ID, &m.ProjectID, &m.Key, &m.Content,
			&m.Level, &m.Scope, &m.Status, &m.BranchID,
		); err != nil {
			return nil, fmt.Errorf("store: branch read scan: %w", err)
		}
		m.ProjectID = projectID
		items = append(items, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: branch read rows: %w", err)
	}
	if m, ok := FirstMatch(items, chain); ok {
		return m, nil
	}
	return nil, fmt.Errorf("store: key %q on branch %s: %w", key, branchID, ErrNotFound)
}

// WriteToBranch inserts a new item scoped to params.BranchID (plan §5.2:
// "Always insert into current branch. Never modify parents"). Shadowing a
// parent key writes a sibling row — the parent row is untouched, so the
// parent chain still resolves its own value. Each call inserts exactly one
// row; updates/supersedes are out of scope here.
func (s *BranchStore) WriteToBranch(ctx context.Context, params BranchWriteParams) (*BranchMemory, error) {
	if err := params.Validate(); err != nil {
		return nil, err
	}
	params.normalizeWriteDefaults()
	m, err := scanBranchMemory(s.db.QueryRow(ctx,
		`INSERT INTO memory_items
		     (project_id, branch_id, key, content, level, scope, status)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 RETURNING `+branchMemoryColumns,
		strings.TrimSpace(params.ProjectID),
		strings.TrimSpace(params.BranchID),
		strings.TrimSpace(params.Key),
		params.Content,
		params.Level, params.Scope, params.Status))
	if err != nil {
		return nil, fmt.Errorf("store: write to branch: %w", err)
	}
	return m, nil
}

// ---------------------------------------------------------------------------
// Branch upkeep (issue #44, plan §5.4): persisted staleness + 30-day
// auto-archive (migration 007). DetectStale itself stays pure in
// branch_diff.go (issue #18) — the methods below only persist its signal.
// ---------------------------------------------------------------------------

// MarkStale flags one branch as potentially stale (SET potentially_stale =
// true) and returns the updated head. It is the explicit, single-branch
// counterpart to SurfaceStaleness: callers that already ran DetectStale
// off-band persist the outcome without re-running it.
func (s *BranchStore) MarkStale(ctx context.Context, branchID string) (*Branch, error) {
	branchID = strings.TrimSpace(branchID)
	if branchID == "" {
		return nil, errors.New("store: mark stale requires a branch id")
	}
	b, err := scanBranch(s.db.QueryRow(ctx,
		`UPDATE memory_branches SET potentially_stale = true WHERE id = $1
		 RETURNING `+branchColumns, branchID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("store: branch %s: %w", branchID, ErrNotFound)
		}
		return nil, fmt.Errorf("store: mark branch stale: %w", err)
	}
	return b, nil
}

// ArchiveBranch fires the 30-day auto-archive for one branch: it loads the
// head, enforces the IsArchivable rule against now, then stamps archived_at.
// Main branches, already-archived branches, and branches younger than
// BranchArchiveTTL are refused with an error (never silently skipped), so a
// sweeper loop can distinguish "not yet eligible" from a failed write. A
// zero now is replaced with time.Now().UTC().
func (s *BranchStore) ArchiveBranch(ctx context.Context, branchID string, now time.Time) (*Branch, error) {
	branchID = strings.TrimSpace(branchID)
	if branchID == "" {
		return nil, errors.New("store: archive branch requires a branch id")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	head, err := s.GetBranchByID(ctx, branchID)
	if err != nil {
		return nil, err
	}
	if !IsArchivable(*head, now) {
		return nil, fmt.Errorf("store: branch %s is not archivable (main, already archived, or younger than %s)",
			branchID, BranchArchiveTTL)
	}
	b, err := scanBranch(s.db.QueryRow(ctx,
		`UPDATE memory_branches SET archived_at = $2 WHERE id = $1
		 RETURNING `+branchColumns, branchID, now))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("store: branch %s: %w", branchID, ErrNotFound)
		}
		return nil, fmt.Errorf("store: archive branch: %w", err)
	}
	return b, nil
}

// SurfaceStaleness persists the DetectStale signal for one child branch
// (plan §5.4: "Parent update on a key that exists on child → flag child's
// item as potentially_stale"). It runs the pure DetectStale over the given
// resolved states, then writes the outcome back: stale found →
// potentially_stale = true; clean → potentially_stale = false (clearing a
// previously surfaced flag). The detected items are returned for review UIs
// whether or not any were found.
func (s *BranchStore) SurfaceStaleness(ctx context.Context, branchID string, fork, parentNow, childNow []MemoryView) ([]StaleItem, error) {
	branchID = strings.TrimSpace(branchID)
	if branchID == "" {
		return nil, errors.New("store: surface staleness requires a branch id")
	}
	stale := DetectStale(fork, parentNow, childNow)
	flag := len(stale) > 0
	_, err := s.db.Exec(ctx,
		`UPDATE memory_branches SET potentially_stale = $2 WHERE id = $1`,
		branchID, flag)
	if err != nil {
		return nil, fmt.Errorf("store: surface branch staleness: %w", err)
	}
	return stale, nil
}

// ---------------------------------------------------------------------------
// Extension points for issue #18 (diff/merge) — intentionally NOT implemented
// here. Diff collects the per-branch items (BuildBranchReadSQL over each
// head) and compares FirstMatch resolutions for shared keys; Merge inserts
// the source branch items as PROPOSED rows on the target via WriteToBranch.
// ---------------------------------------------------------------------------
