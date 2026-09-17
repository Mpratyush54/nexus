// Package materializer regenerates push-model agent instruction files from
// confirmed project memories (issue #16, plan §4.2).
//
// Push vs pull: live MCP agents (Claude, OpenCode) pull memory on demand via
// memory_search. File-based agents (Copilot, Cursor, Windsurf) cannot call
// MCP — they read static instruction files (e.g.
// .github/copilot-instructions.md, .cursorrules). The materializer bridges
// the gap: it subscribes to MEMORY_CONFIRMED / MEMORY_SUPERSEDED, waits for
// a 5-second quiet period (debounce), re-renders the project's confirmed
// memories within each target's context budget, and writes the result into
// a managed section of each target file, preserving user content outside
// the delimiters.
//
// DECOUPLING NOTE: this package does NOT import internal/store (no event
// bus, no memory rows). It imports internal/daemon ONLY for
// daemon.ResolveInSandbox, used as a fail-fast config check in AddTarget
// when a sandbox root is set (issue #41) — no sandbox writes happen here;
// all file I/O still goes through the injected FileStore, which remains
// the enforcement point (the daemon injects its sandboxed fileops behind
// FileWriter at the boundary). Safe from cycles: internal/daemon imports
// only internal/scan (+ internal/project in harvester.go), never this
// package. See ADR-016, ADR-041.
//
// Data flow:
//
//	store event bus → (adapter) → HandleEvent → pending[project] = now
//	                                                  │ 5s quiet
//	                                                  ▼
//	MemorySource.ListMemories → RenderMemories (budget) → MergeManaged
//	                                                     (delimiters)
//	                                                  → FileWriter.WriteFile
//
// Concurrency: Materializer holds only interfaces plus a mutex-guarded
// pending/target map, so one shared instance is safe for concurrent
// HandleEvent callers. Regeneration itself is synchronous (Flush /
// Regenerate); Run adds a background ticker loop for production.
package materializer

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"central-memory/internal/daemon"
)

// Event types this package subscribes to (plan §4.2). Declared locally —
// NOT imported from internal/store — so the materializer never depends on
// the store event bus (see package doc). Values match the store registry
// (events.go) exactly.
const (
	EventMemoryConfirmed  = "MEMORY_CONFIRMED"
	EventMemorySuperseded = "MEMORY_SUPERSEDED"
)

// DefaultDebounce is the plan §4.2 quiet period: regeneration fires only
// after 5 seconds with no new MEMORY_CONFIRMED / MEMORY_SUPERSEDED for
// that project. Rapid confirmation bursts coalesce into one write.
const DefaultDebounce = 5 * time.Second

// DefaultBudgetChars caps a rendered managed block when a target carries
// no explicit budget (~1000 tokens, same scale as context.DefaultBudgetChars).
const DefaultBudgetChars = 4000

// Managed-section delimiters (plan §4.2). The begin marker carries the
// DO NOT EDIT warning; the file watcher treats edits OUTSIDE the managed
// section as new memory input (passive extraction), while edits inside
// are overwritten on the next regeneration.
const (
	BeginMarker = "<!-- BEGIN CENTRAL MEMORY — DO NOT EDIT -->"
	EndMarker   = "<!-- END CENTRAL MEMORY -->"
)

// Event is the minimal memory-lifecycle signal the materializer consumes.
// The daemon/server adapter maps store.Event onto this struct at the
// boundary; MemoryID is informational only (regeneration re-lists the
// whole project, so superseded items vanish without targeted deletes).
type Event struct {
	Type      string
	ProjectID string
	MemoryID  string
}

// EventHandler is the subscription seam: any event bus (store Subscribe,
// tests, fakes) delivers memory events through it. Materializer implements
// it via HandleEvent.
type EventHandler interface {
	HandleEvent(ev Event)
}

// Memory is one confirmed project memory as rendered into instruction
// files. It mirrors the fields the renderer needs — NOT store.MemoryItem
// (no store import; the caller maps at the boundary).
type Memory struct {
	Key        string
	Content    string
	Scope      string
	Level      string
	Confidence float64
}

// Target is one push-model agent file for a project — the Go form of one
// agents-table row (plan §4.1: output_file, output_format, context_budget)
// without importing the store agents package.
type Target struct {
	// ProjectID scopes the memories rendered into this file.
	ProjectID string
	// OutputPath is the workspace-relative instruction file
	// (e.g. ".github/copilot-instructions.md", ".cursorrules").
	OutputPath string
	// ContextBudget caps the managed block in chars (<=0 → DefaultBudgetChars).
	ContextBudget int
	// Format selects the line template: "markdown" (default) or "text".
	Format string
}

