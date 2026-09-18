package mcp

// Regression tests for issue #135: notification side-effects, empty-root
// sandbox, write caps, budget priority order.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestNotificationToolCallDropped(t *testing.T) {
	s, _ := newTestServerWithT(t)
	ctx := context.Background()
	frame := json.RawMessage(`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"memory_write","arguments":{"key":"n/x","content":"notification writes must never execute anywhere ever"}}}`)
	if resp := s.Handle(ctx, frame); resp != nil {
		t.Fatalf("notification should yield no response, got %+v", resp)
	}
	items, err := s.store.SearchMemory(ctx, s.cfg.ProjectID, "notification writes", nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("notification tools/call executed a write: %+v", items)
	}
}

func TestSecureJoinRejectsEmptyRoot(t *testing.T) {
	for _, root := range []string{"", "   "} {
		if _, err := secureJoin(root, "a.txt"); err == nil {
			t.Fatalf("empty root %q should fail closed", root)
		}
	}
}

func TestFileWriteSizeCap(t *testing.T) {
	s, _ := newTestServerWithT(t)
	big := strings.Repeat("x", maxFileBytes+1)
	_, rpcErr := s.handleFileWrite(json.RawMessage(`{"path":"big.txt","content":"` + big + `"}`))
	if rpcErr == nil {
		t.Fatal("over-cap file_write should fail")
	}
}

func TestBudgetDropsLowPriorityFirst(t *testing.T) {
	items := []*MemoryItem{
		{Key: "o/k", Content: strings.Repeat("o", 60), Level: "organization", Scope: "fact", Confidence: 1},
		{Key: "s/k", Content: strings.Repeat("s", 60), Level: "session", Scope: "fact", Confidence: 1},
	}
	// Budget for roughly one item: session (highest priority) must win.
	xml := buildContextXMLBudgeted("demo", "main", items, 300)
	if !strings.Contains(xml, "<session>") {
		t.Fatalf("session section should survive budget pressure:\n%s", xml)
	}
	if strings.Contains(xml, "<organization>") {
		t.Fatalf("organization section should drop first:\n%s", xml)
	}
}

func TestFilterBeforeDedupeKeepsProjectVariant(t *testing.T) {
	items := []*MemoryItem{
		{Key: "k", Content: "session variant content here", Level: "session", Confidence: 1},
		{Key: "k", Content: "project variant content here", Level: "project", Confidence: 0.5},
	}
	// The fixed pipeline filters first: project variant survives.
	filtered := filterByLevel(items, "project")
	if len(filtered) != 1 || filtered[0].Level != "project" {
		t.Fatalf("filter should keep project variant: %+v", filtered)
	}
	deduped := dedupeByKey(filtered)
	if len(deduped) != 1 || deduped[0].Content != "project variant content here" {
		t.Fatalf("dedupe should keep project variant: %+v", deduped)
	}
}
