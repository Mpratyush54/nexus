// Fake-backed mapping tests for the PostgresStore adapter (issue #37).
//
// All DB-free: pgFakeDB scripts canned rows behind the store.DBTX seam and
// records the SQL the adapter emits, proving each Store method maps to the
// intended statement (project/workspace delegation, raw list SELECT, memory
// INSERT/text SELECT, vector search via store.Search, episode INSERT/filter
// SELECT, lifecycle GET/UPDATE) without a live Postgres.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"central-memory/internal/store"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ---------------------------------------------------------------------------
// Scripted DBTX fake
// ---------------------------------------------------------------------------

// pgAssignOne assigns one canned column value to one Scan destination,
// covering every destination type the adapter scans.
func pgAssignOne(dst, v any) error {
	switch d := dst.(type) {
	case *string:
		s, ok := v.(string)
		if !ok {
			return errors.New("pgFake: want string")
		}
		*d = s
	case *[]string:
		if v == nil {
			*d = nil
			return nil
		}
		s, ok := v.([]string)
		if !ok {
			return errors.New("pgFake: want []string")
		}
		*d = append([]string(nil), s...)
	case *float64:
		switch n := v.(type) {
		case float64:
			*d = n
		case float32:
			*d = float64(n)
		case int:
			*d = float64(n)
		default:
			return errors.New("pgFake: want float64")
		}
	case *bool:
		b, ok := v.(bool)
		if !ok {
			return errors.New("pgFake: want bool")
		}
		*d = b
	case *time.Time:
		t, ok := v.(time.Time)
		if !ok {
			return errors.New("pgFake: want time.Time")
		}
		*d = t
	case **time.Time:
		if v == nil {
			*d = nil
			return nil
		}
		t, ok := v.(time.Time)
		if !ok {
			return errors.New("pgFake: want *time.Time")
		}
		cp := t
		*d = &cp
	default:
		return errors.New("pgFake: unsupported dest")
	}
	return nil
}

func pgAssignAll(dest, vals []any) error {
	if len(dest) != len(vals) {
		return errors.New("pgFake: arity mismatch")
	}
	for i := range dest {
		if err := pgAssignOne(dest[i], vals[i]); err != nil {
			return err
		}
	}
	return nil
}

// pgFakeRow is a one-shot pgx.Row.
type pgFakeRow struct {
	vals []any
	err  error
}

func (r *pgFakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	return pgAssignAll(dest, r.vals)
}

// pgFakeRows is a canned multi-row store.Rows.
type pgFakeRows struct {
	rows [][]any
	pos  int
	err  error
}

func (r *pgFakeRows) Next() bool { r.pos++; return r.pos <= len(r.rows) }
func (r *pgFakeRows) Err() error { return r.err }
func (r *pgFakeRows) Close()     {}
func (r *pgFakeRows) Scan(dest ...any) error {
	return pgAssignAll(dest, r.rows[r.pos-1])
}

// pgFakeDB scripts Query/QueryRow and records the last statement + args.
type pgFakeDB struct {
	onQuery    func(sql string, args []any) (store.Rows, error)
	onQueryRow func(sql string, args []any) pgx.Row
	lastSQL    string
	lastArgs   []any
}

func (f *pgFakeDB) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	f.lastSQL, f.lastArgs = sql, args
	return pgconn.CommandTag{}, nil
}

func (f *pgFakeDB) Query(_ context.Context, sql string, args ...any) (store.Rows, error) {
	f.lastSQL, f.lastArgs = sql, args
	return f.onQuery(sql, args)
}

func (f *pgFakeDB) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	f.lastSQL, f.lastArgs = sql, args
	return f.onQueryRow(sql, args)
}

func pgFixedTime() time.Time {
	return time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
}

func newPgAdapter(db *pgFakeDB) *PostgresStore {
	return NewPostgresStore(db)
}

// ---------------------------------------------------------------------------
// Adapter unit tests
// ---------------------------------------------------------------------------

func TestPostgresNewPanicsOnNil(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on nil DBTX")
		}
	}()
	NewPostgresStore(nil)
}

