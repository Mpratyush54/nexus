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

// newTestServer builds a Server backed by MemStore for httptest use.
func newTestServer() *Server {
	s := NewServer(store.NewMemStore())
	// Silence request logs during tests.
	s.Log.SetOutput(&bytes.Buffer{})
	return s
}

// loginAs creates a stub JWT for tests via the authenticator directly.
func loginAs(t *testing.T, s *Server, subject string) string {
	t.Helper()
	token, err := s.Auth.Generate(subject, time.Hour)
	if err != nil {
		t.Fatalf("Generate token: %v", err)
	}
	return token
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
	}
	decodeBody(t, rec, &out)
	if out.Token == "" {
		t.Fatal("expected non-empty token")
	}
	sub, err := s.Auth.Validate(out.Token)
	if err != nil {
		t.Fatalf("Validate(login token): %v", err)
	}
	if sub != "alice" {
		t.Fatalf("subject = %q, want alice", sub)
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

// TestHeartbeatOffline verifies the 90s rule: a workspace whose last heartbeat
// is older than OfflineThreshold no longer counts as active.
func TestHeartbeatOffline(t *testing.T) {
	s := newTestServer()
	token := loginAs(t, s, "daemon-test")

	rec := doJSON(t, s, http.MethodPost, "/projects/resolve", token, map[string]string{
		"folder_name": "offline-proj",
	})
	var project store.Project
	decodeBody(t, rec, &project)

	// RegisterWorkspace stores the *pointer*, so mutating ws ages the stored row.
	ws := &store.Workspace{
		ProjectID: project.ID,
		UserID:    "u_bob",
		MachineID: "m-offline",
		Path:      "/tmp/offline-proj",
	}
	if err := s.Store.RegisterWorkspace(t.Context(), ws); err != nil {
		t.Fatalf("RegisterWorkspace: %v", err)
	}
	ws.LastSeen = time.Now().UTC().Add(-(OfflineThreshold + time.Minute))

	if WorkspaceIsOnline(ws, time.Now()) {
		t.Fatal("stale workspace must not be online")
	}

	rec = doJSON(t, s, http.MethodGet, "/workspaces/"+project.ID+"/active", token, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("active status = %d, want 404 for stale workspace", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"error"`) {
		t.Fatalf("expected error envelope, got %s", rec.Body.String())
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
