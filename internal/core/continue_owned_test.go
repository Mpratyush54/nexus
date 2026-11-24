package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"central-memory/internal/continuex"
	"central-memory/internal/owned"
)

// TestMain keeps continue.here from writing the real owned-sessions registry.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "core-owned-*")
	if err != nil {
		panic(err)
	}
	owned.StatePath = func() string { return filepath.Join(dir, "owned.json") }
	owned.ResetForTest()
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func TestContinueHereMarksFreshNativeSessionOwned(t *testing.T) {
	// streamingTurn starts a new native session "local-native-1".
	deps := Deps{ContinueExecutor: &streamingTurn{}, ContinuationDir: t.TempDir()}
	args, _ := json.Marshal(map[string]any{"session_id": "captured", "agent": "opencode", "resume": "seeded",
		"native_id": "ses_requested", "prompt": "hello", "workspace_root": t.TempDir()})
	if _, err := Call(context.Background(), "continue.here", args, deps); err != nil {
		t.Fatal(err)
	}
	if !owned.IsSession("local-native-1") {
		t.Fatal("fresh native session started by continue.here not marked as Nexus-owned")
	}
	if owned.IsSession("ses_requested") {
		t.Fatal("the requested resume id must not be marked")
	}
}

// resumingTurn continues the requested native session and keeps its id.
type resumingTurn struct{ recordingTurn }

func (r *resumingTurn) TurnDetailed(_ context.Context, plan continuex.LaunchPlan, workspace string) (continuex.TurnOutput, error) {
	r.plan, r.workspace = plan, workspace
	return continuex.TurnOutput{Text: "resumed", NativeID: "ses_resumed_kept"}, nil
}

func TestContinueHereKeepsResumedNativeSessionCapturable(t *testing.T) {
	deps := Deps{ContinueExecutor: &resumingTurn{}, ContinuationDir: t.TempDir()}
	args, _ := json.Marshal(map[string]any{"session_id": "captured", "agent": "opencode", "resume": "native",
		"native_id": "ses_resumed_kept", "prompt": "again", "workspace_root": t.TempDir()})
	if _, err := Call(context.Background(), "continue.here", args, deps); err != nil {
		t.Fatal(err)
	}
	if owned.IsSession("ses_resumed_kept") {
		t.Fatal("a resumed session that kept its id was captured before; it must stay capturable")
	}
}
