//go:build !windows

package agentsetup

import (
	"os/exec"
	"syscall"
)

// The tested server runs in its own process group, so stopping it also
// stops whatever it started (npx starts node, uvx starts python).
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func containTree(_ *exec.Cmd) {}

func killTree(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
