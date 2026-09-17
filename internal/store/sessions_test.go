// Unit tests for sessions (issue #12, plan §§3.1, 3.3, 2.7).
//
// DB-free by design except through a scripted DBTX fake: role validation,
// join/leave validation, session isolation predicates, and the promotion
// counter run with no database at all. Store-method SQL paths (Create,
// Join, Leave, ListActive, ListParticipants, End, CountKeySessionsDB) run
// against sessionFakeDB, which records statements and replays canned rows.
// Live-Postgres coverage lives in sessions_integration_test.go (gated on
// TEST_POSTGRES_DSN).
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

// sessionFakeRow is a pgx.Row replaying one canned Scan.
type sessionFakeRow struct {
	values []any
	err    error
}

func (r sessionFakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != len(r.values) {
		return errors.New("sessionFakeRow: arity mismatch")
	}
	for i := range r.values {
		switch ptr := dest[i].(type) {
		case *string:
			*ptr = r.values[i].(string)
		case *bool:
			*ptr = r.values[i].(bool)
		case *int:
			*ptr = r.values[i].(int)
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
			return errors.New("sessionFakeRow: unsupported dest")
		}
	}
	return nil
}

// sessionFakeRows is a store.Rows replaying canned rows.
type sessionFakeRows struct {
	rows [][]any
	pos  int
}

func (f *sessionFakeRows) Next() bool { f.pos++; return f.pos <= len(f.rows) }
func (f *sessionFakeRows) Err() error { return nil }
func (f *sessionFakeRows) Close()     {}
func (f *sessionFakeRows) Scan(dest ...any) error {
	return sessionFakeRow{values: f.rows[f.pos-1]}.Scan(dest...)
}

// sessionFakeDB scripts QueryRow as a queue and Query as one result set,
// recording every statement for SQL assertions.
type sessionFakeDB struct {
	rowQueue []sessionFakeRow
	rows     *sessionFakeRows
	queryErr error
	queries  []string
	execErr  error
	execs    int
}

func (f *sessionFakeDB) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	f.execs++
	if f.execErr != nil {
		return pgconn.CommandTag{}, f.execErr
	}
	return pgconn.CommandTag{}, nil
}

func (f *sessionFakeDB) Query(_ context.Context, sql string, _ ...any) (store.Rows, error) {
	f.queries = append(f.queries, sql)
	if f.queryErr != nil {
		return nil, f.queryErr
	}
	return f.rows, nil
}

func (f *sessionFakeDB) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	f.queries = append(f.queries, sql)
	if len(f.rowQueue) == 0 {
		return sessionFakeRow{err: pgx.ErrNoRows}
	}
	r := f.rowQueue[0]
	f.rowQueue = f.rowQueue[1:]
	return r
}

func sessionRow(id, project, title, createdBy string, active bool, endedAt any) sessionFakeRow {
	return sessionFakeRow{values: []any{
		id, project, title, createdBy, active,
		time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC), endedAt,
	}}
}

func participantRow(id, sessionID, userID, agentID, role string, leftAt any) sessionFakeRow {
	return sessionFakeRow{values: []any{
		id, sessionID, userID, agentID, role,
		time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC), leftAt,
	}}
}

// ---------------------------------------------------------------------------
// Roles
// ---------------------------------------------------------------------------

func TestSessionRoleNormalize(t *testing.T) {
	if got, ok := store.NormalizeRole(""); !ok || got != store.RoleMember {
		t.Errorf("empty role = %q,%v; want MEMBER,true", got, ok)
	}
	if got, ok := store.NormalizeRole("owner"); !ok || got != store.RoleOwner {
		t.Errorf("owner = %q,%v; want OWNER,true", got, ok)
	}
	if got, ok := store.NormalizeRole("  observer "); !ok || got != store.RoleObserver {
		t.Errorf("observer = %q,%v; want OBSERVER,true", got, ok)
	}
	if _, ok := store.NormalizeRole("ADMIN"); ok {
		t.Error("ADMIN must be rejected")
	}
	if !store.IsValidRole(store.RoleOwner) || !store.IsValidRole(store.RoleMember) ||
		!store.IsValidRole(store.RoleObserver) || store.IsValidRole("") {
		t.Error("IsValidRole must accept exactly OWNER/MEMBER/OBSERVER")
	}
}

