// Package daemon implements the local workspace daemon.
//
// interceptor.go is Layer 1 (Tool Interception) of the passive extraction
// pipeline (implementation-plan.md §1.3, §2.2): every tool call that flows
// through the daemon is silently logged as an event. The agent has no idea
// this is happening.
//
// Ownership note: daemon.go / fileops.go are owned by another agent. This
// file therefore defines its own minimal ToolEventEmitter interface and its own
// ToolEvent type, and coordinates with the rest of the daemon via async channels
// and file existence checks — never by editing foreign files. Importing
// internal/store here would also be cycle-free (store does not import
// daemon), but the interceptor deliberately stays dependency-free so unit
// tests and the daemon skeleton compile with stdlib only.
package daemon

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"central-memory/internal/scan"
)

// RedactedPlaceholder replaces secret matches in event payloads (issue #32).
// Payloads are redacted, never dropped: the event is still emitted so the
// Layer-1 stream stays complete, but the secret bytes never reach the store.
const RedactedPlaceholder = "[REDACTED]"

// RedactSecrets replaces every internal/scan NeverPattern match in s with
// RedactedPlaceholder (pure). It reports whether anything was redacted.
// A no-match input is returned unchanged.
func RedactSecrets(s string) (string, bool) {
	redacted := false
	out := s
	for _, re := range scan.NeverPatterns {
		if re.MatchString(out) {
			out = re.ReplaceAllString(out, RedactedPlaceholder)
			redacted = true
		}
	}
	return out, redacted
}

// redact always returns the redacted string, discarding the hit flag.
func redact(s string) string {
	out, _ := RedactSecrets(s)
	return out
}

// DiffStat summarizes a diff for GIT_DIFF_VIEWED-style stat fields (pure):
// "<added> added / <removed> removed / <n> lines, <m> bytes". Counts ignore
// the "+++"/"---" file-header lines. Empty diffs report "(empty diff)".
func DiffStat(diff string) string {
	if diff == "" {
		return "(empty diff)"
	}
	var added, removed int
	lines := splitLines(diff)
	for _, ln := range lines {
		switch {
		case strings.HasPrefix(ln, "+++"), strings.HasPrefix(ln, "---"):
			// File headers, not content lines.
		case strings.HasPrefix(ln, "+"):
			added++
		case strings.HasPrefix(ln, "-"):
			removed++
		}
	}
	return fmt.Sprintf("%d added / %d removed / %d lines, %d bytes", added, removed, len(lines), len(diff))
}

// ToolEventType is the wire-visible type of a daemon-emitted event.
// Names match the event store vocabulary (plan §2.1).
type ToolEventType string

const (
	ToolEventFileRead               ToolEventType = "FILE_READ"
	ToolEventFileModified           ToolEventType = "FILE_MODIFIED"
	ToolEventCommandExecuted        ToolEventType = "COMMAND_EXECUTED"
	ToolEventGitDiffViewed          ToolEventType = "GIT_DIFF_VIEWED"
	ToolEventGitCommitted           ToolEventType = "GIT_COMMITTED"
	ToolEventInstructionFileChanged ToolEventType = "INSTRUCTION_FILE_CHANGED"
)

// Tool action names accepted by LogAction. They mirror the daemon operation
// names from plan §1.3 so call sites in fileops.go/commands.go/gitops.go can
// pass through their op name verbatim.
const (
	ActionFileRead    = "file_read"
	ActionFileWrite   = "file_write"
	ActionFileModifed = "file_modified" // accepted alias of file_write
	ActionCommandRun  = "command_run"
	ActionGitDiff     = "git_diff"
	ActionGitCommit   = "git_commit"
)

// Payload size caps. Rationale is documented in
// docs/decisions/2026-09-17-interceptor-watcher.md.
const (
	// MaxPreviewBytes caps the FILE_READ preview ("first 200 chars", plan §1.3).
	MaxPreviewBytes = 200
	// MaxOutputBytes caps COMMAND_EXECUTED stdout/stderr (issue #4: 4KB cap).
	MaxOutputBytes = 4 * 1024
	// MaxDiffBytes caps FILE_MODIFIED / GIT_* diffs so a huge generated file
	// or vendored lockfile cannot blow up the events table.
	MaxDiffBytes = 8 * 1024
	// MaxMessageBytes caps commit messages / command lines in payloads.
	MaxMessageBytes = 2 * 1024
	// DefaultToolEventBuffer is the default async queue depth.
	DefaultToolEventBuffer = 256
)

