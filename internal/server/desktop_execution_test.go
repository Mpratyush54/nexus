package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"central-memory/internal/desktopbridge"
	"central-memory/internal/store"
)

func TestDesktopExecutionRequiresOwnerCredentialAndMappedWorkspace(t *testing.T) {
	s := newTestServer()
	mem := s.Store.(*store.MemStore)
	ctx := t.Context()
	project, e := mem.ResolveProject(ctx, "", "", "desktop-test")
	if e != nil {
		t.Fatal(e)
	}
	_, _ = mem.ClaimProject(ctx, project.ID, "owner")
	_ = mem.GrantMember(ctx, project.ID, "editor", "owner")
	token := loginAs(t, s, "owner")
	editor := loginAs(t, s, "editor")
	created := doJSON(t, s, "POST", "/v1/agent-sessions", token, map[string]any{"project_id": project.ID, "harness": "codex", "native_id": "native"})
	var session store.AgentSession
	decodeBody(t, created, &session)
	call := func(method, path, tok, key string, body any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, bytes.NewReader(b))
		r.Header.Set("Authorization", "Bearer "+tok)
		r.Header.Set("Content-Type", "application/json")
		if key != "" {
			r.Header.Set("X-Nexus-Desktop-Key", key)
		}
		out := httptest.NewRecorder()
		s.Handler().ServeHTTP(out, r)
		return out
	}
	device := "desktop-123456789abcdef"
	secret := strings.Repeat("a", 64)
	registration := map[string]any{"id": device, "name": "My PC", "projects": []string{project.ID}, "harnesses": []string{"codex"}}
	if got := call("POST", "/v1/desktop/devices", token, "", registration); got.Code != 400 {
		t.Fatalf("key required: %d %s", got.Code, got.Body)
	}
	if got := call("POST", "/v1/desktop/devices", token, secret, registration); got.Code != 200 {
		t.Fatalf("register: %d %s", got.Code, got.Body)
	}
	if got := call("POST", "/v1/desktop/devices", token, strings.Repeat("b", 64), registration); got.Code != 409 {
		t.Fatalf("replaced device key: %d", got.Code)
	}
	if got := call("GET", "/v1/desktop/devices", token, "", nil); strings.Contains(got.Body.String(), "key_hash") || strings.Contains(got.Body.String(), secret) {
		t.Fatal("public credential leak")
	}
	req := desktopbridge.Request{ID: "req-1", DeviceID: device, ProjectID: project.ID, SessionID: session.ID, Kind: "turn", Agent: "codex", Prompt: "hello"}
	if got := call("POST", "/v1/desktop/jobs", editor, "", req); got.Code != 403 {
		t.Fatalf("non-owner execution: %d %s", got.Code, got.Body)
	}
	if got := call("POST", "/v1/desktop/jobs", token, "", req); got.Code != 202 {
		t.Fatalf("enqueue: %d %s", got.Code, got.Body)
	}
	if got := call("POST", "/v1/desktop/devices/"+device+"/claim", token, "", nil); got.Code != 403 {
		t.Fatalf("browser claimed execution: %d", got.Code)
	}
	got := call("POST", "/v1/desktop/devices/"+device+"/claim", token, secret, nil)
	var claim struct {
		Job desktopbridge.Job `json:"job"`
	}
	decodeBody(t, got, &claim)
	if got.Code != 200 || claim.Job.State != "awaiting_approval" {
		t.Fatalf("claim: %d %s", got.Code, got.Body)
	}
	update := desktopbridge.Update{Revision: claim.Job.Revision, State: "running"}
	if got := call("POST", "/v1/desktop/jobs/req-1/progress", token, "", update); got.Code != 403 {
		t.Fatalf("browser forged progress: %d", got.Code)
	}
	if got := call("POST", "/v1/desktop/jobs/req-1/progress", token, secret, update); got.Code != 200 {
		t.Fatalf("worker progress: %d %s", got.Code, got.Body)
	}
	// The agent's thinking reaches the browser with the job.
	thinking := desktopbridge.Update{Revision: update.Revision + 1, State: "running", Status: "Thinking…", Thinking: "Looking at the request", ThinkingMS: 1200}
	if got := call("POST", "/v1/desktop/jobs/req-1/progress", token, secret, thinking); got.Code != 200 {
		t.Fatalf("thinking progress: %d %s", got.Code, got.Body)
	}
	update.Revision = thinking.Revision
	var seen desktopbridge.Job
	if got := call("GET", "/v1/desktop/jobs/req-1", token, "", nil); got.Code != 200 {
		t.Fatalf("read job: %d", got.Code)
	} else {
		decodeBody(t, got, &seen)
	}
	if seen.Thinking != "Looking at the request" || seen.ThinkingMS != 1200 {
		t.Fatalf("thinking not passed through: %+v", seen)
	}
	if got := call("GET", "/v1/desktop/jobs/req-1", editor, "", nil); got.Code != 404 {
		t.Fatalf("cross-owner read: %d", got.Code)
	}
	if got := call("POST", "/v1/desktop/jobs/req-1/cancel", token, "", nil); got.Code != 200 {
		t.Fatal("cancel", got.Code)
	}
	update.Revision++
	update.State = "completed"
	if got := call("POST", "/v1/desktop/jobs/req-1/progress", token, secret, update); got.Code != 409 {
		t.Fatalf("cancel overwritten: %d", got.Code)
	}
	req.ID = "req-2"
	req.Agent = "opencode"
	if got := call("POST", "/v1/desktop/jobs", token, "", req); got.Code != 409 {
		t.Fatalf("unavailable harness accepted: %d", got.Code)
	}
	// Unknown command/path fields fail strict decoding rather than becoming RPC.
	if got := call("POST", "/v1/desktop/jobs", token, "", map[string]any{"argv": []string{"cmd.exe"}, "workspace_root": "C:\\"}); got.Code != http.StatusBadRequest {
		t.Fatalf("arbitrary execution field accepted: %d", got.Code)
	}
}

func TestDesktopChannelRejectsAgentAPICredentials(t *testing.T) {
	called := false
	handler := desktopAccountOnly(func(http.ResponseWriter, *http.Request) { called = true })
	r := httptest.NewRequest("POST", "/v1/desktop/jobs", nil)
	r.Header.Set("Authorization", "Bearer nxs_agent_secret")
	out := httptest.NewRecorder()
	handler(out, r)
	if called || out.Code != http.StatusForbidden {
		t.Fatal("agent credential reached desktop channel", out.Code)
	}
}

func TestDesktopRequestRejectsTrailingMalformedData(t *testing.T) {
	r := httptest.NewRequest("POST", "/v1/desktop/jobs", strings.NewReader(`{"id":"one"} trailing garbage`))
	out := httptest.NewRecorder()
	var request desktopbridge.Request
	if decodeJSON(out, r, &request) || out.Code != http.StatusBadRequest {
		t.Fatal("malformed trailing body was accepted")
	}
}
