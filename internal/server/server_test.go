// Tests for the central REST API (issue #8). All DB-free: an in-memory fake
// Store stands in for Postgres, and a controllable clock drives the 90s
// offline transition deterministically.
package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"central-memory/internal/store"
)

// ---------------------------------------------------------------------------
// Fake Store
// ---------------------------------------------------------------------------

type fakeUser struct {
	id       string
	password string
}

// fakeStore is a mutex-guarded in-memory Store. now is shared with the
// Server under test so heartbeat timestamps and the staleness filter advance
// together when the test moves the clock.
type fakeStore struct {
	mu         sync.Mutex
	now        func() time.Time
	users      map[string]fakeUser
	projects   map[string]*store.Project
	workspaces map[string]*store.Workspace
	memories   []Memory
	episodes   []Episode
	seq        int
}

func newFakeStore(now func() time.Time) *fakeStore {
	return &fakeStore{
		now:        now,
		users:      map[string]fakeUser{"alice": {id: "user-1", password: "s3cret"}},
		projects:   map[string]*store.Project{},
		workspaces: map[string]*store.Workspace{},
	}
}

func (f *fakeStore) nextID(prefix string) string {
	f.seq++
	return fmt.Sprintf("%s-%d", prefix, f.seq)
}

func (f *fakeStore) Authenticate(_ context.Context, username, password string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[username]
	if !ok || u.password != password {
		return "", ErrUnauthorized
	}
	return u.id, nil
}

func (f *fakeStore) ResolveProject(_ context.Context, params store.ProjectParams) (*store.Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	canonical := store.NormalizeRemoteURL(params.Origin)
	var candidates []store.Project
	for _, p := range f.projects {
		candidates = append(candidates, *p)
	}
	if m, _ := store.MatchProject(candidates, canonical, strings.TrimSpace(params.RootCommit), strings.TrimSpace(params.FolderName)); m != nil {
		return m, nil
	}
	p := &store.Project{
		ID:           f.nextID("proj"),
		CanonicalURL: canonical,
		RootCommit:   strings.TrimSpace(params.RootCommit),
		FolderName:   strings.TrimSpace(params.FolderName),
		DisplayName:  strings.TrimSpace(params.DisplayName),
	}
	f.projects[p.ID] = p
	return p, nil
}

func (f *fakeStore) RegisterWorkspace(_ context.Context, params store.WorkspaceParams) (*store.Workspace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.now()
	for _, w := range f.workspaces {
		if w.MachineID == params.MachineID && w.Path == params.Path {
			w.ProjectID = params.ProjectID
			w.UserID = params.UserID
			w.Branch = strings.TrimSpace(params.Branch)
			w.CommitSHA = strings.TrimSpace(params.CommitSHA)
			w.IsDirty = params.IsDirty
			w.DaemonURL = strings.TrimSpace(params.DaemonURL)
			w.LastSeen = &now
			w.IsOnline = true
			return w, nil
		}
	}
	ws := &store.Workspace{
		ID:        f.nextID("ws"),
		ProjectID: params.ProjectID,
		UserID:    params.UserID,
		MachineID: params.MachineID,
		Path:      params.Path,
		Branch:    strings.TrimSpace(params.Branch),
		CommitSHA: strings.TrimSpace(params.CommitSHA),
		IsDirty:   params.IsDirty,
		IsOnline:  true,
		LastSeen:  &now,
		DaemonURL: strings.TrimSpace(params.DaemonURL),
		CreatedAt: now,
	}
	f.workspaces[ws.ID] = ws
	return ws, nil
}

func (f *fakeStore) HeartbeatWorkspace(_ context.Context, id string, hb store.HeartbeatParams) (*store.Workspace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w, ok := f.workspaces[id]
	if !ok {
		return nil, fmt.Errorf("server: workspace %s: %w", id, store.ErrNotFound)
	}
	now := f.now()
	w.Branch = strings.TrimSpace(hb.Branch)
	w.CommitSHA = strings.TrimSpace(hb.CommitSHA)
	w.IsDirty = hb.IsDirty
	w.LastSeen = &now
	w.IsOnline = true
	return w, nil
}

