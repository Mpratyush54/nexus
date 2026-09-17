package store

// Audit: ConfirmMemory lifecycle DAG + deep-copy semantics (MemStore).
//
// The lifecycle DAG (memory_transitions.go) allows only
// PROPOSED->{CONFIRMED,REJECTED} and CONFIRMED->SUPERSEDED; terminal states
// (REJECTED, SUPERSEDED) accept no outgoing edge. MemStore.ConfirmMemory
// ignores the DAG and unconditionally flips any row to CONFIRMED.

import (
	"context"
	"testing"
)

func auditCreateProposed(t *testing.T, ctx context.Context, s *MemStore, key string) *MemoryItem {
	t.Helper()
	item := &MemoryItem{ProjectID: "p1", Key: key, Content: "content with enough length here"}
	if err := s.CreateMemoryItem(ctx, item); err != nil {
		t.Fatal(err)
	}
	return item
}

// BUG(#89): REJECTED is terminal but ConfirmMemory resurrects it to
// CONFIRMED. Regression documents current MemStore behavior (DAG unenforced,
// unconditional flip).
func TestAuditMemoryConfirmRejectedResurrect(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	item := auditCreateProposed(t, ctx, s, "k-rej")
	stored, err := s.GetMemoryItem(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	stored.Status = StatusRejected // terminal state; no store API models reject
	if err := s.ConfirmMemory(ctx, item.ID, "u"); err != nil {
		t.Fatalf("MemStore ConfirmMemory succeeds unconditionally, got %v", err)
	}
	got, _ := s.GetMemoryItem(ctx, item.ID)
	if got.Status != StatusConfirmed {
		t.Errorf("Status = %q, want CONFIRMED (MemStore resurrects REJECTED)", got.Status)
	}
}

// BUG(#89): SUPERSEDED is terminal but ConfirmMemory resurrects it.
// Regression documents current MemStore behavior.
func TestAuditMemoryConfirmSupersededResurrect(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	item := auditCreateProposed(t, ctx, s, "k-sup")
	stored, err := s.GetMemoryItem(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	stored.Status = StatusSuperseded
	if err := s.ConfirmMemory(ctx, item.ID, "u"); err != nil {
		t.Fatalf("MemStore ConfirmMemory succeeds unconditionally, got %v", err)
	}
	got, _ := s.GetMemoryItem(ctx, item.ID)
	if got.Status != StatusConfirmed {
		t.Errorf("Status = %q, want CONFIRMED (MemStore resurrects SUPERSEDED)", got.Status)
	}
}

func TestAuditMemoryConfirmAlreadyConfirmedIdempotent(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	item := auditCreateProposed(t, ctx, s, "k-idem")
	if err := s.ConfirmMemory(ctx, item.ID, "u1"); err != nil {
		t.Fatal(err)
	}
	if err := s.ConfirmMemory(ctx, item.ID, "u2"); err != nil {
		t.Fatalf("re-confirm should be idempotent, got %v", err)
	}
}

func TestAuditMemoryStatusDAGPureValidator(t *testing.T) {
	ok := [][2]string{
		{StatusProposed, StatusConfirmed},
		{StatusProposed, StatusRejected},
		{StatusConfirmed, StatusSuperseded},
	}
	for _, tc := range ok {
		if err := ValidateStatusTransition(tc[0], tc[1]); err != nil {
			t.Errorf("legal edge %s->%s rejected: %v", tc[0], tc[1], err)
		}
	}
	bad := [][2]string{
		{StatusProposed, StatusSuperseded},
		{StatusProposed, StatusProposed},
		{StatusConfirmed, StatusProposed},
		{StatusConfirmed, StatusConfirmed},
		{StatusConfirmed, StatusRejected},
		{StatusRejected, StatusConfirmed},
		{StatusSuperseded, StatusConfirmed},
		{"BOGUS", StatusConfirmed},
		{StatusProposed, "BOGUS"},
	}
	for _, tc := range bad {
		if err := ValidateStatusTransition(tc[0], tc[1]); err == nil {
			t.Errorf("illegal edge %s->%s accepted", tc[0], tc[1])
		}
	}
	if !IsValidMemoryStatus("proposed") || IsValidMemoryStatus("BOGUS") {
		t.Error("IsValidMemoryStatus case-insensitivity/unknown handling wrong")
	}
}

// BUG(#110): GetMemoryItem returns the internal pointer — callers can
// mutate stored rows without any API call (no defensive copy). Regression
// documents current MemStore behavior.
func TestAuditMemoryGetAliasing(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	item := auditCreateProposed(t, ctx, s, "k-alias")
	const orig = "content with enough length here"
	got, err := s.GetMemoryItem(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	got.Content = "mutated by caller"
	again, err := s.GetMemoryItem(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.Content != "mutated by caller" {
		t.Errorf("mutating a Get result should mutate the store (no copy), got %q want %q", again.Content, "mutated by caller")
	}
	if orig == again.Content {
		t.Log("note: orig content no longer observable after aliasing mutation")
	}
}

// BUG(#110): SearchMemory results alias stored rows too. Regression
// documents current MemStore behavior.
func TestAuditMemorySearchAliasing(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	auditCreateProposed(t, ctx, s, "k-search-alias")
	res, err := s.SearchMemory(ctx, "p1", "", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) == 0 {
		t.Fatal("expected at least one result")
	}
	res[0].Content = "mutated via search result"
	again, _ := s.GetMemoryItem(ctx, res[0].ID)
	if again.Content != "mutated via search result" {
		t.Errorf("mutating a SearchMemory result should mutate the store (no copy), got %q", again.Content)
	}
}

// BUG(#110): CreateMemoryItem stores the caller's pointer — later caller
// mutations (and the ID/timestamp write-back) leak across the boundary.
// Regression documents current MemStore behavior.
func TestAuditMemoryCreateInputAliasing(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	item := &MemoryItem{ProjectID: "p1", Key: "k-input-alias", Content: "content with enough length here"}
	if err := s.CreateMemoryItem(ctx, item); err != nil {
		t.Fatal(err)
	}
	item.Content = "caller mutated after create"
	got, err := s.GetMemoryItem(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != "caller mutated after create" {
		t.Errorf("post-Create caller mutation should leak into store (no copy), got %q", got.Content)
	}
}
