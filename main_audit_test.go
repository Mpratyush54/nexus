package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

func containsAudit(s, sub string) bool { return strings.Contains(s, sub) }

func captureRootStdoutAudit(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()
	fn()
	_ = w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return buf.String()
}

// Audit coverage for the root mem CLI (./main.go): dispatch surface and
// daemon subcommand validation. cmdProjects/cmdStatus hit the live project
// scanner; they must succeed (return nil) regardless of repo layout.

func TestAuditRootDaemonValidation(t *testing.T) {
	if err := cmdDaemon(nil); err == nil {
		t.Error("cmdDaemon(nil): expected error requiring install|uninstall|status")
	}
	if err := cmdDaemon([]string{"bogus"}); err == nil {
		t.Error("cmdDaemon(bogus): expected unknown-subcommand error")
	}
	if err := cmdDaemon([]string{"install"}); err == nil {
		t.Error("cmdDaemon(install) without NEXUS_DAEMON_BIN: expected env error")
	} else if got := err.Error(); !containsAudit(got, "NEXUS_DAEMON_BIN") {
		t.Errorf("install error must mention NEXUS_DAEMON_BIN, got %q", got)
	}
}

func TestAuditRootProjectsStatus(t *testing.T) {
	if err := cmdStatus(); err != nil {
		t.Fatalf("cmdStatus: %v", err)
	}
	if err := cmdProjects(nil); err != nil {
		t.Fatalf("cmdProjects: %v", err)
	}
}

func TestAuditRootUsageOutput(t *testing.T) {
	out := captureRootStdoutAudit(t, usage)
	for _, want := range []string{"mem projects", "mem status", "mem daemon", "nexus"} {
		if !containsAudit(out, want) {
			t.Errorf("usage() missing %q:\n%s", want, out)
		}
	}
	out = captureRootStdoutAudit(t, cmdDaemonUsage)
	if !containsAudit(out, "mem daemon install") {
		t.Errorf("cmdDaemonUsage missing install line:\n%s", out)
	}
}
