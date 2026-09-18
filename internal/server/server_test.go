package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"central-memory/internal/store"
)

// newTestServer builds a Server backed by MemStore for httptest use.
func newTestServer() *Server {
	s := NewServer(store.NewMemStore())
	// Silence request logs during tests.
	s.Log.SetOutput(&bytes.Buffer{})
	return s
}

// loginAs creates a stub JWT for tests via the authenticator directly.
// Production authenticators fail closed without JWT_SECRET (issue #85), so
// the helper injects a test-only key when the server is unconfigured; the
// fail-closed path itself is covered in auth_audit_test.go.
func loginAs(t *testing.T, s *Server, subject string) string {
	t.Helper()
	if !s.Auth.IsConfigured() {
		s.Auth = NewAuthenticator([]byte("test-only-key-0123456789abcdef"))
	}
	token, err := s.Auth.Generate(subject, time.Hour)
	if err != nil {
		t.Fatalf("Generate token: %v", err)
	}
	return token
}

// fakeUsers is an in-memory UserLookup for login tests.
type fakeUsers struct {
	hashes map[string]string // username -> password hash ("" = unset)
	ids    map[string]string
	err    error
}

func newFakeUsers() *fakeUsers {
	return &fakeUsers{hashes: map[string]string{}, ids: map[string]string{}}
}

func (f *fakeUsers) add(t *testing.T, username, password string) {
	t.Helper()
	h, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	f.hashes[username] = h
	f.ids[username] = "user-" + username
}

func (f *fakeUsers) GetPasswordHash(_ context.Context, username string) (string, string, error) {
	if f.err != nil {
		return "", "", f.err
	}
	h, ok := f.hashes[username]
	if !ok {
		return "", "", errors.New("no such user")
	}
	return f.ids[username], h, nil
}

// doJSON performs a JSON request against the server handler.
func doJSON(t *testing.T, s *Server, method, target, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, target, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder, dst any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), dst); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
}

