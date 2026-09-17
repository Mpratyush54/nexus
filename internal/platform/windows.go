//go:build windows

package platform

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func init() { registerBackend("windows", func() Service { return windowsService{} }) }

// windowsService manages the daemon via the Task Scheduler (schtasks): an
// ONLOGON task named SchtasksName. schtasks ships with Windows, so this
// backend needs no third-party service wrapper. Stdlib only (os/exec).
type windowsService struct{}

// taskName allows tests to avoid touching the real scheduler.
var schtasksTaskName = SchtasksName

func (windowsService) Install(executable string, args []string) error {
	if strings.TrimSpace(executable) == "" {
		var err error
		executable, err = defaultExecutable()
		if err != nil {
			return err
		}
	}
	argv := RenderSchtasksCreateArgs(schtasksTaskName, executable, args)
	if out, err := exec.Command("schtasks", argv...).CombinedOutput(); err != nil {
		return fmt.Errorf("platform: schtasks create: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (windowsService) Uninstall() error {
	out, err := exec.Command("schtasks", "/Delete", "/TN", schtasksTaskName, "/F").CombinedOutput()
	if err != nil {
		// Deleting a missing task is already-uninstalled, not a failure.
		if strings.Contains(strings.ToLower(string(out)), "cannot find") ||
			strings.Contains(strings.ToLower(string(out)), "does not exist") {
			return nil
		}
		return fmt.Errorf("platform: schtasks delete: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (windowsService) Status() (ServiceStatus, error) {
	out, err := exec.Command("schtasks", "/Query", "/TN", schtasksTaskName, "/FO", "LIST").CombinedOutput()
	text := strings.ToLower(string(out))
	switch {
	case err != nil && (strings.Contains(text, "cannot find") || strings.Contains(text, "does not exist")):
		return StatusNotInstalled, nil
	case err != nil:
		return StatusUnknown, fmt.Errorf("platform: schtasks query: %w: %s", err, strings.TrimSpace(string(out)))
	case strings.Contains(text, "running"):
		return StatusRunning, nil
	default:
		return StatusStopped, nil
	}
}

// windowsAppDataDir resolves %APPDATA%-adjacent config base without
// hardcoding drive letters: APPDATA wins, else os.UserConfigDir, else
// %USERPROFILE%\AppData\Roaming as a last resort.
func windowsAppDataDir() (string, error) {
	if appData := strings.TrimSpace(os.Getenv("APPDATA")); appData != "" {
		return appData, nil
	}
	if base, err := userConfigDirFunc(); err == nil && strings.TrimSpace(base) != "" {
		return base, nil
	}
	if profile := strings.TrimSpace(os.Getenv("USERPROFILE")); profile != "" {
		return filepath.Join(profile, "AppData", "Roaming"), nil
	}
	return "", fmt.Errorf("platform: windows config base: APPDATA and USERPROFILE unset")
}