// ToolEvent is the daemon-local event envelope. It mirrors store.Event's
// essential fields without importing internal/store, keeping this file
// stdlib-only and free of import cycles with daemon.go/fileops.go owners.
// (Named ToolEvent — not Event — because Layer 2's harvester.go already owns
// the `Event` identifier in this package.)
type ToolEvent struct {
	Type      ToolEventType  `json:"event_type"`
	Payload   map[string]any `json:"payload"`
	CreatedAt time.Time      `json:"created_at"`
}

// ToolEventEmitter is the minimal sink interface for daemon events. The real
// daemon wires this to the server POST path; tests use a fake or ChanEmitter.
// Defined locally (not imported) so parallel agents can implement it without
// touching this file's owners.
type ToolEventEmitter interface {
	Emit(ev ToolEvent)
}

// ChanEmitter is a non-blocking ToolEventEmitter backed by a channel. Emits never
// block the tool-call hot path: when the buffer is full the event is dropped
// and counted instead of stalling the agent's file/command operation.
type ChanEmitter struct {
	Ch      chan ToolEvent
	dropped atomic.Int64
}

// NewChanEmitter returns a ChanEmitter with the given buffer depth.
func NewChanEmitter(buffer int) *ChanEmitter {
	if buffer <= 0 {
		buffer = DefaultToolEventBuffer
	}
	return &ChanEmitter{Ch: make(chan ToolEvent, buffer)}
}

// Emit enqueues ev without blocking; drops + counts when full.
func (c *ChanEmitter) Emit(ev ToolEvent) {
	select {
	case c.Ch <- ev:
	default:
		c.dropped.Add(1)
	}
}

// Dropped reports how many events were shed under backpressure.
func (c *ChanEmitter) Dropped() int64 { return c.dropped.Load() }

// Interceptor logs tool calls as events and forwards them asynchronously.
// LogAction never blocks: it builds the ToolEvent, attempts a non-blocking send
// on the internal queue, and returns the ToolEvent either way.
type Interceptor struct {
	queue chan ToolEvent
	// downstream is optional; drained by background goroutine. Atomic so
	// SetEventSink-style swaps are race-free against the forwarder.
	downstream atomic.Pointer[ToolEventEmitter]
	fwdRunning atomic.Bool

	dropped atomic.Int64

	quit chan struct{}
	wg   sync.WaitGroup
	once sync.Once
}

// NewInterceptor creates an Interceptor. bufferSize <= 0 selects
// DefaultToolEventBuffer. downstream may be nil (events stay queued and can be
// consumed via Events()).
func NewInterceptor(bufferSize int, downstream ToolEventEmitter) *Interceptor {
	if bufferSize <= 0 {
		bufferSize = DefaultToolEventBuffer
	}
	in := &Interceptor{
		queue: make(chan ToolEvent, bufferSize),
		quit:  make(chan struct{}),
	}
	if downstream != nil {
		in.setDownstream(downstream)
	}
	return in
}

// setDownstream atomically swaps the forward sink. A nil sink pauses
// forwarding (events stay queued); the forwarder goroutine is nil-safe.
func (in *Interceptor) setDownstream(downstream ToolEventEmitter) {
	if downstream == nil {
		in.downstream.Store(nil)
		return
	}
	in.downstream.Store(&downstream)
	in.ensureForwarder()
}

// ensureForwarder starts the background drain exactly once.
func (in *Interceptor) ensureForwarder() {
	if in.fwdRunning.CompareAndSwap(false, true) {
		in.wg.Add(1)
		go in.run()
	}
}

// run drains the queue into downstream. Nil-safe: with no sink the event is
// dropped (counted) instead of dereferencing a nil interface.
func (in *Interceptor) run() {
	defer in.wg.Done()
	for {
		select {
		case <-in.quit:
			return
		case ev := <-in.queue:
			if d := in.downstream.Load(); d != nil {
				(*d).Emit(ev)
			} else {
				in.dropped.Add(1)
			}
		}
	}
}

// Close stops the background forwarder. Queued events remain readable.
func (in *Interceptor) Close() {
	in.once.Do(func() { close(in.quit) })
	in.wg.Wait()
}

// Events exposes the internal queue for consumers when no downstream sink is
// wired (tests, daemon skeleton wiring).
func (in *Interceptor) Events() <-chan ToolEvent { return in.queue }

