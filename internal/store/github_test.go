package store

import (
	"context"
	"errors"
	"testing"
)

func TestMemStoreGitHubLinkRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	link := &GitHubLink{
		ProjectID:   "proj_1",
		Owner:       "acme",
		Repo:        "nexus",
		AccessToken: "gho_test",
		SyncMode:    "manual",
		ConnectedBy: "user_1",
	}
	if err := s.UpsertGitHubLink(ctx, link); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetGitHubLink(ctx, "proj_1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Owner != "acme" || got.Repo != "nexus" || got.AccessToken != "gho_test" {
		t.Fatalf("unexpected link: %+v", got)
	}
	if err := s.TouchGitHubImport(ctx, "proj_1"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetGitHubLink(ctx, "proj_1")
	if got.LastImportAt == nil {
		t.Fatal("expected last_import_at")
	}
	if err := s.DeleteGitHubLink(ctx, "proj_1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetGitHubLink(ctx, "proj_1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestMemStoreGitHubUserMap(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	if err := s.UpsertGitHubUserMap(ctx, &GitHubUserMap{
		GitHubLogin: "Alice",
		GitHubID:    42,
		UserID:      "u1",
		Email:       "a@example.com",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetGitHubUserMap(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if got.UserID != "u1" || got.GitHubLogin != "alice" {
		t.Fatalf("unexpected map: %+v", got)
	}
}