// ListWorkspaces returns ALL of the project's workspaces (no staleness
// filtering) — the server must apply the IsOnlineAt gate itself. This is
// what proves the 90s transition lives in the server, not just the DB.
func (f *fakeStore) ListWorkspaces(_ context.Context, projectID string) ([]store.Workspace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.Workspace
	for _, w := range f.workspaces {
		if w.ProjectID == projectID {
			out = append(out, *w)
		}
	}
	return out, nil
}

func (f *fakeStore) CreateMemory(_ context.Context, m Memory) (*Memory, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m.ID = f.nextID("mem")
	m.CreatedAt = f.now().UTC().Format(time.RFC3339)
	f.memories = append(f.memories, m)
	cp := m
	return &cp, nil
}

func (f *fakeStore) SearchMemory(_ context.Context, q MemoryFilter) ([]Memory, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Memory
	for _, m := range f.memories {
		if m.ProjectID != q.ProjectID {
			continue
		}
		if q.Key != "" && m.Key != q.Key {
			continue
		}
		if q.Level != "" && m.Level != q.Level {
			continue
		}
		if len(q.Tags) > 0 && !hasAllTags(m.Tags, q.Tags) {
			continue
		}
		if q.Query != "" {
			hay := strings.ToLower(m.Key + "\n" + m.Content + "\n" + m.ContextSnippet)
			if !strings.Contains(hay, strings.ToLower(q.Query)) {
				continue
			}
		}
		out = append(out, m)
		if len(out) >= q.Limit && q.Limit > 0 {
			break
		}
	}
	return out, nil
}

func hasAllTags(have, want []string) bool {
	set := map[string]bool{}
	for _, t := range have {
		set[t] = true
	}
	for _, t := range want {
		if !set[t] {
			return false
		}
	}
	return true
}

func (f *fakeStore) CreateEpisode(_ context.Context, e Episode) (*Episode, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e.ID = f.nextID("ep")
	e.OpenedAt = f.now().UTC().Format(time.RFC3339)
	f.episodes = append(f.episodes, e)
	cp := e
	return &cp, nil
}

func (f *fakeStore) SearchEpisodes(_ context.Context, q EpisodeFilter) ([]Episode, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Episode
	for _, e := range f.episodes {
		if e.ProjectID != q.ProjectID {
			continue
		}
		if q.Status != "" && e.Status != q.Status {
			continue
		}
		if q.EpisodeType != "" && e.EpisodeType != q.EpisodeType {
			continue
		}
		if q.ErrorPattern != "" && !containsStr(e.ErrorPatterns, q.ErrorPattern) {
			continue
		}
		if q.File != "" && !containsStr(e.FilesInvolved, q.File) {
			continue
		}
		if q.Query != "" {
			hay := strings.ToLower(e.Title + "\n" + e.Trigger + "\n" + e.RootCause + "\n" + e.Resolution)
			if !strings.Contains(hay, strings.ToLower(q.Query)) {
				continue
			}
		}
		out = append(out, e)
		if len(out) >= q.Limit && q.Limit > 0 {
			break
		}
	}
	return out, nil
}

func containsStr(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

type harness struct {
	srv    *Server
	fake   *fakeStore
	now    time.Time
	token  string
	userID string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	h := &harness{now: now}
	fake := newFakeStore(func() time.Time { return h.now })
	h.fake = fake
	h.srv = New(fake, Options{
		JWTSecret: []byte("test-secret-that-is-long-enough-32B!"),
		TokenTTL:  time.Hour,
		Now:       func() time.Time { return h.now },
	})
	// Log in once; every other call in these tests is authenticated.
	h.token = h.login(t, "alice", "s3cret")
	return h
}

func (h *harness) do(t *testing.T, method, path string, body any, withAuth bool) (int, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		rdr = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rdr)
	if withAuth {
		req.Header.Set("Authorization", "Bearer "+h.token)
	}
	rec := httptest.NewRecorder()
	h.srv.Handler().ServeHTTP(rec, req)
	resp := rec.Result()
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp.StatusCode, raw
}

