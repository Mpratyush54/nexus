// Unit tests for workspace heartbeat staleness: threshold predicates and
// app-side filtering. All DB-free with a fixed clock.
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

func TestStore_OfflineAfterIs90s(t *testing.T) {
	if store.OfflineAfter != 90*time.Second {
		t.Fatalf("OfflineAfter = %v, want 90s (plan: offline after 90s silence)", store.OfflineAfter)
	}
	if store.OfflineAfterSeconds() != 90 {
		t.Fatalf("OfflineAfterSeconds() = %v, want 90", store.OfflineAfterSeconds())
	}
}

func TestStore_IsOnlineAt(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		lastSeen time.Time
		online   bool
	}{
		{"just seen", now, true},
		{"one beat missed", now.Add(-30 * time.Second), true},
		{"two beats missed", now.Add(-60 * time.Second), true},
		{"89s silence", now.Add(-89 * time.Second), true},
		{"exactly 90s is still online", now.Add(-90 * time.Second), true},
		{"91s silence is offline", now.Add(-91 * time.Second), false},
		{"minutes of silence", now.Add(-5 * time.Minute), false},
		{"hours of silence", now.Add(-2 * time.Hour), false},
		{"future timestamp (clock skew) is online", now.Add(60 * time.Second), true},
		{"zero time is stale", time.Time{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := store.IsOnlineAt(tc.lastSeen, now); got != tc.online {
				t.Errorf("IsOnlineAt(%v) = %v, want %v", tc.lastSeen, got, tc.online)
			}
			if got := store.IsStaleAt(tc.lastSeen, now); got == tc.online {
				t.Errorf("IsStaleAt(%v) = %v, want negation of %v", tc.lastSeen, got, tc.online)
			}
		})
	}
}

func TestStore_IsOnlineAtPtr(t *testing.T) {
	now := time.Now().UTC()
	if store.IsOnlineAtPtr(nil, now) {
		t.Error("nil last_seen must be offline")
	}
	fresh := now.Add(-10 * time.Second)
	if !store.IsOnlineAtPtr(&fresh, now) {
		t.Error("fresh last_seen must be online")
	}
	stale := now.Add(-10 * time.Minute)
	if store.IsOnlineAtPtr(&stale, now) {
		t.Error("stale last_seen must be offline")
	}
}

func TestStore_ExpiryAt(t *testing.T) {
	seen := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	if got := store.ExpiryAt(seen); !got.Equal(seen.Add(90 * time.Second)) {
		t.Fatalf("ExpiryAt = %v, want +90s", got)
	}
}

func TestStore_FilterOnline(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-10 * time.Second)
	stale := now.Add(-10 * time.Minute)
	in := []store.Workspace{
		{ID: "fresh", LastSeen: &fresh},
		{ID: "stale", LastSeen: &stale},
		{ID: "never"},
	}
	got := store.FilterOnline(in, now)
	if len(got) != 1 || got[0].ID != "fresh" {
		t.Fatalf("FilterOnline = %+v, want only [fresh]", got)
	}
	if len(in) != 3 {
		t.Fatal("FilterOnline must not mutate its input slice")
	}
	if got := store.FilterOnline(nil, now); len(got) != 0 {
		t.Fatalf("FilterOnline(nil) = %v, want empty", got)
	}
}

// ---------------------------------------------------------------------------
// Designated-processor election (issue #36). DB-free via a scripted DBTX
// fake, following the branch/agent/session fake pattern: Exec records
// statements + args, QueryRow replays a queue of canned workspace rows.
// ---------------------------------------------------------------------------

// wsElectFakeRow replays one canned scanWorkspace row: 7 strings, 3 bools,
// one *time.Time (last_seen), one string (daemon_url), one time.Time.
type wsElectFakeRow struct {
	values []any
	err    error
}

