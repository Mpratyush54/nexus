package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"central-memory/internal/store"
)

// TestTimelinePerfPostgres seeds a realistic volume (10 projects, 3,000
// legacy snapshot sessions, 2,000 agent sessions, one 20,000-turn session)
// into the database named by TEST_POSTGRES_DSN and times the chat list and
// chat open endpoints. It is a measurement, not a pass/fail gate: run it
// with -v and compare the logged timings. The seed is reused across runs.
//
//	TEST_POSTGRES_DSN=postgres://... TIMELINE_PERF=1 go test ./internal/server -run TestTimelinePerfPostgres -v
func TestTimelinePerfPostgres(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" || os.Getenv("TIMELINE_PERF") == "" {
		t.Skip("set TEST_POSTGRES_DSN and TIMELINE_PERF=1 to measure timeline performance")
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
	seed := seedTimelinePerf(t, ctx, pool)

	s := NewServer(pg)
	s.Log.SetOutput(&bytes.Buffer{})
	tok := loginAs(t, s, seed.viewer)

	measure := func(name, path string) {
		t.Helper()
		var times []time.Duration
		var rec string
		// TIMELINE_PERF_QUERIES=1 also counts statements per request from
		// pg_stat_database (slow: idle backends flush every 10s on PG15+).
		counting := os.Getenv("TIMELINE_PERF_QUERIES") != ""
		xacts := func() int64 {
			if !counting {
				return 1
			}
			time.Sleep(11 * time.Second)
			var n int64
			_ = pool.QueryRow(ctx, `SELECT xact_commit FROM pg_stat_database WHERE datname = current_database()`).Scan(&n)
			return n
		}
		before := xacts()
		for i := 0; i < 3; i++ {
			start := time.Now()
			res := doJSON(t, s, http.MethodGet, path, tok, nil)
			times = append(times, time.Since(start))
			if res.Code != http.StatusOK {
				t.Fatalf("%s: %d %s", name, res.Code, res.Body.String())
			}
			rec = res.Body.String()
		}
		queries := (xacts() - before - 1) / 3 // minus the counter read itself
		sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
		var body struct {
			Items []json.RawMessage `json:"items"`
			Next  string            `json:"next_cursor"`
		}
		_ = json.Unmarshal([]byte(rec), &body)
		t.Logf("PERF %-34s median %8.1fms  min %8.1fms  queries/req=%d items=%d next=%t bytes=%d", name,
			float64(times[1].Microseconds())/1000, float64(times[0].Microseconds())/1000, queries, len(body.Items), body.Next != "", len(rec))
	}
	p := url.QueryEscape(seed.projects[0])
	if dump := os.Getenv("TIMELINE_PERF_DUMP"); dump != "" {
		// Walk every page of several listings and write the items, to diff
		// two builds' results.
		var out bytes.Buffer
		for _, query := range []string{"", "&project_id=" + p, "&harness=cursor", "&harness=codex&project_id=" + p,
			"&q=deploy", "&q=deploy&project_id=" + p, "&q=perf-legacy-29", "&q=zzzznomatch"} {
			cursor := ""
			for page := 0; page < 500; page++ {
				path := "/v1/timeline?limit=50" + query
				if cursor != "" {
					path += "&cursor=" + url.QueryEscape(cursor)
				}
				res := doJSON(t, s, http.MethodGet, path, tok, nil)
				var body struct {
					Items []store.TimelineItem `json:"items"`
					Next  string               `json:"next_cursor"`
				}
				if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil || res.Code != http.StatusOK {
					t.Fatalf("%s: %d %s", path, res.Code, res.Body.String())
				}
				for _, it := range body.Items {
					line, _ := json.Marshal(it)
					fmt.Fprintf(&out, "%s %s\n", query, line)
				}
				if body.Next == "" {
					break
				}
				cursor = body.Next
			}
		}
		if err := os.WriteFile(dump, out.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	measure("timeline limit=50", "/v1/timeline?limit=50")
	measure("timeline project", "/v1/timeline?limit=50&project_id="+p)
	measure("timeline harness=cursor", "/v1/timeline?limit=50&harness=cursor")
	measure("timeline q=deploy", "/v1/timeline?limit=50&q=deploy")
	measure("timeline q=deploy project", "/v1/timeline?limit=50&q=deploy&project_id="+p)
	measure("timeline q=zzzz (no match)", "/v1/timeline?limit=50&q=zzzznomatch")
	measure("session get (big)", "/v1/agent-sessions/"+seed.big)
	measure("session fetch limit=80 (big)", "/v1/agent-sessions/"+seed.big+"/fetch?limit=80")
	measure("session fetch cursor=15000 (big)", "/v1/agent-sessions/"+seed.big+"/fetch?limit=80&cursor=15000")
	measure("session summary (big)", "/v1/agent-sessions/"+seed.big+"/summary")
	measure("session files (big)", "/v1/agent-sessions/"+seed.big+"/files")
	measure("session operations (big)", "/v1/agent-sessions/"+seed.big+"/operations")
	measure("legacy session get", "/v1/agent-sessions/"+seed.legacy)
	measure("legacy session fetch", "/v1/agent-sessions/"+seed.legacy+"/fetch?limit=80")
}

type timelinePerfSeed struct {
	viewer   string
	projects []string
	big      string
	legacy   string
}

var perfWords = strings.Fields(`the a to of and in for on with build test server client cache index query page cursor
session snapshot memory project branch merge commit review fix bug error log timeout retry config route handler
store migration schema table column value result item list filter search match preview harness agent turn`)

func perfText(r *rand.Rand, n int, needle bool) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(' ')
		}
		if needle && i == n/2 {
			b.WriteString("deploy")
			continue
		}
		b.WriteString(perfWords[r.Intn(len(perfWords))])
	}
	return b.String()
}

