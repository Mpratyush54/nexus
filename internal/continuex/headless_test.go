package continuex

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadableAgentOutputKeepsAssistantTextOnly(t *testing.T) {
	raw := strings.Join([]string{
		`{"type":"item.completed","item":{"type":"command_execution","command":"secret-command"}}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"Codex reply"}}`,
		`{"type":"text","part":{"text":"OpenCode reply"}}`,
		`{"type":"result","result":"Claude reply"}`,
	}, "\n")
	got := readableAgentOutput(raw)
	if strings.Contains(got, "secret-command") || !strings.Contains(got, "Codex reply") || !strings.Contains(got, "OpenCode reply") || !strings.Contains(got, "Claude reply") {
		t.Fatalf("unexpected displayed output: %q", got)
	}
}

// Opt-in smoke test for the installed OpenCode CLI. It uses an empty temporary
// project and a non-writing prompt, so normal CI never contacts a provider.
func TestInstalledOpenCodeSeededChat(t *testing.T) {
	if os.Getenv("NX_OPENCODE_LIVE") != "1" {
		t.Skip("set NX_OPENCODE_LIVE=1 for a live CLI smoke test")
	}
	SetAllowProcessSpawnForTest(true)
	defer SetAllowProcessSpawnForTest(false)
	plan, err := SeededPlan("opencode", "Reply with the single word READY. Do not read or write files.", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	answer, err := (HeadlessExecutor{}).Turn(ctx, plan, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToUpper(answer), "READY") {
		t.Fatalf("unexpected reply: %q", answer)
	}
}

func TestInstalledOpenCodeStreamingProgress(t *testing.T) {
	if os.Getenv("NX_OPENCODE_STREAM_LIVE") != "1" {
		t.Skip("set NX_OPENCODE_STREAM_LIVE=1 for a live, read-only OpenCode stream probe")
	}
	plan, err := SeededPlan("opencode", "Write a numbered list from 1 to 40. Each line should contain the number and three ordinary words. Do not inspect or change files.", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	started := time.Now()
	var firstText time.Duration
	var textEvents, statusEvents int
	workspace, err := os.MkdirTemp("", "nexus-opencode-stream-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for attempt := 0; attempt < 10; attempt++ {
			if os.RemoveAll(workspace) == nil {
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
	}()
	result, err := (HeadlessExecutor{}).TurnStreaming(ctx, plan, workspace, func(update StreamProgress) {
		if update.Kind == "text" {
			textEvents++
			if firstText == 0 {
				firstText = time.Since(started)
			}
		} else if update.Kind == "status" {
			statusEvents++
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("OpenCode completed in %s; first text at %s; text updates=%d; status updates=%d; native ID present=%t", time.Since(started), firstText, textEvents, statusEvents, result.NativeID != "")
	if !strings.Contains(result.Text, "40") {
		t.Fatalf("unexpected final answer: %q", result.Text)
	}
	if textEvents < 2 || firstText >= time.Since(started)-time.Second {
		t.Fatalf("OpenCode did not deliver text incrementally: first=%s total=%s events=%d", firstText, time.Since(started), textEvents)
	}
}

func TestInstalledCursorSeededChat(t *testing.T) {
	if os.Getenv("NX_CURSOR_LIVE") != "1" {
		t.Skip("set NX_CURSOR_LIVE=1 for a live CLI smoke test")
	}
	SetAllowProcessSpawnForTest(true)
	defer SetAllowProcessSpawnForTest(false)
	plan, err := SeededPlanTrusted("cursor", "Reply with the single word READY. Do not read or write files.", "", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	started := time.Now()
	textEvents := 0
	answerResult, err := (HeadlessExecutor{}).TurnStreaming(ctx, plan, t.TempDir(), func(update StreamProgress) {
		if update.Kind == "text" {
			textEvents++
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	answer := answerResult.Text
	t.Logf("Cursor completed in %s; text events=%d; native ID present=%t", time.Since(started), textEvents, answerResult.NativeID != "")
	if !strings.Contains(strings.ToUpper(answer), "READY") {
		t.Fatalf("unexpected reply: %q", answer)
	}
}

// Opt-in, read-only probe of the installed non-OpenCode harness stream.
func TestInstalledOtherHarnessStreaming(t *testing.T) {
	agent := os.Getenv("NX_HARNESS_STREAM_LIVE")
	if agent != "codex" && agent != "claude" {
		t.Skip("set NX_HARNESS_STREAM_LIVE=codex or claude")
	}
	workspace := t.TempDir()
	gitInit := exec.Command("git", "init", "--quiet", workspace)
	hideConsole(gitInit)
	if output, err := gitInit.CombinedOutput(); err != nil {
		t.Fatalf("initialize temporary workspace: %v %s", err, output)
	}
	plan, err := SeededPlan(agent, "Write a numbered list from 1 to 25, with one ordinary word after each number. Do not inspect or change files.", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	start := time.Now()
	var first time.Duration
	var updates int
	result, err := (HeadlessExecutor{}).TurnStreaming(ctx, plan, workspace, func(update StreamProgress) {
		if update.Kind == "text" {
			updates++
			if first == 0 {
				first = time.Since(start)
			}
		}
	})
	t.Logf("%s completed in %s; first text at %s; updates=%d; native ID present=%t", agent, time.Since(start), first, updates, result.NativeID != "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Text, "25") {
		t.Fatalf("unexpected answer: %q", result.Text)
	}
	if updates < 2 {
		t.Fatalf("%s did not provide incremental text through this driver", agent)
	}
}

func TestAllowedImage(t *testing.T) {
	dir := t.TempDir()
	image := filepath.Join(dir, "a.PNG")
	if err := os.WriteFile(image, []byte("image"), 0600); err != nil {
		t.Fatal(err)
	}
	if !AllowedImage(image) || AllowedImage(filepath.Join(dir, "missing.png")) || AllowedImage(filepath.Join(dir, "a.exe")) {
		t.Fatal("image validation failed")
	}
}

func TestCappedOutputReportsTruncation(t *testing.T) {
	var output cappedOutput
	input := strings.Repeat("a", maxHeadlessOutput+10)
	if n, err := output.Write([]byte(input)); err != nil || n != len(input) {
		t.Fatalf("write: %d, %v", n, err)
	}
	if !output.truncated || output.Len() != maxHeadlessOutput {
		t.Fatalf("cap: truncated=%v, bytes=%d", output.truncated, output.Len())
	}
}

func TestStreamingOutputEmitsOnlyAssistantText(t *testing.T) {
	var updates []string
	output := newStreamingOutput(func(update StreamProgress) {
		if update.Kind == "text" {
			updates = append(updates, update.Text)
		}
	})
	rows := []string{
		`{"type":"item.completed","item":{"type":"command_execution","command":"secret-command"}}`,
		`{"type":"item.updated","item":{"type":"agent_message","text":"Hello"}}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"Hello world"}}`,
	}
	for _, row := range rows {
		_, _ = output.Write([]byte(row[:len(row)/2]))
		_, _ = output.Write([]byte(row[len(row)/2:] + "\n"))
	}
	output.Flush()
	if output.shown != "Hello world" || len(updates) == 0 || updates[len(updates)-1] != "Hello world" || strings.Contains(strings.Join(updates, " "), "secret-command") {
		t.Fatalf("stream leaked or lost assistant text: shown=%q updates=%q", output.shown, updates)
	}
}

func TestStreamingOutputConcatenatesClaudeDeltas(t *testing.T) {
	output := newStreamingOutput(func(StreamProgress) {})
	_, _ = output.Write([]byte("{\"type\":\"stream_event\",\"event\":{\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"Hel\"}}}\n"))
	_, _ = output.Write([]byte("{\"type\":\"stream_event\",\"event\":{\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"lo\"}}}\n"))
	if output.shown != "Hello" {
		t.Fatalf("delta stream=%q", output.shown)
	}
}

func TestStreamingOutputConcatenatesCursorDeltasAndStatus(t *testing.T) {
	var events []StreamProgress
	output := newStreamingOutput(func(update StreamProgress) { events = append(events, update) }, "cursor")
	rows := []string{
		`{"type":"system","subtype":"init","session_id":"cursor-id"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Hello "}]},"session_id":"cursor-id"}`,
		`{"type":"tool_call","subtype":"started","call_id":"read-1","tool_call":{"readToolCall":{"args":{"path":"secret.txt"}}}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"world"}]},"session_id":"cursor-id"}`,
		`{"type":"result","result":"Hello world","session_id":"cursor-id"}`,
	}
	_, _ = output.Write([]byte(strings.Join(rows, "\n") + "\n"))
	output.Flush()
	if output.shown != "Hello world" || nativeIDFromAgentOutput(output.String()) != "cursor-id" {
		t.Fatalf("cursor stream=%q native=%q", output.shown, nativeIDFromAgentOutput(output.String()))
	}
	var sawStatus, sawText bool
	for _, event := range events {
		if event.Kind != "activity" && strings.Contains(event.Text, "secret.txt") {
			t.Fatal("tool arguments leaked into assistant text or status")
		}
		if event.Kind == "status" {
			sawStatus = true
		}
		if event.Kind == "text" && event.Text == "Hello world" {
			sawText = true
		}
	}
	if !sawStatus || !sawText {
		t.Fatalf("missing cursor progress events: %+v", events)
	}
}

func TestStreamingOutputReadsOpenCodeTextPart(t *testing.T) {
	var updates []StreamProgress
	out := newStreamingOutput(func(update StreamProgress) { updates = append(updates, update) })
	_, err := io.Copy(out, strings.NewReader("{\"type\":\"text\",\"part\":{\"text\":\"hello\",\"sessionID\":\"ses_1\"}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	out.Flush()
	if out.shown != "hello" || len(updates) == 0 || updates[len(updates)-1].Text != "hello" {
		t.Fatalf("opencode event not streamed: shown=%q events=%+v", out.shown, updates)
	}
}

func TestCappedOutputReadFromHonorsLimit(t *testing.T) {
	var out cappedOutput
	_, err := io.Copy(&out, strings.NewReader(strings.Repeat("x", maxHeadlessOutput+1)))
	if err != nil || !out.truncated || out.Len() != maxHeadlessOutput {
		t.Fatalf("copy: len=%d truncated=%t err=%v", out.Len(), out.truncated, err)
	}
}

func TestNativeIDFromAgentOutput(t *testing.T) {
	for _, row := range []string{
		`{"type":"thread.started","thread_id":"codex-id"}`,
		`{"type":"text","sessionID":"open-id","part":{"text":"hi"}}`,
		`{"type":"result","session_id":"claude-id","result":"hi"}`,
	} {
		if nativeIDFromAgentOutput(row) == "" {
			t.Fatalf("missing native ID from %s", row)
		}
	}
}

func TestQueuedTurnCanBeCancelledBeforeStartingAgent(t *testing.T) {
	headlessTurnSlot <- struct{}{}
	defer func() { <-headlessTurnSlot }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var status string
	_, err := (HeadlessExecutor{}).TurnStreaming(ctx, LaunchPlan{}, "", func(update StreamProgress) {
		status = update.Text
	})
	if err != context.Canceled || !strings.Contains(status, "Waiting") {
		t.Fatalf("queued cancellation: status=%q err=%v", status, err)
	}
}
