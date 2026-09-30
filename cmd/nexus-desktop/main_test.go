package main

import (
	"os"
	"path/filepath"
	"testing"

	"central-memory/internal/config"
	"central-memory/internal/localclient"
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

func TestLocalClientOnlineShape(t *testing.T) {
	// Smoke: constructor defaults; live daemon not required.
	c := localclient.New("")
	if c.Base != localclient.DefaultBase {
		t.Fatalf("base=%q", c.Base)
	}
}
