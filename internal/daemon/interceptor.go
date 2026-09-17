// Passive tool-call interceptor for the workspace daemon (issue #4).
//
// Every tool call that flows through the daemon is silently logged as an
// event for the server. The agent has no idea this is happening: emission is
// fire-and-forget from the caller's perspective — a nil sink is a no-op, a
// panicking sink is isolated via recover, and the sink itself is expected to
// be non-blocking (queue + background sender) once the daemon core injects
// the real server client. The interceptor never fails the underlying tool
// call.
//
// Event types transcribe implementation-plan.md §§1.3 (interceptor table)
// and 2.1 (event store), Layer 1 (tool interception) side:
//
//	FILE_READ        file_read       path, size, first 200 chars
//	FILE_MODIFIED    file_write      path, diff (before/after), size
//	COMMAND_EXECUTED command_run     cmdline, exit code, stdout/stderr (4KB cap each)
//	GIT_DIFF_VIEWED  git diff        ref, diff stat
//	GIT_COMMITTED    git commit      SHA, message, diff stat
//	(see watcher.go for INSTRUCTION_FILE_CHANGED, Layer 3)
//
// All diff/hash/cap helpers are pure functions over strings/bytes so they
// are unit-testable without filesystem or network access.
package daemon

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Event type constants (plan §2.1). Kept as plain strings so the sink
// payload stays JSON-serializable without a translation layer.
const (
	EventFileRead               = "FILE_READ"
	EventFileModified           = "FILE_MODIFIED"
	EventCommandExecuted        = "COMMAND_EXECUTED"
	EventGitDiffViewed          = "GIT_DIFF_VIEWED"
	EventGitCommitted           = "GIT_COMMITTED"
	EventInstructionFileChanged = "INSTRUCTION_FILE_CHANGED"
)

// Emission and payload caps.
const (
	// MaxEventOutputBytes caps COMMAND_EXECUTED stdout/stderr at 4KB each
	// (plan §1.3: "stdout/stderr (capped 4KB)").
	MaxEventOutputBytes = 4 << 10
	// MaxReadHeadChars caps the FILE_READ content preview at 200 chars
	// (plan §1.3: "first 200 chars").
	MaxReadHeadChars = 200
	// MaxEventDiffBytes caps rendered diffs so a huge file rewrite cannot
	// blow up the event payload or daemon memory.
	MaxEventDiffBytes = 8 << 10
	// MaxDiffInputLines bounds each side of DiffLines; beyond it the diff
	// degrades to a one-line summary instead of an O(m*n) LCS table.
	MaxDiffInputLines = 2000
	// maxDiffCells bounds the LCS dynamic-programming table.
	maxDiffCells = 250_000
)

// Note on EventSink: the passive-emission contract
// (type EventSink func(eventType string, payload map[string]any)) is
// declared once for the package in harvester.go (Layer 2, Wave 1 #5) with
// the identical signature this issue specified. It is deliberately NOT
// redeclared here — Go forbids duplicate top-level definitions. Everything
// below takes or returns that shared EventSink: the daemon core injects the
// real server client later without touching this file, e.g.:
//
//	daemon.NewInterceptor(func(t string, p map[string]any) {
//	    client.Enqueue(t, p) // non-blocking queue + background sender
//	})
//
// A nil EventSink is a valid no-op sink (local-only mode, tests).
// Follow-up: hoist EventSink into a shared daemon file (e.g. events.go)
// once Wave 1 lands, as harvester.go's own comment already anticipates.

// Interceptor wraps daemon tool calls with silent event emission.
type Interceptor struct {
	Sink EventSink
}

// NewInterceptor builds an Interceptor around sink (nil allowed).
func NewInterceptor(sink EventSink) *Interceptor { return &Interceptor{Sink: sink} }

// Emit delivers one event. It is nil-receiver and nil-sink safe, and it
// recovers from sink panics: passive observation must never break the tool
// call it observes. Emit is synchronous and ordered; the injected sink is
// responsible for staying non-blocking (see EventSink).
func (i *Interceptor) Emit(eventType string, payload map[string]any) {
	if i == nil || i.Sink == nil {
		return
	}
	defer func() { _ = recover() }()
	i.Sink(eventType, payload)
}

