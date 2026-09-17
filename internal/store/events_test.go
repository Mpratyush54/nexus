// Unit + live tests for the event store (issue #9, plan §§2.1-2.2).
//
// DB-free by design except the two TestEventLive* tests, which are gated on
// TEST_POSTGRES_DSN like the issue-#2 integration tests. `go test
// ./internal/store/ -run TestEvent` runs everything here: units always, live
// tests skip without a DSN.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ---------------------------------------------------------------------------
// Fakes (event-scoped names; memory_test.go owns fakeRows/fakeQuerier).
// ---------------------------------------------------------------------------

// fakeEventRow scripts one pgx.Row.
type fakeEventRow struct {
	values []any
	err    error
}

func (r *fakeEventRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != len(r.values) {
		return errors.New("fakeEventRow: arity mismatch")
	}
	for i := range dest {
		switch ptr := dest[i].(type) {
		case *int64:
			*ptr = r.values[i].(int64)
		case *string:
			*ptr = r.values[i].(string)
		case *time.Time:
			*ptr = r.values[i].(time.Time)
		default:
			return errors.New("fakeEventRow: unsupported dest")
		}
	}
	return nil
}

// fakeEventRows scripts a multi-row result set.
type fakeEventRows struct {
	cols [][]any
	pos  int
}

func (f *fakeEventRows) Next() bool { f.pos++; return f.pos <= len(f.cols) }
func (f *fakeEventRows) Err() error { return nil }
func (f *fakeEventRows) Close()     {}
func (f *fakeEventRows) Scan(dest ...any) error {
	return (&fakeEventRow{values: f.cols[f.pos-1]}).Scan(dest...)
}

// fakeEventDBTX scripts a DBTX: queryRow for AppendEvent/GetEventByID,
// queryRows + queryErr for ListEvents. Calls are counted under mutex so the
// concurrency test can prove AppendEvent holds no shared mutable state.
type fakeEventDBTX struct {
	mu        sync.Mutex
	execs     int
	queries   int
	queryRows int
	row       *fakeEventRow
	rows      *fakeEventRows
	queryErr  error
	gotSQL    []string
	gotArgs   [][]any
}

func (f *fakeEventDBTX) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.execs++
	f.gotSQL = append(f.gotSQL, sql)
	f.gotArgs = append(f.gotArgs, args)
	return pgconn.CommandTag{}, nil
}

func (f *fakeEventDBTX) Query(_ context.Context, sql string, args ...any) (Rows, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries++
	f.gotSQL = append(f.gotSQL, sql)
	f.gotArgs = append(f.gotArgs, args)
	if f.queryErr != nil {
		return nil, f.queryErr
	}
	return f.rows, nil
}

func (f *fakeEventDBTX) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queryRows++
	f.gotSQL = append(f.gotSQL, sql)
	f.gotArgs = append(f.gotArgs, args)
	if f.row != nil {
		return f.row
	}
	return &fakeEventRow{err: pgx.ErrNoRows}
}

func (f *fakeEventDBTX) counts() (execs, queries, queryRows int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.execs, f.queries, f.queryRows
}

func eventRowValues(id int64, project, etype, payload string, ts time.Time) []any {
	return []any{id, project, "", "", "", "", "", etype, payload, ts}
}

// ---------------------------------------------------------------------------
// Constants registry.
// ---------------------------------------------------------------------------

