// Unit tests for memory lifecycle transitions (issue #35).
//
// DB-free: validation, tier-tag round-trips and SQL builders run pure; the
// MemoryStore write paths (SaveProposed, ConfirmDue, PromoteKey, SetStatus)
// run against transFakeDB, a scripted DBTX recording every statement.
package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ---------------------------------------------------------------------------
// Fake DBTX
// ---------------------------------------------------------------------------

// transProposed is one canned PROPOSED candidate row.
type transProposed struct {
	id        string
	source    string
	createdAt time.Time
}

// transProposedRows replays candidate rows for the ConfirmDue select.
type transProposedRows struct {
	rows []transProposed
	pos  int
}

func (f *transProposedRows) Next() bool { f.pos++; return f.pos <= len(f.rows) }
func (f *transProposedRows) Err() error { return nil }
func (f *transProposedRows) Close()     {}
func (f *transProposedRows) Scan(dest ...any) error {
	if len(dest) != 3 {
		return errors.New("transProposedRows: arity mismatch")
	}
	r := f.rows[f.pos-1]
	if p, ok := dest[0].(*string); ok {
		*p = r.id
	} else {
		return errors.New("transProposedRows: dest 0 must be *string")
	}
	if p, ok := dest[1].(*string); ok {
		*p = r.source
	} else {
		return errors.New("transProposedRows: dest 1 must be *string")
	}
	if p, ok := dest[2].(*time.Time); ok {
		*p = r.createdAt
	} else {
		return errors.New("transProposedRows: dest 2 must be *time.Time")
	}
	return nil
}

// transIDRow replays the SaveProposed RETURNING id scan.
type transIDRow struct {
	id  string
	err error
}

func (r transIDRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != 1 {
		return errors.New("transIDRow: arity mismatch")
	}
	p, ok := dest[0].(*string)
	if !ok {
		return errors.New("transIDRow: dest must be *string")
	}
	*p = r.id
	return nil
}

// transFakeDB scripts QueryRow (id), Query (proposed set) and Exec
// (affected count), recording every statement.
type transFakeDB struct {
	rowID    string
	rowErr   error
	proposed []transProposed
	queryErr error
	execErr  error
	affected int64

	queries  []string
	execs    []string
	execArgs [][]any
}

func (f *transFakeDB) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	f.execs = append(f.execs, sql)
	f.execArgs = append(f.execArgs, args)
	if f.execErr != nil {
		return pgconn.CommandTag{}, f.execErr
	}
	return pgconn.NewCommandTag(fmt.Sprintf("UPDATE %d", f.affected)), nil
}

func (f *transFakeDB) Query(_ context.Context, sql string, _ ...any) (Rows, error) {
	f.queries = append(f.queries, sql)
	if f.queryErr != nil {
		return nil, f.queryErr
	}
	return &transProposedRows{rows: f.proposed}, nil
}

func (f *transFakeDB) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	f.queries = append(f.queries, sql)
	return transIDRow{id: f.rowID, err: f.rowErr}
}

func validProposedInput() ProposedInput {
	return ProposedInput{
		ProjectID:      "proj-1",
		SessionID:      "sess-1",
		Key:            "testing/framework",
		Content:        "The team uses pytest with fixture-based setup here.",
		ContextSnippet: "decided by Alice during auth refactor",
		Level:          LevelSession,
		Scope:          "decision",
		Tags:           []string{"testing"},
		Confidence:     0.8,
		Embedding:      []float32{0.1, 0.2},
		ConfirmAfter:   ConfirmDueAfterDefault,
	}
}

// ---------------------------------------------------------------------------
// CHECK-mirroring validators
// ---------------------------------------------------------------------------

func TestTransitionsValidateChecks(t *testing.T) {
	good := validProposedInput()
	if err := good.Validate(); err != nil {
		t.Fatalf("valid input must pass: %v", err)
	}
	for name, mutate := range map[string]func(*ProposedInput){
		"short content":  func(in *ProposedInput) { in.Content = "too short" },
		"long content":   func(in *ProposedInput) { in.Content = strings.Repeat("x", 2001) },
		"bad level":      func(in *ProposedInput) { in.Level = "global" },
		"bad scope":      func(in *ProposedInput) { in.Scope = "vibe" },
		"bad confidence": func(in *ProposedInput) { in.Confidence = 1.5 },
		"empty key":      func(in *ProposedInput) { in.Key = "  " },
		"empty project":  func(in *ProposedInput) { in.ProjectID = "" },
	} {
		in := validProposedInput()
		mutate(&in)
		if err := in.Validate(); err == nil {
			t.Errorf("%s: must fail CHECK-mirroring validation", name)
		}
	}
	// Boundary lengths: 20 ok, 19 rejected, 2000 ok.
	in := validProposedInput()
	in.Content = strings.Repeat("y", 20)
	if err := in.Validate(); err != nil {
		t.Errorf("20-char content must pass: %v", err)
	}
	in.Content = strings.Repeat("y", 19)
	if err := in.Validate(); err == nil {
		t.Error("19-char content must fail")
	}
}

