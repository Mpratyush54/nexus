// Unit tests for memory branching (issue #17, plan §§5.1–5.2).
//
// DB-free by design except through a scripted DBTX fake: visibility and
// chain predicates, first-match resolution, and SQL construction run with no
// database at all. Store-method paths (EnsureMainBranch, Fork, AncestorIDs,
// Read, WriteToBranch) run against branchFakeDB, which records statements
// and replays canned rows — fork-zero-copy is proven by asserting no
// statement ever touches memory_items. Live-Postgres coverage is a follow-up
// gated on TEST_POSTGRES_DSN.
package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"central-memory/internal/store"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ---------------------------------------------------------------------------
// Fake DBTX
// ---------------------------------------------------------------------------

// branchFakeRow is a pgx.Row replaying one canned Scan.
type branchFakeRow struct {
	values []any
	err    error
}

func (r branchFakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != len(r.values) {
		return errors.New("branchFakeRow: arity mismatch")
	}
	for i := range r.values {
		switch ptr := dest[i].(type) {
		case *string:
			*ptr = r.values[i].(string)
		case *int64:
			*ptr = r.values[i].(int64)
		case *bool:
			*ptr = r.values[i].(bool)
		case *time.Time:
			*ptr = r.values[i].(time.Time)
		case **time.Time:
			if r.values[i] == nil {
				*ptr = nil
			} else {
				t := r.values[i].(time.Time)
				*ptr = &t
			}
		default:
			return errors.New("branchFakeRow: unsupported dest")
		}
	}
	return nil
}

// branchFakeRows is a store.Rows replaying canned rows.
type branchFakeRows struct {
	rows [][]any
	pos  int
}

func (f *branchFakeRows) Next() bool { f.pos++; return f.pos <= len(f.rows) }
func (f *branchFakeRows) Err() error { return nil }
func (f *branchFakeRows) Close()     {}
func (f *branchFakeRows) Scan(dest ...any) error {
	return branchFakeRow{values: f.rows[f.pos-1]}.Scan(dest...)
}

// branchFakeDB scripts QueryRow as a queue and Query as one result set,
// recording every statement and its args for SQL assertions.
type branchFakeDB struct {
	rowQueue []branchFakeRow
	rows     *branchFakeRows
	queryErr error
	stmts    []string
	args     [][]any
}

func (f *branchFakeDB) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	f.stmts = append(f.stmts, sql)
	f.args = append(f.args, args)
	return pgconn.CommandTag{}, nil
}

func (f *branchFakeDB) Query(_ context.Context, sql string, args ...any) (store.Rows, error) {
	f.stmts = append(f.stmts, sql)
	f.args = append(f.args, args)
	if f.queryErr != nil {
		return nil, f.queryErr
	}
	if f.rows == nil {
		return &branchFakeRows{}, nil
	}
	return f.rows, nil
}

func (f *branchFakeDB) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	f.stmts = append(f.stmts, sql)
	f.args = append(f.args, args)
	if len(f.rowQueue) == 0 {
		return branchFakeRow{err: pgx.ErrNoRows}
	}
	r := f.rowQueue[0]
	f.rowQueue = f.rowQueue[1:]
	return r
}

// branchRow builds a full branchColumns row: id, project, name, owner,
// parent, fork-event, visibility, created_at, potentially_stale,
// archived_at (active head: stale false, archived nil).
func branchRow(id, project, name, owner, parent string, forkEvent int64, visibility string) branchFakeRow {
	return branchRowUpkeep(id, project, name, owner, parent, forkEvent, visibility,
		time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC), false, nil)
}

// branchRowUpkeep builds a full branchColumns row with explicit upkeep
// fields: created is created_at, stale is potentially_stale, archived is
// archived_at (nil = active).
func branchRowUpkeep(id, project, name, owner, parent string, forkEvent int64, visibility string, created time.Time, stale bool, archived any) branchFakeRow {
	return branchFakeRow{values: []any{
		id, project, name, owner, parent, forkEvent, visibility,
		created, stale, archived,
	}}
}

// parentRow builds a single-column AncestorIDs walk row.
func parentRow(parent string) branchFakeRow {
	return branchFakeRow{values: []any{parent}}
}

// branchMemRow builds a full branchMemoryColumns row: id, project, key,
// content, level, scope, status, branch.
func branchMemRow(id, project, key, content, branch string) []any {
	return []any{id, project, key, content, "project", "fact", "PROPOSED", branch}
}