func seedTimelinePerf(t *testing.T, ctx context.Context, pool *pgxpool.Pool) timelinePerfSeed {
	t.Helper()
	var out timelinePerfSeed
	err := pool.QueryRow(ctx, `SELECT id::text FROM users WHERE username = 'perf-viewer'`).Scan(&out.viewer)
	if err == nil {
		rows, err := pool.Query(ctx, `SELECT id::text FROM projects WHERE folder_name LIKE 'perf-project-%' ORDER BY folder_name`)
		if err != nil {
			t.Fatal(err)
		}
		out.projects, err = pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `SELECT id::text FROM agent_sessions WHERE native_id = 'perf-big'`).Scan(&out.big); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `SELECT session_id::text FROM session_snapshots WHERE conversation_id = 'perf-legacy-2997'`).Scan(&out.legacy); err != nil {
			t.Fatal(err)
		}
		return out
	}
	start := time.Now()
	r := rand.New(rand.NewSource(42))
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
	scan(&out.viewer, `INSERT INTO users(username) VALUES ('perf-viewer') RETURNING id::text`)
	mates := make([]string, 4)
	for i := range mates {
		scan(&mates[i], `INSERT INTO users(username) VALUES ($1) RETURNING id::text`, fmt.Sprintf("perf-mate-%d", i))
	}
	for i := 0; i < 10; i++ {
		var id string
		scan(&id, `INSERT INTO projects(folder_name, created_by) VALUES ($1, $2::uuid) RETURNING id::text`,
			fmt.Sprintf("perf-project-%02d", i), mates[i%len(mates)])
		exec(`INSERT INTO project_members(project_id, user_id) VALUES ($1::uuid, $2::uuid) ON CONFLICT DO NOTHING`, id, out.viewer)
		for _, m := range mates {
			exec(`INSERT INTO project_members(project_id, user_id) VALUES ($1::uuid, $2::uuid) ON CONFLICT DO NOTHING`, id, m)
		}
		out.projects = append(out.projects, id)
	}
	harnesses := []string{"claude", "codex", "cursor", "copilot"}
	now := time.Now().UTC()

	// Legacy snapshots: 3,000 sessions, a third with a second version.
	type snapMeta struct{ id, project, harness, conv, owner string }
	snaps := make([]snapMeta, 3000)
	var snapRows [][]any
	var grantRows [][]any
	for i := range snaps {
		var sid string
		scan(&sid, `SELECT gen_random_uuid()::text`)
		owner := mates[i%len(mates)]
		if i%3 == 0 {
			owner = out.viewer
		}
		m := snapMeta{id: sid, project: out.projects[i%10], harness: harnesses[i%4], conv: fmt.Sprintf("perf-legacy-%d", i), owner: owner}
		snaps[i] = m
		versions := 1
		if i%3 == 1 {
			versions = 2
		}
		for v := 1; v <= versions; v++ {
			var jsonl bytes.Buffer
			for turn := 0; turn < 80; turn++ {
				role := "user"
				if turn%2 == 1 {
					role = "assistant"
				}
				line, _ := json.Marshal(map[string]any{"speaker": role, "content": perfText(r, 50, i%50 == 7 && turn == 41)})
				jsonl.Write(line)
				jsonl.WriteByte('\n')
			}
			var gz bytes.Buffer
			zw := gzip.NewWriter(&gz)
			_, _ = zw.Write(jsonl.Bytes())
			_ = zw.Close()
			bundle := make([]byte, 16<<10)
			r.Read(bundle)
			at := now.Add(-time.Duration(i)*37*time.Minute - time.Duration(2-v)*time.Minute)
			snapRows = append(snapRows, []any{sid, v, m.project, m.harness, m.conv, 80, gz.Bytes(), bundle, owner, at, at})
		}
		if owner != out.viewer && i%10 == 1 {
			grantRows = append(grantRows, []any{sid, m.project, out.viewer})
		}
	}
	if _, err := pool.CopyFrom(ctx, pgx.Identifier{"session_snapshots"},
		[]string{"session_id", "snapshot_version", "project_id", "harness", "conversation_id", "turn_count", "transcript_payload", "artifacts_bundle", "owner_user_id", "created_at", "updated_at"},
		pgx.CopyFromRows(snapRows)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.CopyFrom(ctx, pgx.Identifier{"session_content_grants"}, []string{"session_id", "project_id", "grantee_user_id"}, pgx.CopyFromRows(grantRows)); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO session_content_owners(session_id, project_id, owner_user_id)
		SELECT DISTINCT ON (session_id) session_id, project_id, owner_user_id FROM session_snapshots ORDER BY session_id ON CONFLICT DO NOTHING`)
	out.legacy = snaps[2997].id

	// Agent sessions: 2,000, the first 500 re-capture a snapshot's conversation.
	var turnRows, versionRows, sessGrantRows [][]any
	for i := 0; i < 2000; i++ {
		project := out.projects[(i*7)%10]
		harness := harnesses[(i*3)%4]
		native := fmt.Sprintf("perf-native-%d", i)
		owner := out.viewer
		visibility := "private"
		switch {
		case i%5 == 1:
			owner, visibility = mates[i%4], "team"
		case i%5 == 2 || i%5 == 3:
			owner = mates[i%4]
		}
		if i < 500 {
			snap := snaps[i*6]
			project, harness, native, owner = snap.project, snap.harness, snap.conv, snap.owner
		}
		at := now.Add(-time.Duration(i)*53*time.Minute - 11*time.Second)
		var id string
		scan(&id, `INSERT INTO agent_sessions(project_id, owner_user_id, harness, native_id, origin_machine_id, title, visibility, started_at, last_active_at)
			VALUES ($1::uuid, $2::uuid, $3, $4, 'pc', $5, $6, $7, $7) RETURNING id::text`,
			project, owner, harness, native, perfText(r, 6, false), visibility, at)
		if owner != out.viewer && i%20 == 2 {
			sessGrantRows = append(sessGrantRows, []any{id, out.viewer, true})
		}
		for turn := 0; turn < 20; turn++ {
			role := "user"
			if turn%2 == 1 {
				role = "assistant"
			}
			turnRows = append(turnRows, []any{id, turn, role, at, perfText(r, 45, i%100 == 37 && turn == 13), []byte("[]")})
		}
		versionRows = append(versionRows, []any{id, 1, 20, "complete"})
	}
	// One long-running chat: 20,000 turns with tool calls.
	scan(&out.big, `INSERT INTO agent_sessions(project_id, owner_user_id, harness, native_id, origin_machine_id, title, visibility, last_active_at)
		VALUES ($1::uuid, $2::uuid, 'claude', 'perf-big', 'pc', 'big session', 'private', $3) RETURNING id::text`, out.projects[0], out.viewer, now.Add(-3*time.Hour))
	calls, _ := json.Marshal([]map[string]any{{"name": "bash", "args": map[string]any{"command": perfText(r, 60, false)}}})
	for turn := 0; turn < 20000; turn++ {
		role := "user"
		if turn%2 == 1 {
			role = "assistant"
		}
		turnRows = append(turnRows, []any{out.big, turn, role, now, perfText(r, 60, false), calls})
	}
	versionRows = append(versionRows, []any{out.big, 1, 20000, "complete"})
	if _, err := pool.CopyFrom(ctx, pgx.Identifier{"session_turns"}, []string{"session_id", "idx", "role", "ts", "text_preview", "tool_calls"}, pgx.CopyFromRows(turnRows)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.CopyFrom(ctx, pgx.Identifier{"session_versions"}, []string{"session_id", "version", "turn_count", "state"}, pgx.CopyFromRows(versionRows)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.CopyFrom(ctx, pgx.Identifier{"session_grants"}, []string{"session_id", "grantee_user_id", "live"}, pgx.CopyFromRows(sessGrantRows)); err != nil {
		t.Fatal(err)
	}
	exec(`ANALYZE`)
	t.Logf("seeded in %s", time.Since(start).Round(time.Millisecond))
	return out
}
