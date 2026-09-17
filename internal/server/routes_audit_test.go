package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"central-memory/internal/store"
)

// Audit coverage for internal/server/routes.go + server.go middleware +
// routes_extra.go reject fallback. Reuses helpers from server_test.go
// (newTestServer, loginAs, doJSON, decodeBody); never edits them.

// /healthz must be exactly {"ok":true} with no auth.
func TestAuditRoutesHealthzShape(t *testing.T) {
	s := newTestServer()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("healthz not JSON: %v (%s)", err, rec.Body.String())
	}
	if len(out) != 1 || out["ok"] != true {
		t.Fatalf("healthz = %s, want exactly {\"ok\":true}", rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
}

// handleLogin empty-credential matrix: every empty/blank variant is 400.
func TestAuditRoutesLoginEmptyCredMatrix(t *testing.T) {
	s := newTestServer()
	cases := []map[string]string{
		{"username": "", "password": ""},
		{"username": "", "password": "secret"},
		{"username": "alice", "password": ""},
		{"username": "   ", "password": "secret"},
	}
	for i, body := range cases {
		rec := doJSON(t, s, http.MethodPost, "/auth/login", "", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("case %d: status = %d, want 400 (%s)", i, rec.Code, rec.Body.String())
		}
	}
	// Malformed JSON body is also 400 with the error envelope.
	req := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader("{not-json"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed JSON status = %d, want 400", rec.Code)
	}
}

// requireAuth must stash the subject in X-Auth-Subject for handlers.
func TestAuditRoutesRequireAuthSubjectHeader(t *testing.T) {
	s := newTestServer()
	token := loginAs(t, s, "subject-propagation")
	var seen string
	probe := s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("X-Auth-Subject")
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	probe(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if seen != "subject-propagation" {
		t.Fatalf("X-Auth-Subject = %q, want subject-propagation", seen)
	}
}

// requireAuth has NO ?token= query path on REST routes (unlike the WS
// endpoint in ws.go which accepts ?token=). A token supplied only via query
// must still 401. This documents the REST/WS divergence.
func TestAuditRoutesQueryTokenNotAccepted(t *testing.T) {
	s := newTestServer()
	token := loginAs(t, s, "query-token-user")
	req := httptest.NewRequest(http.MethodGet, "/memory/search?project_id=p&q=x&token="+token, nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("?token= status = %d, want 401 (REST requires Authorization header)", rec.Code)
	}
}

// Search limit validation matrix on both search routes.
func TestAuditRoutesSearchLimitValidation(t *testing.T) {
	s := newTestServer()
	token := loginAs(t, s, "limit-audit")
	rec := doJSON(t, s, http.MethodPost, "/projects/resolve", token, map[string]string{"folder_name": "limit-proj"})
	var project store.Project
	decodeBody(t, rec, &project)

	badLimits := []string{"0", "-5", "-1", "abc", "1.5"}
	for _, lim := range badLimits {
		for _, route := range []string{
			"/memory/search?project_id=" + project.ID + "&q=x&limit=" + lim,
			"/episodes/search?project_id=" + project.ID + "&limit=" + lim,
		} {
			rec := doJSON(t, s, http.MethodGet, route, token, nil)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("GET %s: status = %d, want 400", route, rec.Code)
			}
		}
	}
}

// Large limits pass straight through to the store: no upper-bound clamp
// exists on either search route. Documented as a potential DoS vector
// (a caller can request limit=1000000); the store simply returns what it
// has. This test locks in current behavior, not desired behavior.
func TestAuditRoutesLargeLimitPassThrough(t *testing.T) {
	s := newTestServer()
	token := loginAs(t, s, "large-limit-audit")
	rec := doJSON(t, s, http.MethodPost, "/projects/resolve", token, map[string]string{"folder_name": "large-limit-proj"})
	var project store.Project
	decodeBody(t, rec, &project)

	rec = doJSON(t, s, http.MethodPost, "/memory", token, map[string]any{
		"project_id": project.ID,
		"key":        "audit/large-limit",
		"content":    "The team uses pytest with fixture-based setup for integration tests.",
		"level":      "project",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", rec.Code, rec.Body.String())
	}
	// NOTE(search-limit): no clamp — limit=1000000 is accepted and forwarded.
	for _, route := range []string{
		"/memory/search?project_id=" + project.ID + "&q=pytest&limit=1000000",
		"/episodes/search?project_id=" + project.ID + "&limit=1000000",
	} {
		rec := doJSON(t, s, http.MethodGet, route, token, nil)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s: status = %d, want 200 (large limit passes through)", route, rec.Code)
		}
	}
}

// Reject fallback mutates the live MemStore pointer: the status flip
// persists without a dedicated Store.RejectMemory method. Attribution
// (rejected_by) is silently dropped — MemoryItem has no RejectedBy column.
// TODO(store): add Store.RejectMemory so PostgresStore (which returns
// scanned copies, not live pointers) persists rejections too.
func TestAuditRoutesRejectFallbackPointerMutation(t *testing.T) {
	s := newTestServer()
	token := loginAs(t, s, "reject-audit")
	rec := doJSON(t, s, http.MethodPost, "/projects/resolve", token, map[string]string{"folder_name": "reject-audit-proj"})
	var project store.Project
	decodeBody(t, rec, &project)

	rec = doJSON(t, s, http.MethodPost, "/memory", token, map[string]any{
		"project_id": project.ID,
		"key":        "audit/reject-fallback",
		"content":    "The team uses pytest with fixture-based setup for integration tests.",
		"level":      "project",
	})
	var created store.MemoryItem
	decodeBody(t, rec, &created)

	// MemStore must NOT implement the native reject interface (else this
	// test would exercise the wrong path).
	if _, ok := s.Store.(rejectMemoryStore); ok {
		t.Fatal("MemStore must not implement rejectMemoryStore for fallback test")
	}

	before, err := s.Store.GetMemoryItem(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("GetMemoryItem: %v", err)
	}
	livePtr := before // MemStore returns the live map pointer.

	rec = doJSON(t, s, http.MethodPost, "/memory/"+created.ID+"/reject", token, map[string]any{"rejected_by": "auditor"})
	if rec.Code != http.StatusOK {
		t.Fatalf("reject status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out store.MemoryItem
	decodeBody(t, rec, &out)
	if out.Status != "REJECTED" {
		t.Fatalf("response status = %q, want REJECTED", out.Status)
	}
	if livePtr.Status != "REJECTED" {
		t.Fatal("fallback must mutate the live MemStore pointer in place")
	}
	stored, err := s.Store.GetMemoryItem(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("GetMemoryItem: %v", err)
	}
	if stored.Status != "REJECTED" {
		t.Fatalf("stored status = %q, want REJECTED", stored.Status)
	}
	// TODO(store): rejected_by attribution is dropped (see routes_extra.go
	// `_ = rejectedBy`); no field on the response carries it.
	if !strings.Contains(rec.Body.String(), "REJECTED") {
		t.Fatalf("response must carry REJECTED, got %s", rec.Body.String())
	}
}

// Reject on unknown id is 404.
func TestAuditRoutesRejectUnknownID(t *testing.T) {
	s := newTestServer()
	token := loginAs(t, s, "reject-unknown")
	rec := doJSON(t, s, http.MethodPost, "/memory/does-not-exist/reject", token, map[string]any{})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// Unauthenticated access to an extra route is 401 (auth wrapper parity).
func TestAuditRoutesExtraAuthParity(t *testing.T) {
	s := newTestServer()
	for _, target := range []string{
		"/sessions?project_id=x",
		"/branches?project_id=x",
		"/memory/abc/reject",
		"/episodes/abc/resolve",
	} {
		req := httptest.NewRequest(http.MethodGet, target, bytes.NewReader(nil))
		// POST-shaped routes need a method override where relevant.
		if strings.Contains(target, "/reject") || strings.Contains(target, "/resolve") {
			req = httptest.NewRequest(http.MethodPost, target, bytes.NewReader([]byte(`{}`)))
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", target, rec.Code)
		}
	}
}

// WorkspaceIsOnline boundary: exactly-at-threshold counts as offline
// (strict < comparison), just-inside counts as online.
func TestAuditRoutesWorkspaceOnlineBoundary(t *testing.T) {
	now := time.Now().UTC()
	mk := func(lastSeen time.Time) *store.Workspace {
		return &store.Workspace{IsOnline: true, LastSeen: lastSeen}
	}
	if WorkspaceIsOnline(mk(now.Add(-OfflineThreshold)), now) {
		t.Fatal("exactly-at-threshold must be offline (strict <)")
	}
	if !WorkspaceIsOnline(mk(now.Add(-OfflineThreshold+time.Second)), now) {
		t.Fatal("just-inside-threshold must be online")
	}
	if WorkspaceIsOnline(nil, now) {
		t.Fatal("nil workspace must be offline")
	}
	if WorkspaceIsOnline(&store.Workspace{IsOnline: false, LastSeen: now}, now) {
		t.Fatal("flagged-offline workspace must be offline")
	}
}
