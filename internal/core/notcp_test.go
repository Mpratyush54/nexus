package core

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"central-memory/internal/agents/mcpconfig"
)

func TestNexuscoreSourceHasNoTCPListen(t *testing.T) {
	// Default go build ./cmd/nexuscore must not grow a localhost HTTP gate.
	roots := []string{
		filepath.Join("..", "..", "cmd", "nexuscore"),
		".",
	}
	hits, err := SourceMentionsTCPListen(roots...)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) > 0 {
		t.Fatalf("nexuscore/core must not call Listen*: %v", hits)
	}
}

func TestNoTCPEnv(t *testing.T) {
	t.Setenv("NEXUS_NO_TCP", "1")
	if !NoTCPEnabled() {
		t.Fatal("expected enabled")
	}
	if err := RefuseListen(); err == nil {
		t.Fatal("expected refuse")
	}
	t.Setenv("NEXUS_NO_TCP", "")
	if NoTCPEnabled() {
		t.Fatal("expected disabled")
	}
}

func TestConfigureMCPViaCall(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	payload, err := json.Marshal(map[string]any{
		"agent":      "antigravity",
		"path":       path,
		"server_url": "https://api.example.test",
		"project_id": "p",
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := Call(t.Context(), "agents.configure_mcp", payload, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	res, ok := out.(mcpconfig.Result)
	if !ok {
		t.Fatalf("type %T", out)
	}
	if res.Transport != "proxy" {
		t.Fatalf("transport = %q", res.Transport)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"proxy"`)) {
		t.Fatalf("want proxy in result: %s", raw)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "mcp-proxy") && !strings.Contains(string(body), "proxy") {
		t.Fatalf("config missing proxy: %s", body)
	}
}