func TestSessionJoinParamsValidate(t *testing.T) {
	if err := (store.JoinParams{}).Validate(); err == nil {
		t.Error("join with neither user nor agent must fail")
	}
	if err := (store.JoinParams{UserID: "u1", Role: "ADMIN"}).Validate(); err == nil {
		t.Error("join with unknown role must fail")
	}
	for _, p := range []store.JoinParams{
		{UserID: "u1"},
		{AgentID: "a1", Role: "OBSERVER"},
		{UserID: "u1", AgentID: "a1", Role: "owner"},
	} {
		if err := p.Validate(); err != nil {
			t.Errorf("join %+v must validate: %v", p, err)
		}
	}
}

// ---------------------------------------------------------------------------
// Isolation (plan §3.3)
// ---------------------------------------------------------------------------

func TestSessionVisibilityIsolation(t *testing.T) {
	sessA := store.MemoryItem{Key: "scope/do_not_touch", Level: store.LevelSession, SessionID: "sess-a"}
	if !store.IsVisibleToSession(sessA, "sess-a") {
		t.Error("own session-scoped item must be visible in its session")
	}
	if store.IsVisibleToSession(sessA, "sess-b") {
		t.Error("session-scoped item must be invisible in a sibling session until promoted")
	}
	project := store.MemoryItem{Key: "testing/framework", Level: store.LevelProject}
	if !store.IsVisibleToSession(project, "sess-a") || !store.IsVisibleToSession(project, "sess-b") {
		t.Error("project-scoped item must be visible in every session (inheritance)")
	}
	personal := store.MemoryItem{Key: "style/errors", Level: store.LevelPersonal, UserID: "alice"}
	if !store.IsVisibleToSession(personal, "sess-b") {
		t.Error("non-session-scoped item must pass the session boundary (user filtering is caller-side)")
	}
	if store.IsSessionScoped(project) || !store.IsSessionScoped(sessA) {
		t.Error("IsSessionScoped must track session_id presence")
	}
}

func TestSessionFilterVisiblePreservesOrder(t *testing.T) {
	items := []store.MemoryItem{
		{Key: "a", Level: store.LevelProject},
		{Key: "b", Level: store.LevelSession, SessionID: "other"},
		{Key: "c", Level: store.LevelSession, SessionID: "mine"},
	}
	got := store.FilterVisibleToSession(items, "mine")
	if len(got) != 2 || got[0].Key != "a" || got[1].Key != "c" {
		t.Fatalf("FilterVisibleToSession = %v, want [a c]", got)
	}
}

// ---------------------------------------------------------------------------
// Promotion counter (plan §2.7: same key in 3+ sessions)
// ---------------------------------------------------------------------------

func TestSessionCountKeySessions(t *testing.T) {
	items := []store.MemoryItem{
		{Key: "k", Level: store.LevelSession, SessionID: "s1"},
		{Key: "k", Level: store.LevelSession, SessionID: "s1"}, // same session twice: one vote
		{Key: "k", Level: store.LevelSession, SessionID: "s2"},
		{Key: "k", Level: store.LevelProject}, // promoted copy: no vote
		{Key: "other", Level: store.LevelSession, SessionID: "s3"},
	}
	if got := store.CountKeySessions(items, "k"); got != 2 {
		t.Errorf("CountKeySessions(k) = %d, want 2", got)
	}
	if got := store.CountKeySessions(items, "other"); got != 1 {
		t.Errorf("CountKeySessions(other) = %d, want 1", got)
	}
	if got := store.CountKeySessions(items, "missing"); got != 0 {
		t.Errorf("CountKeySessions(missing) = %d, want 0", got)
	}
}

func TestSessionShouldProposePromotion(t *testing.T) {
	if store.ShouldProposePromotion(2) {
		t.Error("2 sessions must not propose promotion")
	}
	if !store.ShouldProposePromotion(store.PromotionThreshold) {
		t.Errorf("threshold (%d) must propose promotion", store.PromotionThreshold)
	}
	if store.PromotionThreshold != 3 {
		t.Errorf("PromotionThreshold = %d, want 3 (plan §2.7)", store.PromotionThreshold)
	}
	if !store.ShouldProposePromotion(9) {
		t.Error("9 sessions must propose promotion")
	}
}

