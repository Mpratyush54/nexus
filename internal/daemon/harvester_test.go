package daemon

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// sliceEmitter collects events for assertions.
type sliceEmitter struct {
	evs *[]Event
}

func (s sliceEmitter) Emit(e Event) { *s.evs = append(*s.evs, e) }

func writeLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, l := range lines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			t.Fatal(err)
		}
	}
}

func collectHarvester(workspace string) (*Harvester, *[]Event) {
	var got []Event
	h := NewHarvester(workspace, sliceEmitter{&got})
	return h, &got
}

func TestTailOffset(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "session.jsonl")
	writeLines(t, p,
		`{"role":"user","content":"Let's use Redis","timestamp":"2026-09-17T10:00:00Z"}`,
		`{"role":"assistant","content":"Agreed, pub/sub support wins","timestamp":"2026-09-17T10:01:00Z"}`,
	)
	h, got := collectHarvester(dir)
	turns, err := h.TailFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 2 {
		t.Fatalf("got %d turns, want 2", len(turns))
	}
	if len(*got) != 2 || (*got)[0].Type != EventConversationTurn {
		t.Fatalf("expected 2 CONVERSATION_TURN events, got %+v", *got)
	}

	// Re-tailing with no new content yields nothing (byte offset held).
	turns, err = h.TailFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 0 || len(*got) != 2 {
		t.Fatalf("re-tail should be empty, got %d turns %d events", len(turns), len(*got))
	}

	// Appended lines are picked up exactly once.
	writeLines(t, p, `{"role":"user","content":"Ship it","timestamp":"2026-09-17T10:02:00Z"}`)
	turns, err = h.TailFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 || turns[0].Content != "Ship it" {
		t.Fatalf("append tail got %+v", turns)
	}
}

func TestNoiseFiltering(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "noisy.jsonl")
	writeLines(t, p,
		`{"type":"tool_use","role":"assistant","content":"read file x.go"}`,
		`{"type":"tool_result","role":"tool","content":"file bytes..."}`,
		`{"type":"system","content":"session started"}`,
		`{"role":"assistant","content":[{"type":"text","text":"Use Redis for pub/sub"},{"type":"tool_use","input":{"cmd":"ls"}}],"timestamp":"2026-09-17T10:00:00Z"}`,
		`{"role":"user","content":"I prefer tabs","timestamp":"2026-09-17T10:01:00Z"}`,
		``,
		`not json at all`,
	)
	h, got := collectHarvester(dir)
	turns, err := h.TailFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 2 {
		t.Fatalf("got %d turns, want 2 (tool noise skipped): %+v", len(turns), turns)
	}
	if turns[0].Speaker != "assistant" || turns[0].Content != "Use Redis for pub/sub" {
		t.Fatalf("content-block extraction wrong: %+v", turns[0])
	}
	if turns[1].Speaker != "user" {
		t.Fatalf("speaker normalization wrong: %+v", turns[1])
	}
	if len(*got) != 2 {
		t.Fatalf("expected 2 events, got %d", len(*got))
	}
	if (*got)[0].Payload["speaker"] != "assistant" || (*got)[0].Payload["content"] != "Use Redis for pub/sub" {
		t.Fatalf("event payload wrong: %+v", (*got)[0].Payload)
	}
}

func TestIdleDetection(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "idle.jsonl")
	now := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	h, _ := collectHarvester(dir)
	h.now = func() time.Time { return now }
	h.IdleTimeout = 5 * time.Minute

	writeLines(t, p, `{"role":"user","content":"hello","timestamp":"2026-09-17T10:00:00Z"}`)
	if _, err := h.TailFile(p); err != nil {
		t.Fatal(err)
	}
	// Not idle yet: 4 minutes later, no completion.
	now = now.Add(4 * time.Minute)
	if evs := h.CheckIdle(); len(evs) != 0 {
		t.Fatalf("premature completion: %+v", evs)
	}
	// Idle past the window: exactly one SESSION_TRANSCRIPT_COMPLETE batch.
	now = now.Add(2 * time.Minute)
	evs := h.CheckIdle()
	if len(evs) != 1 || evs[0].Type != EventSessionComplete {
		t.Fatalf("expected 1 SESSION_TRANSCRIPT_COMPLETE, got %+v", evs)
	}
	if evs[0].Payload["turn_count"] != 1 {
		t.Fatalf("batch should carry turn_count=1, got %+v", evs[0].Payload)
	}
	// Already completed: does not refire.
	if evs := h.CheckIdle(); len(evs) != 0 {
		t.Fatalf("completion refired: %+v", evs)
	}
	// New activity re-arms; next idle window fires again.
	now = now.Add(time.Minute)
	writeLines(t, p, `{"role":"user","content":"back again","timestamp":"2026-09-17T10:07:00Z"}`)
	if _, err := h.TailFile(p); err != nil {
		t.Fatal(err)
	}
	now = now.Add(6 * time.Minute)
	if evs := h.CheckIdle(); len(evs) != 1 {
		t.Fatalf("re-armed completion missing: %+v", evs)
	}
}

