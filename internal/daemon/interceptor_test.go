package daemon

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestTruncateShortUntouched(t *testing.T) {
	if got := Truncate("hello", 200); got != "hello" {
		t.Fatalf("short string modified: %q", got)
	}
}

func TestTruncateCapsWithMarker(t *testing.T) {
	long := strings.Repeat("a", MaxOutputBytes+100)
	got := Truncate(long, MaxOutputBytes)
	if !strings.Contains(got, "[truncated]") {
		t.Fatalf("expected truncation marker, got len %d", len(got))
	}
	if len(got) > MaxOutputBytes+len("\n[truncated]")+1 {
		t.Fatalf("over cap: len %d", len(got))
	}
}

func TestTruncateRuneBoundary(t *testing.T) {
	// "é" is 2 bytes in UTF-8; cutting mid-rune must not produce invalid output.
	s := strings.Repeat("é", 100) // 200 bytes
	got := Truncate(s, 199)
	for i := range got {
		_ = i
	}
	// Must be valid: re-encoding round trip through []rune must not contain U+FFFD.
	for _, r := range got {
		if r == '\uFFFD' {
			t.Fatalf("truncation split a rune: %q", got)
		}
	}
}

func TestFileReadPreviewCapped200(t *testing.T) {
	in := NewInterceptor(8, nil)
	defer in.Close()
	preview := strings.Repeat("x", 500)
	ev := in.LogFileRead("a.txt", 500, preview)
	p, _ := ev.Payload["preview"].(string)
	if len(p) > MaxPreviewBytes+len("\n[truncated]")+1 {
		t.Fatalf("preview over cap: len %d", len(p))
	}
	if ev.Type != ToolEventFileRead {
		t.Fatalf("wrong type: %s", ev.Type)
	}
	<-in.Events() // drain
}

func TestCommandOutputCapped4KB(t *testing.T) {
	in := NewInterceptor(8, nil)
	defer in.Close()
	big := strings.Repeat("o", MaxOutputBytes*2)
	ev := in.LogCommand("go", []string{"test", "./..."}, 1, big)
	out, _ := ev.Payload["output"].(string)
	if len(out) > MaxOutputBytes+len("\n[truncated]")+1 {
		t.Fatalf("output over 4KB cap: len %d", len(out))
	}
	if ev.Payload["output_truncated"] != true {
		t.Fatalf("expected output_truncated=true")
	}
	if ev.Type != ToolEventCommandExecuted {
		t.Fatalf("wrong type: %s", ev.Type)
	}
	select {
	case <-in.Events():
	case <-time.After(time.Second):
		t.Fatal("event not queued")
	}
}

func TestFileModifiedDiffCap(t *testing.T) {
	in := NewInterceptor(8, nil)
	defer in.Close()
	before := "line1\n" + strings.Repeat("old\n", 3000)
	after := "line1\n" + strings.Repeat("new content line that is long\n", 3000)
	ev := in.LogFileModified("big.txt", before, after, int64(len(after)))
	diff, _ := ev.Payload["diff"].(string)
	if len(diff) > MaxDiffBytes+len("\n[truncated]")+1 {
		t.Fatalf("diff over cap: len %d", len(diff))
	}
	if _, ok := ev.Payload["diff_truncated"]; !ok {
		t.Fatalf("expected diff_truncated key")
	}
	if ev.Type != ToolEventFileModified {
		t.Fatalf("wrong type: %s", ev.Type)
	}
	<-in.Events()
}

func TestExplicitDiffCapped(t *testing.T) {
	in := NewInterceptor(8, nil)
	defer in.Close()
	huge := strings.Repeat("d", MaxDiffBytes+500)
	ev := in.LogFileModifiedDiff("x.go", huge, 99)
	if got := ev.Payload["diff"].(string); len(got) > MaxDiffBytes+len("\n[truncated]")+1 {
		t.Fatalf("explicit diff over cap: %d", len(got))
	}
	<-in.Events()
}

