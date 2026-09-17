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
	item := &MemoryItem{ProjectID: "p1", Key: "k1", Content: "some content here"}
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

// BUG(#102): Postgres defaults scope to 'fact' (migration DEFAULT +
// PostgresStore.CreateMemoryItem); MemStore leaves Scope empty. Regression
// documents current MemStore behavior.
func TestAuditMemoryCreateDefaultsScopeFact(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	item := &MemoryItem{ProjectID: "p1", Key: "k-scope", Content: "content with enough length here"}
	if err := s.CreateMemoryItem(ctx, item); err != nil {
		t.Fatal(err)
	}
	if item.Scope != "" {
		t.Errorf("Scope = %q, want empty (MemStore leaves Scope unset; Postgres parity is fact)", item.Scope)
	}
}

// BUG(#102): Postgres normalises empty tags to []; MemStore keeps nil.
// Regression documents current MemStore behavior.
func TestAuditMemoryCreateDefaultsTagsNonNil(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	item := &MemoryItem{ProjectID: "p1", Key: "k-tags", Content: "content with enough length here"}
	if err := s.CreateMemoryItem(ctx, item); err != nil {
		t.Fatal(err)
	}
	if item.Tags != nil {
		t.Errorf("Tags = %v, want nil (MemStore keeps nil; Postgres parity is non-nil [])", item.Tags)
	}
}

// BUG(#102): content CHECK (20-2000 chars) unenforced — short content
// accepted. Regression documents current MemStore behavior.
func TestAuditMemoryCreateAcceptsShortContent(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	item := &MemoryItem{ProjectID: "p1", Key: "k-short", Content: "hi"}
	if err := s.CreateMemoryItem(ctx, item); err != nil {
		t.Fatalf("MemStore accepts short content, got err %v", err)
	}
	if item.Content != "hi" {
		t.Errorf("Content = %q, want preserved short content", item.Content)
	}
	if err := ValidateMemoryContent(item.Content); err == nil {
		t.Error("validator itself missed short content")
	}
}

// BUG(#102): content over 2000 chars accepted. Regression documents current
// MemStore behavior.
func TestAuditMemoryCreateAcceptsLongContent(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	item := &MemoryItem{ProjectID: "p1", Key: "k-long", Content: strings.Repeat("x", 2001)}
	if err := s.CreateMemoryItem(ctx, item); err != nil {
		t.Fatalf("MemStore accepts 2001-char content, got err %v", err)
	}
	if len([]rune(item.Content)) != 2001 {
		t.Errorf("Content length = %d, want preserved 2001 chars", len([]rune(item.Content)))
	}
}

// BUG(#102): level CHECK unenforced. Regression documents current MemStore
// behavior.
func TestAuditMemoryCreateAcceptsBadLevel(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	item := &MemoryItem{ProjectID: "p1", Key: "k-level", Content: "content with enough length here", Level: "galaxy"}
	if err := s.CreateMemoryItem(ctx, item); err != nil {
		t.Fatalf("MemStore accepts bad level, got err %v", err)
	}
	if item.Level != "galaxy" {
		t.Errorf("Level = %q, want preserved %q (MemStore does no validation)", item.Level, "galaxy")
	}
	if err := ValidateMemoryLevel(item.Level); err == nil {
		t.Error("validator itself missed bad level")
	}
}

// BUG(#102): scope CHECK unenforced. Regression documents current MemStore
// behavior.
func TestAuditMemoryCreateAcceptsBadScope(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	item := &MemoryItem{ProjectID: "p1", Key: "k-scope2", Content: "content with enough length here", Scope: "vibe"}
	if err := s.CreateMemoryItem(ctx, item); err != nil {
		t.Fatalf("MemStore accepts bad scope, got err %v", err)
	}
	if item.Scope != "vibe" {
		t.Errorf("Scope = %q, want preserved %q (MemStore does no validation)", item.Scope, "vibe")
	}
}

// BUG(#102): confidence CHECK (0-1) unenforced. Regression documents current
// MemStore behavior.
func TestAuditMemoryCreateAcceptsBadConfidence(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	for _, c := range []float32{-0.5, 5.0} {
		item := &MemoryItem{ProjectID: "p1", Key: "k-conf", Content: "content with enough length here", Confidence: c}
		if err := s.CreateMemoryItem(ctx, item); err != nil {
			t.Fatalf("MemStore accepts confidence %v, got err %v", c, err)
		}
		if item.Confidence != c {
			t.Fatalf("MemStore rewrote confidence %v -> %v", c, item.Confidence)
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
