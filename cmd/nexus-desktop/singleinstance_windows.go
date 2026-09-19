//go:build windows

package main

import (
	"golang.org/x/sys/windows"
)

const singleInstanceMutex = "Local\\NexusDesktopSingleInstance"

// acquireSingleInstance ensures only one Nexus Desktop process runs.
// On a second launch it returns ok=false (caller should exit quietly;
// the existing tray instance stays in the notification area).
func acquireSingleInstance() (release func(), ok bool) {
	name, err := windows.UTF16PtrFromString(singleInstanceMutex)
	if err != nil {
		return func() {}, true
	}
	handle, err := windows.CreateMutex(nil, false, name)
	if handle == 0 {
		// Fail open so a mutex issue never bricks the tray.
		return func() {}, true
	}
	if err == windows.ERROR_ALREADY_EXISTS {
		_ = windows.CloseHandle(handle)
		return nil, false
	}
	return func() { _ = windows.CloseHandle(handle) }, true
}
