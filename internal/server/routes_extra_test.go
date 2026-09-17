package server

// routes_extra_test.go — httptest coverage for the issue-#8 endpoints
// (sessions, branches, memory confirm/reject, episode resolve).

import (
	"net/http"
	"strings"
	"testing"

	"central-memory/internal/store"
)

// resolveTestProject creates a project via the public HTTP route and
// returns its ID.
func resolveTestProject(t *testing.T, s *Server, token, folder string) string {
	t.Helper()
	rec := doJSON(t, s, http.MethodPost, "/projects/resolve", token, map[string]string{
		"folder_name": folder,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("resolve status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var project store.Project
	decodeBody(t, rec, &project)
	if project.ID == "" {
		t.Fatal("expected project ID")
	}
	return project.ID
}

func TestSessionCreateListJoin(t *testing.T) {
	s := newTestServer()
	token := loginAs(t, s, "alice")
	projectID := resolveTestProject(t, s, token, "sess-proj")

	// Create.
	rec := doJSON(t, s, http.MethodPost, "/sessions", token, map[string]any{
		"title":      "Auth refactor",
		"project_id": projectID,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("session create status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var created store.Session
	decodeBody(t, rec, &created)
	if created.ID == "" || created.ProjectID != projectID || !created.IsActive {
		t.Fatalf("unexpected session: %+v", created)
	}

	// List.
	rec = doJSON(t, s, http.MethodGet, "/sessions?project_id="+projectID, token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("session list status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var list struct {
		Items []*store.Session `json:"items"`
		Count int              `json:"count"`
	}
	decodeBody(t, rec, &list)
	if list.Count != 1 || len(list.Items) != 1 || list.Items[0].ID != created.ID {
		t.Fatalf("unexpected session list: %+v", list)
	}

	// Join with the CLI's empty body: attributed to the auth subject.
	rec = doJSON(t, s, http.MethodPost, "/sessions/"+created.ID+"/join", token, map[string]any{})
	if rec.Code != http.StatusOK {
		t.Fatalf("session join status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var joined store.SessionParticipant
	decodeBody(t, rec, &joined)
	if joined.SessionID != created.ID || joined.UserID != "alice" || joined.Role != store.SessionRoleMember {
		t.Fatalf("unexpected participant: %+v", joined)
	}

	// Validation + auth.
	if rec := doJSON(t, s, http.MethodGet, "/sessions", token, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("list without project_id = %d, want 400", rec.Code)
	}
	if rec := doJSON(t, s, http.MethodPost, "/sessions", token, map[string]any{"title": "x"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("create without project_id = %d, want 400", rec.Code)
	}
	if rec := doJSON(t, s, http.MethodPost, "/sessions/nope/join", token, map[string]any{}); rec.Code != http.StatusNotFound {
		t.Fatalf("join unknown = %d, want 404", rec.Code)
	}
	if rec := doJSON(t, s, http.MethodGet, "/sessions?project_id="+projectID, "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated list = %d, want 401", rec.Code)
	}
}

func TestBranchListForkCheckoutDiffMerge(t *testing.T) {
	s := newTestServer()
	token := loginAs(t, s, "bob")
	projectID := resolveTestProject(t, s, token, "branch-proj")

	// Fresh project lists exactly the auto-created main branch.
	rec := doJSON(t, s, http.MethodGet, "/branches?project_id="+projectID, token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("branch list status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var list struct {
		Items []*store.MemoryBranch `json:"items"`
		Count int                   `json:"count"`
	}
	decodeBody(t, rec, &list)
	if list.Count != 1 || list.Items[0].Name != store.MainBranchName {
		t.Fatalf("expected [main], got %+v", list)
	}

	// Fork with the CLI payload shape.
	rec = doJSON(t, s, http.MethodPost, "/branches", token, map[string]any{
		"name": "bob-exp", "project_id": projectID, "from": "main",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("branch fork status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var forked store.MemoryBranch
	decodeBody(t, rec, &forked)
	if forked.Name != "bob-exp" || forked.ProjectID != projectID || forked.ParentBranchID == "" {
		t.Fatalf("unexpected fork: %+v", forked)
	}

	// Duplicate name conflicts.
	if rec := doJSON(t, s, http.MethodPost, "/branches", token, map[string]any{
		"name": "bob-exp", "project_id": projectID,
	}); rec.Code != http.StatusConflict {
		t.Fatalf("duplicate fork = %d, want 409", rec.Code)
	}

	// List now has main + fork.
	rec = doJSON(t, s, http.MethodGet, "/branches?project_id="+projectID, token, nil)
	decodeBody(t, rec, &list)
	if list.Count != 2 {
		t.Fatalf("list count = %d, want 2", list.Count)
	}

	// Checkout by name (CLI shape: empty body, optional ?project_id=).
	rec = doJSON(t, s, http.MethodPost, "/branches/bob-exp/checkout?project_id="+projectID, token, map[string]any{})
	if rec.Code != http.StatusOK {
		t.Fatalf("checkout status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var checked store.MemoryBranch
	decodeBody(t, rec, &checked)
	if checked.ID != forked.ID {
		t.Fatalf("checkout = %+v, want %+v", checked, forked)
	}
	if rec := doJSON(t, s, http.MethodPost, "/branches/nope/checkout", token, map[string]any{}); rec.Code != http.StatusNotFound {
		t.Fatalf("checkout unknown = %d, want 404", rec.Code)
	}

	// Diff resolves both endpoints (CLI shape: ?project_id=&target=).
	rec = doJSON(t, s, http.MethodGet, "/branches/diff?project_id="+projectID+"&target=bob-exp", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("diff status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var diff struct {
		Source string `json:"source"`
		Target string `json:"target"`
	}
	decodeBody(t, rec, &diff)
	if diff.Source != "main" || diff.Target != "bob-exp" {
		t.Fatalf("unexpected diff endpoints: %+v", diff)
	}

	// Merge resolves both endpoints (CLI shape).
	rec = doJSON(t, s, http.MethodPost, "/branches/merge", token, map[string]any{
		"source": "bob-exp", "target": "main", "project_id": projectID,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("merge status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var merge struct {
		Merged    bool  `json:"merged"`
		Conflicts []any `json:"conflicts"`
	}
	decodeBody(t, rec, &merge)
	if !merge.Merged || len(merge.Conflicts) != 0 {
		t.Fatalf("unexpected merge result: %+v", merge)
	}

	// Validation.
	if rec := doJSON(t, s, http.MethodGet, "/branches", token, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("branch list without project_id = %d, want 400", rec.Code)
	}
	if rec := doJSON(t, s, http.MethodGet, "/branches?project_id="+projectID, "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated branch list = %d, want 401", rec.Code)
	}
}

func TestMemoryConfirmReject(t *testing.T) {
	s := newTestServer()
	token := loginAs(t, s, "carol")
	projectID := resolveTestProject(t, s, token, "mem-decide-proj")

	create := func(key string) string {
		rec := doJSON(t, s, http.MethodPost, "/memory", token, map[string]any{
			"project_id": projectID,
			"key":        key,
			"content":    "The team uses pytest with fixture-based setup for integration tests.",
			"level":      "project",
		})
		if rec.Code != http.StatusCreated {
			t.Fatalf("memory create status = %d, body = %s", rec.Code, rec.Body.String())
		}
		var item store.MemoryItem
		decodeBody(t, rec, &item)
		return item.ID
	}

	// Confirm path (Store.ConfirmMemory).
	id := create("testing/confirm")
	rec := doJSON(t, s, http.MethodPost, "/memory/"+id+"/confirm", token, map[string]any{})
	if rec.Code != http.StatusOK {
		t.Fatalf("confirm status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var confirmed store.MemoryItem
	decodeBody(t, rec, &confirmed)
	if confirmed.Status != "CONFIRMED" {
		t.Fatalf("status = %q, want CONFIRMED", confirmed.Status)
	}

	// Reject path (no Store.RejectMemory: fallback status flip).
	rid := create("testing/reject")
	rec = doJSON(t, s, http.MethodPost, "/memory/"+rid+"/reject", token, map[string]any{})
	if rec.Code != http.StatusOK {
		t.Fatalf("reject status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var rejected store.MemoryItem
	decodeBody(t, rec, &rejected)
	if rejected.Status != "REJECTED" {
		t.Fatalf("status = %q, want REJECTED", rejected.Status)
	}
	// The rejection persists on the MemStore backend.
	stored, err := s.Store.GetMemoryItem(t.Context(), rid)
	if err != nil {
		t.Fatalf("GetMemoryItem: %v", err)
	}
	if stored.Status != "REJECTED" {
		t.Fatalf("stored status = %q, want REJECTED", stored.Status)
	}

	if rec := doJSON(t, s, http.MethodPost, "/memory/nope/confirm", token, map[string]any{}); rec.Code != http.StatusNotFound {
		t.Fatalf("confirm unknown = %d, want 404", rec.Code)
	}
	if rec := doJSON(t, s, http.MethodPost, "/memory/nope/reject", token, map[string]any{}); rec.Code != http.StatusNotFound {
		t.Fatalf("reject unknown = %d, want 404", rec.Code)
	}
}

func TestEpisodeResolve(t *testing.T) {
	s := newTestServer()
	token := loginAs(t, s, "dave")
	projectID := resolveTestProject(t, s, token, "ep-resolve-proj")

	rec := doJSON(t, s, http.MethodPost, "/episodes", token, map[string]any{
		"project_id":   projectID,
		"title":        "Auth timeout on WebSocket upgrade",
		"episode_type": "bug_fix",
		"trigger":      "ConnectionTimeout in ws.go:142",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("episode create status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var created store.Episode
	decodeBody(t, rec, &created)

	rec = doJSON(t, s, http.MethodPost, "/episodes/"+created.ID+"/resolve", token, map[string]any{
		"resolution":   "Increased handshake deadline to 10s",
		"verification": "go test ./internal/server/ passes",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("resolve status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resolved store.Episode
	decodeBody(t, rec, &resolved)
	if resolved.Status != "RESOLVED" || resolved.Resolution != "Increased handshake deadline to 10s" {
		t.Fatalf("unexpected resolved episode: %+v", resolved)
	}

	if rec := doJSON(t, s, http.MethodPost, "/episodes/nope/resolve", token, map[string]any{}); rec.Code != http.StatusNotFound {
		t.Fatalf("resolve unknown = %d, want 404", rec.Code)
	}
	if rec := doJSON(t, s, http.MethodPost, "/episodes/"+created.ID+"/resolve", "", map[string]any{}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated resolve = %d, want 401", rec.Code)
	}
}

func TestBranchDiffRealChanges(t *testing.T) {
	s := newTestServer()
	token := loginAs(t, s, "erin")
	projectID := resolveTestProject(t, s, token, "diff-real-proj")
	ms := s.Store.(*store.MemStore)
	ctx := t.Context()

	main, err := ms.EnsureMainBranch(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	child, err := ms.ForkBranch(ctx, main.ID, "diff-exp", "erin", "private", 0)
	if err != nil {
		t.Fatal(err)
	}
	// Source-only key, shared key with diverged content, child-only key.
	write := func(branchID, key, content string) {
		t.Helper()
		if err := ms.WriteToBranch(ctx, branchID, &store.MemoryItem{
			Key: key, Content: content, Status: "CONFIRMED",
		}); err != nil {
			t.Fatal(err)
		}
	}
	write(main.ID, "gone", "only on main branch here!")
	write(main.ID, "shared", "main version of the content")
	write(child.ID, "shared", "child version of the content")
	write(child.ID, "fresh", "child-only content here!!")

	rec := doJSON(t, s, http.MethodGet,
		"/branches/diff?project_id="+projectID+"&source=main&target=diff-exp", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("diff status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var diff struct {
		Source string `json:"source"`
		Target string `json:"target"`
		Added  []struct {
			Key string `json:"Key"`
		} `json:"added"`
		Removed []struct {
			Key string `json:"Key"`
		} `json:"removed"`
		Modified []struct {
			Key string `json:"Key"`
		} `json:"modified"`
	}
	decodeBody(t, rec, &diff)
	if diff.Source != "main" || diff.Target != "diff-exp" {
		t.Fatalf("endpoints = %s->%s, want main->diff-exp", diff.Source, diff.Target)
	}
	if len(diff.Added) != 1 || diff.Added[0].Key != "fresh" {
		t.Fatalf("added = %+v, want [fresh]", diff.Added)
	}
	if len(diff.Removed) != 1 || diff.Removed[0].Key != "gone" {
		t.Fatalf("removed = %+v, want [gone]", diff.Removed)
	}
	if len(diff.Modified) != 1 || diff.Modified[0].Key != "shared" {
		t.Fatalf("modified = %+v, want [shared]", diff.Modified)
	}
}

func TestBranchMergeWritesProposedAndConflicts(t *testing.T) {
	s := newTestServer()
	token := loginAs(t, s, "frank")
	projectID := resolveTestProject(t, s, token, "merge-real-proj")
	ms := s.Store.(*store.MemStore)
	ctx := t.Context()

	main, err := ms.EnsureMainBranch(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	write := func(branchID, key, content string) {
		t.Helper()
		if err := ms.WriteToBranch(ctx, branchID, &store.MemoryItem{
			Key: key, Content: content, Status: "CONFIRMED",
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Base snapshot lives on the parent (main): cfg=v0.
	write(main.ID, "cfg", "base version zero content")
	src, err := ms.ForkBranch(ctx, main.ID, "merge-src", "frank", "private", 0)
	if err != nil {
		t.Fatal(err)
	}
	dst, err := ms.ForkBranch(ctx, main.ID, "merge-dst", "frank", "private", 0)
	if err != nil {
		t.Fatal(err)
	}
	// Both siblings edit cfg differently (all three of base/source/target
	// differ -> conflict); source adds newkey (auto-merge onto target).
	write(src.ID, "cfg", "source version of cfg here")
	write(src.ID, "newkey", "child-only value to merge")
	write(dst.ID, "cfg", "target version of cfg here")

	rec := doJSON(t, s, http.MethodPost, "/branches/merge", token, map[string]any{
		"source": "merge-src", "target": "merge-dst", "project_id": projectID,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("merge status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Merged      bool     `json:"merged"`
		MergedCount int      `json:"merged_count"`
		MergedKeys  []string `json:"merged_keys"`
		Conflicts   []struct {
			Key string `json:"Key"`
		} `json:"conflicts"`
		Note string `json:"note"`
	}
	decodeBody(t, rec, &out)
	if !out.Merged {
		t.Fatalf("merged = false, body = %s", rec.Body.String())
	}
	if len(out.Conflicts) != 1 || out.Conflicts[0].Key != "cfg" {
		t.Fatalf("conflicts = %+v, want [cfg]", out.Conflicts)
	}
	if out.MergedCount != 1 || len(out.MergedKeys) != 1 || out.MergedKeys[0] != "newkey" {
		t.Fatalf("merged = %+v count=%d, want [newkey]", out.MergedKeys, out.MergedCount)
	}
	if strings.Contains(out.Note, "no rows copied") {
		t.Fatalf("stub note survived: %q", out.Note)
	}
	// The merged key landed on the target as a PROPOSED row.
	items, err := ms.ListBranchItems(ctx, dst.ID)
	if err != nil {
		t.Fatal(err)
	}
	var found *store.MemoryItem
	for _, m := range items {
		if m.Key == "newkey" {
			found = m
		}
	}
	if found == nil {
		t.Fatalf("newkey missing on target, items = %v", items)
	}
	if found.Status != "PROPOSED" {
		t.Fatalf("newkey status = %q, want PROPOSED", found.Status)
	}
	// Conflicted key was NOT overwritten on the target.
	got, err := ms.ResolveRead(ctx, dst.ID, "cfg")
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != "target version of cfg here" {
		t.Fatalf("cfg on target = %q, want target version (conflict must not write)", got.Content)
	}

	// Clean merge: source-only addition onto main -> zero conflicts.
	second, err := ms.ForkBranch(ctx, main.ID, "merge-clean", "frank", "private", 0)
	if err != nil {
		t.Fatal(err)
	}
	write(second.ID, "solo", "clean addition content!!!")
	rec = doJSON(t, s, http.MethodPost, "/branches/merge", token, map[string]any{
		"source": "merge-clean", "target": "main", "project_id": projectID,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("clean merge status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var clean struct {
		Conflicts []any `json:"conflicts"`
	}
	decodeBody(t, rec, &clean)
	if len(clean.Conflicts) != 0 {
		t.Fatalf("clean merge conflicts = %+v, want []", clean.Conflicts)
	}
}

func TestBranchCheckoutPersistsWorkspace(t *testing.T) {
	s := newTestServer()
	token := loginAs(t, s, "gail")
	projectID := resolveTestProject(t, s, token, "checkout-proj")
	ms := s.Store.(*store.MemStore)
	ctx := t.Context()

	main, err := ms.EnsureMainBranch(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	forked, err := ms.ForkBranch(ctx, main.ID, "co-exp", "gail", "private", 0)
	if err != nil {
		t.Fatal(err)
	}
	ws := &store.Workspace{ProjectID: projectID, UserID: "gail", MachineID: "m-co", Path: "/tmp/co-ws"}
	if err := ms.RegisterWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}

	// With ?workspace_id= the branch persists on the workspace row.
	rec := doJSON(t, s, http.MethodPost,
		"/branches/co-exp/checkout?project_id="+projectID+"&workspace_id="+ws.ID,
		token, map[string]any{})
	if rec.Code != http.StatusOK {
		t.Fatalf("checkout status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var checked store.MemoryBranch
	decodeBody(t, rec, &checked)
	if checked.ID != forked.ID {
		t.Fatalf("checkout = %+v, want %+v", checked, forked)
	}
	active, err := ms.GetActiveWorkspace(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if active.Branch != "co-exp" {
		t.Fatalf("workspace branch = %q, want co-exp", active.Branch)
	}

	// Without ?workspace_id= the branch resolves with an explicit
	// nothing-persisted note.
	rec = doJSON(t, s, http.MethodPost,
		"/branches/co-exp/checkout?project_id="+projectID, token, map[string]any{})
	if rec.Code != http.StatusOK {
		t.Fatalf("resolve-only checkout = %d, body = %s", rec.Code, rec.Body.String())
	}
	var bare map[string]any
	decodeBody(t, rec, &bare)
	note, _ := bare["note"].(string)
	if !strings.Contains(note, "nothing was persisted") {
		t.Fatalf("note = %q, want explicit nothing-persisted", note)
	}
	still, err := ms.GetActiveWorkspace(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if still.Branch != "co-exp" {
		t.Fatalf("workspace branch changed to %q on resolve-only checkout", still.Branch)
	}

	// Unknown branch 404; unknown workspace 404; blank name 400.
	if rec := doJSON(t, s, http.MethodPost, "/branches/nope/checkout", token, map[string]any{}); rec.Code != http.StatusNotFound {
		t.Fatalf("checkout unknown branch = %d, want 404", rec.Code)
	}
	if rec := doJSON(t, s, http.MethodPost,
		"/branches/co-exp/checkout?project_id="+projectID+"&workspace_id=ws_nope",
		token, map[string]any{}); rec.Code != http.StatusNotFound {
		t.Fatalf("checkout unknown workspace = %d, want 404", rec.Code)
	}
	if rec := doJSON(t, s, http.MethodPost,
		"/branches/%20/checkout?project_id="+projectID, token, map[string]any{}); rec.Code != http.StatusBadRequest {
		t.Fatalf("checkout blank name = %d, want 400", rec.Code)
	}
}
