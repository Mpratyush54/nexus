//go:build windows

package continuex

import (
	"os/exec"
	"syscall"
)

func hideConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}

// Cursor runs shell tools as child console processes. CREATE_NO_WINDOW leaves
// its Node parent without a console, so each child may allocate a visible one.
// Give Cursor one hidden console that ordinary child processes can inherit.
// Keep stdout/stderr redirected by os/exec for the in-app event stream.
func hideCursorToolConsole(cmd *exec.Cmd) {
	const createNewConsole = 0x00000010
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNewConsole}
}