func TestEventTypeConstantsComplete(t *testing.T) {
	want := []string{
		// Layer 1 — tool interception (5).
		EventFileRead, EventFileModified, EventCommandExecuted,
		EventGitCommitted, EventGitDiffViewed,
		// Layer 2 — transcript harvesting (2).
		EventConversationTurn, EventSessionTranscriptComplete,
		// Layer 3 — file watcher (1).
		EventInstructionFileChanged,
		// Layer 4 — explicit memory actions (4).
		EventMemoryProposed, EventMemoryConfirmed,
		EventMemoryRejected, EventMemorySuperseded,
		// Tasks (3).
		EventTaskCreated, EventTaskUpdated, EventTaskCompleted,
		// Episodes (3).
		EventEpisodeOpened, EventEpisodeUpdated, EventEpisodeResolved,
		// Lifecycle (5).
		EventMessageSent, EventSessionStarted, EventSessionEnded,
		EventWorkspaceRegistered, EventWorkspaceOffline,
		// Steering — live agent control, issue #42 (4).
		EventAgentInterruptRequested, EventAgentSteerPrompt,
		EventAgentPaused, EventAgentResumed,
	}
	if len(want) != 27 {
		t.Fatalf("expected 27 §2.1 event types, listed %d", len(want))
	}
	seen := map[string]struct{}{}
	for _, typ := range want {
		if typ == "" {
			t.Fatal("event type constant must not be empty")
		}
		if _, dup := seen[typ]; dup {
			t.Fatalf("duplicate event type %q", typ)
		}
		seen[typ] = struct{}{}
		if !IsValidEventType(typ) {
			t.Errorf("IsValidEventType(%q) = false, want true", typ)
		}
	}
	if len(ValidEventTypes) != len(want) {
		t.Errorf("ValidEventTypes has %d entries, want %d (registry drift)", len(ValidEventTypes), len(want))
	}
	for _, bad := range []string{"", "file_read", "FILE-READ", "UNKNOWN", " MEMORY_PROPOSED "} {
		if IsValidEventType(bad) {
			t.Errorf("IsValidEventType(%q) = true, want false", bad)
		}
	}
}

// ---------------------------------------------------------------------------
// Payload marshal.
// ---------------------------------------------------------------------------

func TestEventMarshalPayload(t *testing.T) {
	if got, err := MarshalEventPayload(nil); err != nil || string(got) != "{}" {
		t.Errorf("nil = %q, %v; want {}, nil", got, err)
	}
	if got, err := MarshalEventPayload(map[string]any{"path": "a.go", "n": 1}); err != nil {
		t.Fatal(err)
	} else {
		var m map[string]any
		if err := json.Unmarshal(got, &m); err != nil || m["path"] != "a.go" {
			t.Errorf("map round-trip failed: %q, %v", got, err)
		}
	}
	if got, err := MarshalEventPayload(json.RawMessage(`{"a":1}`)); err != nil || string(got) != `{"a":1}` {
		t.Errorf("RawMessage passthrough = %q, %v", got, err)
	}
	if got, err := MarshalEventPayload([]byte(`[1,2]`)); err != nil || string(got) != `[1,2]` {
		t.Errorf("bytes passthrough = %q, %v", got, err)
	}
	if _, err := MarshalEventPayload([]byte(`{broken`)); err == nil {
		t.Error("expected error for invalid JSON bytes")
	}
	if _, err := MarshalEventPayload(json.RawMessage(`nope`)); err == nil {
		t.Error("expected error for invalid RawMessage")
	}
	if _, err := MarshalEventPayload(func() {}); err == nil {
		t.Error("expected error for unmarshalable value")
	}
	if got, err := MarshalEventPayload("hi"); err != nil || string(got) != `"hi"` {
		t.Errorf("string must be JSON-quoted, got %q, %v", got, err)
	}
}

// ---------------------------------------------------------------------------
// LISTEN channel parse (pure).
// ---------------------------------------------------------------------------

func TestEventParseNotification(t *testing.T) {
	n, err := ParseEventNotification(`{"id":42,"project_id":"p1","event_type":"FILE_READ"}`)
	if err != nil {
		t.Fatal(err)
	}
	if n.ID != 42 || n.ProjectID != "p1" || n.EventType != "FILE_READ" {
		t.Errorf("wrong parse: %+v", n)
	}
	for name, payload := range map[string]string{
		"malformed":   `{oops`,
		"empty":       ``,
		"missing id":  `{"project_id":"p","event_type":"FILE_READ"}`,
		"zero id":     `{"id":0,"project_id":"p","event_type":"FILE_READ"}`,
		"missing prj": `{"id":1,"event_type":"FILE_READ"}`,
		"missing typ": `{"id":1,"project_id":"p"}`,
		"wrong kind":  `[1,2]`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseEventNotification(payload); err == nil {
				t.Errorf("expected error for %q", payload)
			}
		})
	}
	// Extra trigger fields are ignored (forward-compatible).
	if _, err := ParseEventNotification(`{"id":7,"project_id":"p","event_type":"GIT_COMMITTED","extra":1}`); err != nil {
		t.Errorf("extra fields must be ignored: %v", err)
	}
}