// Dropped reports events shed because the queue was full.
func (in *Interceptor) Dropped() int64 { return in.dropped.Load() }

// emitTry is the non-blocking enqueue used by every Log path.
func (in *Interceptor) emitTry(ev ToolEvent) {
	select {
	case in.queue <- ev:
	default:
		in.dropped.Add(1)
	}
}

// LogAction builds an ToolEvent for a daemon tool action and enqueues it without
// blocking. payload keys are normalized per action (truncation/diff caps
// applied); unknown actions pass through with an empty-type-tolerant envelope
// so future ops do not break the interceptor.
//
// Accepted actions: file_read, file_write/file_modified, command_run,
// git_diff, git_commit. Returns the constructed ToolEvent.
func (in *Interceptor) LogAction(action string, payload map[string]any) ToolEvent {
	if payload == nil {
		payload = map[string]any{}
	}
	ev := ToolEvent{
		Type:      eventTypeForAction(action),
		Payload:   normalizePayload(action, payload),
		CreatedAt: time.Now().UTC(),
	}
	ev.Payload["action"] = action
	in.emitTry(ev)
	return ev
}

// eventTypeForAction maps a tool action name to its event type.
func eventTypeForAction(action string) ToolEventType {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case ActionFileRead:
		return ToolEventFileRead
	case ActionFileWrite, ActionFileModifed:
		return ToolEventFileModified
	case ActionCommandRun:
		return ToolEventCommandExecuted
	case ActionGitDiff:
		return ToolEventGitDiffViewed
	case ActionGitCommit:
		return ToolEventGitCommitted
	default:
		return ToolEventType("UNKNOWN:" + action)
	}
}

// normalizePayload applies secret redaction (issue #32) then per-action
// caps so oversized tool outputs cannot flood the event stream. Redaction
// runs before truncation so a secret straddling a cap boundary cannot leak
// a partial match. It copies the input map (never mutates caller's).
func normalizePayload(action string, payload map[string]any) map[string]any {
	out := make(map[string]any, len(payload)+2)
	for k, v := range payload {
		out[k] = v
	}
	// Secret-screen every free-text field; hashes/SHAs pass through.
	for _, k := range []string{"path", "preview", "before", "after", "diff", "output", "stdout", "stderr", "command", "message", "stat", "stats"} {
		if s, ok := out[k].(string); ok && s != "" {
			out[k] = redact(s)
		}
	}
	// Argument vectors may carry secrets (tokens, -password values).
	if args, ok := out["args"].([]string); ok {
		redacted := make([]string, len(args))
		for i, a := range args {
			redacted[i] = redact(a)
		}
		out["args"] = redacted
	}
	switch strings.ToLower(strings.TrimSpace(action)) {
	case ActionFileRead:
		if s, ok := out["preview"].(string); ok {
			out["preview"] = Truncate(s, MaxPreviewBytes)
		}
	case ActionFileWrite, ActionFileModifed:
		// Prefer an explicit diff; else synthesize from before/after.
		if d, ok := out["diff"].(string); ok {
			out["diff"] = Truncate(d, MaxDiffBytes)
			out["diff_truncated"] = len(d) > MaxDiffBytes
		} else {
			before, _ := out["before"].(string)
			after, _ := out["after"].(string)
			if before != "" || after != "" {
				diff := BuildDiff(before, after, MaxDiffBytes)
				out["diff"] = diff
				out["diff_truncated"] = diffTruncated(before, after, diff)
				delete(out, "before")
				delete(out, "after")
			}
		}
	case ActionCommandRun:
		for _, k := range []string{"output", "stdout", "stderr"} {
			if s, ok := out[k].(string); ok {
				capped := Truncate(s, MaxOutputBytes)
				out[k] = capped
				if len(s) > MaxOutputBytes {
					out[k+"_truncated"] = true
				}
			}
		}
		if s, ok := out["command"].(string); ok {
			out["command"] = Truncate(s, MaxMessageBytes)
		}
	case ActionGitDiff:
		if s, ok := out["diff"].(string); ok {
			out["diff"] = Truncate(s, MaxDiffBytes)
			out["diff_truncated"] = len(s) > MaxDiffBytes
		}
	case ActionGitCommit:
		if s, ok := out["message"].(string); ok {
			out["message"] = Truncate(s, MaxMessageBytes)
		}
		if s, ok := out["stat"].(string); ok {
			out["stat"] = Truncate(s, MaxDiffBytes)
		}
	}
	return out
}