func stmtsMention(stmts []string, substr string) int {
	n := 0
	for _, s := range stmts {
		if strings.Contains(s, substr) {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// Pure predicates
// ---------------------------------------------------------------------------

func TestBranchNormalizeVisibility(t *testing.T) {
	for in, want := range map[string]string{
		"":         store.VisibilityPrivate,
		"private":  store.VisibilityPrivate,
		"SHARED":   store.VisibilityShared,
		" Shared ": store.VisibilityShared,
	} {
		got, ok := store.NormalizeVisibility(in)
		if !ok || got != want {
			t.Errorf("NormalizeVisibility(%q) = %q, %v; want %q, true", in, got, ok, want)
		}
	}
	if _, ok := store.NormalizeVisibility("public"); ok {
		t.Error("NormalizeVisibility(public) should be invalid (plan CHECK is private/shared)")
	}
}

func TestBranchValidateName(t *testing.T) {
	if err := store.ValidateBranchName("bob-experiment"); err != nil {
		t.Errorf("valid name rejected: %v", err)
	}
	for _, bad := range []string{"", "   "} {
		if err := store.ValidateBranchName(bad); err == nil {
			t.Errorf("name %q should be rejected", bad)
		}
	}
}

func TestBranchVisibilityPredicate(t *testing.T) {
	shared := store.Branch{ID: "b", Visibility: store.VisibilityShared}
	if !store.IsVisibleToUser(shared, "anyone") || !store.IsVisibleToUser(shared, "") {
		t.Error("shared branch should resolve for any user")
	}
	private := store.Branch{ID: "b", OwnerID: "alice", Visibility: store.VisibilityPrivate}
	if !store.IsVisibleToUser(private, "alice") {
		t.Error("private branch should resolve for its owner")
	}
	if store.IsVisibleToUser(private, "bob") {
		t.Error("private branch should not resolve for another user")
	}
	ownerless := store.Branch{ID: "b", Visibility: store.VisibilityPrivate}
	if store.IsVisibleToUser(ownerless, "alice") {
		t.Error("ownerless private branch should fail closed")
	}
}

func TestBranchAncestorChainPure(t *testing.T) {
	heads := map[string]store.Branch{
		"main":  {ID: "main", Name: "main"},
		"child": {ID: "child", ParentBranchID: "main"},
		"leaf":  {ID: "leaf", ParentBranchID: "child"},
	}
	chain := store.AncestorChain(heads, "leaf")
	if len(chain) != 3 || chain[0] != "leaf" || chain[1] != "child" || chain[2] != "main" {
		t.Fatalf("chain = %v, want [leaf child main]", chain)
	}
	if got := store.AncestorChain(heads, "main"); len(got) != 1 || got[0] != "main" {
		t.Fatalf("root chain = %v, want [main]", got)
	}
	// Cycle terminates instead of looping forever.
	loop := map[string]store.Branch{
		"a": {ID: "a", ParentBranchID: "b"},
		"b": {ID: "b", ParentBranchID: "a"},
	}
	if got := store.AncestorChain(loop, "a"); len(got) != 2 {
		t.Fatalf("cycle chain = %v, want 2 elements then stop", got)
	}
	// Depth cap: 6-deep ancestry stops at MaxBranchDepth.
	deep := map[string]store.Branch{}
	for i := 0; i < 6; i++ {
		id := string(rune('a' + i))
		parent := ""
		if i > 0 {
			parent = string(rune('a' + i - 1))
		}
		deep[id] = store.Branch{ID: id, ParentBranchID: parent}
	}
	if got := store.AncestorChain(deep, "f"); len(got) != store.MaxBranchDepth {
		t.Fatalf("deep chain len = %d, want cap %d", len(got), store.MaxBranchDepth)
	}
}

// First-match precedence: nearest branch holding the key wins.
func TestBranchFirstMatchPrecedence(t *testing.T) {
	mk := func(key, branch, content string) store.BranchMemory {
		return store.BranchMemory{
			MemoryItem: store.MemoryItem{Key: key, Content: content},
			BranchID:   branch,
		}
	}
	chain := []string{"leaf", "child", "main"}
	items := []store.BranchMemory{
		mk("k", "main", "main-value"),
		mk("k", "leaf", "leaf-value"),
		mk("k", "child", "child-value"),
	}
	got, ok := store.FirstMatch(items, chain)
	if !ok || got.Content != "leaf-value" {
		t.Fatalf("want leaf shadowing, got %+v, %v", got, ok)
	}
	// Child write removed → parent value shows through.
	got, ok = store.FirstMatch(items[:1], chain)
	if !ok || got.Content != "main-value" {
		t.Fatalf("want parent fallback, got %+v, %v", got, ok)
	}
	// Legacy pre-005 rows lose to every real branch row, but still resolve.
	legacy := []store.BranchMemory{mk("k", "main", "branched"), mk("k", "", "legacy")}
	got, ok = store.FirstMatch(legacy, chain)
	if !ok || got.Content != "branched" {
		t.Fatalf("want branched over legacy, got %+v, %v", got, ok)
	}
	if got, ok := store.FirstMatch(legacy[1:], chain); !ok || got.Content != "legacy" {
		t.Fatalf("legacy alone should resolve, got %+v, %v", got, ok)
	}
	if _, ok := store.FirstMatch(items, []string{"other"}); ok {
		t.Error("off-chain lookup should miss")
	}
	if _, ok := store.FirstMatch(nil, chain); ok {
		t.Error("empty items should miss")
	}
}

func TestBranchBuildReadSQL(t *testing.T) {
	sql := store.BuildBranchReadSQL(3)
	for _, want := range []string{
		"FROM memory_items", "project_id = $1", "key = $2",
		"branch_id IS NULL", "branch_id::TEXT IN ($3, $4, $5)",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("read SQL missing %q:\n%s", want, sql)
		}
	}
	bare := store.BuildBranchReadSQL(0)
	if strings.Contains(bare, "IN (") {
		t.Errorf("zero-chain SQL should not carry an IN list:\n%s", bare)
	}
	if !strings.Contains(bare, "branch_id IS NULL") {
		t.Errorf("zero-chain SQL should still match legacy rows:\n%s", bare)
	}
}

// ---------------------------------------------------------------------------
// Fork: zero-copy + validation
// ---------------------------------------------------------------------------

func TestBranchForkZeroCopy(t *testing.T) {
	fake := &branchFakeDB{rowQueue: []branchFakeRow{
		// GetBranchByID(parent)
		branchRow("parent-id", "proj", "main", "alice", "", 0, store.VisibilityShared),
		// AncestorIDs(parent): parent is a root
		parentRow(""),
		// INSERT RETURNING (child)
		branchRow("child-id", "proj", "bob-exp", "bob", "parent-id", 7, store.VisibilityPrivate),
	}}
	s := store.NewBranchStore(fake)
	child, err := s.Fork(context.Background(), store.ForkParams{
		ProjectID: "proj", ParentBranchID: "parent-id",
		Name: "bob-exp", OwnerID: "bob", ForkedAtEventID: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	if child.ID != "child-id" || child.ParentBranchID != "parent-id" {
		t.Errorf("child = %+v, want id child-id parented at parent-id", child)
	}
	// ACCEPTANCE: fork copies zero rows — no statement touches memory_items.
	if n := stmtsMention(fake.stmts, "memory_items"); n != 0 {
		t.Errorf("fork touched memory_items in %d statements (want 0): %v", n, fake.stmts)
	}
	if n := stmtsMention(fake.stmts, "INSERT INTO memory_branches"); n != 1 {
		t.Errorf("want exactly 1 branch INSERT, got %d: %v", n, fake.stmts)
	}
}

func TestBranchForkValidation(t *testing.T) {
	s := store.NewBranchStore(&branchFakeDB{})
	for name, p := range map[string]store.ForkParams{
		"missing project": {ParentBranchID: "p", Name: "x"},
		"missing parent":  {ProjectID: "proj", Name: "x"},
		"missing name":    {ProjectID: "proj", ParentBranchID: "p"},
		"bad visibility":  {ProjectID: "proj", ParentBranchID: "p", Name: "x", Visibility: "public"},
	} {
		if _, err := s.Fork(context.Background(), p); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}

func TestBranchForkRejectsCrossProject(t *testing.T) {
	fake := &branchFakeDB{rowQueue: []branchFakeRow{
		branchRow("parent-id", "other-proj", "main", "alice", "", 0, store.VisibilityShared),
	}}
	s := store.NewBranchStore(fake)
	if _, err := s.Fork(context.Background(), store.ForkParams{
		ProjectID: "proj", ParentBranchID: "parent-id", Name: "x",
	}); err == nil {
		t.Error("cross-project fork should be rejected")
	}
	if n := stmtsMention(fake.stmts, "INSERT INTO"); n != 0 {
		t.Errorf("rejected fork should not INSERT, got: %v", fake.stmts)
	}
}

func TestBranchForkRejectsDepthOverflow(t *testing.T) {
	// Parent chain already at MaxBranchDepth: b1..b5.
	fake := &branchFakeDB{rowQueue: []branchFakeRow{
		branchRow("b1", "proj", "deep", "bob", "b2", 0, store.VisibilityPrivate),
		parentRow("b2"), parentRow("b3"), parentRow("b4"), parentRow("b5"), parentRow(""),
	}}
	s := store.NewBranchStore(fake)
	if _, err := s.Fork(context.Background(), store.ForkParams{
		ProjectID: "proj", ParentBranchID: "b1", Name: "too-deep",
	}); err == nil {
		t.Error("fork past max depth should be rejected")
	}
}

// ---------------------------------------------------------------------------
// Read: child → parent → main first-match
// ---------------------------------------------------------------------------

func TestBranchReadPrecedence(t *testing.T) {
	fake := &branchFakeDB{
		rowQueue: []branchFakeRow{
			// GetBranchByID(child)
			branchRow("child-id", "proj", "bob-exp", "bob", "parent-id", 0, store.VisibilityPrivate),
			// AncestorIDs(child → parent → main → root)
			parentRow("parent-id"), parentRow("main-id"), parentRow(""),
		},
		rows: &branchFakeRows{rows: [][]any{
			branchMemRow("m1", "proj", "testing/framework", "parent-value", "parent-id"),
			branchMemRow("m2", "proj", "testing/framework", "child-value", "child-id"),
			branchMemRow("m3", "proj", "testing/framework", "main-value", "main-id"),
		}},
	}
	s := store.NewBranchStore(fake)
	got, err := s.Read(context.Background(), "proj", "child-id", "testing/framework")
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != "child-value" || got.BranchID != "child-id" {
		t.Errorf("read = %+v, want child shadowing value", got)
	}
	// The item query scopes to the whole chain in child-first order.
	var queryArgs []any
	for i, stmt := range fake.stmts {
		if strings.Contains(stmt, "FROM memory_items") {
			queryArgs = fake.args[i]
		}
	}
	want := []any{"proj", "testing/framework", "child-id", "parent-id", "main-id"}
	if len(queryArgs) != len(want) {
		t.Fatalf("read args = %v, want %v", queryArgs, want)
	}
	for i := range want {
		if queryArgs[i] != want[i] {
			t.Errorf("read arg %d = %v, want %v", i, queryArgs[i], want[i])
		}
	}
}

func TestBranchReadFallsBackToParent(t *testing.T) {
	fake := &branchFakeDB{
		rowQueue: []branchFakeRow{
			branchRow("child-id", "proj", "bob-exp", "bob", "main-id", 0, store.VisibilityPrivate),
			parentRow("main-id"), parentRow(""),
		},
		rows: &branchFakeRows{rows: [][]any{
			branchMemRow("m1", "proj", "k", "main-value", "main-id"),
		}},
	}
	s := store.NewBranchStore(fake)
	got, err := s.Read(context.Background(), "proj", "child-id", "k")
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != "main-value" {
		t.Errorf("read = %+v, want parent fallback", got)
	}
}

func TestBranchReadNotFound(t *testing.T) {
	fake := &branchFakeDB{
		rowQueue: []branchFakeRow{
			branchRow("child-id", "proj", "x", "bob", "", 0, store.VisibilityPrivate),
			parentRow(""),
		},
		rows: &branchFakeRows{},
	}
	s := store.NewBranchStore(fake)
	if _, err := s.Read(context.Background(), "proj", "child-id", "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Write: always to the current branch
// ---------------------------------------------------------------------------

func TestBranchWriteIsolation(t *testing.T) {
	fake := &branchFakeDB{rowQueue: []branchFakeRow{{
		values: []any{
			"item-1", "proj", "testing/framework", "child-value",
			"project", "fact", "PROPOSED", "child-id",
		},
	}}}
	s := store.NewBranchStore(fake)
	got, err := s.WriteToBranch(context.Background(), store.BranchWriteParams{
		ProjectID: "proj", BranchID: "child-id",
		Key: "testing/framework", Content: "child-value",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.BranchID != "child-id" {
		t.Errorf("write landed on %+v, want branch child-id", got)
	}
	if len(fake.stmts) != 1 || !strings.Contains(fake.stmts[0], "INSERT INTO memory_items") {
		t.Fatalf("write should be a single INSERT, got: %v", fake.stmts)
	}
	if strings.Contains(fake.stmts[0], "UPDATE") {
		t.Errorf("write must never UPDATE (parent rows immutable): %s", fake.stmts[0])
	}
	// ACCEPTANCE: child write leaves parent resolution unchanged — the parent
	// chain still resolves its own value after the child shadows the key.
	parentRow := store.BranchMemory{
		MemoryItem: store.MemoryItem{Key: "testing/framework", Content: "parent-value"},
		BranchID:   "parent-id",
	}
	childRow := store.BranchMemory{
		MemoryItem: store.MemoryItem{Key: "testing/framework", Content: "child-value"},
		BranchID:   "child-id",
	}
	if m, _ := store.FirstMatch(
		[]store.BranchMemory{parentRow, childRow}, []string{"parent-id"},
	); m.Content != "parent-value" {
		t.Errorf("parent chain resolves %q after child write, want parent-value", m.Content)
	}
}

func TestBranchWriteValidation(t *testing.T) {
	fake := &branchFakeDB{rowQueue: []branchFakeRow{{
		values: []any{
			"item-1", "proj", "k", "long enough content here",
			"project", "fact", "PROPOSED", "b",
		},
	}}}
	s := store.NewBranchStore(fake)
	full := store.BranchWriteParams{
		ProjectID: "proj", BranchID: "b", Key: "k", Content: "long enough content here",
	}
	if _, err := s.WriteToBranch(context.Background(), full); err != nil {
		t.Errorf("valid write rejected: %v", err)
	}
	cases := []store.BranchWriteParams{
		{BranchID: "b", Key: "k", Content: "c"},
		{ProjectID: "proj", Key: "k", Content: "c"},
		{ProjectID: "proj", BranchID: "b", Content: "c"},
		{ProjectID: "proj", BranchID: "b", Key: "k"},
	}
	for i, p := range cases {
		if _, err := s.WriteToBranch(context.Background(), p); err == nil {
			t.Errorf("case %d (%+v) should be rejected", i, p)
		}
	}
}

// ---------------------------------------------------------------------------
// EnsureMainBranch
// ---------------------------------------------------------------------------

func TestBranchEnsureMain(t *testing.T) {
	fake := &branchFakeDB{rowQueue: []branchFakeRow{
		branchRow("main-id", "proj", "main", "alice", "", 0, store.VisibilityShared),
	}}
	s := store.NewBranchStore(fake)
	main, err := s.EnsureMainBranch(context.Background(), "proj", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if main.Name != "main" || !main.IsRoot() || main.Visibility != store.VisibilityShared {
		t.Errorf("main = %+v, want shared root branch", main)
	}
	if n := stmtsMention(fake.stmts, "ON CONFLICT (project_id, name) DO NOTHING"); n != 1 {
		t.Errorf("ensure-main should be idempotent upsert, got: %v", fake.stmts)
	}
	if _, err := s.EnsureMainBranch(context.Background(), "  ", ""); err == nil {
		t.Error("empty project id should be rejected")
	}
}

// ---------------------------------------------------------------------------
// Upkeep (issue #44, plan §5.4): 30-day auto-archive + persisted staleness
// ---------------------------------------------------------------------------

func TestBranchIsArchivable(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	old := now.Add(-31 * 24 * time.Hour)
	archivedAt := now.Add(-time.Hour)
	oldBranch := func() store.Branch {
		return store.Branch{ID: "b", Name: "bob-exp", ParentBranchID: "main-id", CreatedAt: old}
	}
	if !store.IsArchivable(oldBranch(), now) {
		t.Error("31-day-old child branch should be archivable")
	}
	// Boundary inclusive: exactly 30 days counts.
	exact := store.Branch{ID: "b", Name: "x", ParentBranchID: "p", CreatedAt: now.Add(-store.BranchArchiveTTL)}
	if !store.IsArchivable(exact, now) {
		t.Error("branch exactly at the 30-day TTL should be archivable")
	}
	young := oldBranch()
	young.CreatedAt = now.Add(-29 * 24 * time.Hour)
	if store.IsArchivable(young, now) {
		t.Error("29-day-old branch should not be archivable")
	}
	main := store.Branch{ID: "m", Name: "main", CreatedAt: old}
	if store.IsArchivable(main, now) {
		t.Error("main should never auto-archive (every chain resolves against it)")
	}
	done := oldBranch()
	done.ArchivedAt = &archivedAt
	if store.IsArchivable(done, now) {
		t.Error("already-archived branch should not be archivable again")
	}
	nocreated := oldBranch()
	nocreated.CreatedAt = time.Time{}
	if store.IsArchivable(nocreated, now) {
		t.Error("branch with unknown creation time should fail closed")
	}
	if store.IsArchivable(oldBranch(), time.Time{}) {
		t.Error("zero now should never archive (fail closed)")
	}
}

func TestBranchMarkStale(t *testing.T) {
	fake := &branchFakeDB{rowQueue: []branchFakeRow{
		branchRowUpkeep("child-id", "proj", "bob-exp", "bob", "main-id", 0,
			store.VisibilityPrivate, time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC), true, nil),
	}}
	s := store.NewBranchStore(fake)
	got, err := s.MarkStale(context.Background(), "child-id")
	if err != nil {
		t.Fatal(err)
	}
	if !got.PotentiallyStale {
		t.Errorf("marked branch = %+v, want potentially_stale true", got)
	}
	if n := stmtsMention(fake.stmts, "potentially_stale = true"); n != 1 {
		t.Errorf("want exactly 1 staleness UPDATE, got %d: %v", n, fake.stmts)
	}
	if _, err := s.MarkStale(context.Background(), "  "); err == nil {
		t.Error("empty branch id should be rejected")
	}
}

func TestBranchArchiveBranchEligible(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	created := now.Add(-31 * 24 * time.Hour)
	fake := &branchFakeDB{rowQueue: []branchFakeRow{
		// GetBranchByID(head): old, active child.
		branchRowUpkeep("child-id", "proj", "bob-exp", "bob", "main-id", 0,
			store.VisibilityPrivate, created, false, nil),
		// UPDATE ... RETURNING (archived head).
		branchRowUpkeep("child-id", "proj", "bob-exp", "bob", "main-id", 0,
			store.VisibilityPrivate, created, false, now),
	}}
	s := store.NewBranchStore(fake)
	got, err := s.ArchiveBranch(context.Background(), "child-id", now)
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsArchived() {
		t.Errorf("archived branch = %+v, want archived_at set", got)
	}
	if n := stmtsMention(fake.stmts, "SET archived_at"); n != 1 {
		t.Errorf("want exactly 1 archive UPDATE, got %d: %v", n, fake.stmts)
	}
}

func TestBranchArchiveBranchRefusesYoung(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	fake := &branchFakeDB{rowQueue: []branchFakeRow{
		branchRowUpkeep("child-id", "proj", "bob-exp", "bob", "main-id", 0,
			store.VisibilityPrivate, now.Add(-time.Hour), false, nil),
	}}
	s := store.NewBranchStore(fake)
	if _, err := s.ArchiveBranch(context.Background(), "child-id", now); err == nil {
		t.Error("1-hour-old branch should be refused (30-day rule)")
	}
	if n := stmtsMention(fake.stmts, "UPDATE"); n != 0 {
		t.Errorf("refused archive must not UPDATE, got: %v", fake.stmts)
	}
}

func TestBranchSurfaceStalenessFlagsAndClears(t *testing.T) {
	view := func(key, content string) store.MemoryView {
		return store.MemoryView{Key: key, Content: content}
	}
	fork := []store.MemoryView{view("k", "v0")}
	parentMoved := []store.MemoryView{view("k", "v1")}
	child := []store.MemoryView{view("k", "v0")}

	// Stale: parent moved a forked key the child carries → flag persists.
	flagged := &branchFakeDB{}
	s := store.NewBranchStore(flagged)
	stale, err := s.SurfaceStaleness(context.Background(), "child-id", fork, parentMoved, child)
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 1 || !stale[0].IsStale() {
		t.Fatalf("stale = %+v, want 1 flagged item", stale)
	}
	if len(flagged.args) != 1 || len(flagged.args[0]) != 2 || flagged.args[0][1] != true {
		t.Errorf("stale UPDATE args = %v, want [child-id true]", flagged.args)
	}

	// Clean: parent untouched since fork → flag cleared, no items returned.
	clean := &branchFakeDB{}
	s = store.NewBranchStore(clean)
	stale, err = s.SurfaceStaleness(context.Background(), "child-id", fork, fork, child)
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 0 {
		t.Fatalf("stale = %+v, want empty", stale)
	}
	if len(clean.args) != 1 || len(clean.args[0]) != 2 || clean.args[0][1] != false {
		t.Errorf("clean UPDATE args = %v, want [child-id false]", clean.args)
	}
}
