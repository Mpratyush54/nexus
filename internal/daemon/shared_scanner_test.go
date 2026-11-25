package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type sharedTestEmitter struct {
	mu     sync.Mutex
	events []Event
}

func (e *sharedTestEmitter) Emit(ev Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, ev)
}

func (e *sharedTestEmitter) turns() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for _, ev := range e.events {
		if ev.Type == EventConversationTurn {
			c, _ := ev.Payload["content"].(string)
			out = append(out, c)
		}
	}
	return out
}

func waitFor(t *testing.T, what string, timeout time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func appendLine(t *testing.T, path, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(line + "\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
}

type sharedFixture struct {
	scanner    *SharedScanner
	transcript string // claude-style projects dir
	alpha      *Harvester
	beta       *Harvester
	alphaOut   *sharedTestEmitter
	betaOut    *sharedTestEmitter
}

func newSharedFixture(t *testing.T, state string) *sharedFixture {
	t.Helper()
	base := t.TempDir()
	projects := filepath.Join(base, "projects")
	for _, dir := range []string{"d-src-alpha-proj", "d-src-beta-proj"} {
		if err := os.MkdirAll(filepath.Join(projects, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	s := NewSharedScanner(state)
	s.sourceList = []TranscriptSource{{Agent: "claude", Dirs: []string{projects}, Format: FormatJSONL}}
	f := &sharedFixture{scanner: s, transcript: projects, alphaOut: &sharedTestEmitter{}, betaOut: &sharedTestEmitter{}}
	f.alpha = NewHarvesterWithPoll(filepath.Join(base, "alpha-proj"), f.alphaOut, time.Hour, time.Hour)
	f.beta = NewHarvesterWithPoll(filepath.Join(base, "beta-proj"), f.betaOut, time.Hour, time.Hour)
	return f
}

func TestSharedScannerRoutesEachTranscriptToItsWorkspaceOnly(t *testing.T) {
	f := newSharedFixture(t, "")
	alphaFile := filepath.Join(f.transcript, "d-src-alpha-proj", "s1.jsonl")
	betaFile := filepath.Join(f.transcript, "d-src-beta-proj", "s2.jsonl")
	appendLine(t, alphaFile, `{"role":"user","content":"alpha question about caching","timestamp":"2026-11-20T10:00:00Z"}`)
	appendLine(t, betaFile, `{"role":"user","content":"beta question about queues","timestamp":"2026-11-20T10:00:00Z"}`)

	f.scanner.Attach(f.alpha)
	f.scanner.Attach(f.beta)
	defer f.scanner.Detach(f.beta)
	defer f.scanner.Detach(f.alpha)

	waitFor(t, "both workspaces to receive their turn", 5*time.Second, func() bool {
		return len(f.alphaOut.turns()) == 1 && len(f.betaOut.turns()) == 1
	})
	if got := f.alphaOut.turns()[0]; !strings.Contains(got, "alpha") {
		t.Fatalf("alpha received %q", got)
	}
	if got := f.betaOut.turns()[0]; !strings.Contains(got, "beta") {
		t.Fatalf("beta received %q", got)
	}
}

func TestSharedScannerPicksUpAppendsFromFileEvents(t *testing.T) {
	f := newSharedFixture(t, "")
	file := filepath.Join(f.transcript, "d-src-alpha-proj", "live.jsonl")
	appendLine(t, file, `{"role":"user","content":"first message in the session","timestamp":"2026-11-20T10:00:00Z"}`)
	f.scanner.Attach(f.alpha)
	defer f.scanner.Detach(f.alpha)
	waitFor(t, "initial turn", 5*time.Second, func() bool { return len(f.alphaOut.turns()) == 1 })

	// The reconcile walk runs every few minutes; an append must arrive well
	// before that, through a file notification.
	appendLine(t, file, `{"role":"assistant","content":"reply delivered through a file event","timestamp":"2026-11-20T10:00:05Z"}`)
	waitFor(t, "appended turn via file notification", 5*time.Second, func() bool { return len(f.alphaOut.turns()) == 2 })
}

func TestSharedScannerDoesNotRereadAfterRestart(t *testing.T) {
	state := filepath.Join(t.TempDir(), "cursors.json")
	f := newSharedFixture(t, state)
	file := filepath.Join(f.transcript, "d-src-alpha-proj", "s.jsonl")
	appendLine(t, file, `{"role":"user","content":"message captured before restart","timestamp":"2026-11-20T10:00:00Z"}`)
	f.scanner.Attach(f.alpha)
	waitFor(t, "first capture", 5*time.Second, func() bool { return len(f.alphaOut.turns()) == 1 })
	f.scanner.Detach(f.alpha)
	if _, err := os.Stat(state); err != nil {
		t.Fatalf("cursor file not written: %v", err)
	}

	// Simulated app restart: new scanner, new harvester, same cursor file.
	appendLine(t, file, `{"role":"assistant","content":"message written while the app was closed","timestamp":"2026-11-20T10:01:00Z"}`)
	again := NewSharedScanner(state)
	again.sourceList = f.scanner.sourceList
	out := &sharedTestEmitter{}
	h := NewHarvesterWithPoll(filepath.Dir(f.alpha.workspace)+string(filepath.Separator)+"alpha-proj", out, time.Hour, time.Hour)
	again.Attach(h)
	defer again.Detach(h)
	waitFor(t, "post-restart capture", 5*time.Second, func() bool { return len(out.turns()) >= 1 })
	time.Sleep(300 * time.Millisecond)
	got := out.turns()
	if len(got) != 1 || !strings.Contains(got[0], "while the app was closed") {
		t.Fatalf("after restart got %q, want only the new message", got)
	}
}

func TestSharedScannerSkipsPausedWorkspaces(t *testing.T) {
	f := newSharedFixture(t, "")
	file := filepath.Join(f.transcript, "d-src-alpha-proj", "s.jsonl")
	appendLine(t, file, `{"role":"user","content":"should wait until capture resumes","timestamp":"2026-11-20T10:00:00Z"}`)
	f.alpha.SetPaused(true)
	f.scanner.Attach(f.alpha)
	defer f.scanner.Detach(f.alpha)
	time.Sleep(500 * time.Millisecond)
	if n := len(f.alphaOut.turns()); n != 0 {
		t.Fatalf("paused workspace received %d turns", n)
	}
	if cursors, _ := f.scanner.engine.cursorSnapshot(); cursors[file] != 0 {
		t.Fatal("paused workspace's transcript was read")
	}
	f.alpha.SetPaused(false)
	f.scanner.ReconcileNow()
	waitFor(t, "turn after resume", 5*time.Second, func() bool { return len(f.alphaOut.turns()) == 1 })
}

func TestSQLiteMainFileMapsSidecars(t *testing.T) {
	for in, want := range map[string]string{
		`C:\x\state.vscdb-wal`:  `C:\x\state.vscdb`,
		`C:\x\opencode.db-shm`:  `C:\x\opencode.db`,
		`C:\x\a.sqlite-journal`: `C:\x\a.sqlite`,
		`C:\x\s.jsonl`:          `C:\x\s.jsonl`,
	} {
		if got := sqliteMainFile(in); got != want {
			t.Errorf("sqliteMainFile(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSQLiteFallbackIsRateLimitedPerFile(t *testing.T) {
	var got []Event
	h := NewHarvesterWithPoll(t.TempDir(), sliceEmitter{&got}, time.Second, time.Minute)
	clock := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)
	h.now = func() time.Time { return clock }
	path := filepath.Join(t.TempDir(), "state.vscdb")
	write := func(payload string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(payload), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h.agents[path] = "cursor"
	write(`sqlite header {"role":"user","content":"baseline turn that is long enough"}`)
	st, _ := os.Stat(path)
	h.TrackSQLite(path, st) // first sight: baseline

	clock = clock.Add(5 * time.Second)
	write(`sqlite header {"role":"user","content":"baseline turn that is long enough"} {"role":"assistant","content":"first change, scanned immediately"}`)
	st, _ = os.Stat(path)
	h.TrackSQLite(path, st)
	if countTurns(got) == 0 {
		t.Fatal("first change must be scanned")
	}
	got = nil

	clock = clock.Add(5 * time.Second)
	write(`sqlite header {"role":"user","content":"baseline turn that is long enough"} {"role":"assistant","content":"first change, scanned immediately"} {"role":"assistant","content":"edit seen inside the rate-limit window"}`)
	st, _ = os.Stat(path)
	if !h.TrackSQLite(path, st) {
		t.Fatal("a change must still count as activity")
	}
	if n := countTurns(got); n != 0 {
		t.Fatalf("content scan emitted %d turns inside the window, want 0", n)
	}

	clock = clock.Add(sqliteFallbackInterval + time.Second)
	write(`sqlite header {"role":"user","content":"baseline turn that is long enough"} {"role":"assistant","content":"first change, scanned immediately"} {"role":"assistant","content":"edit seen inside the rate-limit window"} {"role":"user","content":"edit after the window has passed"}`)
	st, _ = os.Stat(path)
	h.TrackSQLite(path, st)
	if n := countTurns(got); n == 0 {
		t.Fatal("content scan must run again once the window passed")
	}
}

func TestDiscoverProjectsGroupsSessionsByGitRoot(t *testing.T) {
	defer func(f func() []string) { discoverSkipDirs = f }(discoverSkipDirs)
	discoverSkipDirs = func() []string { return nil } // fixtures live in a temp dir
	base := t.TempDir()
	repo := filepath.Join(base, "shop-app")
	sub := filepath.Join(repo, "backend")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	transcripts := filepath.Join(base, "projects")
	_ = os.MkdirAll(transcripts, 0o755)
	write := func(name, cwd string) {
		line := `{"type":"user","cwd":` + strconvQuote(cwd) + `,"message":{"role":"user","content":"hello there agent"}}`
		appendLine(t, filepath.Join(transcripts, name), line)
	}
	write("a.jsonl", repo)
	write("b.jsonl", sub)                                // same repo via subfolder
	write("c.jsonl", filepath.Join(base, "gone-folder")) // missing on disk: ignored
	got := DiscoverProjects([]TranscriptSource{{Agent: "claude", Dirs: []string{transcripts}, Format: FormatJSONL}})
	if len(got) != 1 {
		t.Fatalf("got %d projects, want 1: %+v", len(got), got)
	}
	if !strings.EqualFold(got[0].Path, repo) || got[0].Sessions != 2 || got[0].Agents["claude"] != 2 {
		t.Fatalf("unexpected discovery: %+v", got[0])
	}
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// slowEmitter stands in for a workspace whose upload pipeline is slow.
type slowEmitter struct{ delay time.Duration }

func (e slowEmitter) Emit(Event) { time.Sleep(e.delay) }

// A long first import must save progress during the pass so a restart
// resumes instead of starting over.
func TestSharedScannerSavesCursorsDuringLongImport(t *testing.T) {
	defer func(v time.Duration) { cursorSaveInterval = v }(cursorSaveInterval)
	cursorSaveInterval = 50 * time.Millisecond
	state := filepath.Join(t.TempDir(), "cursors.json")
	f := newSharedFixture(t, state)
	f.scanner.sourceList = f.scanner.sourceList[:1]
	// Many transcripts, each slow to "upload".
	for i := 0; i < 40; i++ {
		appendLine(t, filepath.Join(f.transcript, "d-src-alpha-proj", fmt.Sprintf("s%02d.jsonl", i)),
			`{"role":"user","content":"historical message number that is long enough","timestamp":"2026-11-20T10:00:00Z"}`)
	}
	slow := NewHarvesterWithPoll(f.alpha.workspace, slowEmitter{delay: 30 * time.Millisecond}, time.Hour, time.Hour)
	f.scanner.Attach(slow)
	defer f.scanner.Detach(slow)
	// The whole pass needs ~1.2s; the cursor file must appear well before.
	waitFor(t, "cursor file during the import", 900*time.Millisecond, func() bool {
		_, err := os.Stat(state)
		return err == nil
	})
}