func TestLogActionToolEventTypes(t *testing.T) {
	in := NewInterceptor(32, nil)
	defer in.Close()
	cases := []struct {
		action string
		want   ToolEventType
	}{
		{"file_read", ToolEventFileRead},
		{"file_write", ToolEventFileModified},
		{"file_modified", ToolEventFileModified},
		{"command_run", ToolEventCommandExecuted},
		{"git_diff", ToolEventGitDiffViewed},
		{"git_commit", ToolEventGitCommitted},
	}
	for _, c := range cases {
		ev := in.LogAction(c.action, map[string]any{})
		if ev.Type != c.want {
			t.Errorf("action %s: got %s want %s", c.action, ev.Type, c.want)
		}
		<-in.Events()
	}
}

func TestGitCommittedPayload(t *testing.T) {
	in := NewInterceptor(8, nil)
	defer in.Close()
	msg := strings.Repeat("m", MaxMessageBytes+10)
	ev := in.LogGitCommitted("abc123", msg, "3 files changed")
	if ev.Type != ToolEventGitCommitted {
		t.Fatalf("wrong type: %s", ev.Type)
	}
	if got := ev.Payload["message"].(string); !strings.Contains(got, "[truncated]") {
		t.Fatalf("commit message not capped")
	}
	<-in.Events()
}

func TestLogActionNonBlockingWhenFull(t *testing.T) {
	in := NewInterceptor(1, nil) // tiny buffer
	defer in.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 1000; i++ {
			in.LogAction("file_read", map[string]any{"path": "p"})
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("LogAction blocked on full queue — must be non-blocking")
	}
	if in.Dropped() == 0 {
		t.Fatal("expected some drops with buffer 1 and 1000 rapid logs")
	}
}

func TestBuildDiffMarkers(t *testing.T) {
	d := BuildDiff("a\nb\nc\n", "a\nB\nc\n", MaxDiffBytes)
	if !strings.Contains(d, "- b") || !strings.Contains(d, "+ B") {
		t.Fatalf("missing diff markers: %q", d)
	}
}

func TestBuildDiffEqualEmpty(t *testing.T) {
	if d := BuildDiff("same", "same", MaxDiffBytes); d != "" {
		t.Fatalf("equal inputs should diff empty, got %q", d)
	}
}

