package store

// memory_edit_test.go — MemStore coverage for issue #162 edit/history/revert.

import (
	"context"
	"errors"
	"testing"
)

func TestMemStoreUpdateMemorySnapshotsAndPatches(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	item := &MemoryItem{
		ProjectID: "p1",
		Key:       "deploy/strategy",
		Content:   "Always run migrations before flipping traffic to the new fleet.",
		Status:    StatusProposed,
		Tags:      []string{"ops"},
	}
	if err := s.CreateMemoryItem(ctx, item); err != nil {
		t.Fatalf("CreateMemoryItem: %v", err)
	}

	content := "Always run migrations before flipping traffic to the new fleet. Verify health first."
	tags := []string{"ops", "deploy"}
	updated, err := s.UpdateMemory(ctx, item.ID, MemoryPatch{Content: &content, Tags: &tags}, "editor-1")
	if err != nil {
		t.Fatalf("UpdateMemory: %v", err)
	}
	if updated.Content != content {
		t.Fatalf("content = %q, want patched", updated.Content)
	}
	if len(updated.Tags) != 2 {
		t.Fatalf("tags = %#v, want 2", updated.Tags)
	}

	hist, err := s.ListMemoryVersions(ctx, item.ID)
	if err != nil {
		t.Fatalf("ListMemoryVersions: %v", err)
	}
	if len(hist) != 1 {
		t.Fatalf("history len = %d, want 1", len(hist))
	}
	if hist[0].Version != 1 || hist[0].Content != item.Content {
		t.Fatalf("snapshot = %+v, want version 1 with original content", hist[0])
	}
	if hist[0].EditedBy != "editor-1" {
		t.Fatalf("edited_by = %q, want editor-1", hist[0].EditedBy)
	}
}

func TestMemStoreUpdateMemoryRejectsTerminal(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	item := &MemoryItem{
		ProjectID: "p1",
		Key:       "k/rejected",
		Content:   "Rejected memories must stay frozen after the decision lands.",
		Status:    StatusRejected,
	}
	if err := s.CreateMemoryItem(ctx, item); err != nil {
		t.Fatalf("CreateMemoryItem: %v", err)
	}
	content := "Should not apply this content to a rejected memory item ever."
	_, err := s.UpdateMemory(ctx, item.ID, MemoryPatch{Content: &content}, "u")
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
}

func TestMemStoreSoftDeleteAndRevert(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	item := &MemoryItem{
		ProjectID: "p1",
		Key:       "prefs/theme",
		Content:   "Prefer dark mode in the dashboard unless the user overrides it.",
		Status:    StatusConfirmed,
		Level:     LevelProject,
		Scope:     "preference",
	}
	if err := s.CreateMemoryItem(ctx, item); err != nil {
		t.Fatalf("CreateMemoryItem: %v", err)
	}
	original := item.Content

	next := "Prefer light mode in the dashboard unless the user overrides it."
	if _, err := s.UpdateMemory(ctx, item.ID, MemoryPatch{Content: &next}, "u1"); err != nil {
		t.Fatalf("UpdateMemory: %v", err)
	}
	reverted, err := s.RevertMemory(ctx, item.ID, 1, "u2")
	if err != nil {
		t.Fatalf("RevertMemory: %v", err)
	}
	if reverted.Content != original {
		t.Fatalf("reverted content = %q, want original", reverted.Content)
	}
	hist, err := s.ListMemoryVersions(ctx, item.ID)
	if err != nil {
		t.Fatalf("ListMemoryVersions: %v", err)
	}
	if len(hist) != 2 {
		t.Fatalf("history len = %d, want 2 (edit + revert snapshots)", len(hist))
	}

	deleted, err := s.SoftDeleteMemory(ctx, item.ID)
	if err != nil {
		t.Fatalf("SoftDeleteMemory: %v", err)
	}
	if deleted.Status != StatusSuperseded {
		t.Fatalf("status = %q, want SUPERSEDED", deleted.Status)
	}
	_, err = s.UpdateMemory(ctx, item.ID, MemoryPatch{Content: &next}, "u3")
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("post-delete update err = %v, want ErrConflict", err)
	}
}

func TestMemStoreUpdateMemoryPartialKeyOnly(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	item := &MemoryItem{
		ProjectID: "p1",
		Key:       "old/key",
		Content:   "Content stays the same when only the key field is patched.",
		Status:    StatusProposed,
	}
	if err := s.CreateMemoryItem(ctx, item); err != nil {
		t.Fatalf("CreateMemoryItem: %v", err)
	}
	key := "new/key"
	updated, err := s.UpdateMemory(ctx, item.ID, MemoryPatch{Key: &key}, "u")
	if err != nil {
		t.Fatalf("UpdateMemory: %v", err)
	}
	if updated.Key != "new/key" || updated.Content != item.Content {
		t.Fatalf("got key=%q content=%q", updated.Key, updated.Content)
	}
}

func TestMemStoreRevertMissingVersion(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	item := &MemoryItem{
		ProjectID: "p1",
		Key:       "k",
		Content:   "Enough characters here so the content length check passes ok.",
		Status:    StatusProposed,
	}
	if err := s.CreateMemoryItem(ctx, item); err != nil {
		t.Fatalf("CreateMemoryItem: %v", err)
	}
	if _, err := s.RevertMemory(ctx, item.ID, 99, "u"); err != ErrNotFound {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}
