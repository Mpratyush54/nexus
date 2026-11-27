package agentrun

import (
	"os"
	"path/filepath"
	"testing"

	"central-memory/internal/checkpoint"
	"central-memory/internal/continuex"
)

// A turn in a project folder is snapshotted before the agent gets the
// message; the recorded result carries the checkpoint, the saved message
// index finds it, and RewindTo puts the folder back.
func TestTurnCheckpointAndRewind(t *testing.T) {
	ws := t.TempDir()
	file := filepath.Join(ws, "app.txt")
	if err := os.WriteFile(file, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, 6, t.TempDir()) // state files live outside the project folder
	store := checkpoint.Open(filepath.Join(t.TempDir(), "cp"), checkpoint.Options{})
	h.m.cfg.Checkpoints = store
	h.m.cfg.Record = func(r TurnRecord) (any, error) {
		h.mu.Lock()
		h.saved = append(h.saved, r)
		h.mu.Unlock()
		return map[string]any{"text": r.Answer, "user_idx": int64(4242), "exchange_id": "ex-1"}, nil
	}

	if _, err := h.m.Send(SendRequest{SessionID: "a", ProjectID: "a", Agent: "claude", Workspace: ws, Text: "edit the file", Mode: ModeFull}); err != nil {
		t.Fatal(err)
	}
	f := h.live("a")
	recv(t, f.sent) // the snapshot is taken before the agent gets the message
	if err := os.WriteFile(file, []byte("after\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "new.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// While the turn runs, restoring is refused.
	if _, err := h.m.RewindTo("a", ""); err == nil {
		t.Fatal("a rewind during a running turn should be refused")
	}

	f.events <- continuex.LiveEvent{Kind: "text", Text: "done"}
	f.events <- continuex.LiveEvent{Kind: "turn_done"}
	rec := h.waitFor("recorded", isType("live.recorded", "a"))
	result, _ := rec["result"].(map[string]any)
	if result == nil || result["checkpoint_id"] == "" || result["checkpoint_files"] != 2 {
		t.Fatalf("result = %v", rec)
	}
	if h.saved[0].Checkpoint == "" {
		t.Fatal("the turn record should carry its checkpoint")
	}

	review, err := store.Review(result["checkpoint_id"].(string))
	if err != nil || review.Pending != 2 {
		t.Fatalf("review = %+v %v", review, err)
	}

	plan, err := h.m.PreviewRewind("a", "4242")
	if err != nil || len(plan.Files) != 2 {
		t.Fatalf("plan = %+v %v", plan, err)
	}
	res, err := h.m.RewindTo("a", "4242")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(file); string(b) != "before\n" {
		t.Fatalf("app.txt = %q", b)
	}
	if _, err := os.Stat(filepath.Join(ws, "new.txt")); !os.IsNotExist(err) {
		t.Fatal("the file the agent created should be gone")
	}
	if res.Rewind.FromIdx != 4242 {
		t.Fatalf("rewind = %+v", res.Rewind)
	}
	h.waitFor("rewound event", isType("live.rewound", "a"))
	if _, err := h.m.UndoRewind("a", res.Rewind.UndoID); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(file); string(b) != "after\n" {
		t.Fatalf("after undo app.txt = %q", b)
	}
}

// Without a store nothing is snapshotted and rewinds say checkpoints are off.
func TestNoCheckpointsWithoutStore(t *testing.T) {
	h := newHarness(t, 6, t.TempDir())
	h.send("a", "hi", ModeAsk)
	f := h.live("a")
	recv(t, f.sent)
	f.events <- continuex.LiveEvent{Kind: "text", Text: "ok"}
	f.events <- continuex.LiveEvent{Kind: "turn_done"}
	rec := h.waitFor("recorded", isType("live.recorded", "a"))
	if result, _ := rec["result"].(map[string]any); result["checkpoint_id"] != nil {
		t.Fatalf("result = %v", result)
	}
	if _, err := h.m.RewindTo("a", "1"); err != errNoCheckpoints {
		t.Fatalf("err = %v", err)
	}
}
