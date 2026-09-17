//go:build darwin

package platform

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// commandTimeout bounds launchctl invocations so Install/Uninstall/Status
// never hang the CLI.
const commandTimeout = 30 * time.Second

type darwinManager struct{}

// currentManager returns the launchd manager.
func currentManager() Manager { return darwinManager{} }

// PlistPath returns ~/Library/LaunchAgents/<label>.plist. It creates
// nothing; Install creates the parent directory as needed.
func PlistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("platform: home dir: %w", err)
	}
	if strings.TrimSpace(home) == "" {
		return "", fmt.Errorf("platform: home dir is empty")
	}
	return filepath.Join(home, "Library", "LaunchAgents", LaunchdLabel()+".plist"), nil
}

// currentUID returns the numeric uid for launchctl bootstrap/bootout
// targets (gui/<uid>/<label>).
func currentUID() string { return strconv.Itoa(os.Getuid()) }

func runLaunchctl(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "launchctl", args...)
	return cmd.CombinedOutput()
}

// Install writes the launchd agent plist and bootstraps it in the current
// GUI session so the daemon auto-starts at login (RunAtLoad) and is kept
// alive (KeepAlive). Bootstrapping an already-bootstrapped label is
// tolerated by booting out first (errors ignored).
func (darwinManager) Install(exePath string, args []string) error {
	exe, err := resolveExe(exePath)
	if err != nil {
		return err
	}
	if args == nil {
		args = DefaultDaemonArgs()
	}
	path, err := PlistPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("platform: create LaunchAgents dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(LaunchdPlist(LaunchdLabel(), exe, args)), 0o644); err != nil {
		return fmt.Errorf("platform: write plist: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	target := "gui/" + currentUID() + "/" + LaunchdLabel()
	// Best-effort bootout so re-install replaces the old definition.
	_, _ = runLaunchctl(ctx, "bootout", "gui/"+currentUID(), path)
	if out, err := runLaunchctl(ctx, "bootstrap", "gui/"+currentUID(), path); err != nil {
		return fmt.Errorf("platform: launchctl bootstrap %s: %w (output: %s)", target, err, strings.TrimSpace(string(out)))
	}
	if out, err := runLaunchctl(ctx, "enable", target); err != nil {
		return fmt.Errorf("platform: launchctl enable %s: %w (output: %s)", target, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Uninstall boots the agent out and removes the plist. A missing plist is
// success (idempotent).
func (darwinManager) Uninstall() error {
	path, err := PlistPath()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	_, _ = runLaunchctl(ctx, "bootout", "gui/"+currentUID()+"/"+LaunchdLabel())
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("platform: remove plist: %w", err)
	}
	return nil
}

// Status inspects the launchd job without mutating anything: a missing
// plist means not-installed; otherwise `launchctl print` reveals whether
// the job is running.
func (darwinManager) Status() (Status, error) {
	path, err := PlistPath()
	if err != nil {
		return StatusUnknown, err
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return StatusNotInstalled, nil
		}
		return StatusUnknown, fmt.Errorf("platform: stat plist: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	out, err := runLaunchctl(ctx, "print", "gui/"+currentUID()+"/"+LaunchdLabel())
	if err != nil {
		// Plist exists but job is not loaded/started.
		return StatusStopped, nil
	}
	if strings.Contains(strings.ToLower(string(out)), "state = running") {
		return StatusRunning, nil
	}
	return StatusStopped, nil
}
