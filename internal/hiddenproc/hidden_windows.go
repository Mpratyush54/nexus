//go:build windows

// Package hiddenproc keeps background CLI helpers from creating console
// windows when invoked by the native Nexus desktop application.
package hiddenproc

import (
	"os/exec"
	"syscall"
)

const createNoWindow = 0x08000000

// Configure marks a background command as non-interactive on Windows.
func Configure(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}
