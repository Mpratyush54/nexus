//go:build linux

package platform

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func init() { registerBackend("linux", func() Service { return linuxService{} }) }

// linuxService manages the daemon via a systemd --user unit at
// ~/.config/systemd/user/nexus-daemon.service written by RenderSystemdUnit
// and enabled with systemctl --user enable --now. Stdlib only (os/exec).
type linuxService struct {
	// unitName and unitDir are overridable for tests.
	unitName string
	unitDir  string
}

func (s linuxService) effectiveUnit() string {
	if strings.TrimSpace(s.unitName) != "" {
		return s.unitName
	}
	return SystemdUnitName
}

func (s linuxService) unitPath() (string, error) {
	dir := s.unitDir
	if strings.TrimSpace(dir) == "" {
		base, err := userConfigDirFunc()
		if err != nil {
			return "", fmt.Errorf("platform: systemd unit dir: %w", err)
		}
		if strings.TrimSpace(base) == "" {
			return "", fmt.Errorf("platform: systemd unit dir: empty config base")
		}
		dir = filepath.Join(base, "systemd", "user")
	}
	return filepath.Join(dir, s.effectiveUnit()), nil
}

func (s linuxService) Install(executable string, args []string) error {
	if strings.TrimSpace(executable) == "" {
		var err error
		executable, err = defaultExecutable()
		if err != nil {
			return err
		}
	}
	path, err := s.unitPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("platform: create systemd user dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(RenderSystemdUnit("Nexus workspace daemon", executable, args)), 0o644); err != nil {
		return fmt.Errorf("platform: write systemd unit: %w", err)
	}
	if out, err := exec.Command("systemctl", "--user", "daemon-reload").CombinedOutput(); err != nil {
		return fmt.Errorf("platform: systemctl daemon-reload: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command("systemctl", "--user", "enable", "--now", s.effectiveUnit()).CombinedOutput(); err != nil {
		return fmt.Errorf("platform: systemctl enable: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (s linuxService) Uninstall() error {
	path, err := s.unitPath()
	if err != nil {
		return err
	}
	_ = exec.Command("systemctl", "--user", "disable", "--now", s.effectiveUnit()).Run()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("platform: remove systemd unit: %w", err)
	}
	_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	return nil
}

func (s linuxService) Status() (ServiceStatus, error) {
	path, err := s.unitPath()
	if err != nil {
		return StatusUnknown, err
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return StatusNotInstalled, nil
		}
		return StatusUnknown, fmt.Errorf("platform: stat systemd unit: %w", err)
	}
	out, err := exec.Command("systemctl", "--user", "is-active", s.effectiveUnit()).CombinedOutput()
	if strings.TrimSpace(strings.ToLower(string(out))) == "active" && err == nil {
		return StatusRunning, nil
	}
	return StatusStopped, nil
}
