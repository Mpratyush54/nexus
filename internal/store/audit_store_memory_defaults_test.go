package store

// Audit: CreateMemoryItem defaults and validation gaps (MemStore).
//
// Migration 001 CHECKs require: content 20-2000 chars, level in
// organization|project|personal|session, scope in
// fact|preference|decision|constraint|pattern|episode_summary, confidence
// 0-1, status in PROPOSED|CONFIRMED|REJECTED|SUPERSEDED. Postgres enforces
// these in SQL; MemStore enforces nothing. The pure validators in
// memory_transitions.go mirror the CHECKs but MemStore never calls them.

import (
	"context"
	"strings"
	"testing"
)

func TestAuditMemoryCreateDefaultsConfidenceStatusLevel(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	item := &MemoryItem{ProjectID: "p1", Key: "k1", Content: "some content here with enough length"}
	if err := s.CreateMemoryItem(ctx, item); err != nil {
		t.Fatal(err)
	}
	if item.ID == "" {
		t.Error("expected minted ID")
	}
	if item.Confidence != 1.0 {
		t.Errorf("Confidence = %v, want 1.0 default", item.Confidence)
	}
	if item.Status != StatusProposed {
		t.Errorf("Status = %q, want PROPOSED default", item.Status)
	}
	if item.Level != LevelProject {
		t.Errorf("Level = %q, want project default", item.Level)
	}
	if item.CreatedAt.IsZero() || item.UpdatedAt.IsZero() {
		t.Error("expected CreatedAt/UpdatedAt timestamps")
	}
	if !item.CreatedAt.Equal(item.UpdatedAt) {
		t.Error("fresh row should have CreatedAt == UpdatedAt")
	}
}

// FIXED(#102): Postgres defaults scope to 'fact' (migration DEFAULT +
// PostgresStore.CreateMemoryItem); MemStore now applies the same default.
func TestAuditMemoryCreateDefaultsScopeFact(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	item := &MemoryItem{ProjectID: "p1", Key: "k-scope", Content: "content with enough length here"}
	if err := s.CreateMemoryItem(ctx, item); err != nil {
		t.Fatal(err)
	}
	if item.Scope != "fact" {
		t.Errorf("Scope = %q, want fact default (Postgres parity)", item.Scope)
	}
}

// FIXED(#102): Postgres normalises empty tags to []; MemStore now does the
// same instead of keeping nil.
func TestAuditMemoryCreateDefaultsTagsNonNil(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	item := &MemoryItem{ProjectID: "p1", Key: "k-tags", Content: "content with enough length here"}
	if err := s.CreateMemoryItem(ctx, item); err != nil {
		t.Fatal(err)
	}
	if item.Tags == nil {
		t.Error("Tags = nil, want non-nil [] (Postgres parity)")
	}
}

// FIXED(#102): content CHECK (20-2000 chars) enforced — short content
// rejected like Postgres.
func TestAuditMemoryCreateAcceptsShortContent(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	item := &MemoryItem{ProjectID: "p1", Key: "k-short", Content: "hi"}
	if err := s.CreateMemoryItem(ctx, item); err == nil {
		t.Fatal("MemStore must reject short content (CHECK 20-2000)")
	}
	if err := ValidateMemoryContent(item.Content); err == nil {
		t.Error("validator itself missed short content")
	}
}

// FIXED(#102): content over 2000 chars rejected like Postgres.
func TestAuditMemoryCreateAcceptsLongContent(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	item := &MemoryItem{ProjectID: "p1", Key: "k-long", Content: strings.Repeat("x", 2001)}
	if err := s.CreateMemoryItem(ctx, item); err == nil {
		t.Fatal("MemStore must reject 2001-char content (CHECK 20-2000)")
	}
}

// FIXED(#102): level CHECK enforced (now including the ephemeral 5th tier,
// issue #30).
func TestAuditMemoryCreateAcceptsBadLevel(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	item := &MemoryItem{ProjectID: "p1", Key: "k-level", Content: "content with enough length here", Level: "galaxy"}
	if err := s.CreateMemoryItem(ctx, item); err == nil {
		t.Fatal("MemStore must reject bad level (CHECK)")
	}
	if err := ValidateMemoryLevel(item.Level); err == nil {
		t.Error("validator itself missed bad level")
	}
	// The ephemeral tier is accepted.
	ephem := &MemoryItem{ProjectID: "p1", Key: "k-ephem", Content: "content with enough length here", Level: LevelEphemeral}
	if err := s.CreateMemoryItem(ctx, ephem); err != nil {
		t.Errorf("ephemeral level must be accepted (5th tier): %v", err)
	}
}

// FIXED(#102): scope CHECK enforced.
func TestAuditMemoryCreateAcceptsBadScope(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	item := &MemoryItem{ProjectID: "p1", Key: "k-scope2", Content: "content with enough length here", Scope: "vibe"}
	if err := s.CreateMemoryItem(ctx, item); err == nil {
		t.Fatal("MemStore must reject bad scope (CHECK)")
	}
}

// FIXED(#102): confidence CHECK (0-1) enforced.
func TestAuditMemoryCreateAcceptsBadConfidence(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	for _, c := range []float32{-0.5, 5.0} {
		item := &MemoryItem{ProjectID: "p1", Key: "k-conf", Content: "content with enough length here", Confidence: c}
		if err := s.CreateMemoryItem(ctx, item); err == nil {
			t.Fatalf("MemStore must reject confidence %v (CHECK 0-1)", c)
		}
	}
}

func TestAuditMemoryGetNotFound(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	if _, err := s.GetMemoryItem(ctx, "mem_missing"); err != ErrNotFound {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}

func TestAuditMemoryConfirmHappyPath(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	item := &MemoryItem{ProjectID: "p1", Key: "k-confirm", Content: "content with enough length here"}
	if err := s.CreateMemoryItem(ctx, item); err != nil {
		t.Fatal(err)
	}
	if err := s.ConfirmMemory(ctx, item.ID, "user-1"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetMemoryItem(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusConfirmed {
		t.Errorf("Status = %q, want CONFIRMED", got.Status)
	}
	if got.ConfirmedBy != "user-1" {
		t.Errorf("ConfirmedBy = %q, want user-1", got.ConfirmedBy)
	}
	if got.UpdatedAt.Before(got.CreatedAt) {
		t.Error("UpdatedAt should not precede CreatedAt after confirm")
	}
}

func TestAuditMemoryConfirmUnknown(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	if err := s.ConfirmMemory(ctx, "mem_missing", "u"); err != ErrNotFound {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}
