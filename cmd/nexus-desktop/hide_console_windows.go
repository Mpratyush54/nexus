//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// CREATE_NO_WINDOW prevents a console window from being allocated for the child.
// Pair with HideWindow so helpers (powershell/taskkill/tasklist) never flash.
const createNoWindow = 0x08000000

// hideConsoleAttr configures a Windows child process to stay invisible.
func hideConsoleAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
}

func hideConsoleCmd(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.SysProcAttr = hideConsoleAttr()
}
