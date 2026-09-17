package daemon

import (
	"net/http"
	"net/http/httptest"
	"os"
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

func TestInterceptRedactSecrets(t *testing.T) {
	secrets := []string{
		"token ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ1234 here",
		"key AKIAIOSFODNN7EXAMPLE leaked",
		"glpat-abc123XYZ_-def in text",
		"sk-ant-abc123XYZ-456 exposed",
		`api_key = "supersecretvalue1234567890"`,
	}
	for _, s := range secrets {
		out, hit := RedactSecrets(s)
		if !hit {
			t.Errorf("RedactSecrets(%q): hit=false", s)
			continue
		}
		if strings.Contains(out, s) {
			t.Errorf("RedactSecrets(%q): raw secret survives: %q", s, out)
		}
		if !strings.Contains(out, RedactedPlaceholder) {
			t.Errorf("RedactSecrets(%q): missing placeholder: %q", s, out)
		}
	}
	clean := "just a normal log line with no credentials"
	if out, hit := RedactSecrets(clean); hit || out != clean {
		t.Errorf("clean string mangled: %q hit=%v", out, hit)
	}
}

func TestInterceptPayloadsRedactDontDrop(t *testing.T) {
	const secret = "ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ1234"
	rec := &eventRecorder{}
	in := NewInterceptor(rec.sink())

	in.OnFileRead("notes.txt", 100, "prefix "+secret+" suffix")
	p := rec.last(EventFileRead)
	if p == nil {
		t.Fatal("FILE_READ with secret dropped, want redacted event")
	} else {
		head, _ := p["head"].(string)
		if strings.Contains(head, secret) {
			t.Errorf("FILE_READ head leaks secret: %q", head)
		}
		if !strings.Contains(head, RedactedPlaceholder) {
			t.Errorf("FILE_READ head missing placeholder: %q", head)
		}
	}

	in.OnFileWrite("notes.txt", []byte("line1\n"), []byte("line1\nkey="+secret+"\n"))
	p = rec.last(EventFileModified)
	if p == nil {
		t.Fatal("FILE_MODIFIED with secret dropped, want redacted event")
	} else {
		diff, _ := p["diff"].(string)
		if strings.Contains(diff, secret) {
			t.Errorf("FILE_MODIFIED diff leaks secret:\n%s", diff)
		}
		if !strings.Contains(diff, RedactedPlaceholder) {
			t.Errorf("FILE_MODIFIED diff missing placeholder:\n%s", diff)
		}
	}

	in.OnCommand("deploy", 0, []byte("ok "+secret), nil)
	p = rec.last(EventCommandExecuted)
	if p == nil {
		t.Fatal("COMMAND_EXECUTED with secret dropped, want redacted event")
	} else {
		out, _ := p["stdout"].(string)
		if strings.Contains(out, secret) {
			t.Errorf("COMMAND_EXECUTED stdout leaks secret: %q", out)
		}
		if !strings.Contains(out, RedactedPlaceholder) {
			t.Errorf("COMMAND_EXECUTED stdout missing placeholder: %q", out)
		}
	}

	in.OnGitCommit("abc123", "rotate "+secret, "1 file changed")
	p = rec.last(EventGitCommitted)
	if p == nil {
		t.Fatal("GIT_COMMITTED with secret dropped, want redacted event")
	} else {
		msg, _ := p["message"].(string)
		if strings.Contains(msg, secret) {
			t.Errorf("GIT_COMMITTED message leaks secret: %q", msg)
		}
		if p["hash"] != "abc123" {
			t.Errorf("hash mangled by redaction: %v", p["hash"])
		}
	}

	in.OnGitDiff("", "@@ line with "+secret)
	p = rec.last(EventGitDiffViewed)
	if p == nil {
		t.Fatal("GIT_DIFF_VIEWED with secret dropped, want redacted event")
	} else {
		stat, _ := p["stat"].(string)
		if strings.Contains(stat, secret) {
			t.Errorf("GIT_DIFF_VIEWED stat leaks secret: %q", stat)
		}
	}
}

func TestInterceptDiffStat(t *testing.T) {
	if got := DiffStat(""); got != "(empty diff)" {
		t.Errorf("DiffStat(empty) = %q", got)
	}
	diff := "diff --git a/a.txt b/a.txt\n--- a/a.txt\n+++ b/a.txt\n@@ -1 +1 @@\n-one\n+two\n+three\n"
	stat := DiffStat(diff)
	if !strings.Contains(stat, "2 added") || !strings.Contains(stat, "1 removed") {
		t.Errorf("DiffStat = %q, want 2 added / 1 removed", stat)
	}
}

func TestHandlerEmitsFileReadAndWrite(t *testing.T) {
	d := newTestDaemon(t)
	rec := &eventRecorder{}
	d.SetEventSink(rec.sink())

	req := authReq(t, d, http.MethodPost, "/file/write", `{"path":"notes.txt","content":"hello interceptor\n"}`)
	recW := httptest.NewRecorder()
	d.Handler().ServeHTTP(recW, req)
	if recW.Code != http.StatusOK {
		t.Fatalf("write: got %d", recW.Code)
	}
	if p := rec.last(EventFileModified); p == nil {
		t.Fatal("no FILE_MODIFIED emitted by /file/write")
	} else if p["path"] != "notes.txt" {
		t.Errorf("path = %v", p["path"])
	}

	req2 := authReq(t, d, http.MethodPost, "/file/read", `{"path":"notes.txt"}`)
	recR := httptest.NewRecorder()
	d.Handler().ServeHTTP(recR, req2)
	if recR.Code != http.StatusOK {
		t.Fatalf("read: got %d", recR.Code)
	}
	if p := rec.last(EventFileRead); p == nil {
		t.Fatal("no FILE_READ emitted by /file/read")
	} else {
		if p["path"] != "notes.txt" {
			t.Errorf("path = %v", p["path"])
		}
		head, _ := p["head"].(string)
		if !strings.Contains(head, "hello interceptor") {
			t.Errorf("head = %q", head)
		}
	}
}

func TestHandlerEmitsCommandAndGitDiff(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not on PATH")
	}
	dir := initGitRepo(t)
	if err := os.WriteFile(dir+"/a.txt", []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := New(dir, "", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rec := &eventRecorder{}
	d.SetEventSink(rec.sink())

	req := authReq(t, d, http.MethodGet, "/git/diff", "")
	rd := httptest.NewRecorder()
	d.Handler().ServeHTTP(rd, req)
	if rd.Code != http.StatusOK {
		t.Fatalf("diff: got %d", rd.Code)
	}
	if p := rec.last(EventGitDiffViewed); p == nil {
		t.Fatal("no GIT_DIFF_VIEWED emitted by /git/diff")
	} else if p["ref"] != "" {
		t.Errorf("ref = %v", p["ref"])
	}

	creq := authReq(t, d, http.MethodPost, "/command/run", `{"cmd":"git","args":["version"]}`)
	rc := httptest.NewRecorder()
	d.Handler().ServeHTTP(rc, creq)
	if rc.Code != http.StatusOK {
		t.Fatalf("command: got %d", rc.Code)
	}
	if p := rec.last(EventCommandExecuted); p == nil {
		t.Fatal("no COMMAND_EXECUTED emitted by /command/run")
	} else if p["cmdline"] != "git version" {
		t.Errorf("cmdline = %v", p["cmdline"])
	}
}

func TestCheckGitCommitEmitsOnHeadChange(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not on PATH")
	}
	dir := initGitRepo(t)
	d, err := New(dir, "", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rec := &eventRecorder{}
	d.SetEventSink(rec.sink())

	d.checkGitCommit() // HEAD seeded in New: no change → no event
	if n := rec.count(EventGitCommitted); n != 0 {
		t.Fatalf("seeded HEAD emitted %d events, want 0", n)
	}

	if err := os.WriteFile(dir+"/b.txt", []byte("second\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := RunCommand(t.Context(), dir, "git", []string{"add", "."}); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if _, _, err := RunCommand(t.Context(), dir, "git", []string{"commit", "-m", "second"}); err != nil {
		t.Fatalf("git commit: %v", err)
	}
	d.checkGitCommit()
	p := rec.last(EventGitCommitted)
	if p == nil {
		t.Fatal("no GIT_COMMITTED after HEAD change")
	}
	if want, _ := GitCommit(dir); p["hash"] != strings.TrimSpace(want) {
		t.Errorf("hash = %v, want %v", p["hash"], want)
	}

	d.checkGitCommit() // idempotent: no second event
	if n := rec.count(EventGitCommitted); n != 1 {
		t.Errorf("GIT_COMMITTED count = %d, want 1", n)
	}
}

func TestSetEventSinkWiresEmission(t *testing.T) {
	d := newTestDaemon(t) // nil sink: silent no-op
	d.Interceptor.OnFileRead("x", 1, "y")
	rec := &eventRecorder{}
	d.SetEventSink(rec.sink())
	d.Interceptor.OnFileRead("x", 1, "y")
	if rec.count(EventFileRead) != 1 {
		t.Fatal("SetEventSink did not wire emission")
	}
	d.SetEventSink(nil) // back to no-op, must not panic
	d.Interceptor.OnFileRead("x", 1, "y")
	if rec.count(EventFileRead) != 1 {
		t.Fatal("nil sink emitted")
	}
	var nilD *Daemon
	nilD.SetEventSink(rec.sink()) // nil-receiver safe
	nilD.checkGitCommit()
}