// OnFileRead emits FILE_READ {path, size, head}. head should be the first
// MaxReadHeadChars of the file content; HeadChars builds it.
func (i *Interceptor) OnFileRead(path string, size int64, head string) {
	i.Emit(EventFileRead, FileReadPayload(path, size, head))
}

// OnFileWrite emits FILE_MODIFIED {path, size, diff} by diffing oldContent
// against newContent. It is called after a successful write; file mods are
// therefore emitted without client awareness — the writer never opts in.
func (i *Interceptor) OnFileWrite(path string, oldContent, newContent []byte) {
	i.Emit(EventFileModified, FileModifiedPayload(path, oldContent, newContent))
}

// OnCommand emits COMMAND_EXECUTED {cmdline, exit_code, stdout, stderr}
// with both streams capped at MaxEventOutputBytes.
func (i *Interceptor) OnCommand(cmdline string, exitCode int, stdout, stderr []byte) {
	i.Emit(EventCommandExecuted, CommandPayload(cmdline, exitCode, stdout, stderr))
}

// OnGitDiff emits GIT_DIFF_VIEWED {ref, stat}.
func (i *Interceptor) OnGitDiff(ref, stat string) {
	i.Emit(EventGitDiffViewed, GitDiffPayload(ref, stat))
}

// OnGitCommit emits GIT_COMMITTED {hash, message, stat}. Daemon core detects
// commits via heartbeat (HEAD change) and calls this to record them.
func (i *Interceptor) OnGitCommit(hash, message, stat string) {
	i.Emit(EventGitCommitted, GitCommitPayload(hash, message, stat))
}

// FileReadPayload builds the FILE_READ payload (pure).
func FileReadPayload(path string, size int64, head string) map[string]any {
	return map[string]any{
		"path": path,
		"size": size,
		"head": HeadChars(head, MaxReadHeadChars),
	}
}

// FileModifiedPayload builds the FILE_MODIFIED payload (pure). The diff is
// capped at MaxEventDiffBytes; identical content yields an empty diff but
// the write itself is still recorded.
func FileModifiedPayload(path string, oldContent, newContent []byte) map[string]any {
	diff, truncated := CapString(DiffLines(string(oldContent), string(newContent)), MaxEventDiffBytes)
	return map[string]any{
		"path":      path,
		"size":      int64(len(newContent)),
		"old_size":  int64(len(oldContent)),
		"diff":      diff,
		"truncated": truncated,
	}
}

// CommandPayload builds the COMMAND_EXECUTED payload (pure). stdout and
// stderr are each capped at MaxEventOutputBytes with explicit flags.
func CommandPayload(cmdline string, exitCode int, stdout, stderr []byte) map[string]any {
	out, outTrunc := CapBytes(stdout, MaxEventOutputBytes)
	errBytes, errTrunc := CapBytes(stderr, MaxEventOutputBytes)
	return map[string]any{
		"cmdline":          cmdline,
		"exit_code":        exitCode,
		"stdout":           string(out),
		"stderr":           string(errBytes),
		"stdout_truncated": outTrunc,
		"stderr_truncated": errTrunc,
	}
}

// GitCommitPayload builds the GIT_COMMITTED payload (pure).
func GitCommitPayload(hash, message, stat string) map[string]any {
	return map[string]any{
		"hash":    hash,
		"message": message,
		"stat":    stat,
	}
}

// GitDiffPayload builds the GIT_DIFF_VIEWED payload (pure).
func GitDiffPayload(ref, stat string) map[string]any {
	return map[string]any{
		"ref":  ref,
		"stat": stat,
	}
}

// JoinCmdline renders cmd + args as a single shell-readable command line,
// quoting args that contain whitespace or quotes (pure).
func JoinCmdline(cmd string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, quoteArg(cmd))
	for _, a := range args {
		parts = append(parts, quoteArg(a))
	}
	return strings.Join(parts, " ")
}

func quoteArg(s string) string {
	if s == "" {
		return `""`
	}
	if !strings.ContainsAny(s, " \t\n\"'") {
		return s
	}
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
}

// CapBytes truncates b to at most limit bytes, backing off over UTF-8
// continuation bytes so the result stays valid UTF-8 (pure). It reports
// whether truncation happened.
func CapBytes(b []byte, limit int) ([]byte, bool) {
	if limit < 0 {
		limit = 0
	}
	if len(b) <= limit {
		return b, false
	}
	cut := limit
	for cut > 0 && !utf8.Valid(b[:cut]) {
		cut--
	}
	return b[:cut], true
}