func TestPostgresAuthenticateUnknownUserIsUnauthorized(t *testing.T) {
	db := &pgFakeDB{
		onQueryRow: func(string, []any) pgx.Row {
			return &pgFakeRow{err: pgx.ErrNoRows}
		},
		onQuery: func(string, []any) (store.Rows, error) {
			return &pgFakeRows{}, nil
		},
	}
	_, err := newPgAdapter(db).Authenticate(context.Background(), "ghost", "pw")
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("unknown user: err = %v, want ErrUnauthorized", err)
	}
	if !strings.Contains(db.lastSQL, "FROM users") {
		t.Fatalf("auth SQL missing users lookup:\n%s", db.lastSQL)
	}
}

func TestPostgresAuthenticateKnownUserFailsClosed(t *testing.T) {
	now := pgFixedTime()
	db := &pgFakeDB{
		onQueryRow: func(string, []any) pgx.Row {
			return &pgFakeRow{vals: []any{"user-1", "alice", "", "{}", now}}
		},
		onQuery: func(string, []any) (store.Rows, error) {
			return &pgFakeRows{}, nil
		},
	}
	_, err := newPgAdapter(db).Authenticate(context.Background(), "alice", "s3cret")
	if !errors.Is(err, errAuthPending) {
		t.Fatalf("known user: err = %v, want fail-closed errAuthPending", err)
	}
}

func TestPostgresListWorkspacesRawNoStalenessFilter(t *testing.T) {
	now := pgFixedTime()
	db := &pgFakeDB{
		onQuery: func(string, []any) (store.Rows, error) {
			return &pgFakeRows{rows: [][]any{
				{"ws-1", "proj-1", "user-1", "m", "/r", "main", "abc", false, true, false, now, "", now},
				{"ws-2", "proj-1", "user-1", "m2", "/r2", "", "", false, false, false, nil, "", now},
			}}, nil
		},
		onQueryRow: func(string, []any) pgx.Row { return &pgFakeRow{err: pgx.ErrNoRows} },
	}
	got, err := newPgAdapter(db).ListWorkspaces(context.Background(), "proj-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d workspaces, want 2 (raw list, no staleness filter)", len(got))
	}
	if got[1].LastSeen != nil {
		t.Fatalf("ws-2 LastSeen = %v, want nil (never seen)", got[1].LastSeen)
	}
	if strings.Contains(db.lastSQL, "AND is_online") || strings.Contains(db.lastSQL, "WHERE is_online") {
		t.Fatalf("raw list must not pre-filter on is_online:\n%s", db.lastSQL)
	}
	if !strings.Contains(db.lastSQL, "FROM workspaces") || !strings.Contains(db.lastSQL, "project_id = $1") {
		t.Fatalf("unexpected list SQL:\n%s", db.lastSQL)
	}
}