func (h *harness) login(t *testing.T, username, password string) string {
	t.Helper()
	status, raw := h.do(t, "POST", "/auth/login",
		map[string]string{"username": username, "password": password}, false)
	if status != http.StatusOK {
		t.Fatalf("login: status=%d body=%s", status, raw)
	}
	var lr loginResponse
	if err := json.Unmarshal(raw, &lr); err != nil {
		t.Fatalf("login decode: %v", err)
	}
	if lr.Token == "" || lr.UserID == "" {
		t.Fatalf("login: empty token/user: %s", raw)
	}
	h.userID = lr.UserID
	return lr.Token
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestLoginSuccessAndClaims(t *testing.T) {
	h := newHarness(t)
	userID, ok := h.srv.verifyToken(h.token)
	if !ok {
		t.Fatal("freshly minted token does not verify")
	}
	if userID != "user-1" {
		t.Fatalf("subject = %q, want user-1", userID)
	}
}

func TestLoginBadCredentials401(t *testing.T) {
	h := newHarness(t)
	status, _ := h.do(t, "POST", "/auth/login",
		map[string]string{"username": "alice", "password": "wrong"}, false)
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", status)
	}
}

func TestAuthMiddleware401(t *testing.T) {
	h := newHarness(t)
	cases := []struct {
		name   string
		header string
	}{
		{"missing", ""},
		{"garbage", "Bearer not-a-jwt"},
		{"wrong scheme", "Token " + h.token},
		{"wrong secret", "Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJ4In0.invalid-signature-here"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/memory/search?project_id=p1", nil)
			if c.header != "" {
				req.Header.Set("Authorization", c.header)
			}
			rec := httptest.NewRecorder()
			h.srv.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
		})
	}
}

func TestTokenExpiry401(t *testing.T) {
	h := newHarness(t)
	h.now = h.now.Add(2 * time.Hour) // past the 1h TTL
	status, _ := h.do(t, "GET", "/memory/search?project_id=p1", nil, true)
	if status != http.StatusUnauthorized {
		t.Fatalf("expired token: status = %d, want 401", status)
	}
}

func TestOpenPathsSkipAuth(t *testing.T) {
	h := newHarness(t)
	req := httptest.NewRequest("GET", "/healthz", nil)
	rec := httptest.NewRecorder()
	h.srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz: status = %d, want 200", rec.Code)
	}
}

// TestWorkspaceLifecycle is the required register → heartbeat → active flow.
func TestWorkspaceLifecycle(t *testing.T) {
	h := newHarness(t)

	// Resolve a project first (workspace registration needs a project_id).
	status, raw := h.do(t, "POST", "/projects/resolve", map[string]string{
		"origin": "git@github.com:org/repo.git", "folder_name": "repo",
	}, true)
	if status != http.StatusOK {
		t.Fatalf("resolve: status=%d body=%s", status, raw)
	}
	var proj store.Project
	if err := json.Unmarshal(raw, &proj); err != nil {
		t.Fatalf("resolve decode: %v", err)
	}

	// Register.
	status, raw = h.do(t, "POST", "/workspaces/register", map[string]any{
		"project_id": proj.ID, "user_id": "user-1",
		"machine_id": "laptop", "path": "/home/alice/repo",
		"branch": "main", "commit_sha": "abc123",
	}, true)
	if status != http.StatusCreated {
		t.Fatalf("register: status=%d body=%s", status, raw)
	}
	var ws store.Workspace
	if err := json.Unmarshal(raw, &ws); err != nil {
		t.Fatalf("register decode: %v", err)
	}

	// Heartbeat moves the git state forward.
	h.now = h.now.Add(30 * time.Second)
	status, raw = h.do(t, "POST", "/workspaces/heartbeat", map[string]any{
		"workspace_id": ws.ID, "branch": "feature", "commit_sha": "def456", "is_dirty": true,
	}, true)
	if status != http.StatusOK {
		t.Fatalf("heartbeat: status=%d body=%s", status, raw)
	}
	var hb store.Workspace
	if err := json.Unmarshal(raw, &hb); err != nil {
		t.Fatalf("heartbeat decode: %v", err)
	}
	if hb.Branch != "feature" || hb.CommitSHA != "def456" || !hb.IsDirty {
		t.Fatalf("heartbeat did not apply state: %+v", hb)
	}

	// Active lists it.
	status, raw = h.do(t, "GET", "/workspaces/"+proj.ID+"/active", nil, true)
	if status != http.StatusOK {
		t.Fatalf("active: status=%d body=%s", status, raw)
	}
	var active activeResponse
	if err := json.Unmarshal(raw, &active); err != nil {
		t.Fatalf("active decode: %v", err)
	}
	if active.Count != 1 || active.Workspaces[0].ID != ws.ID {
		t.Fatalf("active = %+v, want the one workspace", active)
	}
}

