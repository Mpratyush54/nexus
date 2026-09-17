package daemon

import (
	"strings"
	"sync"
	"testing"
)

// eventRecorder is a test EventSink that records every emission.
type eventRecorder struct {
	mu     sync.Mutex
	events []recordedEvent
}

type recordedEvent struct {
	typ     string
	payload map[string]any
}

func (r *eventRecorder) sink() EventSink {
	return func(t string, p map[string]any) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.events = append(r.events, recordedEvent{typ: t, payload: p})
	}
}

func (r *eventRecorder) count(typ string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, e := range r.events {
		if e.typ == typ {
			n++
		}
	}
	return n
}

func (r *eventRecorder) last(typ string) map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.events) - 1; i >= 0; i-- {
		if r.events[i].typ == typ {
			return r.events[i].payload
		}
	}
	return nil
}

func TestInterceptFileReadEvent(t *testing.T) {
	rec := &eventRecorder{}
	in := NewInterceptor(rec.sink())
	in.OnFileRead("src/main.go", 1234, strings.Repeat("x", 300))

	p := rec.last(EventFileRead)
	if p == nil {
		t.Fatal("no FILE_READ emitted")
	}
	if p["path"] != "src/main.go" {
		t.Errorf("path = %v", p["path"])
	}
	if p["size"] != int64(1234) {
		t.Errorf("size = %v", p["size"])
	}
	head, _ := p["head"].(string)
	if len([]rune(head)) != MaxReadHeadChars {
		t.Errorf("head = %d runes, want %d", len([]rune(head)), MaxReadHeadChars)
	}
}

func TestInterceptFileModifiedDiff(t *testing.T) {
	rec := &eventRecorder{}
	in := NewInterceptor(rec.sink())

	oldC := []byte("line1\nline2\nline3\n")
	newC := []byte("line1\nline2 changed\nline3\nline4\n")
	in.OnFileWrite("notes.txt", oldC, newC)

	p := rec.last(EventFileModified)
	if p == nil {
		t.Fatal("no FILE_MODIFIED emitted")
	}
	if p["path"] != "notes.txt" {
		t.Errorf("path = %v", p["path"])
	}
	if p["size"] != int64(len(newC)) || p["old_size"] != int64(len(oldC)) {
		t.Errorf("sizes = %v/%v", p["size"], p["old_size"])
	}
	diff, _ := p["diff"].(string)
	if !strings.Contains(diff, "- line2\n") || !strings.Contains(diff, "+ line2 changed\n") {
		t.Errorf("diff missing -/+ lines:\n%s", diff)
	}

	// Identical content: the write is still recorded, with an empty diff.
	in.OnFileWrite("same.txt", oldC, oldC)
	p2 := rec.last(EventFileModified)
	if p2 == nil || p2["path"] != "same.txt" {
		t.Fatal("identical write not recorded")
	}
	if d, _ := p2["diff"].(string); d != "" {
		t.Errorf("identical write diff = %q, want empty", d)
	}
}

func TestInterceptCommandCap4KB(t *testing.T) {
	rec := &eventRecorder{}
	in := NewInterceptor(rec.sink())

	bigOut := []byte(strings.Repeat("o", MaxEventOutputBytes+100))
	bigErr := []byte(strings.Repeat("e", MaxEventOutputBytes+1))
	in.OnCommand("go test ./...", 1, bigOut, bigErr)

	p := rec.last(EventCommandExecuted)
	if p == nil {
		t.Fatal("no COMMAND_EXECUTED emitted")
	}
	if p["cmdline"] != "go test ./..." {
		t.Errorf("cmdline = %v", p["cmdline"])
	}
	if p["exit_code"] != 1 {
		t.Errorf("exit_code = %v", p["exit_code"])
	}
	if out, _ := p["stdout"].(string); len(out) > MaxEventOutputBytes+len("\n... [truncated]") {
		t.Errorf("stdout not capped: %d bytes", len(out))
	}
	if p["stdout_truncated"] != true || p["stderr_truncated"] != true {
		t.Errorf("truncated flags = %v/%v, want true/true", p["stdout_truncated"], p["stderr_truncated"])
	}

	// Small output passes through unflagged.
	in.OnCommand("git status", 0, []byte("ok"), nil)
	p2 := rec.last(EventCommandExecuted)
	if p2["stdout"] != "ok" || p2["stdout_truncated"] != false || p2["stderr_truncated"] != false {
		t.Errorf("small output mangled: %v", p2)
	}
}

func TestInterceptGitCommitted(t *testing.T) {
	rec := &eventRecorder{}
	in := NewInterceptor(rec.sink())
	in.OnGitCommit("abc123", "fix: auth timeout", "2 files changed, 10 insertions(+)")

	p := rec.last(EventGitCommitted)
	if p == nil {
		t.Fatal("no GIT_COMMITTED emitted")
	}
	if p["hash"] != "abc123" || p["message"] != "fix: auth timeout" {
		t.Errorf("payload = %v", p)
	}
	if _, ok := p["stat"]; !ok {
		t.Error("stat missing")
	}
}