func TestSessionPromotionCandidates(t *testing.T) {
	items := []store.MemoryItem{
		{Key: "zeta", Level: store.LevelSession, SessionID: "s1"},
		{Key: "zeta", Level: store.LevelSession, SessionID: "s2"},
		{Key: "zeta", Level: store.LevelSession, SessionID: "s3"},
		{Key: "alpha", Level: store.LevelSession, SessionID: "s1"},
		{Key: "alpha", Level: store.LevelSession, SessionID: "s2"},
		{Key: "alpha", Level: store.LevelSession, SessionID: "s3"},
		{Key: "alpha", Level: store.LevelSession, SessionID: "s4"},
		{Key: "thin", Level: store.LevelSession, SessionID: "s1"},
		{Key: "thin", Level: store.LevelSession, SessionID: "s2"},
		{Key: "shared", Level: store.LevelProject},
	}
	got := store.PromotionCandidates(items, 0) // 0 = default threshold
	if len(got) != 2 {
		t.Fatalf("candidates = %v, want [alpha zeta]", got)
	}
	if got[0].Key != "alpha" || got[1].Key != "zeta" {
		t.Fatalf("candidates not sorted by key: %v", got)
	}
	if got[1].SessionCount != 3 || len(got[1].SessionIDs) != 3 ||
		got[1].SessionIDs[0] != "s1" || got[1].SessionIDs[2] != "s3" {
		t.Errorf("zeta proposal wrong: %+v", got[1])
	}
	if got := store.PromotionCandidates(items, 4); len(got) != 1 || got[0].Key != "alpha" {
		t.Errorf("threshold 4 must yield only alpha, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// Store-method SQL paths (scripted DBTX)
// ---------------------------------------------------------------------------

func TestSessionCreateValidation(t *testing.T) {
	s := store.NewSessionStore(&sessionFakeDB{})
	if _, err := s.Create(context.Background(), store.SessionParams{CreatedBy: "u"}); err == nil {
		t.Error("create without project must fail")
	}
	if _, err := s.Create(context.Background(), store.SessionParams{ProjectID: "p"}); err == nil {
		t.Error("create without creator must fail")
	}
}

func TestSessionCreateSeatsOwner(t *testing.T) {
	fake := &sessionFakeDB{rowQueue: []sessionFakeRow{
		sessionRow("sess-1", "proj-1", "Auth refactor", "user-1", true, nil),
		participantRow("part-1", "sess-1", "user-1", "", "OWNER", nil),
	}}
	sess, err := store.NewSessionStore(fake).Create(context.Background(), store.SessionParams{
		ProjectID: "proj-1", Title: "Auth refactor", CreatedBy: "user-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if sess.ID != "sess-1" || !sess.IsActive || sess.Title != "Auth refactor" {
		t.Errorf("unexpected session: %+v", sess)
	}
	if len(fake.queries) != 2 {
		t.Fatalf("expected 2 statements (session + owner seat), got %d", len(fake.queries))
	}
	if !strings.Contains(fake.queries[0], "INSERT INTO sessions") {
		t.Errorf("stmt 1 must insert the session:\n%s", fake.queries[0])
	}
	if !strings.Contains(fake.queries[1], "'OWNER'") {
		t.Errorf("stmt 2 must seat the creator as OWNER:\n%s", fake.queries[1])
	}
}

func TestSessionJoinIdempotentWhenActive(t *testing.T) {
	fake := &sessionFakeDB{rowQueue: []sessionFakeRow{
		participantRow("part-9", "sess-1", "bob", "", "MEMBER", nil),
	}}
	p, err := store.NewSessionStore(fake).Join(context.Background(), "sess-1", store.JoinParams{UserID: "bob"})
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "part-9" {
		t.Errorf("re-join must return the active seat, got %+v", p)
	}
	if len(fake.queries) != 1 {
		t.Fatalf("idempotent join must not INSERT (queries=%d)", len(fake.queries))
	}
}

func TestSessionJoinInsertsWithDefaultRole(t *testing.T) {
	fake := &sessionFakeDB{rowQueue: []sessionFakeRow{
		{err: pgx.ErrNoRows}, // no active seat
		participantRow("part-2", "sess-1", "bob", "", "MEMBER", nil),
	}}
	p, err := store.NewSessionStore(fake).Join(context.Background(), "sess-1", store.JoinParams{UserID: "bob"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Role != store.RoleMember {
		t.Errorf("empty role must default to MEMBER, got %q", p.Role)
	}
	if len(fake.queries) != 2 || !strings.Contains(fake.queries[1], "INSERT INTO session_participants") {
		t.Errorf("second statement must INSERT the seat: %v", fake.queries)
	}
}

func TestSessionJoinValidationBlocksDB(t *testing.T) {
	fake := &sessionFakeDB{}
	_, err := store.NewSessionStore(fake).Join(context.Background(), "sess-1", store.JoinParams{Role: "ADMIN"})
	if err == nil {
		t.Error("join with bad role must fail before any query")
	}
	if len(fake.queries) != 0 {
		t.Errorf("invalid join must issue no statements, got %v", fake.queries)
	}
}

func TestSessionLeaveNotFound(t *testing.T) {
	fake := &sessionFakeDB{rowQueue: []sessionFakeRow{{err: pgx.ErrNoRows}}}
	_, err := store.NewSessionStore(fake).Leave(context.Background(), "sess-1", "ghost", "")
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("leave with no active seat must wrap ErrNotFound, got %v", err)
	}
}

func TestSessionLeaveReleasesSeat(t *testing.T) {
	now := time.Now().UTC()
	fake := &sessionFakeDB{rowQueue: []sessionFakeRow{
		participantRow("part-3", "sess-1", "bob", "", "MEMBER", now),
	}}
	p, err := store.NewSessionStore(fake).Leave(context.Background(), "sess-1", "bob", "")
	if err != nil {
		t.Fatal(err)
	}
	if p.LeftAt == nil {
		t.Error("released seat must carry left_at")
	}
	if !strings.Contains(fake.queries[0], "left_at = now()") {
		t.Errorf("leave must stamp left_at:\n%s", fake.queries[0])
	}
}

func TestSessionListActiveSQL(t *testing.T) {
	created := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	fake := &sessionFakeDB{rows: &sessionFakeRows{rows: [][]any{
		{"s1", "p1", "One", "u1", true, created, nil},
		{"s2", "p1", "", "u2", true, created, nil},
	}}}
	got, err := store.NewSessionStore(fake).ListActive(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "s1" || got[1].ID != "s2" {
		t.Fatalf("ListActive = %+v, want [s1 s2]", got)
	}
	if !strings.Contains(fake.queries[0], "is_active") || !strings.Contains(fake.queries[0], "project_id = $1") {
		t.Errorf("ListActive must filter project + active:\n%s", fake.queries[0])
	}
}

func TestSessionListParticipantsSQL(t *testing.T) {
	created := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	fake := &sessionFakeDB{rows: &sessionFakeRows{rows: [][]any{
		{"p1", "s1", "alice", "", "OWNER", created, nil},
		{"p2", "s1", "bob", "", "MEMBER", created, nil},
	}}}
	got, err := store.NewSessionStore(fake).ListParticipants(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].UserID != "alice" || got[1].UserID != "bob" {
		t.Fatalf("multi-user join must list both seats: %+v", got)
	}
	if !strings.Contains(fake.queries[0], "left_at IS NULL") {
		t.Errorf("ListParticipants must return active seats only:\n%s", fake.queries[0])
	}
}

func TestSessionEndClosesAndReleases(t *testing.T) {
	now := time.Now().UTC()
	fake := &sessionFakeDB{rowQueue: []sessionFakeRow{
		sessionRow("s1", "p1", "Done", "u1", false, now),
	}}
	sess, err := store.NewSessionStore(fake).End(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if sess.IsActive || sess.EndedAt == nil {
		t.Errorf("ended session must be inactive with ended_at: %+v", sess)
	}
	if fake.execs != 1 {
		t.Errorf("end must release seats with one UPDATE (execs=%d)", fake.execs)
	}
}

func TestSessionCountKeySessionsDB(t *testing.T) {
	fake := &sessionFakeDB{rowQueue: []sessionFakeRow{{values: []any{3}}}}
	n, err := store.NewSessionStore(fake).CountKeySessionsDB(context.Background(), "p1", "testing/framework")
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("count = %d, want 3", n)
	}
	if !strings.Contains(fake.queries[0], "COUNT(DISTINCT session_id)") {
		t.Errorf("counter must count distinct sessions:\n%s", fake.queries[0])
	}
	if !store.ShouldProposePromotion(n) {
		t.Error("count 3 must cross the promotion threshold")
	}
}
