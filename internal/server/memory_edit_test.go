package server

// memory_edit_test.go — httptest coverage for Phase 2 memory edit routes (issue #162).

import (
	"net/http"
	"testing"

	"central-memory/internal/store"
)

func TestMemoryEditHistoryRevertDelete(t *testing.T) {
	s := newTestServer()
	token := loginAs(t, s, "editor")
	projectID := resolveTestProject(t, s, token, "mem-edit-proj")

	rec := doJSON(t, s, http.MethodPost, "/memory", token, map[string]any{
		"project_id": projectID,
		"key":        "testing/edit",
		"content":    "The team uses pytest with fixture-based setup for integration tests.",
		"level":      "project",
		"tags":       []string{"test"},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var created store.MemoryItem
	decodeBody(t, rec, &created)

	// Partial PUT.
	rec = doJSON(t, s, http.MethodPut, "/memory/"+created.ID, token, map[string]any{
		"content": "The team uses pytest with fixture-based setup for integration tests and e2e.",
		"tags":    []string{"test", "qa"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("update status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var updated store.MemoryItem
	decodeBody(t, rec, &updated)
	if updated.Content == created.Content {
		t.Fatal("expected content to change")
	}
	if len(updated.Tags) != 2 {
		t.Fatalf("tags = %#v, want 2", updated.Tags)
	}

	// MEMORY_UPDATED event persisted.
	evs, err := s.Store.ListEvents(t.Context(), projectID, 0, 20)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	foundUpdated := false
	for _, ev := range evs {
		if ev.EventType == "MEMORY_UPDATED" {
			foundUpdated = true
			break
		}
	}
	if !foundUpdated {
		t.Fatal("expected MEMORY_UPDATED event after PUT")
	}

	// History lists the pre-edit snapshot.
	rec = doJSON(t, s, http.MethodGet, "/memory/"+created.ID+"/history", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("history status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var hist struct {
		Items []*store.MemoryVersion `json:"items"`
		Count int                    `json:"count"`
	}
	decodeBody(t, rec, &hist)
	if hist.Count != 1 || len(hist.Items) != 1 {
		t.Fatalf("history = %+v, want 1 version", hist)
	}
	if hist.Items[0].Content != created.Content {
		t.Fatalf("snapshot content = %q, want original", hist.Items[0].Content)
	}

	// Revert to version 1.
	rec = doJSON(t, s, http.MethodPost, "/memory/"+created.ID+"/revert", token, map[string]any{
		"version": 1,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("revert status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var reverted store.MemoryItem
	decodeBody(t, rec, &reverted)
	if reverted.Content != created.Content {
		t.Fatalf("reverted = %q, want original", reverted.Content)
	}

	// Soft delete → SUPERSEDED.
	rec = doJSON(t, s, http.MethodDelete, "/memory/"+created.ID, token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var deleted store.MemoryItem
	decodeBody(t, rec, &deleted)
	if deleted.Status != "SUPERSEDED" {
		t.Fatalf("status = %q, want SUPERSEDED", deleted.Status)
	}

	// Terminal rows reject further edits.
	rec = doJSON(t, s, http.MethodPut, "/memory/"+created.ID, token, map[string]any{
		"content": "Should not apply after soft delete of this memory item row.",
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("post-delete update = %d, want 409", rec.Code)
	}
}

func TestMemoryEditValidationAndAuth(t *testing.T) {
	s := newTestServer()
	token := loginAs(t, s, "alice")
	projectID := resolveTestProject(t, s, token, "mem-edit-auth")

	rec := doJSON(t, s, http.MethodPost, "/memory", token, map[string]any{
		"project_id": projectID,
		"key":        "testing/auth",
		"content":    "Authorization and empty-patch validation for memory edit routes.",
		"level":      "project",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d", rec.Code)
	}
	var created store.MemoryItem
	decodeBody(t, rec, &created)

	if rec := doJSON(t, s, http.MethodPut, "/memory/"+created.ID, token, map[string]any{}); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty patch = %d, want 400", rec.Code)
	}
	if rec := doJSON(t, s, http.MethodPut, "/memory/"+created.ID, "", map[string]any{"key": "x"}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauth put = %d, want 401", rec.Code)
	}
	if rec := doJSON(t, s, http.MethodGet, "/memory/nope/history", token, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("missing history = %d, want 404", rec.Code)
	}
	if rec := doJSON(t, s, http.MethodPost, "/memory/"+created.ID+"/revert", token, map[string]any{"version": 0}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad version = %d, want 400", rec.Code)
	}

	// Non-member cannot edit.
	bob := loginAs(t, s, "bob")
	if rec := doJSON(t, s, http.MethodPut, "/memory/"+created.ID, bob, map[string]any{
		"content": "Bob is not a member so this edit must be rejected by authorize.",
	}); rec.Code != http.StatusForbidden {
		t.Fatalf("non-member put = %d, want 403", rec.Code)
	}
}

func TestMemoryEditRejectedLocked(t *testing.T) {
	s := newTestServer()
	token := loginAs(t, s, "carol")
	projectID := resolveTestProject(t, s, token, "mem-edit-reject")

	rec := doJSON(t, s, http.MethodPost, "/memory", token, map[string]any{
		"project_id": projectID,
		"key":        "testing/locked",
		"content":    "This proposed memory will be rejected then refuse further edits.",
		"level":      "project",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d", rec.Code)
	}
	var created store.MemoryItem
	decodeBody(t, rec, &created)

	rec = doJSON(t, s, http.MethodPost, "/memory/"+created.ID+"/reject", token, map[string]any{})
	if rec.Code != http.StatusOK {
		t.Fatalf("reject status = %d", rec.Code)
	}
	rec = doJSON(t, s, http.MethodPut, "/memory/"+created.ID, token, map[string]any{
		"content": "Editing a rejected memory must fail with conflict not success.",
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("edit rejected = %d, want 409", rec.Code)
	}
}
