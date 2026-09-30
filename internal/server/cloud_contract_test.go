package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"central-memory/internal/store"
)

type captureMail struct {
	to, subject, body string
}

func (c *captureMail) Send(to, subject, body string) error {
	c.to, c.subject, c.body = to, subject, body
	return nil
}

func (c *captureMail) Configured() bool { return true }

func TestCloudContracts(t *testing.T) {
	s := newTestServer()
	mem := s.Store.(*store.MemStore)
	ctx := t.Context()
	org, err := mem.CreateOrganization(ctx, "Labs", "labs", "owner")
	if err != nil {
		t.Fatal(err)
	}
	proj, err := mem.CreateOrgProject(ctx, org.ID, "nexus", "Nexus", "", "", "owner")
	if err != nil {
		t.Fatal(err)
	}
	owner := loginAs(t, s, "owner")
	other := loginAs(t, s, "other")

	const title = "SECRET-TITLE"
	rec := doJSON(t, s, http.MethodPost, "/v1/agent-sessions", owner, map[string]any{
		"project_id": proj.ID, "harness": "claude", "native_id": "ses_contract",
		"origin_machine_id": "lap", "title": title, "summary": "notes for ada@ex.com",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("session: %d %s", rec.Code, rec.Body.String())
	}
	var created store.AgentSession
	decodeBody(t, rec, &created)

	sum := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	rec = doJSON(t, s, http.MethodPost, "/v1/blobs/presign", owner, map[string]any{
		"project_id": proj.ID,
		"files":      []map[string]any{{"sha256": sum, "size": 100}, {"sha256": sum, "size": (64 << 20) + 1}},
	})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "x-amz-checksum-sha256") || !strings.Contains(rec.Body.String(), "multipart") {
		t.Fatalf("presign: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodPost, "/v1/blobs/presign", owner, map[string]any{
		"project_id": proj.ID,
		"files":      []map[string]any{{"sha256": sum, "size": (10 << 30) + 1}},
	})
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("huge: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodPost, "/v1/agent-sessions/"+created.ID+"/versions", owner, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("version: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+created.ID+"/versions/1/preflight", owner, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ready":false`) {
		t.Fatalf("preflight: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+created.ID+"/versions/1/preflight", other, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("preflight other: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodPut, "/v1/agent-sessions/"+created.ID+"/grants/other", owner, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("grant: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+created.ID+"/summary", other, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("grantee summary: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodDelete, "/v1/agent-sessions/"+created.ID+"/grants/other", owner, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("ungrant: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodPut, "/v1/agent-sessions/"+created.ID+"/grants/team", owner, map[string]any{"confirm": false})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("team without confirm: %d", rec.Code)
	}
	rec = doJSON(t, s, http.MethodPut, "/v1/agent-sessions/"+created.ID+"/grants/team", owner, map[string]any{"confirm": true})
	if rec.Code != http.StatusOK {
		t.Fatalf("team: %d %s", rec.Code, rec.Body.String())
	}

	if err := mem.SetStorageUsage(ctx, "user:owner", 90, 100); err != nil {
		t.Fatal(err)
	}
	rec = doJSON(t, s, http.MethodGet, "/v1/storage/usage", owner, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"state":"full"`) && !strings.Contains(rec.Body.String(), `"state":"warn"`) {
		t.Fatalf("usage: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodGet, "/v1/timeline/stream", owner, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "event: timeline") {
		t.Fatalf("stream: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodPost, "/v1/secrets/data-key", owner, map[string]any{"blob_id": "blob-1"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("data-key: %d %s", rec.Code, rec.Body.String())
	}
	var key struct {
		DataKey string `json:"data_key"`
	}
	decodeBody(t, rec, &key)
	if key.DataKey == "" {
		t.Fatal("empty data key")
	}
	rec = doJSON(t, s, http.MethodPost, "/v1/secrets/blob-1/decrypt", owner, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), key.DataKey) {
		t.Fatalf("decrypt owner: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodPost, "/v1/secrets/blob-1/decrypt", other, nil)
	if rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), key.DataKey) {
		t.Fatalf("decrypt other: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodPut, "/v1/secrets/blob-1/grants/other", owner, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("secret grant: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodPost, "/v1/secrets/blob-1/decrypt", other, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), key.DataKey) {
		t.Fatalf("decrypt grantee: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodGet, "/v1/secrets/audit", owner, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "secrets.decrypt") {
		t.Fatalf("secret audit: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodPost, "/v1/continue", owner, map[string]any{
		"session_id": created.ID, "mode": "here", "resume": "native",
	})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "nexus") {
		t.Fatalf("continue: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodPost, "/v1/teleports", owner, map[string]any{
		"session_id": created.ID, "to_user_id": "other",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("teleport: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), title) || strings.Contains(rec.Body.String(), "ada@ex.com") {
		t.Fatalf("teleport leaked: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "[redacted-email]") {
		t.Fatalf("preview: %s", rec.Body.String())
	}
	var tp store.Teleport
	decodeBody(t, rec, &tp)
	rec = doJSON(t, s, http.MethodGet, "/v1/teleports/inbox", other, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), tp.ID) {
		t.Fatalf("inbox: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodPost, "/v1/teleports/"+tp.ID+"/revoke", owner, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodGet, "/ops/v1/status", owner, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("ops customer: %d %s", rec.Code, rec.Body.String())
	}
	if err := mem.SetPlatformAdmin(ctx, "owner", "owner", true); err != nil {
		t.Fatal(err)
	}
	rec = doJSON(t, s, http.MethodGet, "/ops/v1/status", owner, nil)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), title) || !strings.Contains(rec.Body.String(), `"session_content":false`) {
		t.Fatalf("ops: %d %s", rec.Code, rec.Body.String())
	}
	for _, path := range []string{"/ops/v1/tenants", "/ops/v1/queues", "/ops/v1/health"} {
		rec = doJSON(t, s, http.MethodGet, path, owner, nil)
		if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), title) || !strings.Contains(rec.Body.String(), `"session_content":false`) {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
	if !strings.Contains(doJSON(t, s, http.MethodGet, "/ops/v1/tenants", owner, nil).Body.String(), org.ID) {
		t.Fatal("tenants missing org id")
	}
	rec = doJSON(t, s, http.MethodGet, "/ops/v1/tenants", other, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("tenants non-admin: %d", rec.Code)
	}

	mailer := &captureMail{}
	s.Mail = mailer
	rec = doJSON(t, s, http.MethodPost, "/orgs/"+org.ID+"/invites", owner, map[string]any{
		"email": "sam@ex.com", "role": "MEMBER",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("invite: %d %s", rec.Code, rec.Body.String())
	}
	if mailer.to != "sam@ex.com" || !strings.Contains(mailer.body, "token") {
		t.Fatalf("mail = %+v", mailer)
	}
}

func TestOAuthASMetadataUnauthenticated(t *testing.T) {
	s := newTestServer()
	for _, path := range []string{
		"/.well-known/oauth-authorization-server",
		"/v1/agent/mcp/.well-known/oauth-authorization-server",
	} {
		rec := doJSON(t, s, http.MethodGet, path, "", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		if !strings.Contains(body, `"issuer"`) || !strings.Contains(body, `"token_endpoint"`) {
			t.Fatalf("%s missing AS fields: %s", path, body)
		}
		if !strings.Contains(body, "incomplete") {
			t.Fatalf("%s should be labeled incomplete: %s", path, body)
		}
	}
}

func TestAgentSessionForkRoutes(t *testing.T) {
	s := newTestServer()
	mem := s.Store.(*store.MemStore)
	ctx := t.Context()
	proj, err := mem.ResolveProject(ctx, "", "", "fork-routes")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ClaimProject(ctx, proj.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	owner := loginAs(t, s, "owner")
	rec := doJSON(t, s, http.MethodPost, "/v1/agent-sessions", owner, map[string]any{
		"project_id": proj.ID, "harness": "claude", "native_id": "ses_fork_root",
		"origin_machine_id": "lap",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("session: %d %s", rec.Code, rec.Body.String())
	}
	var parent store.AgentSession
	decodeBody(t, rec, &parent)
	rec = doJSON(t, s, http.MethodPost, "/v1/agent-sessions/"+parent.ID+"/fork", owner, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("fork: %d %s", rec.Code, rec.Body.String())
	}
	var child store.AgentSession
	decodeBody(t, rec, &child)
	if child.ParentSessionID != parent.ID || child.LineageKind != "fork" {
		t.Fatalf("child=%+v", child)
	}
	rec = doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+parent.ID+"/forks", owner, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), child.ID) {
		t.Fatalf("forks: %d %s", rec.Code, rec.Body.String())
	}
}

func TestGrantLiveAndPointInTimeBody(t *testing.T) {
	s := newTestServer()
	mem := s.Store.(*store.MemStore)
	ctx := t.Context()
	proj, err := mem.ResolveProject(ctx, "", "", "grant-live")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ClaimProject(ctx, proj.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	owner := loginAs(t, s, "owner")
	other := loginAs(t, s, "other")
	rec := doJSON(t, s, http.MethodPost, "/v1/agent-sessions", owner, map[string]any{
		"project_id": proj.ID, "harness": "claude", "native_id": "ses_grant_live",
		"origin_machine_id": "lap",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("session: %d %s", rec.Code, rec.Body.String())
	}
	var sess store.AgentSession
	decodeBody(t, rec, &sess)
	body := []byte("t")
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	if err := mem.PutBlob(ctx, proj.ID, hash, "plain", "transcript", body); err != nil {
		t.Fatal(err)
	}
	ver, err := mem.CreateSessionVersion(ctx, sess.ID, "owner")
	if err != nil {
		t.Fatal(err)
	}
	man, _ := json.Marshal(map[string]any{"transcript": map[string]string{"blob": "sha256:" + hash}})
	if _, err := mem.CompleteSessionVersion(ctx, sess.ID, ver.Version, man); err != nil {
		t.Fatal(err)
	}
	rec = doJSON(t, s, http.MethodPut, "/v1/agent-sessions/"+sess.ID+"/grants/other", owner, map[string]any{
		"live": false, "version_id": ver.ID,
	})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"live":false`) {
		t.Fatalf("pit grant: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+sess.ID+"/summary", other, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("grantee summary: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodPut, "/v1/agent-sessions/"+sess.ID+"/grants/team", owner, map[string]any{
		"confirm": true, "live": false,
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("team must force live: %d %s", rec.Code, rec.Body.String())
	}
}

func TestSessionMCPTools(t *testing.T) {
	s := newTestServer()
	mem := s.Store.(*store.MemStore)
	ctx := t.Context()
	proj, err := mem.ResolveProject(ctx, "", "", "mcp-sessions")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ClaimProject(ctx, proj.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	owner := loginAs(t, s, "owner")
	rec := doJSON(t, s, http.MethodPost, "/v1/agent-sessions", owner, map[string]any{
		"project_id": proj.ID, "harness": "claude", "native_id": "ses_mcp",
		"origin_machine_id": "lap", "summary": "ship the acl",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("session: %d %s", rec.Code, rec.Body.String())
	}
	var created store.AgentSession
	decodeBody(t, rec, &created)
	rec = doJSON(t, s, http.MethodPost, "/v1/agent-sessions/"+created.ID+"/turns", owner, map[string]any{
		"idx": 1, "role": "user", "text_preview": "hello turn",
	})
	if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("turn: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSONWithHeaders(t, s, http.MethodPost, "/v1/agent/mcp", owner, map[string]string{
		"X-Nexus-Project": proj.ID,
	}, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{
			"name":      "session_summary_get",
			"arguments": map[string]any{"session_id": created.ID},
		},
	})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "ship the acl") {
		t.Fatalf("summary tool: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSONWithHeaders(t, s, http.MethodPost, "/v1/agent/mcp", owner, map[string]string{
		"X-Nexus-Project": proj.ID,
	}, map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]any{
			"name":      "session_fetch",
			"arguments": map[string]any{"session_id": created.ID, "limit": 10},
		},
	})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "hello turn") {
		t.Fatalf("fetch tool: %d %s", rec.Code, rec.Body.String())
	}
	if err := mem.CreateMemoryItem(ctx, &store.MemoryItem{
		ProjectID: proj.ID, Key: "k", Content: "Project knowledge stays visible to members on write.",
		Level: store.LevelProject, Status: store.StatusProposed, Scope: "fact",
	}); err != nil {
		t.Fatal(err)
	}
	rec = doJSONWithHeaders(t, s, http.MethodPost, "/v1/agent/mcp", owner, map[string]string{
		"X-Nexus-Project": proj.ID,
	}, map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "tools/call",
		"params": map[string]any{"name": "project_knowledge", "arguments": map[string]any{"limit": 5}},
	})
	body := rec.Body.String()
	if rec.Code != http.StatusOK || strings.Contains(body, "PROPOSED") || strings.Contains(body, "CONFIRMED") || !strings.Contains(body, "Project knowledge stays visible") {
		t.Fatalf("knowledge tool: %d %s", rec.Code, body)
	}
}
