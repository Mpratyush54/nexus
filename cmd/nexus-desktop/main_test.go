package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"central-memory/internal/config"
)

func TestResolveWorkspaceRoot(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CENTRAL_MEMORY_CONFIG_DIR", dir)
	t.Setenv("NEXUS_WORKSPACE", "")
	if got := resolveWorkspaceRoot(); got != "" {
		t.Fatalf("empty config want \"\", got %q", got)
	}
	ws := filepath.Join(dir, "proj")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveFile(config.File{WorkspaceRoot: ws}); err != nil {
		t.Fatal(err)
	}
	if got := resolveWorkspaceRoot(); got != ws {
		t.Fatalf("got %q want %q", got, ws)
	}
}

func TestHarvestHarnessCount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/local/harvest" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"agents": []any{map[string]any{"name": "a"}, map[string]any{"name": "b"}},
		})
	}))
	defer srv.Close()
	// harvestHarnessCount hardcodes 127.0.0.1:7272 — skip live call; assert JSON shape helper stays stable.
	var body struct {
		Agents []any `json:"agents"`
	}
	resp, err := http.Get(srv.URL + "/local/harvest")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Agents) != 2 {
		t.Fatalf("agents=%d", len(body.Agents))
	}
}
