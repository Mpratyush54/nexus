//go:build windows

package platform

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// commandTimeout bounds schtasks invocations so Install/Uninstall/Status
// never hang the CLI.
const commandTimeout = 30 * time.Second

type windowsManager struct{}

// currentManager returns the Windows Task Scheduler manager.
func currentManager() Manager { return windowsManager{} }

// Install creates (or replaces) a logon-triggered Task Scheduler task that
// auto-starts the daemon. It uses schtasks /SC ONLOGON so no service
// control manager rights or third-party dependencies are required.
func (windowsManager) Install(exePath string, args []string) error {
	exe, err := resolveExe(exePath)
	if err != nil {
		return err
	}
	if args == nil {
		args = DefaultDaemonArgs()
	}
	command := WindowsTaskCommand(exe, args)
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "schtasks", SchtasksCreateArgs(WindowsTaskName(), command)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("platform: schtasks create task %q: %w (output: %s)", WindowsTaskName(), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Uninstall deletes the Task Scheduler task. A missing task is success
// (idempotent) so `nexus daemon uninstall` is safe to repeat.
func (windowsManager) Uninstall() error {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "schtasks", SchtasksDeleteArgs(WindowsTaskName())...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if st, ok := ParseSchtasksStatus(string(out)); ok && st == StatusNotInstalled {
			return nil
		}
		// schtasks reports ERROR: The system cannot find the file specified.
		// for a missing task; treat that as success as well.
		if strings.Contains(strings.ToLower(string(out)), "cannot find") {
			return nil
		}
		return fmt.Errorf("platform: schtasks delete task %q: %w (output: %s)", WindowsTaskName(), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Status queries the Task Scheduler task without mutating anything.
func (windowsManager) Status() (Status, error) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "schtasks", SchtasksQueryArgs(WindowsTaskName())...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if st, ok := ParseSchtasksStatus(string(out)); ok {
			return st, nil
		}
		return StatusUnknown, fmt.Errorf("platform: schtasks query task %q: %w (output: %s)", WindowsTaskName(), err, strings.TrimSpace(string(out)))
	}
	if st, ok := ParseSchtasksStatus(string(out)); ok {
		return st, nil
	}
	return StatusUnknown, nil
}
