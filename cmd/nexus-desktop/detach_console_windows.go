//go:build windows

package main

import "syscall"

var (
	modKernel32              = syscall.NewLazyDLL("kernel32.dll")
	procFreeConsole          = modKernel32.NewProc("FreeConsole")
	procSetConsoleCtrlHandler = modKernel32.NewProc("SetConsoleCtrlHandler")
)

// detachFromParentConsole keeps the tray alive when the user closes the
// console window that launched nexus-desktop.exe (e.g. from a terminal).
// Without this, Windows delivers CTRL_CLOSE_EVENT to the process group and
// the tray exits silently.
func detachFromParentConsole() {
	// HandlerRoutine=NULL, Add=TRUE → ignore Ctrl+C / close-console events.
	_, _, _ = procSetConsoleCtrlHandler.Call(0, 1)
	_, _, _ = procFreeConsole.Call()
}
