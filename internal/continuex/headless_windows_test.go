//go:build windows

package continuex

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDirectExecutableHandlesUppercaseShimExtension(t *testing.T) {
	dir := t.TempDir()
	shim := filepath.Join(dir, "tool.CMD")
	native := filepath.Join(dir, "tool.exe")
	if err := os.WriteFile(shim, []byte("@echo off"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(native, []byte("MZ"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, prefix, err := directExecutable(shim, "unknown")
	if err != nil {
		t.Fatal(err)
	}
	if got != native || len(prefix) != 0 {
		t.Fatalf("resolved %q prefix %v, want %q", got, prefix, native)
	}
}

func TestCursorToolConsoleIsHiddenAndInheritable(t *testing.T) {
	cmd := exec.Command("cursor-agent")
	hideCursorToolConsole(cmd)
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.HideWindow {
		t.Fatal("Cursor's console must be hidden")
	}
	if cmd.SysProcAttr.CreationFlags&0x00000010 == 0 {
		t.Fatal("Cursor needs a console for child shell processes to inherit")
	}
	if cmd.SysProcAttr.CreationFlags&0x08000000 != 0 {
		t.Fatal("CREATE_NO_WINDOW would leave child shells without an inheritable console")
	}
}
