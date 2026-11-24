package core

import (
	"context"
	"encoding/json"
	"testing"
)

func TestContinuationStoreSurvivesReloadAndIsSessionScoped(t *testing.T) {
	root := t.TempDir()
	for _, prompt := range []string{"first", "second"} {
		if _, err := saveContinuation(root, ContinuationExchange{SessionID: "chat-a", Agent: "opencode", Prompt: prompt, Answer: "reply to " + prompt}); err != nil {
			t.Fatal(err)
		}
	}
	items, err := loadContinuations(root, "chat-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Prompt != "first" || items[1].Prompt != "second" {
		t.Fatalf("reload=%+v", items)
	}
	other, err := loadContinuations(root, "chat-b")
	if err != nil || len(other) != 0 {
		t.Fatalf("other session leaked: %+v %v", other, err)
	}
}

// Chats held in Nexus must reach the memory layer, scoped to their project.
func TestContinueRecordQueuesMemoryExtraction(t *testing.T) {
	cloud := &recordingCloud{}
	deps := Deps{Cloud: cloud, Online: true, ContinuationDir: t.TempDir()}
	raw, _ := json.Marshal(map[string]any{
		"session_id": "s1", "project_id": "11111111-2222-3333-4444-555555555555", "agent": "claude",
		"prompt": "We decided to use pgx instead of database/sql.", "answer": "Noted: pgx it is.",
	})
	out, err := Call(context.Background(), "continue.record", raw, deps)
	if err != nil {
		t.Fatal(err)
	}
	result := out.(map[string]any)
	if result["memory_queued"] != true {
		t.Fatalf("memory not queued: %+v", result)
	}
	cloud.mu.Lock()
	defer cloud.mu.Unlock()
	found := false
	for _, p := range cloud.posts {
		if p == "POST /memory/extract" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no extract call in %v", cloud.posts)
	}
}

func TestContinueRecordSkipsMemoryWithoutProject(t *testing.T) {
	cloud := &recordingCloud{}
	raw, _ := json.Marshal(map[string]any{"session_id": "s1", "agent": "claude", "prompt": "hi", "answer": "hello there"})
	out, err := Call(context.Background(), "continue.record", raw, Deps{Cloud: cloud, Online: true, ContinuationDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["memory_queued"] == true {
		t.Fatal("queued without a project")
	}
}
