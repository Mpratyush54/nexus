package mcpconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigureCursorRemote(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".cursor", "mcp.json")
	res, err := Configure(Options{
		Agent:     "cursor",
		Path:      path,
		ServerURL: "https://api.example.test",
		ProjectID: "proj-9",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Transport != "remote" || res.MCPURL != "https://api.example.test/v1/agent/mcp" {
		t.Fatalf("result=%+v", res)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	servers := doc["mcpServers"].(map[string]any)
	nexus := servers["nexus"].(map[string]any)
	if nexus["url"] != "https://api.example.test/v1/agent/mcp" {
		t.Fatalf("url=%v", nexus["url"])
	}
	headers := nexus["headers"].(map[string]any)
	if headers["Authorization"] != "Bearer ${NEXUS_TOKEN}" {
		t.Fatalf("auth=%v", headers["Authorization"])
	}
	if headers["X-Nexus-Project"] != "proj-9" {
		t.Fatalf("project=%v", headers["X-Nexus-Project"])
	}
}

func TestConfigureAntigravityProxy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp_config.json")
	res, err := Configure(Options{
		Agent:     "antigravity",
		Path:      path,
		ProjectID: "p1",
		ServerURL: "https://api.example.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Transport != "proxy" || res.Command != "nexus" {
		t.Fatalf("result=%+v", res)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	nexus := doc["mcpServers"].(map[string]any)["nexus"].(map[string]any)
	if nexus["command"] != "nexus" {
		t.Fatalf("command=%v", nexus["command"])
	}
	args := nexus["args"].([]any)
	if len(args) != 1 || args[0] != "mcp-proxy" {
		t.Fatalf("args=%v", args)
	}
	env := nexus["env"].(map[string]any)
	if env["NEXUS_PROJECT"] != "p1" || env["NEXUS_SERVER"] != "https://api.example.test" {
		t.Fatalf("env=%v", env)
	}
}

func TestConfigureDefaultPathUnderHome(t *testing.T) {
	home := t.TempDir()
	res, err := Configure(Options{Home: home, Agent: "cursor"})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".cursor", "mcp.json")
	if res.Path != want {
		t.Fatalf("path=%q want %q", res.Path, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(want)
	if !strings.Contains(string(raw), "mcpServers") {
		t.Fatalf("content=%s", raw)
	}
}

func TestConfigurePreservesOtherServers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path, []byte(`{"mcpServers":{"other":{"command":"echo"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Configure(Options{Path: path, Agent: "cursor"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	var doc map[string]any
	_ = json.Unmarshal(raw, &doc)
	servers := doc["mcpServers"].(map[string]any)
	if _, ok := servers["other"]; !ok {
		t.Fatal("lost other server")
	}
	if _, ok := servers["nexus"]; !ok {
		t.Fatal("missing nexus")
	}
}