func TestInterceptGitDiffViewed(t *testing.T) {
	rec := &eventRecorder{}
	in := NewInterceptor(rec.sink())
	in.OnGitDiff("HEAD~1", "1 file changed")

	p := rec.last(EventGitDiffViewed)
	if p == nil {
		t.Fatal("no GIT_DIFF_VIEWED emitted")
	}
	if p["ref"] != "HEAD~1" {
		t.Errorf("ref = %v", p["ref"])
	}
}

func TestInterceptJoinCmdline(t *testing.T) {
	cases := []struct {
		cmd, want string
		args      []string
	}{
		{"git", "git status --porcelain", []string{"status", "--porcelain"}},
		{"go", `go test "./my pkg"`, []string{"test", "./my pkg"}},
		{"pytest", `pytest -k "foo bar"`, []string{"-k", "foo bar"}},
		{"cmd", `cmd ""`, []string{""}},
	}
	for _, c := range cases {
		if got := JoinCmdline(c.cmd, c.args); got != c.want {
			t.Errorf("JoinCmdline(%q,%q) = %q, want %q", c.cmd, c.args, got, c.want)
		}
	}
}

func TestInterceptCapBytesUTF8(t *testing.T) {
	// Multi-byte runes must never be split: cap inside "é" (2 bytes).
	s := "ab" + strings.Repeat("é", 10)
	capped, trunc := CapBytes([]byte(s), 5)
	if !trunc {
		t.Error("expected truncation")
	}
	if string(capped) != "abé" {
		t.Errorf("capped = %q, want %q", capped, "abé")
	}
	if _, trunc := CapBytes([]byte("abc"), 3); trunc {
		t.Error("exact-fit flagged truncated")
	}
	if _, trunc := CapBytes([]byte("abc"), 0); !trunc {
		t.Error("zero limit not flagged truncated")
	}
}

func TestInterceptHeadChars(t *testing.T) {
	if got := HeadChars("héllo", 3); got != "hél" {
		t.Errorf("HeadChars = %q", got)
	}
	if got := HeadChars("short", 100); got != "short" {
		t.Errorf("HeadChars short = %q", got)
	}
}

func TestInterceptDiffLines(t *testing.T) {
	// Identical.
	if d := DiffLines("a\nb\n", "a\nb\n"); d != "" {
		t.Errorf("identical diff = %q", d)
	}
	// Pure addition from empty (new instruction file).
	d := DiffLines("", "rule one\nrule two\n")
	if !strings.Contains(d, "+ rule one\n") || !strings.Contains(d, "+ rule two\n") {
		t.Errorf("addition diff:\n%s", d)
	}
	// Deletion keeps "-" lines; context lines are space-prefixed.
	d = DiffLines("keep\n drop\n", "keep\n")
	if !strings.Contains(d, "  keep\n") || !strings.Contains(d, "-  drop\n") {
		t.Errorf("deletion diff:\n%s", d)
	}
	// CRLF normalizes: same logical content, no diff.
	if d := DiffLines("a\r\nb\r\n", "a\nb\n"); d != "" {
		t.Errorf("CRLF diff = %q", d)
	}
	// Oversized input degrades to a summary, not an O(m*n) table.
	big := strings.Repeat("x\n", MaxDiffInputLines+1)
	if d := DiffLines(big, big+"y\n"); !strings.Contains(d, "diff omitted") {
		t.Errorf("oversize diff = %q", d)
	}
	// Rendered output is capped.
	many := ""
	for i := 0; i < 2000; i++ {
		many += strings.Repeat("z", 40) + "\n"
	}
	if d := DiffLines("", many); !strings.Contains(d, "diff truncated") {
		t.Error("large render not truncated")
	}
}

func TestSinkNilSafe(t *testing.T) {
	// Nil sink: silent no-op.
	in := NewInterceptor(nil)
	in.OnFileRead("a", 1, "x")
	in.OnFileWrite("a", nil, []byte("x"))
	in.OnCommand("c", 0, nil, nil)
	in.OnGitCommit("h", "m", "s")
	in.OnGitDiff("", "s")

	// Nil interceptor: also safe (daemon core may hold an unset pointer).
	var nilIn *Interceptor
	nilIn.OnFileRead("a", 1, "x")
	nilIn.Emit(EventFileRead, nil)
}

func TestSinkPanicIsolated(t *testing.T) {
	in := NewInterceptor(func(string, map[string]any) { panic("sink boom") })
	func() {
		defer func() {
			if recover() != nil {
				t.Fatal("sink panic propagated to caller")
			}
		}()
		in.OnFileWrite("a", []byte("old"), []byte("new"))
	}()
}

func TestHashSHA256KnownVector(t *testing.T) {
	// NIST vector: SHA256("abc").
	const want = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if got := HashString("abc"); got != want {
		t.Errorf("HashString(abc) = %s, want %s", got, want)
	}
	if HashBytes([]byte("abc")) != want {
		t.Error("HashBytes disagrees with HashString")
	}
	if HashString("abc") == HashString("abd") {
		t.Error("hash collision on distinct inputs")
	}
}
