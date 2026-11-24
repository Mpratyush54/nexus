package mcpproxy

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func lookOnly(found map[string]string) func(string) (string, error) {
	return func(name string) (string, error) {
		if p, ok := found[name]; ok {
			return p, nil
		}
		return "", errors.New("not found")
	}
}

func TestLocatePrefersBundledProxy(t *testing.T) {
	app := t.TempDir()
	bundled := filepath.Join(app, BinaryName())
	if err := os.WriteFile(bundled, []byte("stub"), 0o700); err != nil {
		t.Fatal(err)
	}
	look := lookOnly(map[string]string{"nexus": "/usr/bin/nexus", "nexus-mcp-proxy": "/usr/bin/nexus-mcp-proxy"})
	argv, err := Locate(app, look)
	if err != nil || len(argv) != 1 || argv[0] != bundled || !filepath.IsAbs(argv[0]) {
		t.Fatalf("argv=%v err=%v", argv, err)
	}
}

func TestLocateFallsBackToPath(t *testing.T) {
	empty := t.TempDir()
	// A directory with the proxy's name is not a binary.
	if err := os.Mkdir(filepath.Join(empty, BinaryName()), 0o700); err != nil {
		t.Fatal(err)
	}
	argv, err := Locate(empty, lookOnly(map[string]string{"nexus-mcp-proxy": "/opt/nexus-mcp-proxy", "nexus": "/usr/bin/nexus"}))
	if err != nil || strings.Join(argv, " ") != "/opt/nexus-mcp-proxy" {
		t.Fatalf("argv=%v err=%v", argv, err)
	}
	argv, err = Locate("", lookOnly(map[string]string{"nexus": "/usr/bin/nexus"}))
	if err != nil || strings.Join(argv, " ") != "/usr/bin/nexus mcp-proxy" {
		t.Fatalf("argv=%v err=%v", argv, err)
	}
}

func TestLocateMissingExplainsWhy(t *testing.T) {
	app := t.TempDir()
	_, err := Locate(app, lookOnly(nil))
	if err == nil || !strings.Contains(err.Error(), BinaryName()) || !strings.Contains(err.Error(), app) {
		t.Fatalf("err=%v", err)
	}
}
