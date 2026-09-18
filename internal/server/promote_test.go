package server

// Tests for POST /memory/{id}/promote (issue #38): session → project
// promotion over HTTP, plus 404/409 state handling. Confirm/reject are
// covered in routes_extra_test.go; this file pins the promote contract the
// dashboard's memoryAction() depends on.

import (
	"context"
	"net/http"
	"testing"

	"central-memory/internal/store"
)

func mustPromoteSessionMemory(t *testing.T, s *Server, tok string) string {
	t.Helper()
	ctx := context.Background()
	ss, ok := s.Store.(store.SessionStore)
	if !ok {
		t.Fatal("test store must implement SessionStore")
	}
	p, err := s.Store.ResolveProject(ctx, "", "", "promote-proj")
	if err != nil {
		t.Fatal(err)
	}
	ensureMembership(t, s, tok, p.ID)
	sess := &store.Session{ProjectID: p.ID, Title: "S1", CreatedBy: "u_alice"}
	if err := ss.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	m := &store.MemoryItem{ProjectID: p.ID, SessionID: sess.ID, Key: "db/cache",
		Content: "session memory about db cache redis pick for pubsub use"}
	if err := ss.CreateSessionMemory(ctx, m); err != nil {
		t.Fatal(err)
	}
	return m.ID
}

func TestMemoryPromoteHappyPath(t *testing.T) {
	s := newTestServer()
	tok := loginAs(t, s, "alice")
	id := mustPromoteSessionMemory(t, s, tok)
	rec := doJSON(t, s, http.MethodPost, "/memory/"+id+"/promote", tok, map[string]any{})
	if rec.Code != http.StatusOK {
		t.Fatalf("promote status = %d, body = %s", rec.Code, rec.Body.String())
	}
	got, err := s.Store.GetMemoryItem(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Level != "project" || got.SessionID != "" {
		t.Fatalf("promotion wrong: %+v", got)
	}
}

func TestMemoryPromoteNonSessionIs409(t *testing.T) {
	s := newTestServer()
	tok := loginAs(t, s, "alice")
	id := mustPromoteSessionMemory(t, s, tok)
	if rec := doJSON(t, s, http.MethodPost, "/memory/"+id+"/promote", tok, map[string]any{}); rec.Code != http.StatusOK {
		t.Fatalf("first promote status = %d, body = %s", rec.Code, rec.Body.String())
	}
	rec := doJSON(t, s, http.MethodPost, "/memory/"+id+"/promote", tok, map[string]any{})
	if rec.Code != http.StatusConflict {
		t.Fatalf("second promote status = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestMemoryPromoteMissingIs404(t *testing.T) {
	s := newTestServer()
	tok := loginAs(t, s, "alice")
	rec := doJSON(t, s, http.MethodPost, "/memory/no-such-id/promote", tok, map[string]any{})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing promote status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
}
