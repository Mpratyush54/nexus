//go:build windows

package hiddenproc

import (
	"os/exec"
	"testing"
)

func TestConfigurePreventsConsoleWindow(t *testing.T) {
	cmd := exec.Command("git", "--version")
	Configure(cmd)
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.HideWindow || cmd.SysProcAttr.CreationFlags&createNoWindow == 0 {
		t.Fatalf("background command may create a console: %+v", cmd.SysProcAttr)
	}
}
