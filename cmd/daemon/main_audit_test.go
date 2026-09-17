package main

import (
	"testing"
)

// Audit coverage for cmd/daemon/main.go flag validation. run() binds a
// listener and blocks on success, so only failing paths are tested here.

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
