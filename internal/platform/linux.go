//go:build linux

package platform

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// commandTimeout bounds systemctl invocations so Install/Uninstall/Status
// never hang the CLI.
const commandTimeout = 30 * time.Second

type linuxManager struct{}

// currentManager returns the systemd user manager.
func currentManager() Manager { return linuxManager{} }

// UnitPath returns ~/.config/systemd/user/nexus.service. It creates
// nothing; Install creates the parent directory as needed.
func UnitPath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("platform: user config dir: %w", err)
	}
	if strings.TrimSpace(base) == "" {
		return "", fmt.Errorf("platform: user config dir is empty")
	}
	return filepath.Join(base, "systemd", "user", SystemdUnitName()), nil
}

func runSystemctl(ctx context.Context, args ...string) ([]byte, error) {
	full := append([]string{"--user"}, args...)
	cmd := exec.CommandContext(ctx, "systemctl", full...)
	return cmd.CombinedOutput()
}

// Install writes the systemd user unit and enables + starts it so the
// daemon auto-starts at login (WantedBy=default.target via enable --now,
// which implies daemon-reload first).
func (linuxManager) Install(exePath string, args []string) error {
	exe, err := resolveExe(exePath)
	if err != nil {
		return err
	}
	if args == nil {
		args = DefaultDaemonArgs()
	}
	path, err := UnitPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("platform: create systemd user dir: %w", err)
	}
	unit := SystemdUnit("nexus workspace daemon", exe, args)
	if err := os.WriteFile(path, []byte(unit), 0o644); err != nil {
		return fmt.Errorf("platform: write unit: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	if out, err := runSystemctl(ctx, "daemon-reload"); err != nil {
		return fmt.Errorf("platform: systemctl daemon-reload: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	if out, err := runSystemctl(ctx, "enable", "--now", SystemdUnitName()); err != nil {
		return fmt.Errorf("platform: systemctl enable --now %s: %w (output: %s)", SystemdUnitName(), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Uninstall disables + stops the unit and removes the unit file. A missing
// unit is success (idempotent).
func (linuxManager) Uninstall() error {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	// Best-effort: ignore errors when the unit was never installed.
	_, _ = runSystemctl(ctx, "disable", "--now", SystemdUnitName())
	if path, err := UnitPath(); err == nil {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("platform: remove unit: %w", err)
		}
		_, _ = runSystemctl(ctx, "daemon-reload")
	}
	return nil
}

// Status inspects the systemd user unit without mutating anything: a
// missing unit file means not-installed; otherwise `is-active` decides
// between running and stopped.
func (linuxManager) Status() (Status, error) {
	path, err := UnitPath()
	if err != nil {
		return StatusUnknown, err
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return StatusNotInstalled, nil
		}
		return StatusUnknown, fmt.Errorf("platform: stat unit: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	out, err := runSystemctl(ctx, "is-active", SystemdUnitName())
	state := strings.TrimSpace(strings.ToLower(string(out)))
	if err == nil && state == "active" {
		return StatusRunning, nil
	}
	switch state {
	case "inactive", "failed", "unknown", "activating":
		return StatusStopped, nil
	}
	return StatusStopped, nil
}