// TestOfflineAfter90sSilence proves the offline transition: the fake returns
// every workspace unfiltered, so exclusion from /active is purely the
// server's IsOnlineAt gate over store.OfflineAfter.
func TestOfflineAfter90sSilence(t *testing.T) {
	h := newHarness(t)

	status, raw := h.do(t, "POST", "/projects/resolve",
		map[string]string{"folder_name": "repo"}, true)
	if status != http.StatusOK {
		t.Fatalf("resolve: status=%d body=%s", status, raw)
	}
	var proj store.Project
	_ = json.Unmarshal(raw, &proj)

	status, raw = h.do(t, "POST", "/workspaces/register", map[string]any{
		"project_id": proj.ID, "user_id": "user-1",
		"machine_id": "m", "path": "/r",
	}, true)
	if status != http.StatusCreated {
		t.Fatalf("register: status=%d body=%s", status, raw)
	}
	active := func() activeResponse {
		status, raw := h.do(t, "GET", "/workspaces/"+proj.ID+"/active", nil, true)
		if status != http.StatusOK {
			t.Fatalf("active: status=%d body=%s", status, raw)
		}
		var a activeResponse
		_ = json.Unmarshal(raw, &a)
		return a
	}

	if got := active().Count; got != 1 {
		t.Fatalf("t=0: count = %d, want 1", got)
	}
	// The exact 90s boundary is still online ("after 90s" = strictly greater).
	h.now = h.now.Add(90 * time.Second)
	if got := active().Count; got != 1 {
		t.Fatalf("t=90s: count = %d, want 1 (boundary still online)", got)
	}
	h.now = h.now.Add(time.Second) // 91s of silence
	if got := active().Count; got != 0 {
		t.Fatalf("t=91s: count = %d, want 0 (offline)", got)
	}

	// A heartbeat revives the workspace without re-registering.
	var wsID string
	h.fake.mu.Lock()
	for id := range h.fake.workspaces {
		wsID = id
	}
	h.fake.mu.Unlock()
	status, _ = h.do(t, "POST", "/workspaces/heartbeat",
		map[string]any{"workspace_id": wsID}, true)
	if status != http.StatusOK {
		t.Fatalf("revive heartbeat: status=%d", status)
	}
	if got := active().Count; got != 1 {
		t.Fatalf("after heartbeat: count = %d, want 1", got)
	}
}

func TestProjectResolveValidation(t *testing.T) {
	h := newHarness(t)
	status, _ := h.do(t, "POST", "/projects/resolve",
		map[string]string{"origin": "x"}, true)
	if status != http.StatusBadRequest {
		t.Fatalf("missing folder_name: status = %d, want 400", status)
	}
}

func TestHeartbeatUnknownWorkspace404(t *testing.T) {
	h := newHarness(t)
	status, _ := h.do(t, "POST", "/workspaces/heartbeat",
		map[string]any{"workspace_id": "nope"}, true)
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
}

