//go:build !windows

package hiddenproc

import "os/exec"

// Configure is a no-op where background commands do not create Windows consoles.
func Configure(cmd *exec.Cmd) {}
