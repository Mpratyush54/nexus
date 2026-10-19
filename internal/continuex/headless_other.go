//go:build !windows

package continuex

import "os/exec"

func hideConsole(_ *exec.Cmd) {}
