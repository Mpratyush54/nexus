package main

import (
	"testing"

	"central-memory/internal/config"
)

// Audit coverage for cmd/daemon/main.go flag validation. run() binds a
// listener and blocks on success, so only failing paths are tested here.

func TestAuditDefaultServerURL(t *testing.T) {
	t.Setenv("CENTRAL_SERVER_URL", "")
	t.Setenv("NEXUS_SERVER", "")
	t.Setenv("CENTRAL_MEMORY_CONFIG_DIR", t.TempDir())
	if defaultServerURL == "" {
		t.Fatal("defaultServerURL must be non-empty for ldflags override")
	}
	if got := config.ResolveServerURL(defaultServerURL); got != defaultServerURL {
		t.Fatalf("ResolveServerURL = %q want %q", got, defaultServerURL)
	}
}

func TestAuditDaemonInvalidPort(t *testing.T) {
	for _, args := range [][]string{
		{"-port", "0"},
		{"-port", "-1"},
		{"-port", "99999"},
		{"-port", "abc"},
	} {
		if err := run(args); err == nil {
			t.Errorf("run(%v): expected error", args)
		}
	}
}

func TestAuditDaemonUnknownFlag(t *testing.T) {
	if err := run([]string{"--nope"}); err == nil {
		t.Error("run(--nope): expected flag parse error")
	}
}

func TestAuditDaemonEmptyBind(t *testing.T) {
	if err := run([]string{"-bind", ""}); err == nil {
		t.Error("run(-bind \"\"): expected empty-bind error")
	}
	if err := run([]string{"-bind", "   "}); err == nil {
		t.Error("run(-bind spaces): expected empty-bind error")
	}
}

func TestAuditDefaultBind(t *testing.T) {
	t.Setenv("DAEMON_BIND", "")
	if got := defaultBind(); got != "127.0.0.1" {
		t.Errorf("defaultBind() = %q, want 127.0.0.1", got)
	}
	t.Setenv("DAEMON_BIND", "0.0.0.0")
	if got := defaultBind(); got != "0.0.0.0" {
		t.Errorf("defaultBind() with env = %q, want 0.0.0.0", got)
	}
}

func TestAuditDefaultPort(t *testing.T) {
	t.Setenv("DAEMON_PORT", "")
	if got := defaultPort(); got != 7687 {
		t.Errorf("defaultPort() = %d, want 7687", got)
	}
	t.Setenv("DAEMON_PORT", "9999")
	if got := defaultPort(); got != 9999 {
		t.Errorf("defaultPort() with env = %d, want 9999", got)
	}
	t.Setenv("DAEMON_PORT", "bogus")
	if got := defaultPort(); got != 7687 {
		t.Errorf("defaultPort() with bogus env = %d, want fallback 7687", got)
	}
}

func TestAuditDesignatedFromEnv(t *testing.T) {
	t.Setenv("CENTRAL_DESIGNATED_PROCESSOR", "")
	if designatedFromEnv() {
		t.Error("designatedFromEnv() empty = true, want fail-closed false")
	}
	for _, v := range []string{"1", "true", "TRUE", "yes"} {
		t.Setenv("CENTRAL_DESIGNATED_PROCESSOR", v)
		if !designatedFromEnv() {
			t.Errorf("designatedFromEnv(%q) = false, want true", v)
		}
	}
	t.Setenv("CENTRAL_DESIGNATED_PROCESSOR", "0")
	if designatedFromEnv() {
		t.Error("designatedFromEnv(0) = true, want false")
	}
}
