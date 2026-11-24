package continuex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func cursorTool(id, subtype, tool string, args, result any) string {
	call := map[string]any{"args": args}
	if result != nil {
		call["result"] = result
	}
	data, _ := json.Marshal(map[string]any{"type": "tool_call", "subtype": subtype, "call_id": id, "tool_call": map[string]any{tool: call}})
	return string(data)
}

func TestCursorToolsStreamSeparatelyWithDetails(t *testing.T) {
	var events []StreamProgress
	out := newStreamingOutput(func(event StreamProgress) { events = append(events, event) }, "cursor")
	for _, line := range []string{
		cursorTool("shell1", "started", "shellToolCall", map[string]any{"command": "go test ./..."}, nil),
		cursorTool("shell1", "completed", "shellToolCall", nil, map[string]any{"success": map[string]any{"stdout": "all tests passed\n", "exitCode": 0}}),
	} {
		// Pipe chunks can end anywhere within JSON; this is the real io.Copy path.
		for _, chunk := range []string{line[:len(line)/2], line[len(line)/2:] + "\n"} {
			_, _ = out.Write([]byte(chunk))
		}
	}
	if out.shown != "" || len(events) != 3 {
		t.Fatalf("text or activities lost: text=%q count=%d", out.shown, len(events))
	}
	var activities []ToolActivity
	for _, event := range events {
		if event.Kind == "activity" {
			var a ToolActivity
			_ = json.Unmarshal([]byte(event.Text), &a)
			activities = append(activities, a)
		}
	}
	if len(activities) != 2 || activities[0].Status != "running" || activities[1].Status != "completed" || activities[1].Command != "go test ./..." || !strings.Contains(activities[1].Output, "all tests passed") {
		t.Fatalf("activities=%+v", activities)
	}
}

func TestCursorInstalledEnvelopeIgnoresHookAndTimestampMetadata(t *testing.T) {
	tracker := cursorActivityTracker{}
	start := `{"type":"tool_call","subtype":"started","call_id":"read1","tool_call":{"toolCallId":"read1","hookAdditionalContexts":[],"startedAtMs":"1000","readToolCall":{"args":{"path":"note.txt"}}}}`
	end := `{"type":"tool_call","subtype":"completed","call_id":"read1","tool_call":{"toolCallId":"read1","hookAdditionalContexts":[],"startedAtMs":"1000","completedAtMs":"1250","readToolCall":{"args":{"path":"note.txt"},"result":{"success":{"content":"hello"}}}}}`
	if a, ok := tracker.observe(start); !ok || a.Status != "running" {
		t.Fatalf("installed start lost: %+v", a)
	}
	if a, ok := tracker.observe(end); !ok || a.Output != "hello" || a.DurationMS != 250 {
		t.Fatalf("installed completion lost: %+v", a)
	}
}

func TestCursorOneofEnvelopeIsAlsoSupported(t *testing.T) {
	tracker := cursorActivityTracker{}
	a, ok := tracker.observe(`{"type":"tool_call","subtype":"completed","call_id":"read1","tool_call":{"tool":{"case":"readToolCall","value":{"args":{"path":"note.txt"},"result":{"result":{"case":"success","value":{"content":"hello"}}}}}}}`)
	if !ok || a.Tool != "read" || a.Output != "hello" {
		t.Fatalf("oneof tool lost: %+v", a)
	}
}

func TestCursorEditDiffUsesActualPreTurnDirtyContent(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "note.txt")
	if err := os.WriteFile(path, []byte("user's existing dirty text\n"), 0600); err != nil {
		t.Fatal(err)
	}
	tracker := cursorActivityTracker{workspace: root}
	started := cursorTool("edit1", "started", "editToolCall", map[string]any{"path": "note.txt", "streamContent": "updated text\n"}, nil)
	if _, ok := tracker.observe(started); !ok {
		t.Fatal("missing start")
	}
	_ = os.WriteFile(path, []byte("updated text\n"), 0600)
	// Duplicate start must not replace the before snapshot with the new file.
	_, _ = tracker.observe(started)
	completed := cursorTool("edit1", "completed", "editToolCall", map[string]any{"path": "note.txt"}, map[string]any{"success": map[string]any{"path": path}})
	activity, ok := tracker.observe(completed)
	if !ok || !strings.Contains(activity.Diff, "-user's existing dirty text") || !strings.Contains(activity.Diff, "+updated text") {
		t.Fatalf("diff=%+v", activity)
	}
	if paths := tracker.changedPaths(); len(paths) != 1 || paths[0] != "note.txt" {
		t.Fatalf("non-git edits lost: %v", paths)
	}
	if _, ok := tracker.observe(completed); ok {
		t.Fatal("duplicate completion changed activity")
	}
	if _, ok := tracker.observe(started); ok {
		t.Fatal("completed activity downgraded")
	}
}