func TestMatchesWorkspace(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "tmp", "central-memory")
	h := NewHarvester(root, nil)
	if !h.MatchesWorkspace(filepath.Join("some", "projects", "central-memory", "abc.jsonl")) {
		t.Fatal("path containing workspace folder name should match")
	}
	if h.MatchesWorkspace(filepath.Join("some", "projects", "other-project", "abc.jsonl")) {
		t.Fatal("other project's path must not match")
	}
	empty := NewHarvester("", nil)
	if !empty.MatchesWorkspace("/anything/at/all.jsonl") {
		t.Fatal("empty workspace should match all (no filter)")
	}
}

func TestSQLiteTracking(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "state.vscdb")
	if err := os.WriteFile(p, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	h, _ := collectHarvester(dir)
	h.now = func() time.Time { return now }
	h.IdleTimeout = 5 * time.Minute

	st, _ := os.Stat(p)
	if h.TrackSQLite(p, st) {
		t.Fatal("first sighting is baseline, not a change")
	}
	now = now.Add(time.Minute)
	if err := os.WriteFile(p, []byte("v1-more-rows"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, _ = os.Stat(p)
	if !h.TrackSQLite(p, st) {
		t.Fatal("size/mtime change should report activity")
	}
	// Idle window after last change -> completion hint for sqlite source.
	now = now.Add(6 * time.Minute)
	evs := h.CheckIdle()
	if len(evs) != 1 || evs[0].Type != EventSessionComplete {
		t.Fatalf("expected sqlite completion hint, got %+v", evs)
	}
	if d, _ := evs[0].Payload["detail"].(string); !strings.Contains(d, "sqlite") {
		t.Fatalf("sqlite hint missing from detail payload: %+v", evs[0].Payload)
	}
}

func TestTornWriteHeldBack(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "torn.jsonl")
	writeLines(t, p, `{"role":"user","content":"full line","timestamp":"2026-09-17T10:00:00Z"}`)
	// Append a partial line with no trailing newline (mid-write torn record).
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"role":"user","content":"part`); err != nil {
		t.Fatal(err)
	}
	f.Close()

	h, _ := collectHarvester(dir)
	turns, err := h.TailFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 || turns[0].Content != "full line" {
		t.Fatalf("torn line must be held back, got %+v", turns)
	}
	// Completing the line ("part" + "ial encore" = "partial encore") delivers
	// it on the next tail as one valid turn.
	writeLines(t, p, `ial encore","timestamp":"2026-09-17T10:01:00Z"}`)
	turns, err = h.TailFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 || turns[0].Content != "partial encore" {
		t.Fatalf("completed torn line should parse, got %+v", turns)
	}
}

// fakeSQLiteExtractor is a test SQLiteExtractor returning canned turns and
// recording the dbPath/since it was called with. It honors the since
// contract: rows are returned only when newer than since (the canned turns
// carry the fake's epoch, so a non-zero since means "already consumed").
type fakeSQLiteExtractor struct {
	mu    sync.Mutex
	calls []string
	since []time.Time
	epoch time.Time
	turns []Turn
	err   error
}

func (f *fakeSQLiteExtractor) ExtractNewRows(dbPath string, since time.Time) ([]Turn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, dbPath)
	f.since = append(f.since, since)
	if f.err != nil {
		return nil, f.err
	}
	if !since.IsZero() && !since.Before(f.epoch) {
		return nil, nil
	}
	out := make([]Turn, len(f.turns))
	copy(out, f.turns)
	return out, nil
}

func (f *fakeSQLiteExtractor) ncalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func countTurns(evs []Event) int {
	n := 0
	for _, e := range evs {
		if e.Type == EventConversationTurn {
			n++
		}
	}
	return n
}