func TestEventDeliverFilter(t *testing.T) {
	n := EventNotification{ID: 1, ProjectID: "p1", EventType: EventFileRead}
	if !deliverNotification(n, "") {
		t.Error("empty filter must deliver everything")
	}
	if !deliverNotification(n, "p1") {
		t.Error("matching project must be delivered")
	}
	if deliverNotification(n, "p2") {
		t.Error("other project must be filtered out")
	}
}

// ---------------------------------------------------------------------------
// Replay SQL builder.
// ---------------------------------------------------------------------------

func TestEventBuildListSQLBare(t *testing.T) {
	sql, args := BuildListEventsSQL(EventFilter{})
	for _, want := range []string{
		"FROM events",
		"ORDER BY id ASC", // replay order, never created_at (ties + clock skew)
		"LIMIT $1",
		"OFFSET $2",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("SQL missing %q:\n%s", want, sql)
		}
	}
	if strings.Contains(sql, "WHERE") {
		t.Errorf("bare filter must have no WHERE:\n%s", sql)
	}
	if len(args) != 2 || args[0] != DefaultEventLimit || args[1] != 0 {
		t.Errorf("bare args = %v, want [%d 0]", args, DefaultEventLimit)
	}
}

func TestEventBuildListSQLFilters(t *testing.T) {
	since := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)
	sql, args := BuildListEventsSQL(EventFilter{
		ProjectID:  "p1",
		SessionID:  "s1",
		EpisodeID:  "e1",
		EventTypes: []string{EventFileRead, EventGitCommitted},
		Since:      &since,
		Until:      &until,
		Limit:      10,
		Offset:     20,
	})
	for _, want := range []string{
		"project_id = $1", "session_id = $2", "episode_id = $3",
		"event_type = ANY($4)", "created_at >= $5", "created_at <= $6",
		"ORDER BY id ASC", "LIMIT $7", "OFFSET $8",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("SQL missing %q:\n%s", want, sql)
		}
	}
	if len(args) != 8 {
		t.Fatalf("expected 8 args, got %d (%v)", len(args), args)
	}
	if args[0] != "p1" || args[6] != 10 || args[7] != 20 {
		t.Errorf("scalar args wrong: %v", args)
	}
	types, ok := args[3].([]string)
	if !ok || len(types) != 2 || types[0] != EventFileRead {
		t.Errorf("event types arg = %v (%T), want [FILE_READ GIT_COMMITTED]", args[3], args[3])
	}
}

func TestEventLimitClamp(t *testing.T) {
	if (EventFilter{}).LimitOrDefault() != DefaultEventLimit {
		t.Error("zero limit should default")
	}
	if (EventFilter{Limit: -5}).LimitOrDefault() != DefaultEventLimit {
		t.Error("negative limit should default")
	}
	if (EventFilter{Limit: 100000}).LimitOrDefault() != MaxEventLimit {
		t.Error("huge limit should cap")
	}
	if (EventFilter{Limit: 7}).LimitOrDefault() != 7 {
		t.Error("in-range limit must pass through")
	}
}

// ---------------------------------------------------------------------------
// Append validation + concurrency (DB-free).
// ---------------------------------------------------------------------------