// BudgetOrDefault clamps the target budget into a positive char cap.
func (t Target) BudgetOrDefault() int {
	if t.ContextBudget <= 0 {
		return DefaultBudgetChars
	}
	return t.ContextBudget
}

// MemorySource lists the current confirmed memories for a project. The
// production implementation queries the store; tests use a fake. Returning
// only confirmed items is the source's contract — the materializer renders
// whatever it returns, so MEMORY_SUPERSEDED removal falls out naturally:
// the superseded item is simply absent on the next list.
type MemorySource interface {
	ListMemories(ctx context.Context, projectID string) ([]Memory, error)
}

// FileReader reads a target file so MergeManaged can preserve user content
// outside the delimiters. Missing files report an error (the materializer
// then writes a managed-only file).
type FileReader interface {
	ReadFile(path string) ([]byte, error)
}

// FileWriter writes a regenerated target file. The daemon injects its
// sandboxed writer (ResolveInSandbox + secret checks, fileops.go) here;
// tests inject a map-backed fake.
type FileWriter interface {
	WriteFile(path string, content []byte) error
}

// FileStore is the combined read/write seam a Materializer holds.
type FileStore interface {
	FileReader
	FileWriter
}

// Clock abstracts time so debounce is unit-testable without sleeping:
// production injects SystemClock, tests inject a fake with a mutable now
// (and optionally a controllable After channel). After exists so Run can
// sleep via the same seam — Flush(now) tests only need Now.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

// systemClock is the production Clock.
type systemClock struct{}

func (systemClock) Now() time.Time                         { return time.Now() }
func (systemClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// SystemClock is the production Clock value to pass to New.
var SystemClock Clock = systemClock{}

// compile-time conformance: *Materializer satisfies EventHandler.
var _ EventHandler = (*Materializer)(nil)

// Materializer debounces memory events per project and regenerates push-model
// agent files. Build with New, register files with AddTarget, feed events
// via HandleEvent, and drive regeneration with Flush (deterministic,
// test hook) or Run (background ticker, production).
type Materializer struct {
	mu       sync.Mutex
	source   MemorySource
	files    FileStore
	clock    Clock
	debounce time.Duration
	targets  map[string][]Target  // projectID → targets
	pending  map[string]time.Time // projectID → last dirty time
	// sandboxRoot, when non-empty, confines AddTarget OutputPaths via
	// daemon.ResolveInSandbox (issue #41). Set with SetSandboxRoot
	// (production passes the daemon workspace root); empty keeps the
	// legacy behaviour (trim + blank check only, enforcement left to the
	// injected FileStore). Guarded by mu.
	sandboxRoot string
}

// New wires a Materializer. A nil clock selects SystemClock; a non-positive
// debounce selects DefaultDebounce. source and files must be non-nil.
func New(source MemorySource, files FileStore, clock Clock, debounce time.Duration) (*Materializer, error) {
	if source == nil {
		return nil, errors.New("materializer: nil MemorySource")
	}
	if files == nil {
		return nil, errors.New("materializer: nil FileStore")
	}
	if clock == nil {
		clock = SystemClock
	}
	if debounce <= 0 {
		debounce = DefaultDebounce
	}
	return &Materializer{
		source:   source,
		files:    files,
		clock:    clock,
		debounce: debounce,
		targets:  map[string][]Target{},
		pending:  map[string]time.Time{},
	}, nil
}

// SetSandboxRoot sets the workspace root AddTarget validates OutputPaths
// against (daemon.ResolveInSandbox: traversal escapes and absolute paths
// outside the root are rejected). Empty clears it (legacy behaviour).
// The value is cleaned lexically; it need not exist — validation is the
// same Clean + HasPrefix confinement the daemon serves.
func (m *Materializer) SetSandboxRoot(root string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if strings.TrimSpace(root) == "" {
		m.sandboxRoot = ""
		return
	}
	m.sandboxRoot = filepath.Clean(root)
}

// AddTarget registers one push-model output file. Multiple targets per
// project are allowed (e.g. copilot + cursor files for one repo). Adding a
// target does not mark the project dirty — only memory events do.
//
// It returns false (and registers nothing) when the target is blank, a
// duplicate output path for the project, or escapes the sandbox root set
// with SetSandboxRoot (traversal/absolute escapes fail fast here instead
// of surfacing as write errors at Regenerate). Existing callers ignore
// the result — registration of valid targets is unchanged.
func (m *Materializer) AddTarget(t Target) bool {
	t.ProjectID = strings.TrimSpace(t.ProjectID)
	t.OutputPath = strings.TrimSpace(t.OutputPath)
	if t.ProjectID == "" || t.OutputPath == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sandboxRoot != "" {
		if _, err := daemon.ResolveInSandbox(m.sandboxRoot, t.OutputPath); err != nil {
			return false
		}
	}
	for _, cur := range m.targets[t.ProjectID] {
		if cur.OutputPath == t.OutputPath {
			return false // idempotent: one entry per output path
		}
	}
	m.targets[t.ProjectID] = append(m.targets[t.ProjectID], t)
	return true
}

// RemoveTarget unregisters one output file. Pending state is kept: if the
// project still has targets, the next Flush regenerates them.
func (m *Materializer) RemoveTarget(projectID, outputPath string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	kept := m.targets[projectID][:0:0]
	for _, cur := range m.targets[projectID] {
		if cur.OutputPath != outputPath {
			kept = append(kept, cur)
		}
	}
	if len(kept) == 0 {
		delete(m.targets, projectID)
	} else {
		m.targets[projectID] = kept
	}
}

// TargetsFor returns the registered targets for a project (copy).
func (m *Materializer) TargetsFor(projectID string) []Target {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Target(nil), m.targets[projectID]...)
}

