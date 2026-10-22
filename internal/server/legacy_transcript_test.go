package server

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"

	"central-memory/internal/store"
)

func gzipJSONL(t *testing.T, lines ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(strings.Join(lines, "\n") + "\n")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestParseTranscriptJSONLShapes(t *testing.T) {
	raw := []byte(strings.Join([]string{
		`{"speaker":"user","content":"ship the fix"}`,
		`{"role":"assistant","message":{"content":[{"type":"text","text":"checking auth.go"},{"type":"tool_use","name":"Read","input":{"path":"auth.go"}}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"claude code line"}]}}`,
		`{"type":"permission-mode","permissionMode":"default"}`,
		`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"codex line"}]}}`,
		`{"type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{\"cmd\":\"ls\"}"}}`,
		`not json at all`,
		`{"partial": true`,
	}, "\n"))
	turns := ParseTranscriptJSONL(raw)
	if len(turns) != 5 {
		t.Fatalf("turns = %d, want 5: %+v", len(turns), turns)
	}
	if turns[0].Role != "user" || turns[0].TextPreview != "ship the fix" {
		t.Fatalf("turn0: %+v", turns[0])
	}
	if turns[1].Role != "assistant" || !strings.Contains(turns[1].TextPreview, "checking auth.go") ||
		!strings.Contains(string(turns[1].ToolCalls), "auth.go") {
		t.Fatalf("turn1: %+v", turns[1])
	}
	if turns[2].Role != "user" || turns[2].TextPreview != "claude code line" {
		t.Fatalf("turn2: %+v", turns[2])
	}
	if turns[3].Role != "assistant" || turns[3].TextPreview != "codex line" {
		t.Fatalf("turn3: %+v", turns[3])
	}
	if turns[4].Role != "tool" || !strings.Contains(string(turns[4].ToolCalls), "shell") {
		t.Fatalf("turn4: %+v", turns[4])
	}
	for i, turn := range turns {
		if turn.Idx != i {
			t.Fatalf("turn %d idx = %d", i, turn.Idx)
		}
	}
}

func TestLegacySessionFetchServesSnapshotTranscript(t *testing.T) {
	s := newTestServer()
	mem := s.Store.(*store.MemStore)
	ctx := t.Context()
	project, err := mem.ResolveProject(ctx, "", "", "legacy-transcript")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ClaimProject(ctx, project.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	legacyID := "3f7d0a54-1111-4c2e-9c48-8a2b1f4e6d10"
	payload := gzipJSONL(t,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"Fix the login bug"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Inspecting auth.go"},{"type":"tool_use","name":"Read","input":{"path":"auth.go"}}]}}`,
	)
	if err := mem.UpsertSessionSnapshot(ctx, &store.SessionSnapshot{
		SessionID: legacyID, ProjectID: project.ID, OwnerUserID: "owner",
		Harness: "claude", ConversationID: "conv-legacy", TurnCount: 2,
		TranscriptPayload: payload,
	}); err != nil {
		t.Fatal(err)
	}
	owner := loginAs(t, s, "owner")

	rec := doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+legacyID+"/fetch?limit=100", owner, nil)
	if rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), "Fix the login bug") ||
		!strings.Contains(rec.Body.String(), "auth.go") {
		t.Fatalf("legacy fetch: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+legacyID+"/summary", owner, nil)
	if rec.Code != http.StatusOK ||
		strings.Contains(rec.Body.String(), "Archived snapshot") ||
		!strings.Contains(rec.Body.String(), "Fix the login bug") {
		t.Fatalf("legacy summary: %d %s", rec.Code, rec.Body.String())
	}
}

func TestTeleportAcceptGrantsRecipientRead(t *testing.T) {
	s := newTestServer()
	mem := s.Store.(*store.MemStore)
	ctx := t.Context()
	project, err := mem.ResolveProject(ctx, "", "", "teleport-accept")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ClaimProject(ctx, project.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	owner := loginAs(t, s, "owner")
	other := loginAs(t, s, "other")

	rec := doJSON(t, s, http.MethodPost, "/v1/agent-sessions", owner, map[string]any{
		"project_id": project.ID, "harness": "claude", "native_id": "ses_accept", "title": "handoff",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("session: %d %s", rec.Code, rec.Body.String())
	}
	var created store.AgentSession
	decodeBody(t, rec, &created)

	transcript := []byte(`{"turn":1}`)
	sum := shaHex(transcript)
	rec = doJSON(t, s, http.MethodPut, "/v1/blobs/"+sum, owner, map[string]any{
		"project_id": project.ID, "purpose": "transcript",
		"body_b64": base64.StdEncoding.EncodeToString(transcript),
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("put transcript: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodPost, "/v1/agent-sessions/"+created.ID+"/versions", owner, map[string]any{})
	if rec.Code != http.StatusCreated {
		t.Fatalf("version: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodPost, "/v1/agent-sessions/"+created.ID+"/versions/1/complete", owner, map[string]any{
		"harness": "claude", "native_id": "ses_accept",
		"transcript": map[string]any{"blob": "sha256:" + sum},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("complete: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodPost, "/v1/teleports", owner, map[string]any{
		"session_id": created.ID, "to_user_id": "other",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("teleport: %d %s", rec.Code, rec.Body.String())
	}
	var tp store.Teleport
	decodeBody(t, rec, &tp)

	rec = doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+created.ID, other, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("pre-accept read should be forbidden: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodPost, "/v1/teleports/"+tp.ID+"/accept", other, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("accept: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+created.ID, other, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("grantee read after accept: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+created.ID+"/fetch?limit=10", other, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("grantee fetch after accept: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodPost, "/v1/teleports/"+tp.ID+"/accept", other, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("re-accept must stay idempotent: %d %s", rec.Code, rec.Body.String())
	}
}
