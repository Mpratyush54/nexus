//go:build !windows

package continuex

import "os/exec"

func hideConsole(_ *exec.Cmd) {}

func hideCursorToolConsole(_ *exec.Cmd) {}
