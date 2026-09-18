package server

// Branch enumeration/merge, checkout pointer, and session-leave flow tests
// (nexus issues #97, #104, #78).
//
//   - Diff/merge enumerate real store content via ListBranchContents
//     (SearchMemory universe + ResolveRead views) and apply merge results
//     through WriteToBranch — no stub shapes.
//   - Checkout mutates the active-branch pointer (project-scoped); unknown
//     branches 404.
//   - POST /sessions/{id}/leave stamps the membership row.

import (
	"context"
	"net/http"
	"testing"

	"central-memory/internal/store"
)

func branchFlowSetup(t *testing.T) (*Server, string, string) {
	t.Helper()
	s := newTestServer()
	token := loginAs(t, s, "bob")
	projectID := resolveTestProject(t, s, token, "branch-flow-proj")
	return s, projectID, token
}

func branchIDs(t *testing.T, s *Server, token, projectID string) map[string]string {
	t.Helper()
	rec := doJSON(t, s, http.MethodGet, "/branches?project_id="+projectID, token, nil)
	var list struct {
		Items []*store.MemoryBranch `json:"items"`
	}
	decodeBody(t, rec, &list)
	out := map[string]string{}
	for _, b := range list.Items {
		out[b.Name] = b.ID
	}
	return out
}