func TestHarvestSQLiteNilExtractorLiveness(t *testing.T) {
	// Default (no extractor registered): sqlite files never emit
	// CONVERSATION_TURN but still refresh idle timers, with the explicit
	// liveness-only reason logged.
	now := time.Now()
	var got []Event
	h := NewHarvesterWithPoll(t.TempDir(), sliceEmitter{&got}, time.Second, time.Minute)
	h.now = func() time.Time { return now }

	var logs bytes.Buffer
	prevOut := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(prevOut)

	dir := t.TempDir()
	path := filepath.Join(dir, "state.vscdb")
	if err := os.WriteFile(path, []byte("sqlite-format-data"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.agents[path] = "cursor"
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if h.TrackSQLite(path, st) {
		t.Fatal("first sight must only record state, want false")
	}
	// Grow the file so the signature changes.
	if err := os.WriteFile(path, []byte("sqlite-format-data-more"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !h.TrackSQLite(path, st) {
		t.Fatal("changed sqlite file must report activity, want true")
	}
	if n := countTurns(got); n != 0 {
		t.Fatalf("nil extractor must not emit CONVERSATION_TURN, got %d", n)
	}
	h.mu.Lock()
	_, ok := h.lastActive[path]
	h.mu.Unlock()
	if !ok {
		t.Fatal("nil-extractor sqlite file must still refresh liveness")
	}
	if out := logs.String(); !strings.Contains(out, "liveness-only") {
		t.Fatalf("expected explicit liveness-only reason in logs, got %q", out)
	}
}

func TestHarvestSQLiteFakeExtractorEmitsTurns(t *testing.T) {
	// A registered extractor turns sqlite changes into CONVERSATION_TURN
	// events; unregistered agents stay liveness-only.
	now := time.Now()
	var got []Event
	h := NewHarvesterWithPoll(t.TempDir(), sliceEmitter{&got}, time.Second, time.Minute)
	h.now = func() time.Time { return now }
	// The fake only returns rows newer than `since`; with a frozen test
	// clock the first sight records lastActive == now, so the epoch must
	// sit ahead of now for rows to count as new.
	fake := &fakeSQLiteExtractor{epoch: now.Add(time.Hour), turns: []Turn{
		{Speaker: "user", Content: "cursor turn one", Timestamp: now},
		{Speaker: "assistant", Content: "cursor reply", Timestamp: now},
	}}
	h.SetSQLiteExtractor("cursor", fake)

	dir := t.TempDir()
	path := filepath.Join(dir, "state.vscdb")
	if err := os.WriteFile(path, []byte("sqlite-format-data"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.agents[path] = "cursor"
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if h.TrackSQLite(path, st) {
		t.Fatal("first sight must only record state, want false")
	}
	// Grow the file so the signature changes, then track again.
	if err := os.WriteFile(path, []byte("sqlite-format-data-more"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !h.TrackSQLite(path, st) {
		t.Fatal("changed sqlite file must report activity, want true")
	}
	if fake.ncalls() != 1 {
		t.Fatalf("expected 1 extractor call, got %d", fake.ncalls())
	}
	if n := countTurns(got); n != 2 {
		t.Fatalf("expected 2 CONVERSATION_TURN, got %d", n)
	}
	first := got[0].Payload
	if first["speaker"] != "user" || first["content"] != "cursor turn one" {
		t.Fatalf("unexpected turn payload: %v", first)
	}
	if first["session_id"] != sessionID(path) || first["agent"] != "cursor" {
		t.Fatalf("unexpected identity fields: %v", first)
	}
	if first["path"] != path {
		t.Fatalf("unexpected attribution fields: %v", first)
	}

	// A second sqlite file extracts independently through the same seam.
	path2 := filepath.Join(dir, "other.vscdb")
	if err := os.WriteFile(path2, []byte("more-sqlite-data"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.agents[path2] = "cursor"
	st2, err := os.Stat(path2)
	if err != nil {
		t.Fatal(err)
	}
	h.TrackSQLite(path2, st2) // first sight: record only
	if err := os.WriteFile(path2, []byte("more-sqlite-data-changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	st2, err = os.Stat(path2)
	if err != nil {
		t.Fatal(err)
	}
	h.TrackSQLite(path2, st2)
	if n := countTurns(got); n != 4 {
		t.Fatalf("expected 2 more turns (total 4), got %d", n)
	}

	// Unregistering restores liveness-only for that agent.
	h.SetSQLiteExtractor("cursor", nil)
	if err := os.WriteFile(path, []byte("sqlite-format-data-third"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !h.TrackSQLite(path, st) {
		t.Fatal("changed file must still report activity after unregister")
	}
	if n := countTurns(got); n != 4 {
		t.Fatalf("unregistered extractor must not emit, got %d total", n)
	}
	if fake.ncalls() != 2 {
		t.Fatalf("unregistered extractor must not be called, calls = %d", fake.ncalls())
	}
}
