package continuex

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// These tests must not run in parallel: the binary lookup seam is package-wide.
func TestClaudeLive(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "fakeclaude")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", binary, "./testdata/fakeclaude").CombinedOutput(); err != nil {
		t.Fatalf("build fake: %v: %s", err, out)
	}
	previous := claudeLookPath
	claudeLookPath = func(name string) (string, error) {
		if name != "claude" {
			t.Errorf("lookup %q", name)
		}
		return binary, nil
	}
	t.Cleanup(func() { claudeLookPath = previous })
	start := func(t *testing.T, ctx context.Context) *claudeLive {
		t.Helper()
		s, err := StartClaudeLive(ctx, LiveOptions{Workspace: t.TempDir(), Model: "sonnet", PermissionMode: "plan", ResumeID: "resume-id", SystemContext: "seed context"})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := s.Close(); err != nil {
				t.Error(err)
			}
		})
		return s.(*claudeLive)
	}
	t.Run("DeltasNativeIDAndFollowUp", func(t *testing.T) {
		s := start(t, context.Background())
		for turn := 0; turn < 2; turn++ {
			if err := s.Send(context.Background(), "hello"); err != nil {
				t.Fatal(err)
			}
			var deltas []string
			for {
				e := liveNext(t, s)
				if e.Kind == "text_delta" {
					deltas = append(deltas, e.Text)
				}
				if e.Kind == "turn_done" {
					if e.CostUSD != 0.125 {
						t.Fatal(e)
					}
					break
				}
			}
			if strings.Join(deltas, "|") != "Hello| world" {
				t.Fatal(deltas)
			}
		}
		if s.NativeID() != "fake-session" {
			t.Fatal(s.NativeID())
		}
	})
	for _, allow := range []bool{true, false} {
		name := "Deny"
		if allow {
			name = "Allow"
		}
		t.Run(name, func(t *testing.T) {
			s := start(t, context.Background())
			if err := s.Send(context.Background(), "tool"); err != nil {
				t.Fatal(err)
			}
			for {
				e := liveNext(t, s)
				if e.Kind == "tool_start" && strings.Contains(string(e.ToolInput), "original-secret") {
					t.Fatal("input leaked")
				}
				if e.Kind == "approval" {
					if e.RequestID != "permission-1" || e.ToolName != "Bash" {
						t.Fatal(e)
					}
					if err := s.Approve(e.RequestID, allow, "no thanks"); err != nil {
						t.Fatal(err)
					}
					break
				}
			}
			for {
				e := liveNext(t, s)
				if e.Kind == "tool_end" {
					if e.IsError == allow || e.ToolID != "tool-1" || len(e.ToolOutput) > 64*1024 || strings.Contains(e.ToolOutput, "supersecret") {
						t.Fatal("bad tool result", e.IsError, len(e.ToolOutput))
					}
					break
				}
			}
			liveUntil(t, s, "turn_done")
			if err := s.Approve("permission-1", allow, ""); err == nil {
				t.Fatal("duplicate approval accepted")
			}
		})
	}
	t.Run("Interrupt", func(t *testing.T) {
		s := start(t, context.Background())
		_ = s.Send(context.Background(), "wait")
		for {
			if e := liveNext(t, s); e.Kind == "text" && e.Text == "waiting" {
				break
			}
		}
		if err := s.Interrupt(); err != nil {
			t.Fatal(err)
		}
		if e := liveUntil(t, s, "turn_done"); !e.IsError || e.Text != "interrupted" {
			t.Fatal(e)
		}
		if err := s.Send(context.Background(), "hello"); err != nil {
			t.Fatal(err)
		}
		liveUntil(t, s, "turn_done")
	})
	t.Run("Argv", func(t *testing.T) {
		s := start(t, context.Background())
		_ = s.Send(context.Background(), "argv")
		e := liveUntil(t, s, "text")
		var args []string
		if err := json.Unmarshal([]byte(e.Text), &args); err != nil {
			t.Fatal(err, e.Text)
		}
		joined := strings.Join(args, " ")
		for _, want := range []string{"--permission-prompts host", "--input-format stream-json", "--output-format stream-json", "--verbose", "--include-partial-messages", "--resume resume-id", "--model sonnet", "--permission-mode plan", "--append-system-prompt seed context"} {
			if !strings.Contains(joined, want) {
				t.Fatal("missing", want, joined)
			}
		}
		if strings.Contains(joined, "--dangerously-skip-permissions") {
			t.Fatal(joined)
		}
	})
	t.Run("CloseAndCancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		s := start(t, ctx)
		liveUntil(t, s, "status")
		cancel()
		select {
		case <-s.done:
		case <-time.After(5 * time.Second):
			t.Fatal("cancel did not stop process")
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if s.cmd.ProcessState == nil || !s.cmd.ProcessState.Exited() {
			t.Fatal("process still running")
		}
		for range s.Events() {
		}
		if err := s.Send(context.Background(), "late"); err == nil {
			t.Fatal("send after exit")
		}
	})
	t.Run("Stderr", func(t *testing.T) {
		s := start(t, context.Background())
		_ = s.Send(context.Background(), "crash")
		e := liveUntil(t, s, "error")
		if !strings.Contains(e.Text, "fake failure") || strings.Contains(e.Text, "supersecret") {
			t.Fatal(e)
		}
		liveUntil(t, s, "exited")
	})
	t.Run("ForceKill", func(t *testing.T) {
		s := start(t, context.Background())
		if err := s.Send(context.Background(), "hang"); err != nil {
			t.Fatal(err)
		}
		for {
			if e := liveNext(t, s); e.Kind == "text" && e.Text == "hanging" {
				break
			}
		}
		began := time.Now()
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if time.Since(began) > 5*time.Second {
			t.Fatal("close exceeded bound")
		}
		select {
		case <-s.done:
		default:
			t.Fatal("process was not reaped")
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("ConcurrentSend", func(t *testing.T) {
		s := start(t, context.Background())
		var wg sync.WaitGroup
		for i := 0; i < 12; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := s.Send(context.Background(), "hello"); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		for i := 0; i < 12; i++ {
			liveUntil(t, s, "turn_done")
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := s.Send(ctx, "cancelled"); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	})
	t.Run("SlowConsumer", func(t *testing.T) {
		s := start(t, context.Background())
		_ = s.Send(context.Background(), "flood")
		deadline := time.After(10 * time.Second)
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
			select {
			case <-deadline:
				t.Fatal("reader blocked on consumer")
			case <-time.After(10 * time.Millisecond):
			}
		}
		var text strings.Builder
		for {
			e := liveNext(t, s)
			if e.Kind == "text_delta" {
				text.WriteString(e.Text)
			}
			if e.Kind == "turn_done" {
				break
			}
		}
		if text.String() != strings.Repeat("Hello world", 2000) {
			t.Fatal("coalescing lost or reordered deltas")
		}
	})
	t.Run("ParallelSessions", func(t *testing.T) {
		a := start(t, context.Background())
		b := start(t, context.Background())
		_ = a.Send(context.Background(), "wait")
		_ = b.Send(context.Background(), "hello")
		liveUntil(t, b, "turn_done")
	})
	t.Run("Validation", func(t *testing.T) {
		for _, opts := range []LiveOptions{{}, {Workspace: t.TempDir(), Model: "--bad"}, {Workspace: t.TempDir(), PermissionMode: "yolo"}} {
			if s, err := StartClaudeLive(context.Background(), opts); err == nil {
				_ = s.Close()
				t.Fatal("invalid options accepted")
			}
		}
		claudeLookPath = func(string) (string, error) { return "", errors.New("missing") }
		defer func() { claudeLookPath = func(string) (string, error) { return binary, nil } }()
		if _, err := StartClaudeLive(context.Background(), LiveOptions{Workspace: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "Claude Code is not installed or not on PATH") {
			t.Fatal(err)
		}
	})
}

func liveNext(t *testing.T, s LiveSession) LiveEvent {
	t.Helper()
	select {
	case e, ok := <-s.Events():
		if !ok {
			t.Fatal("events closed unexpectedly")
		}
		return e
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for event")
	}
	return LiveEvent{}
}
func liveUntil(t *testing.T, s LiveSession, kind string) LiveEvent {
	t.Helper()
	for {
		e := liveNext(t, s)
		if e.Kind == kind {
			return e
		}
	}
}
