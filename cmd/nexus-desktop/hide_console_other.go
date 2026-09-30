//go:build !windows

package main

import "os/exec"

func hideConsoleCmd(cmd *exec.Cmd) {}