func (r wsElectFakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != len(r.values) {
		return errors.New("wsElectFakeRow: arity mismatch")
	}
	for i := range r.values {
		switch ptr := dest[i].(type) {
		case *string:
			s, ok := r.values[i].(string)
			if !ok {
				return errors.New("wsElectFakeRow: want string value")
			}
			*ptr = s
		case *bool:
			b, ok := r.values[i].(bool)
			if !ok {
				return errors.New("wsElectFakeRow: want bool value")
			}
			*ptr = b
		case **time.Time:
			if r.values[i] == nil {
				*ptr = nil
				continue
			}
			ts, ok := r.values[i].(*time.Time)
			if !ok {
				return errors.New("wsElectFakeRow: want *time.Time value")
			}
			*ptr = ts
		case *time.Time:
			ts, ok := r.values[i].(time.Time)
			if !ok {
				return errors.New("wsElectFakeRow: want time.Time value")
			}
			*ptr = ts
		default:
			return errors.New("wsElectFakeRow: unsupported dest")
		}
	}
	return nil
}

// wsElectFakeDB scripts QueryRow as a queue and Exec as a recorded no-op
// returning a canned command tag (so RowsAffected paths are testable).
type wsElectFakeDB struct {
	rowQueue []wsElectFakeRow
	execTag  pgconn.CommandTag
	execErr  error
	stmts    []string
	args     [][]any
}

func (f *wsElectFakeDB) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	f.stmts = append(f.stmts, sql)
	f.args = append(f.args, args)
	if f.execErr != nil {
		return pgconn.CommandTag{}, f.execErr
	}
	return f.execTag, nil
}

func (f *wsElectFakeDB) Query(_ context.Context, _ string, _ ...any) (store.Rows, error) {
	return nil, errors.New("wsElectFakeDB: Query not implemented")
}

func (f *wsElectFakeDB) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	f.stmts = append(f.stmts, sql)
	f.args = append(f.args, args)
	if len(f.rowQueue) == 0 {
		return wsElectFakeRow{err: pgx.ErrNoRows}
	}
	r := f.rowQueue[0]
	f.rowQueue = f.rowQueue[1:]
	return r
}

// wsElectWorkspaceRow builds a full workspaceColumns row in scan order:
// id, project, user, machine, path, branch, commit, dirty, online,
// designated, last_seen, daemon_url, created_at.
func wsElectWorkspaceRow(id, project string, seen *time.Time, online, designated bool) wsElectFakeRow {
	return wsElectFakeRow{values: []any{
		id, project, "user-1", "machine-1", "/tmp/ws",
		"main", "abc123", false, online, designated, seen, "",
		time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC),
	}}
}

func TestElectDesignatedProcessorRejectsEmptyProject(t *testing.T) {
	for _, projectID := range []string{"", "   "} {
		fake := &wsElectFakeDB{}
		s := store.NewWorkspaceStore(fake)
		if _, err := s.ElectDesignatedProcessor(context.Background(), projectID); err == nil {
			t.Errorf("project %q: expected validation error", projectID)
		}
		if len(fake.stmts) != 0 {
			t.Errorf("project %q: validation failure must not touch the DB (%d stmts)", projectID, len(fake.stmts))
		}
	}
}

func TestElectDesignatedProcessorRevokesThenGrantsFreshest(t *testing.T) {
	seen := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	fake := &wsElectFakeDB{
		rowQueue: []wsElectFakeRow{wsElectWorkspaceRow("winner", "proj-1", &seen, true, true)},
	}
	s := store.NewWorkspaceStore(fake)
	w, err := s.ElectDesignatedProcessor(context.Background(), "proj-1")
	if err != nil {
		t.Fatalf("elect: %v", err)
	}
	if w.ID != "winner" || !w.IsDesignatedProcessor || !w.IsOnline {
		t.Fatalf("elected = %+v, want winner designated+online", w)
	}
	if len(fake.stmts) != 2 {
		t.Fatalf("expected exactly 2 statements (revoke + grant), got %d: %q", len(fake.stmts), fake.stmts)
	}
	revoke, grant := fake.stmts[0], fake.stmts[1]
	if !strings.Contains(revoke, "is_designated_processor = false") || !strings.Contains(revoke, "project_id = $1") {
		t.Errorf("revoke step must clear project flags, got: %s", revoke)
	}
	if strings.Contains(revoke, "RETURNING") {
		t.Errorf("revoke step must not return rows, got: %s", revoke)
	}
	for _, want := range []string{
		"is_designated_processor = true",
		"ORDER BY last_seen DESC", // heartbeat preference: freshest wins
		"make_interval",
		"is_online",
		"RETURNING",
	} {
		if !strings.Contains(grant, want) {
			t.Errorf("grant step must contain %q, got: %s", want, grant)
		}
	}
	if len(fake.args) != 2 || len(fake.args[0]) != 1 || fake.args[0][0] != "proj-1" {
		t.Errorf("revoke args = %v, want [proj-1]", fake.args)
	}
	if len(fake.args[1]) != 2 || fake.args[1][0] != "proj-1" || fake.args[1][1] != store.OfflineAfterSeconds() {
		t.Errorf("grant args = %v, want [proj-1 %v]", fake.args[1], store.OfflineAfterSeconds())
	}
}

