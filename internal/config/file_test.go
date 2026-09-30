package config_test

import (
	"path/filepath"
	"runtime"
	"testing"

	"central-memory/internal/config"
)

func TestPushRecentWorkspace(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CENTRAL_MEMORY_CONFIG_DIR", dir)

	if err := config.PushRecentWorkspace(""); err != nil {
		t.Fatalf("empty push: %v", err)
	}
	cfg, err := config.LoadFile()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.RecentWorkspaces) != 0 {
		t.Fatalf("empty list want 0, got %#v", cfg.RecentWorkspaces)
	}

	a := filepath.Join(dir, "a")
	b := filepath.Join(dir, "b")
	if err := config.PushRecentWorkspace(a); err != nil {
		t.Fatal(err)
	}
	if err := config.PushRecentWorkspace(b); err != nil {
		t.Fatal(err)
	}
	cfg, _ = config.LoadFile()
	if len(cfg.RecentWorkspaces) != 2 || cfg.RecentWorkspaces[0] != config.NormalizeWorkspacePath(b) {
		t.Fatalf("after two pushes: %#v", cfg.RecentWorkspaces)
	}

	// Duplicate moves to front.
	if err := config.PushRecentWorkspace(a); err != nil {
		t.Fatal(err)
	}
	cfg, _ = config.LoadFile()
	if len(cfg.RecentWorkspaces) != 2 || cfg.RecentWorkspaces[0] != config.NormalizeWorkspacePath(a) {
		t.Fatalf("dup push: %#v", cfg.RecentWorkspaces)
	}

	for i := 0; i < 8; i++ {
		p := filepath.Join(dir, "ws", string(rune('c'+i)))
		if err := config.PushRecentWorkspace(p); err != nil {
			t.Fatal(err)
		}
	}
	cfg, _ = config.LoadFile()
	if len(cfg.RecentWorkspaces) != 5 {
		t.Fatalf("cap 5: got %d %#v", len(cfg.RecentWorkspaces), cfg.RecentWorkspaces)
	}
}

func TestPushRecentWorkspaceWindowsDriveCase(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows drive letter normalization")
	}
	dir := t.TempDir()
	t.Setenv("CENTRAL_MEMORY_CONFIG_DIR", dir)

	if err := config.PushRecentWorkspace(`D:\foo\bar`); err != nil {
		t.Fatal(err)
	}
	if err := config.PushRecentWorkspace(`d:\foo\bar`); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFile()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.RecentWorkspaces) != 1 {
		t.Fatalf("drive case should dedupe, got %#v", cfg.RecentWorkspaces)
	}
	if got := cfg.RecentWorkspaces[0]; got[0] != 'd' {
		t.Fatalf("want lowercase drive, got %q", got)
	}
}

func TestSaveFileUpdatesRecentWorkspaces(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CENTRAL_MEMORY_CONFIG_DIR", dir)
	ws := filepath.Join(dir, "project")
	if err := config.SaveFile(config.File{WorkspaceRoot: ws}); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFile()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WorkspaceRoot != ws {
		t.Fatalf("WorkspaceRoot = %q", cfg.WorkspaceRoot)
	}
	want := config.NormalizeWorkspacePath(ws)
	if len(cfg.RecentWorkspaces) != 1 || cfg.RecentWorkspaces[0] != want {
		t.Fatalf("RecentWorkspaces = %#v, want [%q]", cfg.RecentWorkspaces, want)
	}
}

func TestResolveDesktopTokenPrefersSessionOverEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CENTRAL_MEMORY_CONFIG_DIR", dir)
	t.Setenv("NEXUS_TOKEN", "nxs_stale_env_token")
	t.Setenv("CENTRAL_MEMORY_TOKEN", "")
	t.Setenv("CENTRAL_SERVER_TOKEN", "")

	if got := config.SessionToken(); got != "" {
		t.Fatalf("empty session: %q", got)
	}
	if got := config.ResolveToken(); got != "nxs_stale_env_token" {
		t.Fatalf("ResolveToken should prefer env, got %q", got)
	}
	if got := config.ResolveDesktopToken(); got != "nxs_stale_env_token" {
		t.Fatalf("no session → env fallback, got %q", got)
	}

	if err := config.SaveFile(config.File{Token: "nxs_session_ok"}); err != nil {
		t.Fatal(err)
	}
	if got := config.SessionToken(); got != "nxs_session_ok" {
		t.Fatalf("SessionToken = %q", got)
	}
	if got := config.ResolveDesktopToken(); got != "nxs_session_ok" {
		t.Fatalf("desktop must prefer config session over stale env, got %q", got)
	}
	if got := config.ResolveToken(); got != "nxs_stale_env_token" {
		t.Fatalf("ResolveToken still prefers env for headless agents, got %q", got)
	}
}
