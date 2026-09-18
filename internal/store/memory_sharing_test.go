package store

import (
	"context"
	"testing"
)

func TestNormalizeVisibility(t *testing.T) {
	if NormalizeVisibility("") != VisibilityProject {
		t.Fatalf("blank want project, got %q", NormalizeVisibility(""))
	}
	if NormalizeVisibility("PUBLIC") != VisibilityPublic {
		t.Fatalf("PUBLIC want public")
	}
	if NormalizeVisibility("nope") != VisibilityPrivate {
		t.Fatalf("unknown must fail closed to private")
	}
}

func TestCanViewMemoryMatrix(t *testing.T) {
	creator := "u-creator"
	other := "u-other"
	item := &MemoryItem{ProposedBy: creator, Visibility: VisibilityPrivate, ProjectID: "p1"}

	if !CanViewMemory(item, creator, true, false) {
		t.Fatal("creator must see private")
	}
	if CanViewMemory(item, other, true, false) {
		t.Fatal("member must not see private")
	}

	item.Visibility = VisibilityProject
	if !CanViewMemory(item, other, true, false) {
		t.Fatal("member must see project")
	}
	if CanViewMemory(item, other, false, false) {
		t.Fatal("non-member must not see project")
	}

	item.Visibility = VisibilityPublic
	if !CanViewMemory(item, "", false, false) {
		t.Fatal("public must be visible unauthenticated")
	}

	item.Visibility = VisibilityShared
	if !CanViewMemory(item, creator, false, false) {
		t.Fatal("creator must see shared")
	}
	if CanViewMemory(item, other, true, false) {
		t.Fatal("member without share must not see shared")
	}
	if !CanViewMemory(item, other, false, true) {
		t.Fatal("explicit share must grant access")
	}
}

func TestSearchMemoryEnforcesVisibility(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	proj, err := s.ResolveProject(ctx, "https://example.com/vis.git", "root", "vis")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimProject(ctx, proj.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	_ = s.GrantMember(ctx, proj.ID, "bob", "alice")
	_ = s.GrantMember(ctx, proj.ID, "carol", "alice")

	mk := func(key, vis, by string) *MemoryItem {
		m := &MemoryItem{
			ProjectID:  proj.ID,
			Key:        key,
			Content:    "visibility content long enough here ok",
			Visibility: vis,
			ProposedBy: by,
			Status:     StatusConfirmed,
		}
		if err := s.CreateMemoryItem(ctx, m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	priv := mk("k-private", VisibilityPrivate, "alice")
	shared := mk("k-shared", VisibilityShared, "alice")
	_ = mk("k-project", VisibilityProject, "alice")
	_ = mk("k-public", VisibilityPublic, "alice")

	if _, err := s.ShareMemory(ctx, shared.ID, "bob", "", "alice"); err != nil {
		t.Fatal(err)
	}

	// Bob (member + share): sees shared, project, public — not alice's private.
	got, err := s.SearchMemory(WithViewer(ctx, "bob"), proj.ID, "", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]bool{}
	for _, m := range got {
		keys[m.Key] = true
	}
	if keys["k-private"] || !keys["k-shared"] || !keys["k-project"] || !keys["k-public"] {
		t.Fatalf("bob keys=%v", keys)
	}

	// Carol (member, no share): project + public only.
	got, err = s.SearchMemory(WithViewer(ctx, "carol"), proj.ID, "", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	keys = map[string]bool{}
	for _, m := range got {
		keys[m.Key] = true
	}
	if keys["k-private"] || keys["k-shared"] || !keys["k-project"] || !keys["k-public"] {
		t.Fatalf("carol keys=%v", keys)
	}

	// Alice (creator): sees all including private.
	got, err = s.SearchMemory(WithViewer(ctx, "alice"), proj.ID, "", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 4 {
		t.Fatalf("alice should see all, got %d", len(got))
	}

	// GetMemoryItem with viewer hides private from bob.
	if _, err := s.GetMemoryItem(WithViewer(ctx, "bob"), priv.ID); err != ErrNotFound {
		t.Fatalf("bob GetMemoryItem private: %v", err)
	}
	if _, err := s.GetMemoryItem(WithViewer(ctx, "alice"), priv.ID); err != nil {
		t.Fatalf("alice GetMemoryItem private: %v", err)
	}

	// Empty viewer: project + public only.
	got, err = s.SearchMemory(ctx, proj.ID, "", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	keys = map[string]bool{}
	for _, m := range got {
		keys[m.Key] = true
	}
	if keys["k-private"] || keys["k-shared"] || !keys["k-project"] || !keys["k-public"] {
		t.Fatalf("anon keys=%v", keys)
	}
}

func TestShareUnshareAndCopy(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	src, err := s.ResolveProject(ctx, "https://example.com/src.git", "root1", "src")
	if err != nil {
		t.Fatal(err)
	}
	dst, err := s.ResolveProject(ctx, "https://example.com/dst.git", "root2", "dst")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimProject(ctx, src.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimProject(ctx, dst.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	_ = s.GrantMember(ctx, dst.ID, "bob", "alice")

	m := &MemoryItem{
		ProjectID:  src.ID,
		Key:        "copy-me",
		Content:    "content long enough to copy across projects",
		ProposedBy: "alice",
		Status:     StatusConfirmed,
	}
	if err := s.CreateMemoryItem(ctx, m); err != nil {
		t.Fatal(err)
	}

	sh, err := s.ShareMemory(ctx, m.ID, "bob", "", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if sh.SharedWithUserID != "bob" {
		t.Fatalf("share=%+v", sh)
	}
	got, _ := s.GetMemoryItem(ctx, m.ID)
	if got.Visibility != VisibilityShared {
		t.Fatalf("visibility after share=%q", got.Visibility)
	}

	list, err := s.ListMemoryShares(ctx, m.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("list=%v err=%v", list, err)
	}
	if err := s.UnshareMemory(ctx, m.ID, "bob"); err != nil {
		t.Fatal(err)
	}
	list, _ = s.ListMemoryShares(ctx, m.ID)
	if len(list) != 0 {
		t.Fatalf("expected empty shares, got %d", len(list))
	}

	dup, err := s.CopyMemory(ctx, m.ID, dst.ID, "bob")
	if err != nil {
		t.Fatal(err)
	}
	if dup.ID == m.ID || dup.ProjectID != dst.ID {
		t.Fatalf("dup=%+v", dup)
	}
	if dup.Status != StatusProposed || dup.ProposedBy != "bob" {
		t.Fatalf("dup lifecycle=%+v", dup)
	}
	if dup.Visibility != VisibilityProject {
		t.Fatalf("dup visibility=%q", dup.Visibility)
	}
	if dup.Key != m.Key || dup.Content != m.Content {
		t.Fatalf("dup content mismatch")
	}
}
