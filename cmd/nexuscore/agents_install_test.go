//go:build nexuscorelib

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"central-memory/internal/agentsetup"
	"central-memory/internal/core"
	"central-memory/internal/mcpproxy"
)

func installTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("PATH", "") // no real CLIs: only the temp home decides
	if err := os.MkdirAll(filepath.Join(home, ".cursor"), 0o700); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestAgentsInstallAllMintsScopedToken(t *testing.T) {
	home := installTestHome(t)
	var minted []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/auth/tokens" || r.Header.Get("Authorization") != "Bearer session-jwt" {
			http.Error(w, "unexpected", http.StatusBadRequest)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		minted = append(minted, body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"token":"nxs_scoped","id":"tok-1"}`))
	}))
	defer srv.Close()

	nxState.mu.Lock()
	previous := nxState.deps
	nxState.deps.Cloud = &core.HTTPCloud{BaseURL: srv.URL, Token: "session-jwt"}
	nxState.mu.Unlock()
	t.Cleanup(func() { nxState.mu.Lock(); nxState.deps = previous; nxState.mu.Unlock() })

	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(args)
		out, err := agentsInstallCall(context.Background(), name, raw)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return out.(map[string]any)
	}
	cursorReport := func(out map[string]any) agentsetup.HarnessReport {
		t.Helper()
		for _, r := range out["harnesses"].([]agentsetup.HarnessReport) {
			if r.Harness == "cursor" {
				return r
			}
		}
		t.Fatal("no cursor report")
		return agentsetup.HarnessReport{}
	}

	dry := call("agents.install_all", map[string]any{"dry_run": true})
	if cursorReport(dry).Action != "added" || len(minted) != 0 {
		t.Fatalf("dry run: %+v minted=%v", cursorReport(dry), minted)
	}
	if _, err := os.Stat(filepath.Join(home, ".cursor", "mcp.json")); !os.IsNotExist(err) {
		t.Fatal("dry run wrote")
	}

	out := call("agents.install_all", map[string]any{"project_id": "p1"})
	if cursorReport(out).Action != "added" || len(minted) != 1 || minted[0]["agent_id"] != "cursor" {
		t.Fatalf("%+v minted=%v", cursorReport(out), minted)
	}
	cfg, _ := os.ReadFile(filepath.Join(home, ".cursor", "mcp.json"))
	if !strings.Contains(string(cfg), "Bearer nxs_scoped") || strings.Contains(string(cfg), "session-jwt") || !strings.Contains(string(cfg), srv.URL+"/v1/agent/mcp") {
		t.Fatalf("config: %s", cfg)
	}

	again := call("agents.install_all", map[string]any{"project_id": "p1"})
	if cursorReport(again).Action != "unchanged" || len(minted) != 1 {
		t.Fatalf("rerun: %+v minted=%v", cursorReport(again), minted)
	}

	removed := call("agents.uninstall_all", map[string]any{})
	if cursorReport(removed).Action != "removed" {
		t.Fatalf("uninstall: %+v", cursorReport(removed))
	}
}

func TestAgentsInstallAllRequiresSignIn(t *testing.T) {
	installTestHome(t)
	nxState.mu.Lock()
	previous := nxState.deps
	nxState.deps = core.Deps{}
	nxState.mu.Unlock()
	t.Cleanup(func() { nxState.mu.Lock(); nxState.deps = previous; nxState.mu.Unlock() })
	if _, err := agentsInstallCall(context.Background(), "agents.install_all", json.RawMessage(`{}`)); err == nil {
		t.Fatal("install without sign-in must fail")
	}
}

// Antigravity is installed through the proxy bundled in app_dir (the desktop
// app folder), with no nexus CLI anywhere on PATH.
func TestAgentsInstallAllAntigravityUsesBundledProxy(t *testing.T) {
	home := installTestHome(t)
	if err := os.MkdirAll(filepath.Join(home, ".gemini", "antigravity"), 0o700); err != nil {
		t.Fatal(err)
	}
	app := t.TempDir()
	bundled := filepath.Join(app, mcpproxy.BinaryName())
	if err := os.WriteFile(bundled, []byte("stub"), 0o700); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"token":"nxs_` + body["agent_id"].(string) + `"}`))
	}))
	defer srv.Close()
	nxState.mu.Lock()
	previous := nxState.deps
	nxState.deps.Cloud = &core.HTTPCloud{BaseURL: srv.URL, Token: "session-jwt"}
	nxState.mu.Unlock()
	t.Cleanup(func() { nxState.mu.Lock(); nxState.deps = previous; nxState.mu.Unlock() })

	raw, _ := json.Marshal(map[string]any{"app_dir": app})
	out, err := agentsInstallCall(context.Background(), "agents.install_all", raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range out.(map[string]any)["harnesses"].([]agentsetup.HarnessReport) {
		if r.Harness == "antigravity" && r.Action != "added" {
			t.Fatalf("antigravity: %+v", r)
		}
	}
	cfg, _ := os.ReadFile(filepath.Join(home, ".gemini", "antigravity", "mcp_config.json"))
	var doc struct {
		MCPServers map[string]struct {
			Command string            `json:"command"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(cfg, &doc); err != nil {
		t.Fatalf("%v: %s", err, cfg)
	}
	if e := doc.MCPServers["nexus"]; e.Command != bundled || e.Env["NEXUS_TOKEN"] != "nxs_antigravity" {
		t.Fatalf("antigravity entry: %s", cfg)
	}
}
