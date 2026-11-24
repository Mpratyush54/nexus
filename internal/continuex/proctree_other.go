//go:build !windows

package continuex

import "os/exec"

// On Unix the agents are killed with their own process; helper processes
// inherit the closed stdin and exit. Process-group cleanup can be added here.
func containTree(_ *exec.Cmd) {}

func killTree(_ *exec.Cmd) {}
