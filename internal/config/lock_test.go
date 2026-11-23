package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConfigLockHelper(t *testing.T) {
	if os.Getenv("NEXUS_CONFIG_LOCK_HELPER") != "1" {
		return
	}
	dir := os.Getenv("CENTRAL_MEMORY_CONFIG_DIR")
	if err := withConfigLock(func() error {
		if err := os.WriteFile(filepath.Join(dir, "held"), []byte("1"), 0o600); err != nil {
			return err
		}
		time.Sleep(10 * time.Second)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestConfigLockBlocksAcrossProcesses(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CENTRAL_MEMORY_CONFIG_DIR", dir)
	env := make([]string, 0, len(os.Environ())+2)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "CENTRAL_MEMORY_CONFIG_DIR=") || strings.HasPrefix(entry, "NEXUS_CONFIG_LOCK_HELPER=") {
			continue
		}
		env = append(env, entry)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestConfigLockHelper$")
	cmd.Env = append(env, "NEXUS_CONFIG_LOCK_HELPER=1", "CENTRAL_MEMORY_CONFIG_DIR="+dir)
	var output strings.Builder
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	marker := filepath.Join(dir, "held")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("helper did not acquire lock: %s", output.String())
		}
		time.Sleep(20 * time.Millisecond)
	}

	started := time.Now()
	err := withConfigLock(func() error { return nil })
	if err == nil {
		t.Fatal("expected lock timeout while helper holds the lock")
	}
	if !strings.Contains(err.Error(), "lock timeout") {
		t.Fatalf("unexpected lock error: %v", err)
	}
	if elapsed := time.Since(started); elapsed < 4*time.Second {
		t.Fatalf("lock timed out too early: %s", elapsed)
	}
}
