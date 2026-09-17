package materializer

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeClock is the injectable clock/timer: tests pin Now and advance it
// manually, so debounce tests never sleep. After returns a channel fed by
// advance (only Run uses it; Flush tests ignore it). now is mutex-guarded:
// After polls it from a background goroutine while the test goroutine
// advances it, which races without the lock (issue #47).
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Now()} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	c.mu.Lock()
	deadline := c.now.Add(d)
	c.mu.Unlock()
	go func() {
		for {
			time.Sleep(time.Millisecond)
			c.mu.Lock()
			now, due := c.now, !c.now.Before(deadline)
			c.mu.Unlock()
			if due {
				ch <- now
				return
			}
		}
	}()
	return ch
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// fakeSource serves scripted confirmed memories per project.
type fakeSource struct {
	items map[string][]Memory
	err   error
}

func (s *fakeSource) ListMemories(_ context.Context, projectID string) ([]Memory, error) {
	if s.err != nil {
		return nil, s.err
	}
	return append([]Memory(nil), s.items[projectID]...), nil
}

// fakeFiles is a map-backed FileStore: missing paths error like ENOENT.
type fakeFiles struct {
	files     map[string]string
	readErr   map[string]error
	writeErr  map[string]error
	writes    []string
	readCount int
}

func newFakeFiles() *fakeFiles {
	return &fakeFiles{files: map[string]string{}, readErr: map[string]error{}, writeErr: map[string]error{}}
}

func (f *fakeFiles) ReadFile(path string) ([]byte, error) {
	f.readCount++
	if err, ok := f.readErr[path]; ok {
		return nil, err
	}
	s, ok := f.files[path]
	if !ok {
		return nil, errors.New("file not found")
	}
	return []byte(s), nil
}

func (f *fakeFiles) WriteFile(path string, content []byte) error {
	if err, ok := f.writeErr[path]; ok {
		return err
	}
	f.files[path] = string(content)
	f.writes = append(f.writes, path)
	return nil
}

func testSetup() (*fakeClock, *fakeSource, *fakeFiles, *Materializer) {
	clk := newFakeClock()
	src := &fakeSource{items: map[string][]Memory{}}
	files := newFakeFiles()
	m, err := New(src, files, clk, DefaultDebounce)
	if err != nil {
		panic(err)
	}
	return clk, src, files, m
}

func sampleMemories() []Memory {
	return []Memory{
		{Key: "testing/framework", Content: "The team uses pytest with fixture-based setup.", Scope: "decision", Level: "project", Confidence: 0.95},
		{Key: "security/auth", Content: "All APIs must use JWT authentication.", Scope: "constraint", Level: "organization", Confidence: 1.0},
		{Key: "style/errors", Content: "Prefer detailed error messages with full stack context.", Scope: "preference", Level: "personal", Confidence: 0.9},
	}
}

func TestHandleEventFiltersTypes(t *testing.T) {
	_, _, _, m := testSetup()
	m.AddTarget(Target{ProjectID: "p1", OutputPath: ".cursorrules", ContextBudget: 4000})
	m.HandleEvent(Event{Type: "MEMORY_PROPOSED", ProjectID: "p1"})
	m.HandleEvent(Event{Type: "FILE_MODIFIED", ProjectID: "p1"})
	m.HandleEvent(Event{Type: EventMemoryConfirmed, ProjectID: "  "})
	if got := m.PendingCount(); got != 0 {
		t.Fatalf("unrelated/blank events must not mark dirty, got %d pending", got)
	}
	m.HandleEvent(Event{Type: EventMemoryConfirmed, ProjectID: "p1"})
	m.HandleEvent(Event{Type: EventMemorySuperseded, ProjectID: "p1"})
	if got := m.PendingCount(); got != 1 {
		t.Fatalf("confirmed+superseded for one project coalesce to 1 pending, got %d", got)
	}
}