// CapString truncates s to at most limit bytes with a "[truncated]" marker
// on truncation (pure). Marker text matches commands.go's convention.
func CapString(s string, limit int) (string, bool) {
	capped, truncated := CapBytes([]byte(s), limit)
	if !truncated {
		return s, false
	}
	return string(capped) + "\n... [truncated]", true
}

// HeadChars returns the first n runes of s (pure). Rune-based (not byte)
// so the FILE_READ preview is always exactly bounded in visible chars.
func HeadChars(s string, n int) string {
	if n < 0 {
		n = 0
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// DiffLines renders a line-oriented diff between oldText and newText (pure):
//
//	"  ctx"  unchanged line
//	"- old"  removed line
//	"+ new"  added line
//
// CRLF is normalized to LF before comparison so Windows-edited files diff
// cleanly. Identical inputs yield "". Oversized inputs degrade to a one-line
// summary instead of an O(m*n) LCS table; the rendered diff is capped at
// MaxEventDiffBytes with a truncation marker.
func DiffLines(oldText, newText string) string {
	// Normalize first so a pure line-ending rewrite yields "" (no event
	// noise for a zero-content change); splitDiffLines repeats the
	// normalization idempotently.
	oldText = strings.ReplaceAll(oldText, "\r\n", "\n")
	newText = strings.ReplaceAll(newText, "\r\n", "\n")
	if oldText == newText {
		return ""
	}
	a := splitDiffLines(oldText)
	b := splitDiffLines(newText)
	if len(a) > MaxDiffInputLines || len(b) > MaxDiffInputLines ||
		int64(len(a))*int64(len(b)) > maxDiffCells {
		return fmt.Sprintf("(diff omitted: %d -> %d lines, too large)", len(a), len(b))
	}
	ops := diffOps(lcsTable(a, b), a, b)
	var sb strings.Builder
	for _, op := range ops {
		switch op.kind {
		case diffSame:
			sb.WriteString("  " + op.line + "\n")
		case diffDel:
			sb.WriteString("- " + op.line + "\n")
		case diffAdd:
			sb.WriteString("+ " + op.line + "\n")
		}
	}
	out := sb.String()
	if len(out) > MaxEventDiffBytes {
		cut := MaxEventDiffBytes
		for cut > 0 && !utf8.ValidString(out[:cut]) {
			cut--
		}
		out = out[:cut] + "\n... [diff truncated]"
	}
	return out
}

func splitDiffLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	// A trailing newline terminates the last line; it is not an extra line.
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

type diffKind int

const (
	diffSame diffKind = iota
	diffDel
	diffAdd
)

type diffOp struct {
	kind diffKind
	line string
}

// lcsTable builds the classic LCS length table for a/b.
func lcsTable(a, b []string) [][]int {
	m, n := len(a), len(b)
	t := make([][]int, m+1)
	for i := range t {
		t[i] = make([]int, n+1)
	}
	for i := m - 1; i >= 0; i-- {
		for j := n - 1; j >= 0; j-- {
			if a[i] == b[j] {
				t[i][j] = t[i+1][j+1] + 1
			} else if t[i+1][j] >= t[i][j+1] {
				t[i][j] = t[i+1][j]
			} else {
				t[i][j] = t[i][j+1]
			}
		}
	}
	return t
}

// diffOps backtracks the LCS table into an edit script. Deletions sort
// before additions at the same position (stable, deterministic output).
func diffOps(t [][]int, a, b []string) []diffOp {
	var ops []diffOp
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			ops = append(ops, diffOp{diffSame, a[i]})
			i++
			j++
		case t[i+1][j] >= t[i][j+1]:
			ops = append(ops, diffOp{diffDel, a[i]})
			i++
		default:
			ops = append(ops, diffOp{diffAdd, b[j]})
			j++
		}
	}
	for ; i < len(a); i++ {
		ops = append(ops, diffOp{diffDel, a[i]})
	}
	for ; j < len(b); j++ {
		ops = append(ops, diffOp{diffAdd, b[j]})
	}
	return ops
}
