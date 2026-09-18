package server

// routes_extra_test.go — httptest coverage for the issue-#8 endpoints
// (sessions, branches, memory confirm/reject, episode resolve).

import (
	"net/http"
	"testing"

	"central-memory/internal/store"
)

// resolveTestProject creates a project via the public HTTP route and
// returns its ID. It also registers a workspace for the token's subject so
// the caller is a project member (issue #141): project-scoped routes 403
// without membership, mirroring the real daemon flow (resolve → register).
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
	rec = doJSON(t, s, http.MethodPost, "/workspaces/register", token, map[string]string{
		"project_id": project.ID,
		"machine_id": "test-machine-" + folder,
		"path":       "/tmp/test-ws-" + folder,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("register status = %d, body = %s", rec.Code, rec.Body.String())
	}
	return project.ID
}

// ensureMembership registers a workspace for the token's subject on an
// existing project (issue #141): project-scoped routes 403 without it.
// The path embeds the project ID so repeated calls never collide.
func ensureMembership(t *testing.T, s *Server, token, projectID string) {
	t.Helper()
	rec := doJSON(t, s, http.MethodPost, "/workspaces/register", token, map[string]string{
		"project_id": projectID,
		"machine_id": "test-machine",
		"path":       "/tmp/test-ws-" + projectID,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("register status = %d, body = %s", rec.Code, rec.Body.String())
	}
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

	// Reject path (native Store.RejectMemory: durable on every backend).
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