// HandleEvent implements EventHandler: MEMORY_CONFIRMED and
// MEMORY_SUPERSEDED mark the event's project dirty at clock.Now(); every
// other type (and events with a blank project) is ignored so unrelated
// bus traffic never triggers a write. Repeated calls reset the quiet
// timer — that reset IS the 5s debounce.
func (m *Materializer) HandleEvent(ev Event) {
	if ev.Type != EventMemoryConfirmed && ev.Type != EventMemorySuperseded {
		return
	}
	if strings.TrimSpace(ev.ProjectID) == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pending[strings.TrimSpace(ev.ProjectID)] = m.clock.Now()
}

// PendingCount reports how many projects are dirty (test seam).
func (m *Materializer) PendingCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.pending)
}

// DueProjects returns dirty projects quiet for at least the debounce as of
// now (pure selection; does not regenerate or clear state).
func (m *Materializer) DueProjects(now time.Time) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for projectID, dirty := range m.pending {
		if now.Sub(dirty) >= m.debounce {
			out = append(out, projectID)
		}
	}
	sort.Strings(out)
	return out
}

// Flush regenerates every project quiet for at least the debounce as of
// now, clearing each from pending whether regeneration succeeds or fails
// (a failing source must not wedge later events — the next event re-marks
// the project dirty). It returns one error per failed project, joined with
// errors.Join (nil when all succeed).
func (m *Materializer) Flush(now time.Time) error {
	due := m.DueProjects(now)
	var errs []error
	for _, projectID := range due {
		if err := m.Regenerate(projectID); err != nil {
			errs = append(errs, err)
		}
		m.mu.Lock()
		delete(m.pending, projectID)
		m.mu.Unlock()
	}
	return errors.Join(errs...)
}

// FlushDue is the clock-driven shorthand: Flush(m.clock.Now()).
func (m *Materializer) FlushDue() error {
	return m.Flush(m.clock.Now())
}

