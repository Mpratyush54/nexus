package server

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"net/http"
	"testing"

	"central-memory/internal/store"
)

func TestProvenanceAndSnapshotRoutes(t *testing.T) {
	s := newTestServer()
	tok := loginAs(t, s, "user-test")
	mem := s.Store.(*store.MemStore)
	proj, err := mem.ResolveProject(t.Context(), "", "", "teleport-test")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = mem.ClaimProject(t.Context(), proj.ID, "user-test")
	_ = mem.GrantMember(t.Context(), proj.ID, "user-test", "user-test")

	sessionID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	rec := doJSON(t, s, http.MethodPost, "/sessions/"+sessionID+"/operations", tok, map[string]any{
		"project_id": proj.ID,
		"harness":    "antigravity",
		"file_operations": []map[string]any{{
			"tool_name": "view_file", "file_path": "a.go", "op_type": "read",
		}},
		"tool_executions": []map[string]any{{
			"tool_name": "run_command", "command_line": "go test",
		}},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("operations: %d %s", rec.Code, rec.Body.String())
	}

	gz := mustGzipBytes(t, []byte(`{"turn":1}`))
	rec = doJSON(t, s, http.MethodPost, "/sessions/"+sessionID+"/snapshot", tok, map[string]any{
		"project_id":             proj.ID,
		"harness":                "antigravity",
		"conversation_id":        "c1",
		"turn_count":             1,
		"transcript_payload_b64": base64.StdEncoding.EncodeToString(gz),
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("snapshot: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodGet, "/projects/"+proj.ID+"/snapshots", tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodGet, "/sessions/"+sessionID+"/operations", tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get ops: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodGet, "/sessions/"+sessionID+"/files", tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("files: %d %s", rec.Code, rec.Body.String())
	}
}

func mustGzipBytes(t *testing.T, raw []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