func TestEventAppendValidation(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	fake := &fakeEventDBTX{row: &fakeEventRow{values: eventRowValues(9, "p1", EventFileRead, `{"path":"a.go"}`, now)}}
	es := NewEventStore(fake)

	for name, params := range map[string]AppendEventParams{
		"missing project": {EventType: EventFileRead, Payload: map[string]string{"a": "b"}},
		"blank project":   {ProjectID: "  ", EventType: EventFileRead},
		"missing type":    {ProjectID: "p1"},
		"unknown type":    {ProjectID: "p1", EventType: "FILE_DELETED"},
		"lowercase type":  {ProjectID: "p1", EventType: "file_read"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := es.AppendEvent(context.Background(), params); err == nil {
				t.Error("expected validation error, got nil")
			}
		})
	}
	if _, _, n := fake.counts(); n != 0 {
		t.Errorf("validation failures must not touch the DB (%d QueryRow calls)", n)
	}

	got, err := es.AppendEvent(context.Background(), AppendEventParams{
		ProjectID: "p1", EventType: EventFileRead,
		Payload: map[string]string{"path": "a.go"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != 9 || got.ProjectID != "p1" || got.EventType != EventFileRead {
		t.Errorf("wrong append result: %+v", got)
	}
	var payload map[string]string
	if err := json.Unmarshal(got.Payload, &payload); err != nil || payload["path"] != "a.go" {
		t.Errorf("payload round-trip failed: %q, %v", got.Payload, err)
	}
	if !strings.Contains(fake.gotSQL[0], "INSERT INTO events") || !strings.Contains(fake.gotSQL[0], "$8::JSONB") {
		t.Errorf("append SQL wrong:\n%s", fake.gotSQL[0])
	}
}

func TestEventAppendConcurrentSafe(t *testing.T) {
	now := time.Now().UTC()
	fake := &fakeEventDBTX{row: &fakeEventRow{values: eventRowValues(1, "p", EventMessageSent, `{}`, now)}}
	es := NewEventStore(fake)
	const goroutines, perG = 8, 25
	var wg sync.WaitGroup
	errs := make(chan error, goroutines*perG)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				_, err := es.AppendEvent(context.Background(), AppendEventParams{
					ProjectID: "p", EventType: EventMessageSent,
				})
				if err != nil {
					errs <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if _, _, n := fake.counts(); n != goroutines*perG {
		t.Errorf("got %d appends, want %d (call lost under concurrency)", n, goroutines*perG)
	}
}

// ---------------------------------------------------------------------------
// Replay via fakes.
// ---------------------------------------------------------------------------

func TestEventListReplay(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	fake := &fakeEventDBTX{rows: &fakeEventRows{cols: [][]any{
		eventRowValues(1, "p1", EventFileRead, `{"path":"a"}`, now),
		eventRowValues(2, "p1", EventGitCommitted, `{"sha":"abc"}`, now),
	}}}
	es := NewEventStore(fake)
	out, err := es.ListEvents(context.Background(), EventFilter{ProjectID: "p1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0].ID != 1 || out[1].ID != 2 {
		t.Fatalf("replay order/content wrong: %+v", out)
	}
	if out[1].EventType != EventGitCommitted || string(out[0].Payload) != `{"path":"a"}` {
		t.Errorf("row fields wrong: %+v", out)
	}
	if !strings.Contains(fake.gotSQL[0], "ORDER BY id ASC") {
		t.Errorf("replay must order by id:\n%s", fake.gotSQL[0])
	}

	fakeErr := &fakeEventDBTX{queryErr: errors.New("boom")}
	if _, err := NewEventStore(fakeErr).ListEvents(context.Background(), EventFilter{}); err == nil {
		t.Error("expected query error, got nil")
	}
}

func TestEventGetByIDNotFound(t *testing.T) {
	fake := &fakeEventDBTX{row: &fakeEventRow{err: pgx.ErrNoRows}}
	if _, err := NewEventStore(fake).GetEventByID(context.Background(), 123); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected wrapped ErrNotFound, got %v", err)
	}
}

func TestEventSubscribeRequiresPool(t *testing.T) {
	if _, err := Subscribe(context.Background(), nil, ""); err == nil {
		t.Error("expected error for nil pool, got nil")
	}
}

// ---------------------------------------------------------------------------
// Live Postgres tests (gated on TEST_POSTGRES_DSN).
// ---------------------------------------------------------------------------

func eventLiveDB(t *testing.T) (*DB, *pgxpool.Pool, context.Context) {
	t.Helper()
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not set; skipping live event-store test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	db, err := Connect(ctx, DefaultConfig(dsn))
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(db.Close)
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open subscribe pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return db, pool, ctx
}

// eventEnsureSchema applies each migration file individually, tolerating
// "already exists" per file so the test is re-runnable whether the database
// is fresh, migrated to 001, or fully migrated.
func eventEnsureSchema(t *testing.T, ctx context.Context, db *DB) {
	t.Helper()
	files, err := ListMigrationFiles("../../migrations")
	if err != nil {
		t.Fatalf("list migrations: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no migration files found")
	}
	for _, f := range files {
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read migration %s: %v", f, err)
		}
		if _, err := db.Exec(ctx, string(body)); err != nil &&
			!strings.Contains(err.Error(), "already exists") {
			t.Fatalf("apply migration %s: %v", f, err)
		}
	}
}

func eventLiveProject(t *testing.T, ctx context.Context, db *DB) string {
	t.Helper()
	var id string
	suffix := strings.ReplaceAll(t.Name(), "/", "_") + "_proj"
	if err := db.QueryRow(ctx,
		`INSERT INTO projects (folder_name) VALUES ($1) RETURNING id::TEXT`,
		suffix).Scan(&id); err != nil {
		t.Fatalf("insert test project: %v", err)
	}
	return id
}

func TestEventLiveAppendListReplay(t *testing.T) {
	db, _, ctx := eventLiveDB(t)
	eventEnsureSchema(t, ctx, db)
	es := NewEventStore(db)
	projectID := eventLiveProject(t, ctx, db)

	appended := []struct {
		typ     string
		payload any
	}{
		{EventFileRead, map[string]any{"path": "main.go"}},
		{EventGitCommitted, map[string]any{"sha": "abc", "message": "fix"}},
		{EventConversationTurn, nil}, // nil payload must store as {}
	}
	var ids []int64
	for _, a := range appended {
		e, err := es.AppendEvent(ctx, AppendEventParams{
			ProjectID: projectID, EventType: a.typ, Payload: a.payload,
		})
		if err != nil {
			t.Fatalf("append %s: %v", a.typ, err)
		}
		if e.ID <= 0 || e.ProjectID != projectID || e.EventType != a.typ {
			t.Fatalf("bad stored event: %+v", e)
		}
		if !json.Valid(e.Payload) {
			t.Fatalf("stored payload invalid JSON: %q", e.Payload)
		}
		ids = append(ids, e.ID)
	}
	if !(ids[0] < ids[1] && ids[1] < ids[2]) {
		t.Fatalf("ids not append-ordered: %v", ids)
	}

	all, err := es.ListEvents(ctx, EventFilter{ProjectID: projectID})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(all) < 3 {
		t.Fatalf("expected >=3 replayed events, got %d", len(all))
	}
	for i := 1; i < len(all); i++ {
		if all[i].ID <= all[i-1].ID {
			t.Fatalf("replay not id-ordered: %+v", all)
		}
	}

	typed, err := es.ListEvents(ctx, EventFilter{
		ProjectID: projectID, EventTypes: []string{EventGitCommitted},
	})
	if err != nil {
		t.Fatalf("filtered replay: %v", err)
	}
	if len(typed) != 1 || typed[0].EventType != EventGitCommitted {
		t.Fatalf("type filter wrong: %+v", typed)
	}

	got, err := es.GetEventByID(ctx, ids[0])
	if err != nil {
		t.Fatalf("get by id: %v", err)
	}
	if got.ID != ids[0] || string(got.Payload) != `{"path": "main.go"}` {
		t.Fatalf("hydration mismatch: %+v", got)
	}
}

func TestEventLiveSubscribeNotify(t *testing.T) {
	db, pool, ctx := eventLiveDB(t)
	eventEnsureSchema(t, ctx, db)
	es := NewEventStore(db)
	projectID := eventLiveProject(t, ctx, db)
	otherID := eventLiveProject(t, ctx, db)

	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	ch, err := Subscribe(subCtx, pool, projectID)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	// An event for another project must be filtered client-side: it is
	// appended first, so if filtering broke, it would arrive first.
	if _, err := es.AppendEvent(ctx, AppendEventParams{
		ProjectID: otherID, EventType: EventFileRead,
	}); err != nil {
		t.Fatalf("append other-project event: %v", err)
	}
	sent, err := es.AppendEvent(ctx, AppendEventParams{
		ProjectID: projectID, EventType: EventGitCommitted,
		Payload: map[string]string{"sha": "deadbee"},
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	select {
	case n := <-ch:
		if n.ID != sent.ID || n.ProjectID != projectID || n.EventType != EventGitCommitted {
			t.Fatalf("wrong notification: %+v (want id=%d project=%s)", n, sent.ID, projectID)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for pg_notify")
	}

	cancel()
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("expected channel close after cancel (drain then close)")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("subscription channel did not close after cancel")
	}
}