// Truncate cuts s to at most max bytes on a UTF-8 rune boundary and appends
// a "[truncated]" marker when capped. max <= 0 returns "".
func Truncate(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !isRuneBoundary(s, cut) {
		cut--
	}
	return s[:cut] + "\n[truncated]"
}

func isRuneBoundary(s string, i int) bool {
	if i <= 0 || i >= len(s) {
		return i == len(s)
	}
	c := s[i]
	return c < 0x80 || c >= 0xC0
}

// BuildDiff renders a simple line-oriented diff between before and after,
// capped at maxBytes (Truncate marker appended when capped). Lines only in
// before get a "- " prefix, lines only in after get "+ ". Common lines are
// elided except for a short context window so instruction-file and source
// diffs stay small. Stdlib only — no external diff dependency.
func BuildDiff(before, after string, maxBytes int) string {
	if before == after {
		return ""
	}
	if maxBytes <= 0 {
		return ""
	}
	var sb strings.Builder
	beforeLines := splitLines(before)
	afterLines := splitLines(after)

	// LCS-lite via longest common prefix/suffix elision: keeps the diff
	// readable without an O(n*m) table for large files.
	pre := commonPrefixLen(beforeLines, afterLines)
	post := commonSuffixLen(beforeLines[pre:], afterLines[pre:])

	removed := beforeLines[pre : len(beforeLines)-post]
	added := afterLines[pre : len(afterLines)-post]

	writeLines := func(prefix string, lines []string) {
		for _, l := range lines {
			sb.WriteString(prefix)
			sb.WriteString(l)
			sb.WriteByte('\n')
			if sb.Len() > maxBytes {
				break
			}
		}
	}
	writeLines("- ", removed)
	writeLines("+ ", added)

	out := sb.String()
	if len(out) > maxBytes || len(removed)+len(added) > countLines(out) {
		return Truncate(out, maxBytes)
	}
	return out
}

func diffTruncated(before, after, diff string) bool {
	return strings.Contains(diff, "[truncated]")
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func commonPrefixLen(a, b []string) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	return i
}

func commonSuffixLen(a, b []string) int {
	i := 0
	for i < len(a) && i < len(b) && a[len(a)-1-i] == b[len(b)-1-i] {
		i++
	}
	return i
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

// Convenience constructors — thin wrappers over LogAction so call sites in
// fileops.go / commands.go / gitops.go stay one-liners. Each applies the
// documented caps and returns the emitted ToolEvent.

// LogFileRead records a FILE_READ event: path, size, first-200-char preview.
func (in *Interceptor) LogFileRead(path string, size int64, preview string) ToolEvent {
	return in.LogAction(ActionFileRead, map[string]any{
		"path":    path,
		"size":    size,
		"preview": preview,
	})
}

// LogFileModified records a FILE_MODIFIED event with a capped diff.
func (in *Interceptor) LogFileModified(path string, before, after string, newSize int64) ToolEvent {
	return in.LogAction(ActionFileWrite, map[string]any{
		"path":   path,
		"before": before,
		"after":  after,
		"size":   newSize,
	})
}

// LogFileModifiedDiff records a FILE_MODIFIED event when the caller already
// computed a diff string.
func (in *Interceptor) LogFileModifiedDiff(path, diff string, newSize int64) ToolEvent {
	return in.LogAction(ActionFileWrite, map[string]any{
		"path": path,
		"diff": diff,
		"size": newSize,
	})
}

// LogCommand records a COMMAND_EXECUTED event; output capped at 4KB.
func (in *Interceptor) LogCommand(command string, args []string, exitCode int, output string) ToolEvent {
	return in.LogAction(ActionCommandRun, map[string]any{
		"command":   command,
		"args":      args,
		"exit_code": exitCode,
		"output":    output,
	})
}

// LogGitDiff records a GIT_DIFF_VIEWED event with capped diff/stats.
func (in *Interceptor) LogGitDiff(ref, diff, stats string) ToolEvent {
	return in.LogAction(ActionGitDiff, map[string]any{
		"ref":   ref,
		"diff":  diff,
		"stats": stats,
	})
}

// LogGitCommitted records a GIT_COMMITTED event (detected via heartbeat per
// plan §1.3): commit SHA, message, diff stat.
func (in *Interceptor) LogGitCommitted(sha, message, stat string) ToolEvent {
	return in.LogAction(ActionGitCommit, map[string]any{
		"commit":  sha,
		"message": message,
		"stat":    stat,
	})
}