func TestElectDesignatedProcessorNoOnlineCandidateIsNotFound(t *testing.T) {
	fake := &wsElectFakeDB{} // empty queue: grant matches nothing
	s := store.NewWorkspaceStore(fake)
	if _, err := s.ElectDesignatedProcessor(context.Background(), "proj-1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("no online workspace: want ErrNotFound, got %v", err)
	}
	if len(fake.stmts) != 2 {
		t.Fatalf("revoke must still run before the empty grant (%d stmts)", len(fake.stmts))
	}
}

func TestElectDesignatedProcessorPropagatesRevokeError(t *testing.T) {
	seen := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	fake := &wsElectFakeDB{
		rowQueue: []wsElectFakeRow{wsElectWorkspaceRow("winner", "proj-1", &seen, true, true)},
		execErr:  errors.New("boom"),
	}
	s := store.NewWorkspaceStore(fake)
	if _, err := s.ElectDesignatedProcessor(context.Background(), "proj-1"); err == nil ||
		!strings.Contains(err.Error(), "revoke") {
		t.Fatalf("want revoke error, got %v", err)
	}
	if len(fake.rowQueue) != 1 {
		t.Fatal("revoke failure must not reach the grant step")
	}
}

func TestDesignatedProcessorReassignStaleSweepsOnlySilent(t *testing.T) {
	fake := &wsElectFakeDB{execTag: pgconn.NewCommandTag("UPDATE 2")}
	s := store.NewWorkspaceStore(fake)
	n, err := s.ReassignStaleDesignated(context.Background())
	if err != nil {
		t.Fatalf("reassign: %v", err)
	}
	if n != 2 {
		t.Fatalf("swept = %d, want RowsAffected 2", n)
	}
	if len(fake.stmts) != 1 {
		t.Fatalf("expected 1 sweep statement, got %q", fake.stmts)
	}
	stmt := fake.stmts[0]
	for _, want := range []string{
		"is_online = false",
		"is_designated_processor = false",
		"is_designated_processor", // sweep touches only designatees
		"last_seen IS NULL",       // never-seen counts as stale
		"last_seen < now()",       // strict <: the 90s boundary stays online
		"make_interval",
	} {
		if !strings.Contains(stmt, want) {
			t.Errorf("sweep must contain %q, got: %s", want, stmt)
		}
	}
	if len(fake.args) != 1 || len(fake.args[0]) != 1 || fake.args[0][0] != store.OfflineAfterSeconds() {
		t.Errorf("sweep args = %v, want [%v]", fake.args, store.OfflineAfterSeconds())
	}
}

func TestDesignatedProcessorReassignPropagatesExecError(t *testing.T) {
	fake := &wsElectFakeDB{execErr: errors.New("boom")}
	s := store.NewWorkspaceStore(fake)
	if n, err := s.ReassignStaleDesignated(context.Background()); err == nil || n != 0 {
		t.Fatalf("want (0, error), got (%d, %v)", n, err)
	}
}

func TestWorkspaceElectionBoundaryStaysAlignedWithPredicates(t *testing.T) {
	// The sweep's strict `<` must match IsStaleAt exactly: silence == 90s is
	// still online on both sides (same contract as MarkStaleOffline).
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	if store.IsStaleAt(now.Add(-90*time.Second), now) {
		t.Fatal("exactly 90s silence must not be stale (IsStaleAt)")
	}
	fake := &wsElectFakeDB{}
	s := store.NewWorkspaceStore(fake)
	if _, err := s.ReassignStaleDesignated(context.Background()); err != nil {
		t.Fatal(err)
	}
	stmt := fake.stmts[0]
	if strings.Contains(stmt, "<=") {
		t.Errorf("sweep must use strict < (boundary-aligned), got: %s", stmt)
	}
}
