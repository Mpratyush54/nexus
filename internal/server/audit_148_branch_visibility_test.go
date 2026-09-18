package server

// Branch visibility enforcement tests (issue #148): private branches are
// hidden from non-owner members on list and forbidden on checkout/diff/
// merge; shared branches stay readable.

import (
	"context"
	"net/http"
	"testing"

	"central-memory/internal/store"
)

func visibilitySetup(t *testing.T) (*Server, string, string, string) {
	t.Helper()
	s := newTestServer()
	owner := loginAs(t, s, "owner")
	member := loginAs(t, s, "member")
	proj := resolveTestProject(t, s, owner, "vis-proj")
	// Member joins via grant (not via workspace bootstrap).
	if err := s.Store.GrantMember(context.Background(), proj, "member", "owner"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	bs := s.Store.(store.BranchStore)
	main, err := bs.EnsureMainBranch(ctx, proj)
	if err != nil {
		t.Fatal(err)
	}
	priv, err := bs.ForkBranch(ctx, main.ID, "secret-exp", "owner", "private", 0)
	if err != nil {
		t.Fatal(err)
	}
	return s, proj, priv.ID, member
}

func TestPrivateBranchHiddenFromList(t *testing.T) {
	s, proj, privID, member := visibilitySetup(t)
	rec := doJSON(t, s, http.MethodGet, "/branches?project_id="+proj, member, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d", rec.Code)
	}
	var out struct {
		Items []store.MemoryBranch `json:"items"`
	}
	decodeBody(t, rec, &out)
	for _, b := range out.Items {
		if b.ID == privID {
			t.Fatal("private branch leaked in member list")
		}
	}
}

func TestPrivateBranchCheckoutDiffMergeForbidden(t *testing.T) {
	s, proj, privID, member := visibilitySetup(t)
	// Checkout by ID.
	rec := doJSON(t, s, http.MethodPost, "/branches/"+privID+"/checkout?project_id="+proj, member, map[string]any{})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("checkout private = %d, want 403", rec.Code)
	}
	// Diff with private target.
	rec = doJSON(t, s, http.MethodGet, "/branches/diff?project_id="+proj+"&source=main&target=secret-exp", member, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("diff private = %d, want 403", rec.Code)
	}
	// Merge from private source.
	rec = doJSON(t, s, http.MethodPost, "/branches/merge", member, map[string]any{
		"source": "secret-exp", "target": "main", "project_id": proj,
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("merge private = %d, want 403", rec.Code)
	}
}

func TestSharedBranchReadableByMember(t *testing.T) {
	s, proj, _, member := visibilitySetup(t)
	ctx := context.Background()
	bs := s.Store.(store.BranchStore)
	main, err := bs.EnsureMainBranch(ctx, proj)
	if err != nil {
		t.Fatal(err)
	}
	shared, err := bs.ForkBranch(ctx, main.ID, "open-exp", "owner", "shared", 0)
	if err != nil {
		t.Fatal(err)
	}
	rec := doJSON(t, s, http.MethodGet, "/branches?project_id="+proj, member, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d", rec.Code)
	}
	var out struct {
		Items []store.MemoryBranch `json:"items"`
	}
	decodeBody(t, rec, &out)
	found := false
	for _, b := range out.Items {
		if b.ID == shared.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("shared branch missing from member list")
	}
	rec = doJSON(t, s, http.MethodPost, "/branches/"+shared.ID+"/checkout?project_id="+proj, member, map[string]any{})
	if rec.Code != http.StatusOK {
		t.Fatalf("checkout shared = %d, want 200", rec.Code)
	}
}
