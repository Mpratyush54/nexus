package mcpproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestForwardInitializeAndToolsCall(t *testing.T) {
	var gotAuth, gotProject, gotPath string
	var frames []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotProject = r.Header.Get("X-Nexus-Project")
		raw, _ := io.ReadAll(r.Body)
		frames = append(frames, string(raw))
		var req map[string]any
		if err := json.Unmarshal(raw, &req); err != nil {
			t.Errorf("body: %v", err)
			http.Error(w, "bad json", 400)
			return
		}
		method, _ := req["method"].(string)
		id := req["id"]
		switch method {
		case "initialize":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result": map[string]any{
					"protocolVersion": "2024-11-05",
					"serverInfo":      map[string]any{"name": "nexus", "version": "test"},
				},
			})
		case "tools/call":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result":  map[string]any{"content": []any{map[string]any{"type": "text", "text": "ok"}}},
			})
		default:
			http.Error(w, "unexpected method "+method, 400)
		}
	}))
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL, Token: "tok-abc", ProjectID: "proj-1", HTTP: srv.Client()}
	initReq := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	initBody, err := cfg.Forward(context.Background(), []byte(initReq))
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	callReq := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"memory_search","arguments":{"query":"redis"}}}`
	callBody, err := cfg.Forward(context.Background(), []byte(callReq))
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}

	if gotPath != "/v1/agent/mcp" {
		t.Fatalf("path=%q", gotPath)
	}
	if gotAuth != "Bearer tok-abc" {
		t.Fatalf("auth=%q", gotAuth)
	}
	if gotProject != "proj-1" {
		t.Fatalf("project=%q", gotProject)
	}
	if len(frames) != 2 {
		t.Fatalf("frames=%d", len(frames))
	}
	var initResp, callResp map[string]any
	if err := json.Unmarshal(initBody, &initResp); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(callBody, &callResp); err != nil {
		t.Fatal(err)
	}
	if initResp["result"] == nil || callResp["result"] == nil {
		t.Fatalf("missing results: init=%v call=%v", initResp, callResp)
	}
}

func TestServeStdioRoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var req struct {
			ID any `json:"id"`
		}
		_ = json.Unmarshal(raw, &req)
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"ok": true}})
	}))
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL, Token: "t", ProjectID: "p", HTTP: srv.Client()}
	in := strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"initialize","params":{}}` + "\n")
	var out bytes.Buffer
	if err := cfg.Serve(context.Background(), in, &out); err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(out.String())
	if !strings.Contains(line, `"id":7`) || !strings.Contains(line, `"ok":true`) {
		t.Fatalf("out=%s", line)
	}
}

func TestForwardNoContentNotification(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	cfg := Config{BaseURL: srv.URL, Token: "t", HTTP: srv.Client()}
	body, err := cfg.Forward(context.Background(), []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	if err != nil || body != nil {
		t.Fatalf("got %q %v", body, err)
	}
}