// Diff reflects real writes: mainline v1 vs branch-overlay v2 shows one
// modification, computed over store data.
func TestBranchDiffEnumeratesRealContent(t *testing.T) {
	s, projectID, token := branchFlowSetup(t)
	ctx := context.Background()

	rec := doJSON(t, s, http.MethodPost, "/memory", token, map[string]any{
		"project_id": projectID, "key": "flow/key",
		"content": "The team uses pytest with fixture-based setup for integration tests.",
		"level":   "project",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("memory create = %d, %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodPost, "/branches", token, map[string]any{
		"name": "exp", "project_id": projectID, "from": "main",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("fork = %d, %s", rec.Code, rec.Body.String())
	}
	ids := branchIDs(t, s, token, projectID)
	bs := s.Store.(store.BranchStore)
	if err := bs.WriteToBranch(ctx, ids["exp"], &store.MemoryItem{
		Key: "flow/key", Content: "The team uses pytest with fixture-based setup for integration tests, revised on exp.",
	}); err != nil {
		t.Fatalf("WriteToBranch: %v", err)
	}

	rec = doJSON(t, s, http.MethodGet, "/branches/diff?project_id="+projectID+"&source=main&target=exp", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("diff = %d, %s", rec.Code, rec.Body.String())
	}
	var diff struct {
		Added    []any `json:"added"`
		Removed  []any `json:"removed"`
		Modified []struct {
			Key string `json:"key"`
		} `json:"modified"`
		Unchanged []string `json:"unchanged"`
	}
	decodeBody(t, rec, &diff)
	if len(diff.Modified) != 1 || diff.Modified[0].Key != "flow/key" {
		t.Fatalf("diff = %+v, want one modification on flow/key", diff)
	}
	if len(diff.Added) != 0 || len(diff.Removed) != 0 {
		t.Fatalf("diff = %+v, want no adds/removes", diff)
	}
}

// Merge applies: source-only edits land on the target via WriteToBranch, so
// a follow-up diff is clean.
func TestBranchMergeAppliesToTarget(t *testing.T) {
	s, projectID, token := branchFlowSetup(t)
	ctx := context.Background()

	rec := doJSON(t, s, http.MethodPost, "/memory", token, map[string]any{
		"project_id": projectID, "key": "merge/key",
		"content": "The team uses pytest with fixture-based setup for integration tests.",
		"level":   "project",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("memory create = %d, %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodPost, "/branches", token, map[string]any{
		"name": "feat", "project_id": projectID, "from": "main",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("fork = %d, %s", rec.Code, rec.Body.String())
	}
	ids := branchIDs(t, s, token, projectID)
	bs := s.Store.(store.BranchStore)
	if err := bs.WriteToBranch(ctx, ids["feat"], &store.MemoryItem{
		Key: "merge/key", Content: "The team uses pytest with fixture-based setup for integration tests, revised on feat.",
	}); err != nil {
		t.Fatalf("WriteToBranch: %v", err)
	}

	rec = doJSON(t, s, http.MethodPost, "/branches/merge", token, map[string]any{
		"source": "feat", "target": "main", "project_id": projectID,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("merge = %d, %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Merged    bool  `json:"merged"`
		Conflicts []any `json:"conflicts"`
	}
	decodeBody(t, rec, &out)
	if !out.Merged || len(out.Conflicts) != 0 {
		t.Fatalf("merge = %+v, want clean merge", out)
	}

	// Applied: main now resolves the source content.
	resolved, err := bs.ResolveRead(ctx, ids["main"], "merge/key")
	if err != nil {
		t.Fatalf("ResolveRead main: %v", err)
	}
	if resolved.Content != "The team uses pytest with fixture-based setup for integration tests, revised on feat." {
		t.Fatalf("main content = %q, want merged content", resolved.Content)
	}
}

// Checkout switches the project-scoped active pointer; unknown names 404.
func TestBranchCheckoutSwitchesPointer(t *testing.T) {
	s, projectID, token := branchFlowSetup(t)
	for _, name := range []string{"b1", "b2"} {
		rec := doJSON(t, s, http.MethodPost, "/branches", token, map[string]any{
			"name": name, "project_id": projectID, "from": "main",
		})
		if rec.Code != http.StatusCreated {
			t.Fatalf("fork %s = %d", name, rec.Code)
		}
	}
	ids := branchIDs(t, s, token, projectID)

	rec := doJSON(t, s, http.MethodPost, "/branches/b1/checkout?project_id="+projectID, token, map[string]any{})
	if rec.Code != http.StatusOK {
		t.Fatalf("checkout b1 = %d", rec.Code)
	}
	if got := s.activeBranchFor(projectID); got != ids["b1"] {
		t.Fatalf("active = %q, want %q", got, ids["b1"])
	}
	rec = doJSON(t, s, http.MethodPost, "/branches/b2/checkout?project_id="+projectID, token, map[string]any{})
	if rec.Code != http.StatusOK {
		t.Fatalf("checkout b2 = %d", rec.Code)
	}
	if got := s.activeBranchFor(projectID); got != ids["b2"] {
		t.Fatalf("active = %q, want %q", got, ids["b2"])
	}
	if rec := doJSON(t, s, http.MethodPost, "/branches/nope/checkout?project_id="+projectID, token, map[string]any{}); rec.Code != http.StatusNotFound {
		t.Fatalf("checkout unknown = %d, want 404", rec.Code)
	}
}

// Leave stamps the membership row and reports it.
func TestSessionLeaveFlow(t *testing.T) {
	s := newTestServer()
	token := loginAs(t, s, "alice")
	projectID := resolveTestProject(t, s, token, "leave-proj")
	rec := doJSON(t, s, http.MethodPost, "/sessions", token, map[string]any{
		"title": "leave me", "project_id": projectID,
	})
	var sess store.Session
	decodeBody(t, rec, &sess)

	rec = doJSON(t, s, http.MethodPost, "/sessions/"+sess.ID+"/join", token, map[string]any{})
	if rec.Code != http.StatusOK {
		t.Fatalf("join = %d, %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodPost, "/sessions/"+sess.ID+"/leave", token, map[string]any{})
	if rec.Code != http.StatusOK {
		t.Fatalf("leave = %d, %s", rec.Code, rec.Body.String())
	}
	if rec := doJSON(t, s, http.MethodPost, "/sessions/nope/leave", token, map[string]any{}); rec.Code != http.StatusNotFound {
		t.Fatalf("leave unknown = %d, want 404", rec.Code)
	}
}
