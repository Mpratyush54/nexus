package continuex

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

var hostedHarnessTests = []string{"opencode", "codex", "cursor", "claude", "gemini", "kimi"}

func TestHostedHarnessProviderFailureMatrix(t *testing.T) {
	cases := []struct{ message, kind string }{
		{"Free usage exceeded, subscribe to Go [retrying in 10h 11m attempt #1]", "quota"},
		{"You have hit your usage limit", "quota"},
		{"insufficient_quota", "quota"},
		{"Credit balance too low", "quota"},
		{"HTTP 401 invalid API key", "auth"},
		{"ProviderAuthError", "auth"},
		{"429 Too many requests", "rate_limit"},
		{"ModelNotFoundError: requested model is retired", "model"},
		{"model context window exceeded", "context"},
		{"503 provider overloaded", "unavailable"},
	}
	for _, agent := range hostedHarnessTests {
		if !SupportsHere(agent) {
			t.Fatalf("update matrix: %s no longer hosted", agent)
		}
		for _, test := range cases {
			problem := providerDiagnostic(agent, test.message)
			if problem == nil || problem.kind != test.kind || !strings.Contains(problem.Error(), harnessName(agent)) {
				t.Errorf("%s %s: %v", agent, test.kind, problem)
			}
		}
	}
}

func TestHostedHarnessErrorEnvelopesDoNotBecomeReplies(t *testing.T) {
	rows := []string{
		`{"type":"turn.failed","error":{"message":"You have hit your usage limit. token=private-value"}}`,
		`{"type":"result","is_error":true,"subtype":"error_during_execution","result":"Free usage exceeded, subscribe to Go"}`,
		`{"type":"assistant","error":"billing_error","message":{"content":[{"type":"text","text":"Credit balance too low"}]}}`,
		`{"type":"result","status":"error","error":{"message":"RESOURCE_EXHAUSTED"}}`,
		`{"type":"error","error":{"message":"Authentication failed"}}`,
		`{"type":"rate_limit_event","rate_limit_info":{"status":"rejected"}}`,
		`{"type":"system","subtype":"api_retry","attempt":1,"retry_delay_ms":1000,"error":{"message":"Free usage exceeded, subscribe to Go"}}`,
	}
	for _, agent := range hostedHarnessTests {
		for _, line := range rows {
			problem := streamHarnessProblem(agent, line, false)
			if problem == nil || strings.Contains(problem.Error(), "private-value") {
				t.Errorf("%s ignored/leaked failure: %v", agent, problem)
			}
			output := newStreamingOutput(func(StreamProgress) {}, agent)
			output.WriteString(line + "\n")
			if output.shown != "" {
				t.Errorf("%s rendered provider error as assistant reply: %q", agent, output.shown)
			}
		}
	}
}

func TestProviderGuardDoesNotMisclassifyConversationOrToolFailures(t *testing.T) {
	rows := []string{
		`{"type":"item.completed","item":{"type":"command_execution","output":"quota exceeded, HTTP 429","exit_code":1}}`,
		`{"type":"tool_result","is_error":true,"content":"authentication failed"}`,
		`{"type":"assistant","error":false,"message":{"content":"Explain rate limits and quotas"}}`,
		`{"type":"result","result":"The model needs authentication and quota handling.","is_error":false}`,
		`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed"}}`,
		`{"type":"message","role":"assistant","content":"free usage exceeded is an error message"}`,
		`{"level":"info","message":"quota diagnostics enabled"}`,
	}
	for _, line := range rows {
		if err := streamHarnessProblem("codex", line, false); err != nil {
			t.Errorf("false failure: %s: %v", line, err)
		}
	}
	if err := streamHarnessProblem("kimi", "INFO: quota tracking enabled", true); err != nil {
		t.Errorf("normal stderr log failed: %v", err)
	}
}

func TestProviderRetryPolicyStopsLongOrRepeatedRetries(t *testing.T) {
	for _, agent := range hostedHarnessTests {
		for _, line := range []string{
			"Free usage exceeded, subscribe to Go [retrying in 10h 11m attempt #1]",
			"Error: temporarily disconnected [retrying in 10h 11m attempt #1]",
			`{"type":"retry","attempt":3,"retry_after":1,"message":"temporary connection failure"}`,
		} {
			if err := streamHarnessProblem(agent, line, true); err == nil {
				t.Errorf("%s accepted long/repeated retry %s", agent, line)
			}
		}
		var failed error
		var statuses int
		writer := &harnessDiagnosticOutput{destination: io.Discard, agent: agent, onFailure: func(err error) { failed = err }, onProgress: func(StreamProgress) { statuses++ }}
		for i := 0; i < 3; i++ {
			writer.WriteString(`{"type":"retry","retry_after":1,"message":"temporary connection failure"}` + "\n")
		}
		if failed == nil || statuses == 0 {
			t.Errorf("%s didn't bound unnumbered retries: %v statuses=%d", agent, failed, statuses)
		}
	}
}

func TestDiagnosticWriterHandlesChunksReaderFromAndNoNewline(t *testing.T) {
	for _, agent := range hostedHarnessTests {
		for _, stderr := range []bool{true, false} {
			var problem error
			writer := &harnessDiagnosticOutput{destination: io.Discard, agent: agent, stderr: stderr, onFailure: func(err error) { problem = err }}
			writer.WriteString("Free usage ex")
			if problem != nil {
				t.Fatal("incomplete message must not fail")
			}
			if _, err := io.Copy(writer, strings.NewReader("ceeded, subscribe to Go")); err != nil {
				t.Fatal(err)
			}
			if problem == nil || !strings.Contains(problem.Error(), "quota") {
				t.Errorf("%s lost stderr=%v diagnostic: %v", agent, stderr, problem)
			}
		}
	}
}

func TestHarnessDiagnosticChild(t *testing.T) {
	if os.Getenv("NX_HEALTH_TEST_CHILD") != "1" {
		return
	}
	io.WriteString(os.Stderr, "Free usage exceeded, subscribe to Go [retrying in 10h 11m attempt #1]")
	time.Sleep(20 * time.Second)
}

func TestEveryHostedHarnessCancelsARetryingWorkerPromptly(t *testing.T) {
	for _, agent := range hostedHarnessTests {
		t.Run(agent, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			workerCtx, stop := context.WithCancelCause(ctx)
			defer stop(nil)
			cmd := exec.CommandContext(workerCtx, os.Args[0], "-test.run=^TestHarnessDiagnosticChild$")
			cmd.Env = append(os.Environ(), "NX_HEALTH_TEST_CHILD=1")
			hideConsole(cmd)
			cmd.WaitDelay = time.Second
			cmd.Stdout = io.Discard
			cmd.Stderr = &harnessDiagnosticOutput{destination: io.Discard, agent: agent, stderr: true, onFailure: stop}
			started := time.Now()
			if err := cmd.Run(); err == nil {
				t.Fatal("retrying worker should be interrupted")
			}
			var failure *harnessFailure
			if !errors.As(context.Cause(workerCtx), &failure) || failure.kind != "quota" || time.Since(started) > 4*time.Second {
				t.Fatalf("worker hung or lost reason: %v elapsed=%s", context.Cause(workerCtx), time.Since(started))
			}
		})
	}
}
