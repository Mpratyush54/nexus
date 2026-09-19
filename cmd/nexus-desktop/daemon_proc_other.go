//go:build !windows

package main

import "os/exec"

func configureDaemonCmd(cmd *exec.Cmd) {}
