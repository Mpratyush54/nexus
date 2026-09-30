//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// CREATE_NEW_PROCESS_GROUP keeps the daemon from receiving CTRL_CLOSE_EVENT
// when a console that once parented the tray is closed.
// CREATE_NO_WINDOW stops nexus-daemon.exe (console subsystem) from flashing a terminal.
const createNewProcessGroup = 0x00000200

func configureDaemonCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow | createNewProcessGroup,
	}
}