func TestLoginIssuesValidToken(t *testing.T) {
	s := newTestServer()
	s.Auth = NewAuthenticator([]byte("test-only-key-0123456789abcdef"))
	users := newFakeUsers()
	users.add(t, "alice", "secret")
	s.Users = users
	rec := doJSON(t, s, http.MethodPost, "/auth/login", "", map[string]string{
		"username": "alice",
		"password": "secret",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("login status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Token    string `json:"token"`
		Username string `json:"username"`
		UserID   string `json:"user_id"`
	}
	decodeBody(t, rec, &out)
	if out.Token == "" {
		t.Fatal("expected non-empty token")
	}
	// Canonical identity (issue #140): sub is the user UUID, username rides
	// as the display claim for Postgres UUID columns.
	sub, username, err := s.Auth.ValidateClaims(out.Token)
	if err != nil {
		t.Fatalf("Validate(login token): %v", err)
	}
	if sub != "user-alice" {
		t.Fatalf("subject = %q, want user UUID user-alice", sub)
	}
	if username != "alice" || out.Username != "alice" || out.UserID != "user-alice" {
		t.Fatalf("username claim = %q/%q user_id = %q", username, out.Username, out.UserID)
	}
}

func TestLoginRejectsEmptyCredentials(t *testing.T) {
	s := newTestServer()
	rec := doJSON(t, s, http.MethodPost, "/auth/login", "", map[string]string{
		"username": "",
		"password": "",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"error"`) {
		t.Fatalf("expected error envelope, got %s", rec.Body.String())
	}
}

func TestAuthMiddlewareRejectsUnauthenticated(t *testing.T) {
	s := newTestServer()
	rec := doJSON(t, s, http.MethodPost, "/projects/resolve", "", map[string]string{
		"folder_name": "x",
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

// TestResolveRegisterHeartbeatActive exercises the full daemon lifecycle:
// resolve -> register -> heartbeat -> active.
func TestResolveRegisterHeartbeatActive(t *testing.T) {
	s := newTestServer()
	token := loginAs(t, s, "daemon-test")

	// 1. Resolve project.
	rec := doJSON(t, s, http.MethodPost, "/projects/resolve", token, map[string]string{
		"canonical_url": "git@github.com:acme/nexus.git",
		"folder_name":   "nexus",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("resolve status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var project store.Project
	decodeBody(t, rec, &project)
	if project.ID == "" {
		t.Fatal("expected project ID")
	}

	// 2. Register workspace.
	rec = doJSON(t, s, http.MethodPost, "/workspaces/register", token, map[string]any{
		"project_id": project.ID,
		"user_id":    "u_alice",
		"machine_id": "ws-machine-1",
		"path":       "/home/alice/nexus",
		"branch":     "main",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("register status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var ws store.Workspace
	decodeBody(t, rec, &ws)
	if ws.ID == "" {
		t.Fatal("expected workspace ID")
	}

	// 3. Heartbeat with new branch state.
	rec = doJSON(t, s, http.MethodPost, "/workspaces/heartbeat", token, map[string]any{
		"workspace_id": ws.ID,
		"branch":       "feature/x",
		"commit_sha":   "abc123",
		"is_dirty":     true,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("heartbeat status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// 4. Active workspace reflects the heartbeat.
	rec = doJSON(t, s, http.MethodGet, "/workspaces/"+project.ID+"/active", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("active status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var active store.Workspace
	decodeBody(t, rec, &active)
	if active.ID != ws.ID {
		t.Fatalf("active workspace = %q, want %q", active.ID, ws.ID)
	}
	if active.Branch != "feature/x" || !active.IsDirty {
		t.Fatalf("heartbeat state not reflected: %+v", active)
	}
}

// TestHeartbeatOffline verifies the 90s rule and the Wave-1 store fix:
// RegisterWorkspace now stores a copy (upsert on machine_id+path), so
// mutating the caller's struct no longer ages the stored row. The 90s rule
// itself still lives in WorkspaceIsOnline (pure helper) and in the store's
// GetActiveWorkspace filter.
func TestHeartbeatOffline(t *testing.T) {
	s := newTestServer()
	token := loginAs(t, s, "daemon-test")

	rec := doJSON(t, s, http.MethodPost, "/projects/resolve", token, map[string]string{
		"folder_name": "offline-proj",
	})
	var project store.Project
	decodeBody(t, rec, &project)

	// Not a member yet: project state is forbidden, not 404 (issue #141 —
	// 403 for missing and forbidden alike leaks nothing).
	rec = doJSON(t, s, http.MethodGet, "/workspaces/"+project.ID+"/active", token, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("active status = %d, want 403 for non-member", rec.Code)
	}

	// Register via HTTP (membership bootstrap; UserID forced to self).
	ensureMembership(t, s, token, project.ID)
	rec = doJSON(t, s, http.MethodGet, "/workspaces/"+project.ID+"/active", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("active status = %d, want 200 for member", rec.Code)
	}

	ws := &store.Workspace{
		ProjectID: project.ID,
		UserID:    "u_bob",
		MachineID: "m-offline",
		Path:      "/tmp/offline-proj",
	}
	if err := s.Store.RegisterWorkspace(t.Context(), ws); err != nil {
		t.Fatalf("RegisterWorkspace: %v", err)
	}
	if ws.ID == "" {
		t.Fatal("expected server-assigned workspace ID")
	}

	// Mutating the caller's struct must NOT age the stored row (the store
	// keeps a copy now): the pure helper sees the stale copy as offline,
	// but the persisted workspace is still active.
	ws.LastSeen = time.Now().UTC().Add(-(OfflineThreshold + time.Minute))
	if WorkspaceIsOnline(ws, time.Now()) {
		t.Fatal("stale copy must not be online")
	}
	rec = doJSON(t, s, http.MethodGet, "/workspaces/"+project.ID+"/active", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("active status = %d, want 200 (stored row unaffected by caller mutation)", rec.Code)
	}

	// Upsert: re-registering the same machine/path keeps the workspace ID.
	dup := &store.Workspace{
		ProjectID: project.ID,
		UserID:    "u_bob",
		MachineID: "m-offline",
		Path:      "/tmp/offline-proj",
	}
	if err := s.Store.RegisterWorkspace(t.Context(), dup); err != nil {
		t.Fatalf("RegisterWorkspace upsert: %v", err)
	}
	if dup.ID != ws.ID {
		t.Fatalf("upsert ID = %q, want %q", dup.ID, ws.ID)
	}
}

func TestMemoryCRUD(t *testing.T) {
	s := newTestServer()
	token := loginAs(t, s, "alice")

	rec := doJSON(t, s, http.MethodPost, "/projects/resolve", token, map[string]string{
		"folder_name": "mem-proj",
	})
	var project store.Project
	decodeBody(t, rec, &project)
	ensureMembership(t, s, token, project.ID)

	content := "The team uses pytest with fixture-based setup for integration tests."
	rec = doJSON(t, s, http.MethodPost, "/memory", token, map[string]any{
		"project_id": project.ID,
		"key":        "testing/framework",
		"content":    content,
		"level":      "project",
		"scope":      "decision",
		"tags":       []string{"testing"},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("memory create status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var created store.MemoryItem
	decodeBody(t, rec, &created)
	if created.ID == "" {
		t.Fatal("expected memory ID")
	}

	rec = doJSON(t, s, http.MethodGet, "/memory/search?project_id="+project.ID+"&q=pytest", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("memory search status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var search struct {
		Items []*store.MemoryItem `json:"items"`
		Count int                 `json:"count"`
	}
	decodeBody(t, rec, &search)
	if search.Count == 0 {
		t.Fatal("expected at least one search hit")
	}
	if search.Items[0].Key != "testing/framework" {
		t.Fatalf("unexpected first hit: %+v", search.Items[0])
	}
}

func TestEpisodeCRUD(t *testing.T) {
	s := newTestServer()
	token := loginAs(t, s, "bob")

	rec := doJSON(t, s, http.MethodPost, "/projects/resolve", token, map[string]string{
		"folder_name": "ep-proj",
	})
	var project store.Project
	decodeBody(t, rec, &project)
	ensureMembership(t, s, token, project.ID)

	rec = doJSON(t, s, http.MethodPost, "/episodes", token, map[string]any{
		"project_id":     project.ID,
		"title":          "Auth timeout on WebSocket upgrade",
		"episode_type":   "bug_fix",
		"trigger":        "ConnectionTimeout in ws.go:142",
		"error_patterns": []string{"ConnectionTimeout"},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("episode create status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var created store.Episode
	decodeBody(t, rec, &created)
	if created.ID == "" {
		t.Fatal("expected episode ID")
	}

	rec = doJSON(t, s, http.MethodGet, "/episodes/search?project_id="+project.ID+"&error_pattern=ConnectionTimeout", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("episode search status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var search struct {
		Items []*store.Episode `json:"items"`
		Count int              `json:"count"`
	}
	decodeBody(t, rec, &search)
	if search.Count == 0 {
		t.Fatal("expected at least one episode hit")
	}
}

func TestAuthStubRoundTripAndTamper(t *testing.T) {
	a := NewAuthenticator([]byte("test-key-1234567890"))
	token, err := a.Generate("carol", time.Hour)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if sub, err := a.Validate(token); err != nil || sub != "carol" {
		t.Fatalf("Validate = %q, %v", sub, err)
	}

	// Tampered signature must fail.
	tampered := token[:len(token)-2] + "xx"
	if _, err := a.Validate(tampered); err == nil {
		t.Fatal("expected tampered token to fail validation")
	}

	// Expired token must fail with ErrExpiredToken.
	expired, err := a.Generate("carol", -time.Hour)
	if err != nil {
		t.Fatalf("Generate expired: %v", err)
	}
	if _, err := a.Validate(expired); err == nil {
		t.Fatal("expected expired token to fail validation")
	}
}