func TestCursorProvidedDiffAndFailedEdits(t *testing.T) {
	tracker := cursorActivityTracker{workspace: t.TempDir()}
	activity, ok := tracker.observe(cursorTool("edit1", "completed", "editToolCall", map[string]any{"path": "main.go"}, map[string]any{"success": map[string]any{"diffString": "@@ -1 +1 @@\n-old\n+new"}}))
	if !ok || !strings.Contains(activity.Diff, "+new") {
		t.Fatalf("provided diff lost: %+v", activity)
	}
	failed, ok := tracker.observe(cursorTool("edit2", "completed", "editToolCall", map[string]any{"path": "failed.go"}, map[string]any{"writePermissionDenied": map[string]any{"message": "Denied"}}))
	if !ok || failed.Status != "error" || !strings.Contains(failed.Error, "Denied") || strings.Contains(strings.Join(tracker.changedPaths(), " "), "failed.go") {
		t.Fatalf("failed edit marked applied: %+v", failed)
	}
}

func TestCursorSensitiveDetailsAreHidden(t *testing.T) {
	tracker := cursorActivityTracker{workspace: t.TempDir()}
	activity, _ := tracker.observe(cursorTool("secret", "completed", "editToolCall", map[string]any{"path": ".env", "streamContent": "PRIVATE_FIXTURE_VALUE"}, map[string]any{"success": map[string]any{"diffString": "+PRIVATE_FIXTURE_VALUE"}}))
	encoded, _ := json.Marshal(activity)
	if strings.Contains(string(encoded), "PRIVATE_FIXTURE_VALUE") || activity.Diff != "" || len(tracker.changedPaths()) > 0 {
		t.Fatalf("sensitive file preview escaped: %s", encoded)
	}
	shell, _ := tracker.observe(cursorTool("shell", "completed", "shellToolCall", map[string]any{"command": "tool --token PRIVATE_FIXTURE_VALUE"}, map[string]any{"success": map[string]any{"stdout": "password=PRIVATE_FIXTURE_VALUE"}}))
	encoded, _ = json.Marshal(shell)
	if strings.Contains(string(encoded), "PRIVATE_FIXTURE_VALUE") {
		t.Fatalf("credential escaped: %s", encoded)
	}
}

func TestCursorPreviewBoundsAndWorkspaceBoundary(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{"../outside.txt", filepath.Join(t.TempDir(), "outside.txt"), ".env", "keys/private.pem"} {
		if full, ok := safeActivityPath(root, path); ok {
			t.Fatalf("unsafe preview path: %q", full)
		}
	}
	if text := editPreview("large.txt", strings.Repeat("line\n", 3000), "new\n"); !strings.Contains(text, "too large") {
		t.Fatal("unbounded diff")
	}
	tracker := cursorActivityTracker{workspace: root}
	for i := 0; i < maxCursorActivities+5; i++ {
		tracker.observe(cursorTool(strings.Repeat("x", i+1), "started", "readToolCall", map[string]any{"path": "note.txt"}, nil))
	}
	if len(tracker.items) > maxCursorActivities {
		t.Fatal("unbounded activity map")
	}
}

func TestInstalledCursorToolActivity(t *testing.T) {
	if os.Getenv("NX_CURSOR_ACTIVITY_LIVE") != "1" {
		t.Skip("opt-in: installed Cursor edits an isolated temporary fixture")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "fixture.txt"), []byte("before nexus test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	plan, err := SeededPlanTrusted("cursor", "In this temporary test directory, read fixture.txt and replace its contents with exactly: after nexus test\nThen reply DONE. Use the file editing tool, not a shell. Do not access other directories.", "", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	tools, diffs := 0, 0
	start := time.Now()
	output, err := (HeadlessExecutor{}).TurnStreaming(ctx, plan, root, func(event StreamProgress) {
		if event.Kind == "activity" {
			var activity ToolActivity
			_ = json.Unmarshal([]byte(event.Text), &activity)
			tools++
			if activity.Diff != "" {
				diffs++
			}
			t.Logf("activity: tool=%s state=%s has_diff=%t elapsed=%s", activity.Tool, activity.Status, activity.Diff != "", time.Since(start).Round(time.Millisecond))
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(root, "fixture.txt"))
	if tools == 0 || diffs == 0 || !strings.Contains(string(data), "after nexus test") || len(output.ChangedFiles) == 0 {
		t.Fatalf("tool/edit display missing: tools=%d diffs=%d changed_paths=%d fixture_changed=%t reply=%s", tools, diffs, len(output.ChangedFiles), strings.Contains(string(data), "after nexus test"), activityDetail(output.Text, 800))
	}
	if len(output.FileDiffs) != 1 || !strings.Contains(output.FileDiffs[0].Diff, "-before nexus test") || !strings.Contains(output.FileDiffs[0].Diff, "+after nexus test") {
		t.Fatalf("workspace diff missing from final response: %+v", output.FileDiffs)
	}
}