func TestDebounceCoalescing(t *testing.T) {
	clk, src, files, m := testSetup()
	src.items["p1"] = sampleMemories()
	m.AddTarget(Target{ProjectID: "p1", OutputPath: ".cursorrules", ContextBudget: 4000})

	// Burst of 3 events, 1s apart: each resets the quiet timer.
	m.HandleEvent(Event{Type: EventMemoryConfirmed, ProjectID: "p1"})
	clk.advance(time.Second)
	m.HandleEvent(Event{Type: EventMemoryConfirmed, ProjectID: "p1"})
	clk.advance(time.Second)
	m.HandleEvent(Event{Type: EventMemorySuperseded, ProjectID: "p1"})

	// 4s after the last event: still quiet-violated, no write.
	clk.advance(4 * time.Second)
	if err := m.FlushDue(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if len(files.writes) != 0 {
		t.Fatalf("flushed before 5s quiet: %d writes", len(files.writes))
	}
	if got := m.PendingCount(); got != 1 {
		t.Fatalf("early flush must keep pending, got %d", got)
	}

	// 1s more (5s of quiet): exactly one regen for the whole burst.
	clk.advance(time.Second)
	if err := m.FlushDue(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if len(files.writes) != 1 {
		t.Fatalf("burst must coalesce to 1 write, got %d", len(files.writes))
	}
	if got := m.PendingCount(); got != 0 {
		t.Fatalf("flush must clear pending, got %d", got)
	}
	content := files.files[".cursorrules"]
	for _, mem := range sampleMemories() {
		if !strings.Contains(content, mem.Key) {
			t.Errorf("regenerated file missing key %q", mem.Key)
		}
	}
}

func TestDelimiterPreserve(t *testing.T) {
	clk, src, files, m := testSetup()
	src.items["p1"] = sampleMemories()
	m.AddTarget(Target{ProjectID: "p1", OutputPath: ".github/copilot-instructions.md", ContextBudget: 4000})

	userTop := "# My project notes\n\nNever delete this line.\n"
	userBottom := "\nMy local runbook: make dev.\n"
	files.files[".github/copilot-instructions.md"] = userTop + WrapManaged("stale body\n") + userBottom

	m.HandleEvent(Event{Type: EventMemoryConfirmed, ProjectID: "p1"})
	clk.advance(6 * time.Second)
	if err := m.FlushDue(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	got := files.files[".github/copilot-instructions.md"]
	if !strings.Contains(got, "Never delete this line.") {
		t.Error("user content before managed block was not preserved")
	}
	if !strings.Contains(got, "My local runbook: make dev.") {
		t.Error("user content after managed block was not preserved")
	}
	if strings.Contains(got, "stale body") {
		t.Error("stale managed body was not replaced")
	}
	if !strings.Contains(got, BeginMarker) || !strings.Contains(got, EndMarker) {
		t.Error("delimiters missing after regen")
	}
	if c := strings.Count(got, BeginMarker); c != 1 {
		t.Errorf("expected exactly 1 managed block, got %d", c)
	}
	// Second regen must be idempotent: byte-identical output.
	before := got
	m.HandleEvent(Event{Type: EventMemoryConfirmed, ProjectID: "p1"})
	clk.advance(6 * time.Second)
	if err := m.FlushDue(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if files.files[".github/copilot-instructions.md"] != before {
		t.Error("regen is not idempotent")
	}
}

func TestMergeManagedAppendAndHalfSection(t *testing.T) {
	// No delimiters: append, keep user notes.
	merged := MergeManaged("# notes\nkeep me\n", "fresh\n")
	if !strings.Contains(merged, "keep me") {
		t.Error("append path dropped user content")
	}
	if !strings.Contains(merged, BeginMarker) || !strings.Contains(merged, "fresh") {
		t.Error("append path missing managed block")
	}
	// Empty file: managed-only.
	if got := MergeManaged("", "x\n"); strings.TrimSpace(got) != strings.TrimSpace(WrapManaged("x\n")) {
		t.Error("empty file should yield managed-only content")
	}
	// Half section (BEGIN without END): preserved as user content, new block appended.
	half := "notes\n" + BeginMarker + "\nhand-written\n"
	got := MergeManaged(half, "fresh\n")
	if !strings.Contains(got, "hand-written") {
		t.Error("half-section user text must be preserved")
	}
	if c := strings.Count(got, "fresh"); c != 1 {
		t.Errorf("expected fresh block appended once, got %d", c)
	}
}

func TestBudgetTruncation(t *testing.T) {
	var mems []Memory
	for i := 0; i < 20; i++ {
		mems = append(mems, Memory{Key: "k", Content: strings.Repeat("x", 100), Scope: "fact"})
	}
	body, included, dropped := RenderMemories(mems, "markdown", 500)
	if len(body) > 500 {
		t.Errorf("body %d chars exceeds 500 budget", len(body))
	}
	if included+dropped != len(mems) {
		t.Errorf("included(%d)+dropped(%d) != %d", included, dropped, len(mems))
	}
	if dropped == 0 {
		t.Error("expected drops under a tight budget")
	}
	// Whole-item granularity: never a cut mid-line.
	if body != "" && !strings.HasSuffix(body, "\n") {
		t.Error("body must end on an item boundary")
	}
	for _, line := range strings.Split(strings.TrimRight(body, "\n"), "\n") {
		if !strings.HasPrefix(line, "- **") {
			t.Errorf("truncated body holds a partial line: %q", line)
		}
	}
	// Text format renders plain lines.
	textBody, _, _ := RenderMemories(mems[:1], "text", 4000)
	if strings.Contains(textBody, "**") {
		t.Errorf("text format must not use markdown bold: %q", textBody)
	}
}

func TestSupersededRemoval(t *testing.T) {
	clk, src, files, m := testSetup()
	src.items["p1"] = sampleMemories()
	m.AddTarget(Target{ProjectID: "p1", OutputPath: ".cursorrules", ContextBudget: 4000})

	m.HandleEvent(Event{Type: EventMemoryConfirmed, ProjectID: "p1"})
	clk.advance(6 * time.Second)
	if err := m.FlushDue(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if !strings.Contains(files.files[".cursorrules"], "testing/framework") {
		t.Fatal("precondition: initial regen must contain the key")
	}

	// Supersede: source no longer lists the item; the event triggers regen
	// that must drop it — no targeted delete needed.
	src.items["p1"] = []Memory{
		{Key: "security/auth", Content: "All APIs must use JWT authentication.", Scope: "constraint"},
		{Key: "style/errors", Content: "Prefer detailed error messages.", Scope: "preference"},
	}
	m.HandleEvent(Event{Type: EventMemorySuperseded, ProjectID: "p1", MemoryID: "testing/framework"})
	clk.advance(6 * time.Second)
	if err := m.FlushDue(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	got := files.files[".cursorrules"]
	if strings.Contains(got, "testing/framework") {
		t.Error("superseded memory still present after regen")
	}
	if !strings.Contains(got, "security/auth") {
		t.Error("surviving memories lost during superseded regen")
	}
}

func TestSplitManagedRoundTrip(t *testing.T) {
	before, after := "head\n", "\ntail\n"
	existing := before + BeginMarker + "\nbody\n" + EndMarker + after
	b, mid, a, found := SplitManaged(existing)
	if !found || b != before || a != after || strings.TrimSpace(mid) != "body" {
		t.Fatalf("split mismatch: found=%v before=%q mid=%q after=%q", found, b, mid, a)
	}
	if _, _, _, found := SplitManaged("no markers here"); found {
		t.Error("markerless content must report found=false")
	}
}

func TestDebounceDefaultAndValidation(t *testing.T) {
	clk := newFakeClock()
	files := newFakeFiles()
	src := &fakeSource{items: map[string][]Memory{}}
	m, err := New(src, files, clk, 0)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if m.debounce != DefaultDebounce {
		t.Errorf("zero debounce should select %v, got %v", DefaultDebounce, m.debounce)
	}
	if _, err := New(nil, files, clk, 0); err == nil {
		t.Error("nil source must fail")
	}
	if _, err := New(src, nil, clk, 0); err == nil {
		t.Error("nil files must fail")
	}
	// Nil clock selects SystemClock.
	m2, err := New(src, files, nil, 0)
	if err != nil || m2.clock == nil {
		t.Errorf("nil clock should select SystemClock: %v", err)
	}
	// Blank target registration is ignored.
	m.AddTarget(Target{ProjectID: "", OutputPath: ""})
	if len(m.targets) != 0 {
		t.Error("blank target must be ignored")
	}
}
