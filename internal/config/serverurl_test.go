package config_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"central-memory/internal/config"
)

func TestResolveServerURLCascade(t *testing.T) {
	t.Setenv("CENTRAL_SERVER_URL", "")
	t.Setenv("NEXUS_SERVER", "")
	dir := t.TempDir()
	t.Setenv("CENTRAL_MEMORY_CONFIG_DIR", dir)

	const compileDefault = "https://api-nexus.pratyushes.dev"

	// Tier 4: compile default when nothing else is set.
	if got := config.ResolveServerURL(compileDefault); got != compileDefault {
		t.Fatalf("tier4: got %q want %q", got, compileDefault)
	}

	// Tier 3: config file wins over compile default.
	cfgPath := filepath.Join(dir, config.ConfigFileName)
	raw, _ := json.Marshal(map[string]string{"server_url": "https://from-config.example"})
	if err := os.WriteFile(cfgPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := config.ResolveServerURL(compileDefault); got != "https://from-config.example" {
		t.Fatalf("tier3: got %q want config URL", got)
	}

	// Tier 2: NEXUS_SERVER wins over config.
	t.Setenv("NEXUS_SERVER", "https://from-nexus.example")
	if got := config.ResolveServerURL(compileDefault); got != "https://from-nexus.example" {
		t.Fatalf("tier2 NEXUS_SERVER: got %q", got)
	}

	// CENTRAL_SERVER_URL wins over NEXUS_SERVER.
	t.Setenv("CENTRAL_SERVER_URL", "https://from-central.example")
	if got := config.ResolveServerURL(compileDefault); got != "https://from-central.example" {
		t.Fatalf("tier2 CENTRAL_SERVER_URL: got %q", got)
	}
}

func TestServerURLFromConfigMissing(t *testing.T) {
	t.Setenv("CENTRAL_MEMORY_CONFIG_DIR", t.TempDir())
	if got := config.ServerURLFromConfig(); got != "" {
		t.Fatalf("missing config: got %q want empty", got)
	}
}

func TestConfigPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CENTRAL_MEMORY_CONFIG_DIR", dir)
	got, err := config.ConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, config.ConfigFileName)
	if got != want {
		t.Fatalf("ConfigPath = %q want %q", got, want)
	}
}