func TestMemoryRoundtrip(t *testing.T) {
	h := newHarness(t)
	body := map[string]any{
		"project_id": "proj-1", "key": "testing/framework",
		"content": "The team uses pytest with fixture-based setup for tests.",
		"level":   "project", "scope": "decision", "tags": []string{"testing"},
	}
	status, raw := h.do(t, "POST", "/memory", body, true)
	if status != http.StatusCreated {
		t.Fatalf("create memory: status=%d body=%s", status, raw)
	}
	status, raw = h.do(t, "GET", "/memory/search?project_id=proj-1&q=pytest", nil, true)
	if status != http.StatusOK {
		t.Fatalf("search memory: status=%d body=%s", status, raw)
	}
	var res struct {
		Items []Memory `json:"items"`
		Count int      `json:"count"`
	}
	_ = json.Unmarshal(raw, &res)
	if res.Count != 1 || res.Items[0].Key != "testing/framework" {
		t.Fatalf("search = %s, want the created item", raw)
	}
}

func TestMemoryValidation(t *testing.T) {
	h := newHarness(t)
	cases := []struct {
		name string
		body map[string]any
	}{
		{"short content", map[string]any{"project_id": "p", "key": "k", "content": "too short"}},
		{"missing key", map[string]any{"project_id": "p", "content": strings.Repeat("x", 30)}},
		{"bad level", map[string]any{"project_id": "p", "key": "k", "content": strings.Repeat("x", 30), "level": "galaxy"}},
		{"bad scope", map[string]any{"project_id": "p", "key": "k", "content": strings.Repeat("x", 30), "scope": "vibe"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if status, _ := h.do(t, "POST", "/memory", c.body, true); status != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", status)
			}
		})
	}
}

func TestEpisodeRoundtrip(t *testing.T) {
	h := newHarness(t)
	body := map[string]any{
		"project_id": "proj-1", "title": "Auth timeout on WebSocket upgrade",
		"episode_type": "bug_fix", "trigger": "ConnectionTimeout in ws.go",
		"error_patterns": []string{"ConnectionTimeout"},
		"files_involved": []string{"internal/server/ws.go"},
	}
	status, raw := h.do(t, "POST", "/episodes", body, true)
	if status != http.StatusCreated {
		t.Fatalf("create episode: status=%d body=%s", status, raw)
	}
	status, raw = h.do(t, "GET", "/episodes/search?project_id=proj-1&error_pattern=ConnectionTimeout", nil, true)
	if status != http.StatusOK {
		t.Fatalf("search episodes: status=%d body=%s", status, raw)
	}
	var res struct {
		Episodes []Episode `json:"episodes"`
		Count    int       `json:"count"`
	}
	_ = json.Unmarshal(raw, &res)
	if res.Count != 1 || res.Episodes[0].Title != "Auth timeout on WebSocket upgrade" {
		t.Fatalf("search = %s, want the created episode", raw)
	}
}

func TestEpisodeValidation(t *testing.T) {
	h := newHarness(t)
	status, _ := h.do(t, "POST", "/episodes", map[string]any{
		"project_id": "p", "title": "t", "episode_type": "mystery",
	}, true)
	if status != http.StatusBadRequest {
		t.Fatalf("bad episode_type: status = %d, want 400", status)
	}
}

func TestSearchRequiresProjectID(t *testing.T) {
	h := newHarness(t)
	if status, _ := h.do(t, "GET", "/memory/search", nil, true); status != http.StatusBadRequest {
		t.Fatalf("memory search w/o project_id: status = %d, want 400", status)
	}
	if status, _ := h.do(t, "GET", "/episodes/search", nil, true); status != http.StatusBadRequest {
		t.Fatalf("episode search w/o project_id: status = %d, want 400", status)
	}
}

func TestUnknownFieldsRejected(t *testing.T) {
	h := newHarness(t)
	status, _ := h.do(t, "POST", "/workspaces/heartbeat",
		map[string]any{"workspace_id": "x", "commmit_sha": "typo"}, true)
	if status != http.StatusBadRequest {
		t.Fatalf("unknown field: status = %d, want 400", status)
	}
}

func TestNewPanicsWithoutSecret(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on empty JWTSecret")
		}
	}()
	New(newFakeStore(time.Now), Options{})
}
