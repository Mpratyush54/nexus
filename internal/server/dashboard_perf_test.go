//go:build !race

package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"central-memory/internal/store"
)

// TestDashboardPerfPostgres seeds a project at the size of a heavy real one
// (50,000 agent turns over 300 days, 2,000 memories, 40,000 events with a
// few very large payloads, 120 archived transcripts, plus a noisy neighbour
// project) into TEST_POSTGRES_DSN and holds GET /projects/{id}/dashboard to
// a time budget. The per-project cache is cleared before every timed
// request, so the budget covers the database path, not the cache.
//
// The race detector slows the server ~10x, so this file is excluded under
// -race (build tag) instead of carrying a budget nobody can trust.
//
//	TEST_POSTGRES_DSN=postgres://... go test ./internal/server -run TestDashboardPerfPostgres -v -p 1
//
// DASHBOARD_PERF_BUDGET_MS overrides the p95 budget (default 300).
func TestDashboardPerfPostgres(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set TEST_POSTGRES_DSN to measure the dashboard at scale")
	}
	if testing.Short() {
		t.Skip("short mode")
	}
	budget := 300 * time.Millisecond
	if v, err := strconv.Atoi(os.Getenv("DASHBOARD_PERF_BUDGET_MS")); err == nil && v > 0 {
		budget = time.Duration(v) * time.Millisecond
	}
	ctx := t.Context()
	pg, err := store.NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	migrations := os.Getenv("TEST_MIGRATIONS_DIR")
	if migrations == "" {
		migrations = "../../migrations"
	}
	if err := pg.RunMigrations(ctx, migrations); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	seed := seedDashboardPerf(t, ctx, pool)

	s := NewServer(pg)
	s.Log.SetOutput(&bytes.Buffer{})
	tok := loginAs(t, s, seed.owner)
	path := "/projects/" + seed.project + "/dashboard?utc_offset_minutes=330"

	request := func() (time.Duration, *http.Response, []byte) {
		t.Helper()
		start := time.Now()
		res := doJSON(t, s, http.MethodGet, path, tok, nil)
		took := time.Since(start)
		return took, res.Result(), res.Body.Bytes()
	}
	// The first request warms the archived-transcript date cache (it reads
	// every archived transcript once per process); report it separately.
	coldTook, res, body := request()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("dashboard: %d %s", res.StatusCode, body)
	}
	var first struct {
		Metrics struct {
			Memories int `json:"memories"`
			Events7d int `json:"events_7d"`
		} `json:"metrics"`
		Agent struct {
			Total   int  `json:"total"`
			Partial bool `json:"partial"`
		} `json:"agent_activity"`
		Recent []json.RawMessage `json:"recent"`
	}
	if err := json.Unmarshal(body, &first); err != nil {
		t.Fatal(err)
	}
	if first.Metrics.Memories < 1000 || first.Agent.Total < 10000 {
		t.Fatalf("dashboard numbers look wrong: memories=%d agent total=%d", first.Metrics.Memories, first.Agent.Total)
	}
	if len(body) > 512<<10 {
		t.Fatalf("dashboard response is %d bytes; it must stay small", len(body))
	}

	var times []time.Duration
	for i := 0; i < 20; i++ {
		s.resetDashboardCache()
		took, res, body := request()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("dashboard: %d %s", res.StatusCode, body)
		}
		times = append(times, took)
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	p50, p95 := times[len(times)/2], times[len(times)*95/100-1]
	cachedTook, _, _ := request()
	cachedTook2, _, _ := request()
	t.Logf("PERF dashboard cold %.1fms  uncached p50 %.1fms p95 %.1fms max %.1fms  cached %.2fms  bytes=%d",
		ms(coldTook), ms(p50), ms(p95), ms(times[len(times)-1]), ms(min(cachedTook, cachedTook2)), len(body))
	if p95 > budget {
		t.Fatalf("dashboard p95 %.1fms is over the %s budget", ms(p95), budget)
	}
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

type dashboardPerfSeed struct{ owner, project string }