func TestChanEmitterDropsInsteadOfBlocking(t *testing.T) {
	c := NewChanEmitter(1)
	c.Emit(ToolEvent{Type: ToolEventFileRead})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 500; i++ {
			c.Emit(ToolEvent{Type: ToolEventFileRead})
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ChanEmitter.Emit blocked — must be non-blocking")
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
	in := NewInterceptor(8, nil)
	defer in.Close()

	ev := in.LogFileRead("notes.txt", 100, "prefix "+secret+" suffix")
	head, _ := ev.Payload["preview"].(string)
	if strings.Contains(head, secret) {
		t.Errorf("FILE_READ preview leaks secret: %q", head)
	}
	if !strings.Contains(head, RedactedPlaceholder) {
		t.Errorf("FILE_READ preview missing placeholder: %q", head)
	}

	ev = in.LogFileModified("notes.txt", "line1\n", "line1\nkey="+secret+"\n", 100)
	diff, _ := ev.Payload["diff"].(string)
	if strings.Contains(diff, secret) {
		t.Errorf("FILE_MODIFIED diff leaks secret:\n%s", diff)
	}
	if !strings.Contains(diff, RedactedPlaceholder) {
		t.Errorf("FILE_MODIFIED diff missing placeholder:\n%s", diff)
	}

	ev = in.LogCommand("deploy", nil, 0, "ok "+secret)
	out, _ := ev.Payload["output"].(string)
	if strings.Contains(out, secret) {
		t.Errorf("COMMAND_EXECUTED output leaks secret: %q", out)
	}
	if !strings.Contains(out, RedactedPlaceholder) {
		t.Errorf("COMMAND_EXECUTED output missing placeholder: %q", out)
	}

	ev = in.LogGitCommitted("abc123", "rotate "+secret, "1 file changed")
	msg, _ := ev.Payload["message"].(string)
	if strings.Contains(msg, secret) {
		t.Errorf("GIT_COMMITTED message leaks secret: %q", msg)
	}
	if ev.Payload["commit"] != "abc123" {
		t.Errorf("commit sha mangled by redaction: %v", ev.Payload["commit"])
	}

	ev = in.LogGitDiff("", "@@ line with "+secret, "stat with "+secret)
	stat, _ := ev.Payload["stats"].(string)
	if strings.Contains(stat, secret) {
		t.Errorf("GIT_DIFF_VIEWED stats leaks secret: %q", stat)
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
	d, err := NewDaemon(t.TempDir(), "test-token-123")
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}

	post := func(target, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer test-token-123")
		w := httptest.NewRecorder()
		d.Handler().ServeHTTP(w, r)
		return w
	}

	if w := post("/file/write", `{"path":"notes.txt","content":"hello interceptor\n"}`); w.Code != http.StatusOK {
		t.Fatalf("write: got %d", w.Code)
	}
	ev := nextEvent(t, d)
	if ev.Type != ToolEventFileModified {
		t.Fatalf("type = %s, want FILE_MODIFIED", ev.Type)
	}
	if ev.Payload["path"] != "notes.txt" {
		t.Errorf("path = %v", ev.Payload["path"])
	}

	if w := post("/file/read", `{"path":"notes.txt"}`); w.Code != http.StatusOK {
		t.Fatalf("read: got %d", w.Code)
	}
	ev = nextEvent(t, d)
	if ev.Type != ToolEventFileRead {
		t.Fatalf("type = %s, want FILE_READ", ev.Type)
	}
	if ev.Payload["path"] != "notes.txt" {
		t.Errorf("path = %v", ev.Payload["path"])
	}
	head, _ := ev.Payload["preview"].(string)
	if !strings.Contains(head, "hello interceptor") {
		t.Errorf("preview = %q", head)
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
	d, err := NewDaemon(dir, "test-token-123")
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}

	r := httptest.NewRequest(http.MethodGet, "/git/diff", nil)
	r.Header.Set("Authorization", "Bearer test-token-123")
	rd := httptest.NewRecorder()
	d.Handler().ServeHTTP(rd, r)
	if rd.Code != http.StatusOK {
		t.Fatalf("diff: got %d", rd.Code)
	}
	ev := nextEvent(t, d)
	if ev.Type != ToolEventGitDiffViewed {
		t.Fatalf("type = %s, want GIT_DIFF_VIEWED", ev.Type)
	}
	if ev.Payload["ref"] != "" {
		t.Errorf("ref = %v", ev.Payload["ref"])
	}

	cr := httptest.NewRequest(http.MethodPost, "/command/run", strings.NewReader(`{"cmd":"git","args":["version"]}`))
	cr.Header.Set("Authorization", "Bearer test-token-123")
	rc := httptest.NewRecorder()
	d.Handler().ServeHTTP(rc, cr)
	if rc.Code != http.StatusOK {
		t.Fatalf("command: got %d", rc.Code)
	}
	ev = nextEvent(t, d)
	if ev.Type != ToolEventCommandExecuted {
		t.Fatalf("type = %s, want COMMAND_EXECUTED", ev.Type)
	}
	if ev.Payload["command"] != "git" {
		t.Errorf("command = %v", ev.Payload["command"])
	}
}

func TestCheckGitCommitEmitsOnHeadChange(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not on PATH")
	}
	dir := initGitRepo(t)
	d, err := NewDaemon(dir, "test-token-123")
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}

	d.checkGitCommit() // HEAD seeded in NewDaemon: no change → no event
	select {
	case ev := <-d.Interceptor.Events():
		t.Fatalf("seeded HEAD emitted %v, want nothing", ev.Type)
	case <-time.After(100 * time.Millisecond):
	}

	if err := os.WriteFile(dir+"/b.txt", []byte("second\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := RunCommand(dir, []string{"git", "add", "."}); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if _, err := RunCommand(dir, []string{"git", "commit", "-m", "second"}); err != nil {
		t.Fatalf("git commit: %v", err)
	}
	d.checkGitCommit()
	ev := nextEvent(t, d)
	if ev.Type != ToolEventGitCommitted {
		t.Fatalf("type = %s, want GIT_COMMITTED", ev.Type)
	}
	_, want, _, _, _ := GitStatus(dir)
	if ev.Payload["commit"] != strings.TrimSpace(want) {
		t.Errorf("commit = %v, want %v", ev.Payload["commit"], want)
	}

	d.checkGitCommit() // idempotent: no second event
	select {
	case ev := <-d.Interceptor.Events():
		t.Fatalf("second checkGitCommit emitted %v, want nothing", ev.Type)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestSetEventSinkWiresEmission(t *testing.T) {
	d, err := NewDaemon(t.TempDir(), "test-token-123")
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}
	// Nil downstream: events stay queued.
	d.Interceptor.LogFileRead("x", 1, "y")
	select {
	case <-d.Interceptor.Events():
	case <-time.After(2 * time.Second):
		t.Fatal("queued event not readable via Events()")
	}
	// Attach a downstream sink: emission still works, must not panic.
	d.SetEventSink(NewChanEmitter(8))
	d.Interceptor.LogFileRead("x", 1, "y")
	d.SetEventSink(nil) // back to no-sink, must not panic
	d.Interceptor.LogFileRead("x", 1, "y")
	var nilD *Daemon
	nilD.SetEventSink(nil) // nil-receiver safe
	nilD.checkGitCommit()
}

// nextEvent reads one queued tool event (nil-downstream mode) or fails.
func nextEvent(t *testing.T, d *Daemon) ToolEvent {
	t.Helper()
	select {
	case ev := <-d.Interceptor.Events():
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for tool event")
		return ToolEvent{}
	}
}

// gitAvailable reports whether git is on PATH.
func gitAvailable() bool {
	_, err := RunCommand(os.TempDir(), []string{"git", "version"})
	return err == nil
}

// initGitRepo creates a temp git repo with one commit and returns its path.
func initGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		if _, err := RunCommand(dir, append([]string{"git"}, args...)); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	run("init")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "test")
	if err := os.WriteFile(dir+"/a.txt", []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "first")
	return dir
}