func TestPostgresCreateMemoryMapping(t *testing.T) {
	now := pgFixedTime()
	db := &pgFakeDB{
		onQuery: func(string, []any) (store.Rows, error) {
			return &pgFakeRows{rows: [][]any{{"mem-1", now}}}, nil
		},
		onQueryRow: func(string, []any) pgx.Row { return &pgFakeRow{err: pgx.ErrNoRows} },
	}
	in := Memory{
		ProjectID: "proj-1", Key: "testing/framework",
		Content: "The team uses pytest with fixture-based setup for tests.",
		Level:   "project", Scope: "decision",
		Tags: []string{"testing"}, Confidence: 1.0,
		Status: "PROPOSED", Source: "agent:test",
	}
	got, err := newPgAdapter(db).CreateMemory(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "mem-1" || got.Key != in.Key || got.Content != in.Content {
		t.Fatalf("mapping = %+v, want fields preserved with id mem-1", got)
	}
	if got.CreatedAt != now.UTC().Format(time.RFC3339) {
		t.Fatalf("CreatedAt = %q, want RFC3339 of %v", got.CreatedAt, now)
	}
	if !strings.Contains(db.lastSQL, "INSERT INTO memory_items") || !strings.Contains(db.lastSQL, "RETURNING") {
		t.Fatalf("unexpected create SQL:\n%s", db.lastSQL)
	}
}

func TestPostgresSearchMemoryTextSQLAndMapping(t *testing.T) {
	now := pgFixedTime()
	db := &pgFakeDB{
		onQuery: func(string, []any) (store.Rows, error) {
			return &pgFakeRows{rows: [][]any{
				{"mem-1", "proj-1", "testing/framework", "pytest with fixtures", "ctx", "project", "decision", []string{"testing"}, 0.95, "CONFIRMED", "agent:test", now},
			}}, nil
		},
		onQueryRow: func(string, []any) pgx.Row { return &pgFakeRow{err: pgx.ErrNoRows} },
	}
	got, err := newPgAdapter(db).SearchMemory(context.Background(), MemoryFilter{
		ProjectID: "proj-1", Query: "100%_x", Tags: []string{"testing"},
		Key: "testing/framework", Level: "project",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "mem-1" || got[0].Status != "CONFIRMED" || got[0].Source != "agent:test" {
		t.Fatalf("mapping = %+v, want the full-fidelity text row", got)
	}
	for _, want := range []string{"ILIKE", "tags @> $", "key = $", "level = $", "LIMIT $"} {
		if !strings.Contains(db.lastSQL, want) {
			t.Errorf("text SQL missing %q:\n%s", want, db.lastSQL)
		}
	}
	// LIKE wildcards in ?q= must match literally, not as patterns.
	found := false
	for _, a := range db.lastArgs {
		if s, ok := a.(string); ok && strings.Contains(s, `100\%\_x`) {
			found = true
		}
	}
	if !found {
		t.Errorf("LIKE arg not escaped (%v)", db.lastArgs)
	}
	// Zero limit defaults to the API default (20), clamped by the same consts.
	if n := db.lastArgs[len(db.lastArgs)-1]; n != defaultSearchLimit {
		t.Errorf("LIMIT arg = %v, want default %d", n, defaultSearchLimit)
	}
}

func TestPostgresSearchMemoryVectorUsesCosineSQL(t *testing.T) {
	now := pgFixedTime()
	db := &pgFakeDB{
		// store.Search scans: id,key,content,level,scope,confidence,
		// tags,snippet,lastused,created,similarity (memory.go).
		onQuery: func(sql string, args []any) (store.Rows, error) {
			if !strings.Contains(sql, "<=>") {
				return nil, errors.New("pgFake: vector SQL must use pgvector cosine operator")
			}
			return &pgFakeRows{rows: [][]any{
				{"mem-9", "k9", "vector matched content", "project", "fact", 0.9, []string{"auth"}, "sctx", now, now, 0.92},
			}}, nil
		},
		onQueryRow: func(string, []any) pgx.Row { return &pgFakeRow{err: pgx.ErrNoRows} },
	}
	got, err := newPgAdapter(db).SearchMemoryVector(context.Background(), MemoryFilter{
		ProjectID: "proj-1", Tags: []string{"auth"}, Limit: 5,
	}, []float32{0.1, 0.2})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "mem-9" || got[0].Key != "k9" {
		t.Fatalf("vector mapping = %+v, want the ranked row", got)
	}
	if got[0].Status != store.StatusConfirmed {
		t.Errorf("vector Status = %q, want CONFIRMED (the SQL guard)", got[0].Status)
	}
	if got[0].ProjectID != "proj-1" {
		t.Errorf("vector ProjectID = %q, want backfilled proj-1", got[0].ProjectID)
	}
}

func TestPostgresSearchMemoryVectorRequiresEmbedding(t *testing.T) {
	db := &pgFakeDB{
		onQuery: func(string, []any) (store.Rows, error) { return &pgFakeRows{}, nil },
		onQueryRow: func(string, []any) pgx.Row {
			return &pgFakeRow{err: pgx.ErrNoRows}
		},
	}
	if _, err := newPgAdapter(db).SearchMemoryVector(context.Background(), MemoryFilter{}, nil); err == nil {
		t.Fatal("empty embedding: expected error, got nil")
	}
}

func TestPostgresCreateEpisodePreservesStatus(t *testing.T) {
	now := pgFixedTime()
	db := &pgFakeDB{
		onQuery: func(string, []any) (store.Rows, error) {
			return &pgFakeRows{rows: [][]any{{"ep-1", now}}}, nil
		},
		onQueryRow: func(string, []any) pgx.Row { return &pgFakeRow{err: pgx.ErrNoRows} },
	}
	in := Episode{
		ProjectID: "proj-1", Title: "Auth timeout on WebSocket upgrade",
		EpisodeType: "bug_fix", Trigger: "ConnectionTimeout",
		ErrorPatterns: []string{"ConnectionTimeout"},
		FilesInvolved: []string{"internal/server/ws.go"}, Status: "OPEN",
	}
	got, err := newPgAdapter(db).CreateEpisode(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "ep-1" || got.Status != "OPEN" || got.Title != in.Title {
		t.Fatalf("mapping = %+v, want OPEN status preserved with id ep-1", got)
	}
	if got.OpenedAt != now.UTC().Format(time.RFC3339) {
		t.Fatalf("OpenedAt = %q, want RFC3339", got.OpenedAt)
	}
	if !strings.Contains(db.lastSQL, "INSERT INTO episodes") {
		t.Fatalf("unexpected create SQL:\n%s", db.lastSQL)
	}
}

func TestPostgresSearchEpisodesFiltersAndMapping(t *testing.T) {
	now := pgFixedTime()
	db := &pgFakeDB{
		onQuery: func(string, []any) (store.Rows, error) {
			return &pgFakeRows{rows: [][]any{
				{"ep-1", "Auth timeout", "bug_fix", "ConnectionTimeout", "traced pool", "short lifetime", "increased to 30m", "load test", []string{"auth"}, []string{"internal/server/ws.go"}, []string{"ConnectionTimeout"}, "RESOLVED", now},
			}}, nil
		},
		onQueryRow: func(string, []any) pgx.Row { return &pgFakeRow{err: pgx.ErrNoRows} },
	}
	got, err := newPgAdapter(db).SearchEpisodes(context.Background(), EpisodeFilter{
		ProjectID: "proj-1", Query: "timeout", ErrorPattern: "ConnectionTimeout",
		File: "internal/server/ws.go", Status: "RESOLVED", EpisodeType: "bug_fix",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "ep-1" {
		t.Fatalf("mapping = %+v, want the episode row", got)
	}
	if got[0].ProjectID != "proj-1" {
		t.Errorf("ProjectID = %q, want backfilled proj-1", got[0].ProjectID)
	}
	for _, want := range []string{"status = $", "episode_type = $", "= ANY(error_patterns)", "= ANY(files_involved)", "ILIKE", "LIMIT $"} {
		if !strings.Contains(db.lastSQL, want) {
			t.Errorf("episode SQL missing %q:\n%s", want, db.lastSQL)
		}
	}
}

func TestPostgresLifecycleGetAndUpdate(t *testing.T) {
	now := pgFixedTime()
	row := []any{"mem-1", "proj-1", "k", "content that is long enough here", "ctx", "session", "fact", []string{}, 1.0, "PROPOSED", "", now}
	promoted := []any{"mem-1", "proj-1", "k", "content that is long enough here", "ctx", "project", "fact", []string{}, 1.0, "PROPOSED", "", now}
	db := &pgFakeDB{
		onQuery: func(sql string, _ []any) (store.Rows, error) {
			if strings.Contains(sql, "UPDATE") {
				return &pgFakeRows{rows: [][]any{promoted}}, nil
			}
			return &pgFakeRows{rows: [][]any{row}}, nil
		},
		onQueryRow: func(string, []any) pgx.Row { return &pgFakeRow{err: pgx.ErrNoRows} },
	}
	ad := newPgAdapter(db)
	got, err := ad.GetMemory(context.Background(), "mem-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "PROPOSED" || got.Level != "session" {
		t.Fatalf("get = %+v, want PROPOSED/session", got)
	}

	got.Level = store.LevelProject
	updated, err := ad.UpdateMemory(context.Background(), *got)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Level != store.LevelProject {
		t.Fatalf("update = %+v, want project level", updated)
	}
	if !strings.Contains(db.lastSQL, "UPDATE memory_items") || !strings.Contains(db.lastSQL, "session_id = CASE") {
		t.Fatalf("update SQL must NULL session_id on promote:\n%s", db.lastSQL)
	}

	empty := &pgFakeDB{
		onQuery: func(string, []any) (store.Rows, error) { return &pgFakeRows{}, nil },
		onQueryRow: func(string, []any) pgx.Row {
			return &pgFakeRow{err: pgx.ErrNoRows}
		},
	}
	if _, err := newPgAdapter(empty).GetMemory(context.Background(), "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing memory: err = %v, want ErrNotFound", err)
	}
}

// ---------------------------------------------------------------------------
// ?embedding= parsing
// ---------------------------------------------------------------------------

func TestParseEmbeddingParam(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    []float32
		wantErr bool
	}{
		{"absent", "", nil, false},
		{"pgvector literal", "[0.1,0.2,0.3]", []float32{0.1, 0.2, 0.3}, false},
		{"spaced literal", "[0.5, 0.25]", []float32{0.5, 0.25}, false},
		{"bare csv", "1,0,-2.5", []float32{1, 0, -2.5}, false},
		{"garbage", "abc", nil, true},
		{"mixed garbage", "0.1,nope", nil, true},
		{"nan rejected", "NaN", nil, true},
		{"inf rejected", "Inf", nil, true},
		{"empty brackets", "[]", nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseEmbeddingParam(c.raw)
			if c.wantErr {
				if err == nil {
					t.Fatalf("raw %q: expected error, got %v", c.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("raw %q: unexpected error %v", c.raw, err)
			}
			if len(got) != len(c.want) {
				t.Fatalf("raw %q: got %v, want %v", c.raw, got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("raw %q: got %v, want %v", c.raw, got, c.want)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Handler wiring: ?embedding= routes to VectorMemorySearcher, text is kept
// ---------------------------------------------------------------------------

// pgVectorFake implements Store (trivial stubs) plus the optional vector
// extension, recording which search path the handler chose.
type pgVectorFake struct {
	Store // embedded nil: only Authenticate/SearchMemory/SearchMemoryVector are exercised
	text  []Memory
	vec   []Memory
	used  string
}

func (f *pgVectorFake) Authenticate(_ context.Context, _, _ string) (string, error) {
	return "user-1", nil
}

func (f *pgVectorFake) SearchMemory(_ context.Context, _ MemoryFilter) ([]Memory, error) {
	f.used = "text"
	return f.text, nil
}

func (f *pgVectorFake) SearchMemoryVector(_ context.Context, _ MemoryFilter, _ []float32) ([]Memory, error) {
	f.used = "vector"
	return f.vec, nil
}

func pgHandlerServer(t *testing.T, st Store) (*Server, string) {
	t.Helper()
	srv := New(st, Options{JWTSecret: []byte("test-secret-that-is-long-enough-32B!")})
	tok, _, err := srv.mintToken("user-1", "alice")
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}
	return srv, tok
}

func pgGet(t *testing.T, srv *Server, tok, path string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func TestSearchMemoryHandlerVectorPath(t *testing.T) {
	fake := &pgVectorFake{
		text: []Memory{{ID: "t1", Key: "text-hit"}},
		vec:  []Memory{{ID: "v1", Key: "vector-hit"}},
	}
	srv, tok := pgHandlerServer(t, fake)

	status, raw := pgGet(t, srv, tok, "/memory/search?project_id=p1&embedding=[0.5,0.25]")
	if status != http.StatusOK {
		t.Fatalf("vector: status=%d body=%s", status, raw)
	}
	if fake.used != "vector" {
		t.Fatalf("used = %q, want vector path", fake.used)
	}
	var res struct {
		Items []Memory `json:"items"`
		Count int      `json:"count"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Count != 1 || res.Items[0].ID != "v1" {
		t.Fatalf("res = %s, want the vector item", raw)
	}

	status, raw = pgGet(t, srv, tok, "/memory/search?project_id=p1&q=text")
	if status != http.StatusOK {
		t.Fatalf("text: status=%d body=%s", status, raw)
	}
	if fake.used != "text" {
		t.Fatalf("used = %q, want text path preserved", fake.used)
	}
	var res2 struct {
		Items []Memory `json:"items"`
		Count int      `json:"count"`
	}
	_ = json.Unmarshal(raw, &res2)
	if res2.Count != 1 || res2.Items[0].ID != "t1" {
		t.Fatalf("res = %s, want the text item", raw)
	}
}

func TestSearchMemoryHandlerVectorUnsupportedStore400(t *testing.T) {
	// newFakeStore (server_test.go) predates the vector extension.
	srv, tok := pgHandlerServer(t, newFakeStore(time.Now))
	if status, raw := pgGet(t, srv, tok, "/memory/search?project_id=p1&embedding=[0.1]"); status != http.StatusBadRequest {
		t.Fatalf("unsupported vector: status=%d body=%s, want 400", status, raw)
	}
}

func TestSearchMemoryHandlerBadEmbedding400(t *testing.T) {
	fake := &pgVectorFake{}
	srv, tok := pgHandlerServer(t, fake)
	if status, raw := pgGet(t, srv, tok, "/memory/search?project_id=p1&embedding=nope"); status != http.StatusBadRequest {
		t.Fatalf("bad embedding: status=%d body=%s, want 400", status, raw)
	}
}
