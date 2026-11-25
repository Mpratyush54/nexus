package continuex

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func acpNext(t *testing.T, s LiveSession) LiveEvent {
	t.Helper()
	select {
	case e, ok := <-s.Events():
		if !ok {
			t.Fatal("events closed unexpectedly")
		}
		return e
	case <-time.After(15 * time.Second):
		t.Fatal("ACP event timeout")
	}
	return LiveEvent{}
}
func acpUntil(t *testing.T, s LiveSession, kind string) LiveEvent {
	t.Helper()
	for {
		e := acpNext(t, s)
		if e.Kind == "error" && kind != "error" {
			t.Fatal(e.Text)
		}
		if e.Kind == kind {
			return e
		}
	}
}

// Lookup and argv seams are package-wide; these tests must stay serial.
func TestACPLive(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "fakeacp")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", binary, "./testdata/fakeacp").CombinedOutput(); err != nil {
		t.Fatalf("build fake: %v: %s", err, out)
	}
	previousLookup, previousArgv := acpLookPath, acpArgv
	acpLookPath = func(string) (string, error) { return binary, nil }
	acpArgv = map[string][]string{"cursor": {"fake"}, "opencode": {"fake"}, "gemini": {"fake"}, "kimi": {"fake"}}
	t.Cleanup(func() { acpLookPath = previousLookup; acpArgv = previousArgv })
	start := func(t *testing.T, ctx context.Context, resume string) *acpLive {
		t.Helper()
		session, err := StartACPLive(ctx, "cursor", LiveOptions{Workspace: t.TempDir(), Model: "test-model", ResumeID: resume, SystemContext: "seed context"})
		if err != nil {
			t.Fatal(err)
		}
		s := session.(*acpLive)
		t.Cleanup(func() {
			if err := s.Close(); err != nil {
				t.Error(err)
			}
		})
		return s
	}
	t.Run("ChunksNativeIDResumeAndSeed", func(t *testing.T) {
		for _, resume := range []string{"", "fake-session"} {
			s := start(t, context.Background(), resume)
			for i := 0; i < 2; i++ {
				if err := s.Send(context.Background(), "hello"); err != nil {
					t.Fatal(err)
				}
				var chunks []string
				for {
					e := acpNext(t, s)
					if e.Kind == "status" && e.NativeID != "fake-session" {
						t.Fatal(e)
					}
					if e.Kind == "text_delta" {
						chunks = append(chunks, e.Text)
					}
					if e.Kind == "turn_done" {
						if e.IsError {
							t.Fatal(e)
						}
						break
					}
				}
				if strings.Join(chunks, "|") != "Hello| world" {
					t.Fatal(chunks)
				}
			}
			if s.NativeID() != "fake-session" {
				t.Fatal(s.NativeID())
			}
		}
	})
	for _, allow := range []bool{true, false} {
		name := "Deny"
		if allow {
			name = "Allow"
		}
		t.Run(name, func(t *testing.T) {
			s := start(t, context.Background(), "")
			prompt := "tool-deny"
			if allow {
				prompt = "tool-allow"
			}
			_ = s.Send(context.Background(), prompt)
			e := acpUntil(t, s, "tool_start")
			if e.ToolID != "tool-1" || strings.Contains(string(e.ToolInput), "original-secret") {
				t.Fatal(e)
			}
			e = acpUntil(t, s, "approval")
			want := "9007199254740993"
			if allow {
				want = "permission-1"
			}
			if e.RequestID != want || e.ToolName != "Write" || strings.Contains(string(e.ToolInput), "original-secret") {
				t.Fatal(e)
			}
			if err := s.Approve(e.RequestID, allow, ""); err != nil {
				t.Fatal(err)
			}
			if err := s.Approve(e.RequestID, allow, ""); err == nil {
				t.Fatal("duplicate approval")
			}
			e = acpUntil(t, s, "tool_end")
			if e.IsError == allow || strings.Contains(e.ToolOutput, "supersecret") || !strings.Contains(e.ToolOutput, "+++ hello.txt") {
				t.Fatal(e)
			}
			acpUntil(t, s, "turn_done")
		})
	}
	t.Run("InterruptPermission", func(t *testing.T) {
		s := start(t, context.Background(), "")
		_ = s.Send(context.Background(), "interrupt")
		acpUntil(t, s, "approval")
		if err := s.Interrupt(); err != nil {
			t.Fatal(err)
		}
		acpUntil(t, s, "turn_done")
		_ = s.Send(context.Background(), "hello")
		acpUntil(t, s, "turn_done")
	})
	t.Run("Queue", func(t *testing.T) {
		s := start(t, context.Background(), "")
		_ = s.Send(context.Background(), "wait")
		_ = s.Send(context.Background(), "hello")
		if e := acpUntil(t, s, "text_delta"); e.Text != "waiting" {
			t.Fatal(e)
		}
		if err := s.Interrupt(); err != nil {
			t.Fatal(err)
		}
		acpUntil(t, s, "turn_done")
		if e := acpUntil(t, s, "text_delta"); e.Text != "Hello" {
			t.Fatal(e)
		}
		acpUntil(t, s, "turn_done")
	})
	t.Run("UnknownRequestAndMalformed", func(t *testing.T) {
		s := start(t, context.Background(), "")
		_ = s.Send(context.Background(), "unknown")
		if e := acpUntil(t, s, "text_delta"); e.Text != "unknown rejected" {
			t.Fatal(e)
		}
		acpUntil(t, s, "turn_done")
		_ = s.Send(context.Background(), "hello")
		acpUntil(t, s, "turn_done")
	})
	t.Run("TurnDoneBeforeExit", func(t *testing.T) {
		s := start(t, context.Background(), "")
		_ = s.Send(context.Background(), "finish-exit")
		var kinds []string
		for e := range s.Events() {
			kinds = append(kinds, e.Kind)
		}
		if strings.Join(kinds, ",") != "status,text_delta,turn_done,exited" {
			t.Fatal(kinds)
		}
	})
	t.Run("Crash", func(t *testing.T) {
		s := start(t, context.Background(), "")
		_ = s.Send(context.Background(), "crash")
		var last string
		found := false
		for e := range s.Events() {
			last = e.Kind
			if e.Kind == "error" && strings.Contains(e.Text, "fake failure") {
				found = true
			}
			if strings.Contains(e.Text, "supersecret") {
				t.Fatal("stderr secret leaked")
			}
		}
		if last != "exited" || !found {
			t.Fatal(last, found)
		}
	})
	t.Run("AuthAndRefusal", func(t *testing.T) {
		s := start(t, context.Background(), "")
		_ = s.Send(context.Background(), "auth")
		e := acpUntil(t, s, "turn_done")
		if !e.IsError || !strings.Contains(e.Text, "Sign in to Cursor first") {
			t.Fatal(e)
		}
		_ = s.Send(context.Background(), "refusal")
		if e = acpUntil(t, s, "turn_done"); !e.IsError {
			t.Fatal(e)
		}
	})
	t.Run("CancelClose", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		s := start(t, ctx, "")
		acpUntil(t, s, "status")
		cancel()
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if s.cmd.ProcessState == nil || !s.cmd.ProcessState.Exited() {
			t.Fatal("process not reaped")
		}
		var last string
		for e := range s.Events() {
			last = e.Kind
		}
		if last != "exited" {
			t.Fatal(last)
		}
		if s.Send(context.Background(), "late") == nil {
			t.Fatal("send after exit")
		}
	})
	t.Run("ForceKill", func(t *testing.T) {
		s := start(t, context.Background(), "")
		_ = s.Send(context.Background(), "hang")
		acpUntil(t, s, "text_delta")
		began := time.Now()
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if time.Since(began) > 5*time.Second || s.cmd.ProcessState == nil {
			t.Fatal("Close not bounded/reaped")
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("SlowConsumer", func(t *testing.T) {
		s := start(t, context.Background(), "")
		_ = s.Send(context.Background(), "flood")
		deadline := time.Now().Add(15 * time.Second)
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
				t.Fatal("stdout blocked on consumer")
			}
			time.Sleep(10 * time.Millisecond)
		}
		var text strings.Builder
		for {
			e := acpNext(t, s)
			if e.Kind == "text_delta" {
				text.WriteString(e.Text)
			}
			if e.Kind == "turn_done" {
				break
			}
		}
		if text.String() != strings.Repeat("Hello world", 2000) {
			t.Fatal("delta order/loss")
		}
	})
	t.Run("OptionalModelsAndLoadCapability", func(t *testing.T) {
		for _, feature := range []string{"config", "no-model", "no-load"} {
			t.Run(feature, func(t *testing.T) {
				previous := acpArgv["cursor"]
				acpArgv["cursor"] = []string{"fake", feature}
				defer func() { acpArgv["cursor"] = previous }()
				s := start(t, context.Background(), "fake-session")
				if err := s.Send(context.Background(), "hello"); err != nil {
					t.Fatal(err)
				}
				acpUntil(t, s, "turn_done")
			})
		}
	})
	t.Run("QueuedCancellation", func(t *testing.T) {
		s := start(t, context.Background(), "")
		_ = s.Send(context.Background(), "wait")
		acpUntil(t, s, "text_delta")
		ctx, cancel := context.WithCancel(context.Background())
		_ = s.Send(ctx, "hello")
		cancel()
		if err := s.Interrupt(); err != nil {
			t.Fatal(err)
		}
		acpUntil(t, s, "turn_done")
		// Send's context only covers queueing: a message already queued still
		// runs even though the caller's context was cancelled afterwards.
		if e := acpUntil(t, s, "turn_done"); e.IsError {
			t.Fatalf("queued message was aborted: %+v", e)
		}
	})
	t.Run("ValidationAndFallback", func(t *testing.T) {
		for _, a := range []string{"cursor", "opencode", "gemini", "kimi"} {
			if !ACPSupported(a) || !ACPAgents[a] {
				t.Fatal(a)
			}
		}
		if ACPSupported("claude") {
			t.Fatal("unsupported")
		}
		for _, o := range []LiveOptions{{}, {Workspace: t.TempDir(), Model: "--bad"}, {Workspace: t.TempDir(), PermissionMode: "bypassPermissions"}} {
			if s, err := StartACPLive(context.Background(), "cursor", o); err == nil {
				_ = s.Close()
				t.Fatal("invalid options")
			}
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := StartACPLive(ctx, "cursor", LiveOptions{}); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		// A bare "agent" on PATH may be another vendor's CLI (Grok installs
		// one); it must never be used as Cursor.
		agentLooked := false
		acpLookPath = func(name string) (string, error) {
			if name == "agent" {
				agentLooked = true
				return binary, nil
			}
			return "", errors.New("missing")
		}
		defer func() { acpLookPath = func(string) (string, error) { return binary, nil } }()
		t.Setenv("LOCALAPPDATA", t.TempDir()) // no Cursor install there either
		if s, err := StartACPLive(context.Background(), "cursor", LiveOptions{Workspace: t.TempDir()}); err == nil {
			_ = s.Close()
			t.Fatal("started without a Cursor install")
		}
		if agentLooked {
			t.Fatal("looked up a bare \"agent\" binary as Cursor")
		}
		if _, err := StartACPLive(context.Background(), "gemini", LiveOptions{Workspace: t.TempDir()}); err == nil {
			t.Fatal("missing agent")
		}
	})
}