func TestTransitionsStatusEdges(t *testing.T) {
	legal := [][2]string{
		{StatusProposed, StatusConfirmed},
		{StatusProposed, StatusRejected},
		{StatusConfirmed, StatusSuperseded},
		{"proposed", "confirmed"}, // case-insensitive endpoints
	}
	for _, e := range legal {
		if err := ValidateStatusTransition(e[0], e[1]); err != nil {
			t.Errorf("%s -> %s must be legal: %v", e[0], e[1], err)
		}
	}
	illegal := [][2]string{
		{StatusProposed, StatusSuperseded},  // skip-a-step
		{StatusConfirmed, StatusProposed},   // demotion without re-propose
		{StatusConfirmed, StatusRejected},   // confirmed never rejected
		{StatusRejected, StatusConfirmed},   // rejected is terminal
		{StatusSuperseded, StatusConfirmed}, // superseded is terminal
		{"BOGUS", StatusConfirmed},          // CHECK-mirroring: unknown current
		{StatusProposed, "ARCHIVED"},        // CHECK-mirroring: outside CHECK set
	}
	for _, e := range illegal {
		if err := ValidateStatusTransition(e[0], e[1]); err == nil {
			t.Errorf("%s -> %s must be rejected", e[0], e[1])
		}
	}
}

// ---------------------------------------------------------------------------
// Confirm-tier tag round-trip
// ---------------------------------------------------------------------------

func TestTransitionsConfirmTierRoundTrip(t *testing.T) {
	for _, d := range []time.Duration{time.Hour, 4 * time.Hour, 24 * time.Hour} {
		got, ok := ParseConfirmAfter(FormatConfirmSource(d))
		if !ok || got != d {
			t.Errorf("tier %v round-trip = %v,%v", d, got, ok)
		}
	}
	if got := FormatConfirmSource(0); got != "processor" {
		t.Errorf("zero tier source = %q, want processor", got)
	}
	for _, src := range []string{"processor", "", "processor:confirm_after=bogus", "processor:confirm_after=0s"} {
		if _, ok := ParseConfirmAfter(src); ok {
			t.Errorf("source %q must not parse a tier", src)
		}
	}
}

func TestTransitionsIsConfirmDue(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	if !IsConfirmDue(now.Add(-25*time.Hour), 24*time.Hour, now) {
		t.Error("25h-old 24h-tier row must be due")
	}
	if IsConfirmDue(now.Add(-23*time.Hour), 24*time.Hour, now) {
		t.Error("23h-old 24h-tier row must not be due")
	}
	if !IsConfirmDue(now.Add(-61*time.Minute), time.Hour, now) {
		t.Error("61m-old 1h-tier row must be due (fast track honored)")
	}
	if IsConfirmDue(time.Time{}, time.Hour, now) {
		t.Error("zero proposal time must never flag due")
	}
}

// ---------------------------------------------------------------------------
// SQL builders
// ---------------------------------------------------------------------------

func TestTransitionsSaveProposedSQL(t *testing.T) {
	sql, args := BuildSaveProposedSQL(validProposedInput())
	for _, want := range []string{"INSERT INTO memory_items", "'PROPOSED'", "RETURNING id::TEXT"} {
		if !strings.Contains(sql, want) {
			t.Errorf("SQL missing %q:\n%s", want, sql)
		}
	}
	if len(args) != 11 {
		t.Fatalf("want 11 args, got %d", len(args))
	}
	if src, _ := args[10].(string); !strings.Contains(src, "confirm_after=") {
		t.Errorf("source arg = %q, want confirm tier tag", src)
	}
	if emb, _ := args[7].(string); emb != "[0.1,0.2]" {
		t.Errorf("embedding arg = %v, want pgvector literal", args[7])
	}
	// Absent embedding stores NULL for later backfill.
	noVec := validProposedInput()
	noVec.Embedding = nil
	_, args2 := BuildSaveProposedSQL(noVec)
	if args2[7] != nil {
		t.Errorf("nil embedding must bind NULL, got %v", args2[7])
	}
}

func TestTransitionsPromoteKeySQL(t *testing.T) {
	sql := BuildPromoteKeySQL()
	for _, want := range []string{"session_id = NULL", "level = 'project'", "session_id IS NOT NULL", "key = $2"} {
		if !strings.Contains(sql, want) {
			t.Errorf("promote SQL missing %q:\n%s", want, sql)
		}
	}
}

