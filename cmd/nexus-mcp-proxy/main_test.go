package main

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

// fakeMCP is a remote /v1/agent/mcp that answers initialize and accepts
// notifications with 204, recording what the proxy sent.
func fakeMCP(t *testing.T, wantToken, wantProject string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/agent/mcp" || r.Method != http.MethodPost {
			http.Error(w, "bad route", http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+wantToken || r.Header.Get("X-Nexus-Project") != wantProject {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.Unmarshal(raw, &msg); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if len(msg.ID) == 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(msg.ID) + `,"result":{"protocolVersion":"2025-06-18","serverInfo":{"name":"nexus-fake","version":"1"},"capabilities":{"tools":{}}}}`))
	}))
}

const initFrames = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}
{"jsonrpc":"2.0","method":"notifications/initialized"}
`

func TestProxyInitializeFromEnv(t *testing.T) {
	srv := fakeMCP(t, "nxs_env", "proj-1")
	defer srv.Close()
	t.Setenv("NEXUS_SERVER", srv.URL)
	t.Setenv("NEXUS_TOKEN", "nxs_env")
	t.Setenv("NEXUS_PROJECT", "proj-1")
	var out, errOut bytes.Buffer
	if code := run(context.Background(), nil, strings.NewReader(initFrames), &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("want one response (notification has none), got %q", out.String())
	}
	var resp struct {
		ID     int `json:"id"`
		Result struct {
			ServerInfo struct{ Name string } `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &resp); err != nil || resp.ID != 1 || resp.Result.ServerInfo.Name != "nexus-fake" {
		t.Fatalf("response %q: %v", lines[0], err)
	}
}

func TestProxyFlagsOverrideEnv(t *testing.T) {
	srv := fakeMCP(t, "nxs_flag", "proj-flag")
	defer srv.Close()
	t.Setenv("NEXUS_SERVER", "http://127.0.0.1:1")
	t.Setenv("NEXUS_TOKEN", "wrong")
	t.Setenv("NEXUS_PROJECT", "")
	t.Setenv("MY_AGENT_TOKEN", "nxs_flag")
	var out, errOut bytes.Buffer
	args := []string{"mcp-proxy", "--server", srv.URL, "--token-env", "MY_AGENT_TOKEN", "--project", "proj-flag"}
	if code := run(context.Background(), args, strings.NewReader(initFrames), &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), `"nexus-fake"`) || strings.Contains(out.String(), `"error"`) {
		t.Fatalf("output %q", out.String())
	}
}

func TestProxyNeedsToken(t *testing.T) {
	t.Setenv("NEXUS_TOKEN", "")
	t.Setenv("CENTRAL_MEMORY_TOKEN", "")
	t.Setenv("CENTRAL_SERVER_TOKEN", "")
	var out, errOut bytes.Buffer
	if code := run(context.Background(), nil, strings.NewReader(initFrames), &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "NEXUS_TOKEN") {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if code := run(context.Background(), []string{"extra"}, strings.NewReader(""), &out, &errOut); code != 2 {
		t.Fatalf("stray argument accepted: %d", code)
	}
}