func seedDashboardPerf(t *testing.T, ctx context.Context, pool *pgxpool.Pool) dashboardPerfSeed {
	t.Helper()
	var out dashboardPerfSeed
	if err := pool.QueryRow(ctx, `SELECT id::text FROM users WHERE username = 'dash-perf-owner'`).Scan(&out.owner); err == nil {
		if err := pool.QueryRow(ctx, `SELECT id::text FROM projects WHERE folder_name = 'dash-perf-project'`).Scan(&out.project); err != nil {
			t.Fatal(err)
		}
		return out
	}
	start := time.Now()
	r := rand.New(rand.NewSource(54))
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	scan := func(dst *string, sql string, args ...any) {
		t.Helper()
		if err := pool.QueryRow(ctx, sql, args...).Scan(dst); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	copyRows := func(table string, cols []string, rows [][]any) {
		t.Helper()
		if _, err := pool.CopyFrom(ctx, pgx.Identifier{table}, cols, pgx.CopyFromRows(rows)); err != nil {
			t.Fatalf("copy %s: %v", table, err)
		}
	}
	var mate string
	scan(&out.owner, `INSERT INTO users(username) VALUES ('dash-perf-owner') RETURNING id::text`)
	scan(&mate, `INSERT INTO users(username) VALUES ('dash-perf-mate') RETURNING id::text`)
	scan(&out.project, `INSERT INTO projects(folder_name, created_by) VALUES ('dash-perf-project', $1::uuid) RETURNING id::text`, out.owner)
	var noise string
	scan(&noise, `INSERT INTO projects(folder_name, created_by) VALUES ('dash-perf-noise', $1::uuid) RETURNING id::text`, mate)
	for _, p := range []string{out.project, noise} {
		for _, u := range []string{out.owner, mate} {
			exec(`INSERT INTO project_members(project_id, user_id) VALUES ($1::uuid, $2::uuid) ON CONFLICT DO NOTHING`, p, u)
		}
	}
	now := time.Now().UTC()
	harnesses := []string{"claude", "codex", "cursor", "opencode"}

	// Agent sessions and turns: 50,000 turns in the project over 300 days
	// (most by the owner), and 40,000 in the neighbour project.
	seedTurns := func(project, owner string, sessions, turns int, prefix string) {
		var turnRows [][]any
		perSession := turns / sessions
		for i := 0; i < sessions; i++ {
			who := owner
			if i%6 == 5 {
				who = mate
			}
			native := fmt.Sprintf("%s-%d", prefix, i)
			if prefix == "dash-perf" && i < 12 {
				native = fmt.Sprintf("dash-legacy-%d", i) // also archived: deduplicated against it
			}
			var id string
			scan(&id, `INSERT INTO agent_sessions(project_id, owner_user_id, harness, native_id, origin_machine_id, title, visibility, started_at, last_active_at)
				VALUES ($1::uuid, $2::uuid, $3, $4, 'pc', $5, 'private', $6, $6) RETURNING id::text`,
				project, who, harnesses[i%len(harnesses)], native, perfText(r, 5, false), now)
			for turn := 0; turn < perSession; turn++ {
				role := "user"
				switch turn % 3 {
				case 1:
					role = "assistant"
				case 2:
					role = "tool"
				}
				at := now.Add(-time.Duration(r.Int63n(int64(300 * 24 * time.Hour))))
				var source any = at
				if turn%40 == 39 {
					source = nil // older captures have no source time
				}
				turnRows = append(turnRows, []any{id, turn, role, now, source, perfText(r, 40, false), []byte("[]")})
			}
		}
		copyRows("session_turns", []string{"session_id", "idx", "role", "ts", "source_ts", "text_preview", "tool_calls"}, turnRows)
	}
	seedTurns(out.project, out.owner, 60, 50000, "dash-perf")
	seedTurns(noise, mate, 40, 40000, "dash-noise")

	// 2,000 memories (plus 1,000 in the neighbour project).
	var memRows [][]any
	statuses := []string{"CONFIRMED", "CONFIRMED", "PROPOSED", "REJECTED"}
	for i := 0; i < 3000; i++ {
		project := out.project
		if i >= 2000 {
			project = noise
		}
		visibility := "project"
		if i%10 == 3 {
			visibility = "private"
		}
		memRows = append(memRows, []any{project, out.owner, fmt.Sprintf("dash/perf/%d", i), perfText(r, 30, false), statuses[i%len(statuses)], visibility, now.Add(-time.Duration(i) * time.Hour)})
	}
	copyRows("memory_items", []string{"project_id", "user_id", "key", "content", "status", "visibility", "created_at"}, memRows)
	// Real memories carry a 1536-dimension embedding; reading them back as
	// text is ~19 KB a row.
	exec(`UPDATE memory_items SET embedding = (SELECT '[' || string_agg(to_char(random() - 0.5, 'FM0.000000000'), ',') || ']' FROM generate_series(1, 1536))
		WHERE key LIKE 'dash/perf/%'`)

	// 40,000 events. Most are small; every 400th carries a 256 KiB payload
	// (captured tool output), which is what made loading them in bulk slow.
	big := strings.Repeat("x", 256<<10)
	types := []string{"MEMORY_PROPOSED", "MCP_TOOL_CALL", "SESSION_CAPTURED", "MEMORY_CONFIRMED", "BRANCH_CHECKOUT"}
	var eventRows [][]any
	for i := 0; i < 40000; i++ {
		payload := map[string]any{"note": perfText(r, 60, false)}
		if i%400 == 0 {
			payload["output"] = big
		}
		raw, _ := json.Marshal(payload)
		at := now.Add(-time.Duration(40000-i) * 11 * time.Minute)
		eventRows = append(eventRows, []any{out.project, out.owner, types[i%len(types)], raw, at})
		if i%4 == 0 {
			eventRows = append(eventRows, []any{noise, mate, types[i%len(types)], raw, at})
		}
	}
	copyRows("events", []string{"project_id", "user_id", "event_type", "payload", "created_at"}, eventRows)

	// 120 archived (legacy) transcripts with dated lines, 300 lines each.
	var snapRows [][]any
	for i := 0; i < 120; i++ {
		var sid string
		scan(&sid, `SELECT gen_random_uuid()::text`)
		var jsonl bytes.Buffer
		for line := 0; line < 300; line++ {
			role := "user"
			if line%2 == 1 {
				role = "assistant"
			}
			at := now.Add(-time.Duration(r.Int63n(int64(300 * 24 * time.Hour))))
			b, _ := json.Marshal(map[string]any{"type": role, "timestamp": at.Format(time.RFC3339Nano),
				"message": map[string]any{"role": role, "content": perfText(r, 40, false)}})
			jsonl.Write(b)
			jsonl.WriteByte('\n')
		}
		var gz bytes.Buffer
		zw := gzip.NewWriter(&gz)
		_, _ = zw.Write(jsonl.Bytes())
		_ = zw.Close()
		at := now.Add(-time.Duration(i) * 49 * time.Hour)
		snapRows = append(snapRows, []any{sid, 1, out.project, harnesses[i%len(harnesses)], fmt.Sprintf("dash-legacy-%d", i), 300, gz.Bytes(), out.owner, at, at})
	}
	copyRows("session_snapshots", []string{"session_id", "snapshot_version", "project_id", "harness", "conversation_id", "turn_count", "transcript_payload", "owner_user_id", "created_at", "updated_at"}, snapRows)
	exec(`INSERT INTO session_content_owners(session_id, project_id, owner_user_id)
		SELECT DISTINCT ON (session_id) session_id, project_id, owner_user_id FROM session_snapshots WHERE conversation_id LIKE 'dash-legacy-%'
		ORDER BY session_id ON CONFLICT DO NOTHING`)
	exec(`ANALYZE`)
	t.Logf("seeded dashboard perf data in %s", time.Since(start).Round(time.Millisecond))
	return out
}