func TestTransitionsConfirmSQL(t *testing.T) {
	if sql := BuildListProposedSQL(); !strings.Contains(sql, "status = 'PROPOSED'") {
		t.Errorf("list SQL must select PROPOSED rows:\n%s", sql)
	}
	if sql := BuildConfirmIDsSQL(); !strings.Contains(sql, "status = 'CONFIRMED'") ||
		!strings.Contains(sql, "status = 'PROPOSED'") {
		t.Errorf("confirm SQL must flip PROPOSED -> CONFIRMED:\n%s", sql)
	}
}

// ---------------------------------------------------------------------------
// Store write paths (scripted DBTX)
// ---------------------------------------------------------------------------

func TestTransitionsSaveProposedValidationBlocksDB(t *testing.T) {
	fake := &transFakeDB{rowID: "mem-1"}
	in := validProposedInput()
	in.Level = "global"
	if _, err := NewMemoryStore(fake).SaveProposed(context.Background(), in); err == nil {
		t.Fatal("invalid level must fail before any SQL")
	}
	if len(fake.queries) != 0 {
		t.Errorf("invalid input must issue no statements, got %v", fake.queries)
	}
	id, err := NewMemoryStore(fake).SaveProposed(context.Background(), validProposedInput())
	if err != nil {
		t.Fatal(err)
	}
	if id != "mem-1" {
		t.Errorf("id = %q, want mem-1", id)
	}
	if !strings.Contains(fake.queries[0], "INSERT INTO memory_items") {
		t.Errorf("save must INSERT:\n%s", fake.queries[0])
	}
}

func TestTransitionsConfirmDueSweep(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	fake := &transFakeDB{
		proposed: []transProposed{
			{id: "due-default", source: "processor", createdAt: now.Add(-25 * time.Hour)},
			{id: "due-fast", source: FormatConfirmSource(time.Hour), createdAt: now.Add(-2 * time.Hour)},
			{id: "fresh-default", source: FormatConfirmSource(24 * time.Hour), createdAt: now.Add(-time.Hour)},
			{id: "fresh-fast", source: FormatConfirmSource(4 * time.Hour), createdAt: now.Add(-time.Hour)},
		},
		affected: 2,
	}
	n, err := NewMemoryStore(fake).ConfirmDue(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("confirmed = %d, want 2", n)
	}
	if len(fake.execs) != 1 || !strings.Contains(fake.execs[0], "status = 'CONFIRMED'") {
		t.Fatalf("sweep must issue one confirm UPDATE, got %v", fake.execs)
	}
	ids, _ := fake.execArgs[0][0].([]string)
	if len(ids) != 2 || ids[0] != "due-default" || ids[1] != "due-fast" {
		t.Errorf("due ids = %v, want [due-default due-fast]", ids)
	}
}

func TestTransitionsConfirmDueEmptyIssuesNoUpdate(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	fake := &transFakeDB{proposed: []transProposed{
		{id: "fresh", source: FormatConfirmSource(24 * time.Hour), createdAt: now},
	}}
	n, err := NewMemoryStore(fake).ConfirmDue(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 || len(fake.execs) != 0 {
		t.Errorf("no due rows: want (0, no UPDATE), got (%d, %v)", n, fake.execs)
	}
}

func TestTransitionsPromoteKey(t *testing.T) {
	fake := &transFakeDB{affected: 3}
	n, err := NewMemoryStore(fake).PromoteKey(context.Background(), "proj-1", "testing/framework")
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("promoted = %d, want 3", n)
	}
	if !strings.Contains(fake.execs[0], "session_id = NULL") {
		t.Errorf("promote must NULL session_id:\n%s", fake.execs[0])
	}
	if _, err := NewMemoryStore(fake).PromoteKey(context.Background(), "proj-1", "  "); err == nil {
		t.Error("empty key must fail before any SQL")
	}
}

func TestTransitionsSetStatus(t *testing.T) {
	fake := &transFakeDB{affected: 1}
	if err := NewMemoryStore(fake).SetStatus(context.Background(), "mem-1", "PROPOSED", "CONFIRMED"); err != nil {
		t.Errorf("legal edge must apply: %v", err)
	}
	if len(fake.execs) != 1 {
		t.Fatalf("want 1 UPDATE, got %v", fake.execs)
	}
	// Illegal edge fails before SQL.
	if err := NewMemoryStore(fake).SetStatus(context.Background(), "mem-1", "PROPOSED", "SUPERSEDED"); err == nil {
		t.Error("skip-a-step edge must fail before SQL")
	}
	if len(fake.execs) != 1 {
		t.Errorf("illegal edge must issue no SQL (execs=%d)", len(fake.execs))
	}
	// Zero touched rows = unknown id or lost race.
	empty := &transFakeDB{affected: 0}
	if err := NewMemoryStore(empty).SetStatus(context.Background(), "ghost", "PROPOSED", "CONFIRMED"); !errors.Is(err, ErrNotFound) {
		t.Errorf("zero-row transition must wrap ErrNotFound, got %v", err)
	}
}
