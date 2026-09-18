package store

// Audit: ConfirmMemory lifecycle DAG + deep-copy semantics (MemStore).
//
// The lifecycle DAG (memory_transitions.go) allows only
// PROPOSED->{CONFIRMED,REJECTED} and CONFIRMED->SUPERSEDED; terminal states
// (REJECTED, SUPERSEDED) accept no outgoing edge. MemStore.ConfirmMemory
// ignores the DAG and unconditionally flips any row to CONFIRMED.

import (
	"context"
	"errors"
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

// FIXED(#89): REJECTED is terminal — ConfirmMemory refuses to resurrect it.
// The row is created terminal directly (no store API models reject-before-
// RejectMemory except RejectMemory itself, which only leaves PROPOSED).
func TestAuditMemoryConfirmRejectedResurrect(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	item := &MemoryItem{ProjectID: "p1", Key: "k-rej", Content: "content with enough length here", Status: StatusRejected}
	if err := s.CreateMemoryItem(ctx, item); err != nil {
		t.Fatal(err)
	}
	if err := s.ConfirmMemory(ctx, item.ID, "u"); !errors.Is(err, ErrConflict) {
		t.Fatalf("confirming REJECTED must fail with ErrConflict, got %v", err)
	}
	got, _ := s.GetMemoryItem(ctx, item.ID)
	if got.Status != StatusRejected {
		t.Errorf("Status = %q, want REJECTED (terminal, no resurrection)", got.Status)
	}
}

// FIXED(#89): SUPERSEDED is terminal — ConfirmMemory refuses to resurrect it.
func TestAuditMemoryConfirmSupersededResurrect(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	item := &MemoryItem{ProjectID: "p1", Key: "k-sup", Content: "content with enough length here", Status: StatusSuperseded}
	if err := s.CreateMemoryItem(ctx, item); err != nil {
		t.Fatal(err)
	}
	if err := s.ConfirmMemory(ctx, item.ID, "u"); !errors.Is(err, ErrConflict) {
		t.Fatalf("confirming SUPERSEDED must fail with ErrConflict, got %v", err)
	}
	got, _ := s.GetMemoryItem(ctx, item.ID)
	if got.Status != StatusSuperseded {
		t.Errorf("Status = %q, want SUPERSEDED (terminal, no resurrection)", got.Status)
	}
}

// FIXED(#98): RejectMemory persists PROPOSED -> REJECTED; second rejects and
// confirms of terminal rows fail.
func TestAuditMemoryRejectLifecycle(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	item := auditCreateProposed(t, ctx, s, "k-reject")
	if err := s.RejectMemory(ctx, item.ID, "u1"); err != nil {
		t.Fatalf("RejectMemory PROPOSED: %v", err)
	}
	got, _ := s.GetMemoryItem(ctx, item.ID)
	if got.Status != StatusRejected {
		t.Errorf("Status = %q, want REJECTED persisted", got.Status)
	}
	if err := s.RejectMemory(ctx, item.ID, "u1"); !errors.Is(err, ErrConflict) {
		t.Errorf("double reject must fail with ErrConflict, got %v", err)
	}
	if err := s.ConfirmMemory(ctx, item.ID, "u1"); !errors.Is(err, ErrConflict) {
		t.Errorf("confirm of REJECTED must fail with ErrConflict, got %v", err)
	}
	if err := s.RejectMemory(ctx, "mem_missing", "u1"); err != ErrNotFound {
		t.Errorf("reject of missing id: got %v, want ErrNotFound", err)
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

// FIXED(#110): GetMemoryItem returns a defensive copy — caller mutations
// no longer corrupt stored rows.
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
	if again.Content != orig {
		t.Errorf("store corrupted via Get aliasing: got %q want %q", again.Content, orig)
	}
}

// FIXED(#110): SearchMemory results are copies too.
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
	if again.Content == "mutated via search result" {
		t.Error("store corrupted via SearchMemory aliasing")
	}
}

// FIXED(#110): CreateMemoryItem copies the caller's struct — later caller
// mutations no longer leak into the store.
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
	if got.Content == "caller mutated after create" {
		t.Error("post-Create caller mutation leaked into store")
	}
}
