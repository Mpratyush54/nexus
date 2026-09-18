package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"central-memory/internal/store"
)

func TestMemoryShareCopyAndSearchVisibility(t *testing.T) {
	s := newTestServer()
	ctx := t.Context()
	ms := s.Store.(*store.MemStore)

	proj, err := ms.ResolveProject(ctx, "https://example.com/share.git", "r1", "share")
	if err != nil {
		t.Fatal(err)
	}
	other, err := ms.ResolveProject(ctx, "https://example.com/other.git", "r2", "other")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ms.ClaimProject(ctx, proj.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := ms.ClaimProject(ctx, other.ID, "bob"); err != nil {
		t.Fatal(err)
	}
	_ = ms.GrantMember(ctx, proj.ID, "bob", "alice")
	_ = ms.GrantMember(ctx, other.ID, "alice", "bob")

	aliceTok := loginAs(t, s, "alice")
	bobTok := loginAs(t, s, "bob")

	// Create memory as alice.
	rec := doJSON(t, s, http.MethodPost, "/memory", aliceTok, map[string]any{
		"project_id": proj.ID,
		"key":        "secret-fact",
		"content":    "this is a private memory content body",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var created store.MemoryItem
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	// Set private.
	rec = doJSON(t, s, http.MethodPost, "/memory/"+created.ID+"/share", aliceTok, map[string]any{
		"visibility": "private",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("set private: %d %s", rec.Code, rec.Body.String())
	}

	// Bob search must not see private alice memory.
	rec = doJSON(t, s, http.MethodGet, "/memory/search?project_id="+proj.ID, bobTok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("bob search: %d %s", rec.Code, rec.Body.String())
	}
	var search struct {
		Items []store.MemoryItem `json:"items"`
		Count int                `json:"count"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &search)
	for _, it := range search.Items {
		if it.ID == created.ID {
			t.Fatal("bob must not see private memory in search")
		}
	}

	// Share with bob.
	rec = doJSON(t, s, http.MethodPost, "/memory/"+created.ID+"/share", aliceTok, map[string]any{
		"user_id": "bob",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("share: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodGet, "/memory/"+created.ID+"/shares", aliceTok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list shares: %d %s", rec.Code, rec.Body.String())
	}
	var shares struct {
		Count int `json:"count"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &shares)
	if shares.Count != 1 {
		t.Fatalf("shares count=%d", shares.Count)
	}

	// Bob search now sees it.
	rec = doJSON(t, s, http.MethodGet, "/memory/search?project_id="+proj.ID, bobTok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("bob search after share: %d", rec.Code)
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &search)
	found := false
	for _, it := range search.Items {
		if it.ID == created.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("bob should see shared memory")
	}

	// Copy into bob's project (alice is member → memory:write).
	rec = doJSON(t, s, http.MethodPost, "/memory/"+created.ID+"/copy", aliceTok, map[string]any{
		"project_id": other.ID,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("copy: %d %s", rec.Code, rec.Body.String())
	}
	var copied store.MemoryItem
	_ = json.Unmarshal(rec.Body.Bytes(), &copied)
	if copied.ID == created.ID || copied.ProjectID != other.ID {
		t.Fatalf("copied=%+v", copied)
	}
	if copied.Status != "PROPOSED" || copied.Visibility != store.VisibilityProject {
		t.Fatalf("copied lifecycle=%+v", copied)
	}

	// Unshare.
	rec = doJSON(t, s, http.MethodDelete, "/memory/"+created.ID+"/share/bob", aliceTok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("unshare: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodGet, "/memory/search?project_id="+proj.ID, bobTok, nil)
	_ = json.Unmarshal(rec.Body.Bytes(), &search)
	for _, it := range search.Items {
		if it.ID == created.ID {
			t.Fatal("bob must not see memory after unshare")
		}
	}
}

func TestMemoryCopyRequiresTargetMembership(t *testing.T) {
	s := newTestServer()
	ctx := t.Context()
	ms := s.Store.(*store.MemStore)

	proj, _ := ms.ResolveProject(ctx, "https://example.com/a.git", "ra", "a")
	other, _ := ms.ResolveProject(ctx, "https://example.com/b.git", "rb", "b")
	if _, err := ms.ClaimProject(ctx, proj.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := ms.ClaimProject(ctx, other.ID, "bob"); err != nil {
		t.Fatal(err)
	}

	aliceTok := loginAs(t, s, "alice")
	rec := doJSON(t, s, http.MethodPost, "/memory", aliceTok, map[string]any{
		"project_id": proj.ID,
		"key":        "k",
		"content":    "content long enough for copy deny test",
	})
	var created store.MemoryItem
	_ = json.Unmarshal(rec.Body.Bytes(), &created)

	rec = doJSON(t, s, http.MethodPost, "/memory/"+created.ID+"/copy", aliceTok, map[string]any{
		"project_id": other.ID,
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d %s", rec.Code, rec.Body.String())
	}
}
