package store_test

import (
	"context"
	"testing"

	"central-memory/internal/store"
)

func TestListProjectsForUser(t *testing.T) {
	s := store.NewMemStore()
	ctx := context.Background()
	p, err := s.ResolveProject(ctx, "", "", "central-memory")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimProject(ctx, p.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	p2, err := s.ResolveProject(ctx, "", "", "other")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimProject(ctx, p2.ID, "bob"); err != nil {
		t.Fatal(err)
	}
	items, err := s.ListProjectsForUser(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != p.ID {
		t.Fatalf("got %+v want alice project %s", items, p.ID)
	}
}
