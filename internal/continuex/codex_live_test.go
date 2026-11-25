package continuex

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func codexLiveNext(t *testing.T, s LiveSession) LiveEvent {
	t.Helper()
	select {
	case e, ok := <-s.Events():
		if !ok {
			t.Fatal("events closed unexpectedly")
		}
		return e
	case <-time.After(15 * time.Second):
		t.Fatal("event timeout")
	}
	return LiveEvent{}
}
func codexLiveUntil(t *testing.T, s LiveSession, kind string) LiveEvent {
	t.Helper()
	for {
		e := codexLiveNext(t, s)
		if e.Kind == "error" || e.Kind == "exited" {
			if e.Kind != kind {
				t.Fatalf("unexpected event: %+v", e)
			}
		}
		if e.Kind == kind {
			return e
		}
	}
}

// The process lookup seam is global, so these tests are intentionally serial.
func TestCodexLive(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "fakecodex")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", binary, "./testdata/fakecodex").CombinedOutput(); err != nil {
		t.Fatalf("build fake: %v: %s", err, out)
	}
	previous := codexLookPath
	codexLookPath = func(name string) (string, error) {
		if name != "codex" {
			t.Errorf("lookup %s", name)
		}
		return binary, nil
	}
	t.Cleanup(func() { codexLookPath = previous })
	start := func(t *testing.T, ctx context.Context, resume string) *codexLive {
		t.Helper()
		s, err := StartCodexLive(ctx, LiveOptions{Workspace: t.TempDir(), Model: "test-model", SystemContext: "seed context", ResumeID: resume})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := s.Close(); err != nil {
				t.Error(err)
			}
		})
		return s.(*codexLive)
	}
	t.Run("HandshakeDeltasNativeIDQueuedSend", func(t *testing.T) {
		s := start(t, context.Background(), "")
		if err := s.Send(context.Background(), "hello"); err != nil {
			t.Fatal(err)
		}
		if err := s.Send(context.Background(), "hello again"); err != nil {
			t.Fatal(err)
		}
		first := codexLiveNext(t, s)
		if first.Kind != "status" || first.NativeID != "fake-thread" {
			t.Fatal(first)
		}
		for turn := 0; turn < 2; turn++ {
			var deltas []string
			for {
				e := codexLiveNext(t, s)
				if e.Kind == "error" {
					t.Fatal(e)
				}
				if e.Kind == "text_delta" {
					deltas = append(deltas, e.Text)
				}
				if e.Kind == "turn_done" {
					if e.IsError {
						t.Fatal(e)
					}
					break
				}
			}
			if strings.Join(deltas, "|") != "Hello| world" {
				t.Fatal(deltas)
			}
		}
		if s.NativeID() != "fake-thread" {
			t.Fatal(s.NativeID())
		}
	})
	t.Run("ResumeRolloutUUID", func(t *testing.T) {
		s := start(t, context.Background(), "rollout-2026-11-22T10-00-00-12345678-1234-5678-9012-123456789abc.jsonl")
		codexLiveUntil(t, s, "status")
		if err := s.Send(context.Background(), "hello"); err != nil {
			t.Fatal(err)
		}
		codexLiveUntil(t, s, "turn_done")
	})
	for _, prompt := range []string{"allow", "decline", "file", "interrupt"} {
		t.Run("Approval_"+prompt, func(t *testing.T) {
			s := start(t, context.Background(), "")
			if err := s.Send(context.Background(), prompt); err != nil {
				t.Fatal(err)
			}
			tool := codexLiveUntil(t, s, "tool_start")
			approval := codexLiveUntil(t, s, "approval")
			name := "Bash"
			if prompt == "file" {
				name = "Edit"
				if !strings.Contains(string(approval.ToolInput), "hello.txt") {
					t.Fatal(approval)
				}
			}
			if tool.ToolName != name || approval.ToolName != name || approval.RequestID == "" {
				t.Fatal(tool, approval)
			}
			if prompt == "interrupt" {
				if err := s.Interrupt(); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := s.Approve(approval.RequestID, prompt != "decline", ""); err != nil {
					t.Fatal(err)
				}
			}
			end := codexLiveUntil(t, s, "tool_end")
			if end.IsError != (prompt == "decline" || prompt == "interrupt") || strings.Contains(end.ToolOutput, "supersecret") || len(end.ToolOutput) > 65536 {
				t.Fatal("bad tool output", end.IsError, len(end.ToolOutput))
			}
			done := codexLiveUntil(t, s, "turn_done")
			if done.IsError != (prompt == "interrupt") {
				t.Fatal(done)
			}
			if err := s.Approve(approval.RequestID, true, ""); err == nil {
				t.Fatal("duplicate approval")
			}
		})
	}
	t.Run("InterruptAndFollowup", func(t *testing.T) {
		s := start(t, context.Background(), "")
		if err := s.Interrupt(); err != nil {
			t.Fatal(err)
		}
		_ = s.Send(context.Background(), "wait")
		codexLiveUntil(t, s, "text")
		// This must stay queued while the fake's first turn is still active.
		if err := s.Send(context.Background(), "hello"); err != nil {
			t.Fatal(err)
		}
		if err := s.Interrupt(); err != nil {
			t.Fatal(err)
		}
		if e := codexLiveUntil(t, s, "turn_done"); !e.IsError || e.Text != "interrupted" {
			t.Fatal(e)
		}
		codexLiveUntil(t, s, "turn_done")
	})
	t.Run("MCPToolAndRedaction", func(t *testing.T) {
		s := start(t, context.Background(), "")
		if err := s.Send(context.Background(), "mcp"); err != nil {
			t.Fatal(err)
		}
		begin := codexLiveUntil(t, s, "tool_start")
		if begin.ToolName != "lookup" || strings.Contains(string(begin.ToolInput), "original-secret") {
			t.Fatal(begin)
		}
		end := codexLiveUntil(t, s, "tool_end")
		if !end.IsError || end.ToolID != "mcp-1" || !strings.Contains(end.ToolOutput, "lookup failed") || strings.Contains(end.ToolOutput, "supersecret") {
			t.Fatal(end)
		}
		codexLiveUntil(t, s, "turn_done")
	})
	t.Run("UnknownRequest", func(t *testing.T) {
		s := start(t, context.Background(), "")
		_ = s.Send(context.Background(), "unknown")
		if e := codexLiveUntil(t, s, "text"); e.Text != "unknown answered" {
			t.Fatal(e)
		}
		codexLiveUntil(t, s, "turn_done")
	})
	t.Run("SafeArgv", func(t *testing.T) {
		s := start(t, context.Background(), "")
		_ = s.Send(context.Background(), "argv")
		e := codexLiveUntil(t, s, "text")
		var args []string
		if err := json.Unmarshal([]byte(e.Text), &args); err != nil {
			t.Fatal(err)
		}
		if strings.Join(args, " ") != "app-server" {
			t.Fatal(args)
		}
		for _, bad := range []string{"bypass", "yolo", "dangerously"} {
			if strings.Contains(e.Text, bad) {
				t.Fatal(args)
			}
		}
		codexLiveUntil(t, s, "turn_done")
	})
	t.Run("CrashRedactionExitLast", func(t *testing.T) {
		s := start(t, context.Background(), "")
		_ = s.Send(context.Background(), "crash")
		found := false
		last := ""
		timer := time.After(10 * time.Second)
		for {
			select {
			case e, ok := <-s.Events():
				if !ok {
					if !found || last != "exited" {
						t.Fatal(found, last)
					}
					return
				}
				if e.Kind == "error" && strings.Contains(e.Text, "fake failure") {
					found = true
				}
				if strings.Contains(e.Text, "supersecret") {
					t.Fatal("stderr secret leaked")
				}
				last = e.Kind
			case <-timer:
				t.Fatal("exit drain timeout")
			}
		}
	})
	t.Run("CancelCloseIdempotent", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		s := start(t, ctx, "")
		codexLiveUntil(t, s, "status")
		cancel()
		select {
		case <-s.done:
		case <-time.After(5 * time.Second):
			t.Fatal("cancellation timeout")
		}
		for i := 0; i < 2; i++ {
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
		}
		for range s.Events() {
		}
		if err := s.Send(context.Background(), "late"); err == nil {
			t.Fatal("send after close")
		}
	})
	t.Run("ForceKill", func(t *testing.T) {
		s := start(t, context.Background(), "")
		_ = s.Send(context.Background(), "hang")
		codexLiveUntil(t, s, "text")
		began := time.Now()
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if time.Since(began) > 5*time.Second {
			t.Fatal("unbounded close")
		}
	})
	t.Run("SlowConsumer", func(t *testing.T) {
		s := start(t, context.Background(), "")
		_ = s.Send(context.Background(), "flood")
		deadline := time.Now().Add(10 * time.Second)
		for {
			s.mu.Lock()
			found := false
			for _, e := range s.queue {
				if e.Kind == "turn_done" {
					found = true
				}
			}
			s.mu.Unlock()
			if found {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("stdout reader blocked")
			}
			time.Sleep(10 * time.Millisecond)
		}
		var b strings.Builder
		for {
			e := codexLiveNext(t, s)
			if e.Kind == "text_delta" {
				b.WriteString(e.Text)
			}
			if e.Kind == "turn_done" {
				break
			}
		}
		if b.String() != strings.Repeat("Hello world", 2000) {
			t.Fatal("lost deltas")
		}
	})
	t.Run("Validation", func(t *testing.T) {
		for _, opts := range []LiveOptions{{}, {Workspace: t.TempDir(), Model: "--invalid"}, {Workspace: t.TempDir(), PermissionMode: "bypassPermissions"}} {
			if s, err := StartCodexLive(context.Background(), opts); err == nil {
				_ = s.Close()
				t.Fatal("invalid options accepted")
			}
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := StartCodexLive(ctx, LiveOptions{Workspace: t.TempDir()}); err != context.Canceled {
			t.Fatal(err)
		}
	})
}
