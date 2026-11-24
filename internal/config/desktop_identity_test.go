package config_test

import (
	"central-memory/internal/config"
	"os"
	"runtime"
	"strings"
	"testing"
)

func TestDesktopIdentityProtectedStableAndOptIn(t *testing.T) {
	t.Setenv("CENTRAL_MEMORY_CONFIG_DIR", t.TempDir())
	id, key, e := config.DesktopIdentity()
	if e != nil {
		t.Fatal(e)
	}
	other, secret, e := config.DesktopIdentity()
	if e != nil || id != other || key != secret || len(key) < 32 {
		t.Fatal("unstable identity", e)
	}
	cfg, e := config.LoadFile()
	if e != nil || cfg.DesktopExecutionEnabled {
		t.Fatal("execution must default off", e)
	}
	if e = config.SetDesktopExecution(true); e != nil {
		t.Fatal(e)
	}
	if e = config.SaveFile(config.File{Username: "new name"}); e != nil {
		t.Fatal(e)
	}
	cfg, e = config.LoadFile()
	if e != nil || cfg.DesktopDeviceKey != key || !cfg.DesktopExecutionEnabled {
		t.Fatal("config merge lost identity", e)
	}
	path, _ := config.ConfigPath()
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	if runtime.GOOS == "windows" && strings.Contains(string(b), key) {
		t.Fatal("device secret not OS protected")
	}
	if e = config.SetDesktopExecution(false); e != nil {
		t.Fatal(e)
	}
	cfg, _ = config.LoadFile()
	if cfg.DesktopExecutionEnabled {
		t.Fatal("could not revoke")
	}
}
