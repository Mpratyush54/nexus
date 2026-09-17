package daemon

import (
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