// issue99Sink is a recording ToolEventEmitter: every forwarded event lands
// on ch (buffered; only a handful of events are expected).
type issue99Sink struct {
	ch chan ToolEvent
}

func (s *issue99Sink) Emit(ev ToolEvent) { s.ch <- ev }

// TestIssue99InterceptorHookupRegression (issue #99, STALE finding).
// The interceptor field, NewDaemon/Start init, and LogFileRead/
// LogFileModified/LogCommand hooks already exist (issue #32); this test
// pins the hookup end-to-end so a future removal fails loudly: a Daemon
// with a recording sink attached via SetEventSink must emit one event per
// tool call when POST /file/write, /file/read and /command/run are driven
// over the mux with auth. Interceptor must be non-nil after NewDaemon.
// Fast and deterministic (plain TempDir, no git repo, no sleeps).
func TestIssue99InterceptorHookupRegression(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not on PATH")
	}
	root := t.TempDir()
	if err := os.WriteFile(root+"/hook.txt", []byte("seed content here"), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := NewDaemon(root, "test-token-99")
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}
	if d.Interceptor == nil {
		t.Fatal("NewDaemon left Interceptor nil, want non-nil")
	}
	sink := &issue99Sink{ch: make(chan ToolEvent, 16)}
	d.SetEventSink(sink)

	doPost := func(target, body string) {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer test-token-99")
		w := httptest.NewRecorder()
		d.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("POST %s: got %d (%s)", target, w.Code, w.Body.String())
		}
	}
	doPost("/file/write", `{"path":"hook.txt","content":"hello hookup content"}`)
	doPost("/file/read", `{"path":"hook.txt"}`)
	doPost("/command/run", `{"cmd":"git","args":["version"]}`)

	want := []ToolEventType{ToolEventFileModified, ToolEventFileRead, ToolEventCommandExecuted}
	for i, wt := range want {
		select {
		case ev := <-sink.ch:
			if ev.Type != wt {
				t.Fatalf("event %d: type = %s, want %s", i, ev.Type, wt)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("event %d (%s): timed out waiting for sink emission", i, wt)
		}
	}
}