// Regenerate re-renders every target of one project from the source's
// current confirmed memories. Superseded items need no special casing:
// the source no longer lists them, so they drop out of the managed block.
// Projects with no targets are a no-op (nil). A read error on an existing
// file is treated as "no user content" only when the file is missing; other
// read/write errors abort that target with an error.
func (m *Materializer) Regenerate(projectID string) error {
	targets := m.TargetsFor(projectID)
	if len(targets) == 0 {
		return nil
	}
	memories, err := m.source.ListMemories(context.Background(), projectID)
	if err != nil {
		return err
	}
	var errs []error
	for _, t := range targets {
		body, _, _ := RenderMemories(memories, t.Format, t.BudgetOrDefault())
		existing, readErr := m.files.ReadFile(t.OutputPath)
		var merged string
		if readErr != nil {
			// Missing file (or unreadable): start from managed-only content.
			// A genuinely corrupt read surfaces on WriteFile instead, so
			// regeneration degrades to overwrite rather than abort.
			merged = WrapManaged(body)
		} else {
			merged = MergeManaged(string(existing), body)
		}
		if err := m.files.WriteFile(t.OutputPath, []byte(merged)); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Run ticks until ctx is done, flushing due projects on each tick. The
// tick interval defaults to a third of the debounce (≥100ms) so a project
// regenerates within ~debounce+interval of quiet — well inside the "regen
// within 5s of quiet" acceptance (worst case ≈ debounce + tick).
// Production entrypoint; tests drive Flush directly.
func (m *Materializer) Run(ctx context.Context) {
	interval := m.debounce / 3
	if interval < 100*time.Millisecond {
		interval = 100 * time.Millisecond
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.clock.After(interval):
			_ = m.FlushDue()
		}
	}
}

// RenderMemories renders memories into one agent-format body capped at
// budget chars (<=0 → DefaultBudgetChars). Items keep source order under
// whole-item granularity: an item that does not fit is skipped (counted
// dropped) and smaller later items may still fit, so output is always a
// prefix-complete list, never mid-line truncation. It returns the body
// plus included/dropped counts for stats and tests.
func RenderMemories(memories []Memory, format string, budget int) (body string, included, dropped int) {
	if budget <= 0 {
		budget = DefaultBudgetChars
	}
	var b strings.Builder
	for _, mem := range memories {
		line := renderLine(mem, format)
		if b.Len()+len(line) > budget {
			dropped++
			continue
		}
		b.WriteString(line)
		included++
	}
	return b.String(), included, dropped
}

func renderLine(mem Memory, format string) string {
	content := strings.TrimSpace(mem.Content)
	if content == "" {
		content = "(empty memory)"
	}
	key := strings.TrimSpace(mem.Key)
	if key == "" {
		key = "unnamed"
	}
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "markdown":
		var b strings.Builder
		b.WriteString("- **")
		b.WriteString(key)
		b.WriteString("**")
		if strings.TrimSpace(mem.Scope) != "" {
			b.WriteString(" (" + strings.TrimSpace(mem.Scope) + ")")
		}
		b.WriteString(": ")
		b.WriteString(content)
		b.WriteString("\n")
		return b.String()
	default: // "text" and any other push format: plain lines
		var b strings.Builder
		b.WriteString("- ")
		b.WriteString(key)
		b.WriteString(": ")
		b.WriteString(content)
		b.WriteString("\n")
		return b.String()
	}
}

// WrapManaged wraps a rendered body in the managed delimiters.
func WrapManaged(body string) string {
	var b strings.Builder
	b.WriteString(BeginMarker)
	b.WriteString("\n")
	b.WriteString(body)
	if body != "" && !strings.HasSuffix(body, "\n") {
		b.WriteString("\n")
	}
	b.WriteString(EndMarker)
	b.WriteString("\n")
	return b.String()
}

// SplitManaged splits existing file content around the managed section.
// It returns (before, managedBody, after, found): found is true only when
// BOTH delimiters are present in order; before/after are the user content
// outside them (preserved verbatim, byte-for-byte).
func SplitManaged(existing string) (before, managed, after string, found bool) {
	start := strings.Index(existing, BeginMarker)
	if start < 0 {
		return existing, "", "", false
	}
	rest := existing[start+len(BeginMarker):]
	end := strings.Index(rest, EndMarker)
	if end < 0 {
		return existing, "", "", false
	}
	before = existing[:start]
	managed = rest[:end]
	after = rest[end+len(EndMarker):]
	return before, managed, after, true
}

// MergeManaged splices a freshly rendered body into existing file content:
//   - both delimiters present → the block between them is replaced,
//     user content before/after is preserved byte-for-byte;
//   - no (or half — BEGIN without END or vice versa) managed section →
//     the wrapped block is appended (separated by one blank line when the
//     file already holds content), never deleting user notes.
//
// Malformed half-sections are deliberately left in place and treated as
// user content: deleting text the user may have written by hand would
// violate the "user notes preserved" acceptance.
func MergeManaged(existing, body string) string {
	before, _, after, found := SplitManaged(existing)
	if !found {
		wrapped := WrapManaged(body)
		if strings.TrimSpace(existing) == "" {
			return wrapped
		}
		var b strings.Builder
		b.WriteString(strings.TrimRight(existing, "\n"))
		b.WriteString("\n\n")
		b.WriteString(wrapped)
		return b.String()
	}
	var b strings.Builder
	b.WriteString(before)
	b.WriteString(BeginMarker)
	b.WriteString("\n")
	b.WriteString(body)
	if body != "" && !strings.HasSuffix(body, "\n") {
		b.WriteString("\n")
	}
	b.WriteString(EndMarker)
	b.WriteString(after)
	return b.String()
}
