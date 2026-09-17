//go:build darwin

package platform

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func init() { registerBackend("darwin", func() Service { return darwinService{} }) }

// darwinService manages the daemon via launchd: a plist at
// ~/Library/LaunchAgents/<label>.plist written by RenderLaunchdPlist and
// loaded with launchctl bootstrap/bootout. Stdlib only (os/exec).
type darwinService struct {
	// label and agentsDir are overridable for tests.
	label     string
	agentsDir string
}

func (s darwinService) effectiveLabel() string {
	if strings.TrimSpace(s.label) != "" {
		return s.label
	}
	return LaunchdLabel
}

func (s darwinService) plistPath() (string, error) {
	dir := s.agentsDir
	if strings.TrimSpace(dir) == "" {
		home, err := userHomeDirFunc()
		if err != nil {
			return "", fmt.Errorf("platform: launchd agents dir: %w", err)
		}
		if strings.TrimSpace(home) == "" {
			return "", fmt.Errorf("platform: launchd agents dir: empty home")
		}
		dir = filepath.Join(home, "Library", "LaunchAgents")
	}
	return filepath.Join(dir, s.effectiveLabel()+".plist"), nil
}

func (s darwinService) Install(executable string, args []string) error {
	if strings.TrimSpace(executable) == "" {
		var err error
		executable, err = defaultExecutable()
		if err != nil {
			return err
		}
	}
	path, err := s.plistPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("platform: create LaunchAgents dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(RenderLaunchdPlist(s.effectiveLabel(), executable, args)), 0o644); err != nil {
		return fmt.Errorf("platform: write launchd plist: %w", err)
	}
	// Best effort: unload a stale job before (re)loading.
	_ = exec.Command("launchctl", "bootout", "gui/"+uid(), path).Run()
	if out, err := exec.Command("launchctl", "bootstrap", "gui/"+uid(), path).CombinedOutput(); err != nil {
		return fmt.Errorf("platform: launchctl bootstrap: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (s darwinService) Uninstall() error {
	path, err := s.plistPath()
	if err != nil {
		return err
	}
	// Idempotent: no plist means already uninstalled. Otherwise a bootout
	// failure must surface — silently dropping it leaves the daemon running
	// while reporting success.
	if _, serr := os.Stat(path); serr != nil {
		if os.IsNotExist(serr) {
			return nil
		}
		return fmt.Errorf("platform: stat launchd plist: %w", serr)
	}
	bootOut, bootErr := exec.Command("launchctl", "bootout", "gui/"+uid(), path).CombinedOutput()
	removeErr := error(nil)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		removeErr = fmt.Errorf("platform: remove launchd plist: %w", err)
	}
	switch {
	case bootErr != nil && removeErr != nil:
		return fmt.Errorf("platform: launchctl bootout: %w: %s; %v", bootErr, strings.TrimSpace(string(bootOut)), removeErr)
	case bootErr != nil:
		return fmt.Errorf("platform: launchctl bootout: %w: %s", bootErr, strings.TrimSpace(string(bootOut)))
	default:
		return removeErr
	}
}

func (s darwinService) Status() (ServiceStatus, error) {
	path, err := s.plistPath()
	if err != nil {
		return StatusUnknown, err
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return StatusNotInstalled, nil
		}
		return StatusUnknown, fmt.Errorf("platform: stat launchd plist: %w", err)
	}
	out, err := exec.Command("launchctl", "print", "gui/"+uid()+"/"+s.effectiveLabel()).CombinedOutput()
	if err != nil {
		// Plist on disk but job not loaded => stopped.
		return StatusStopped, nil
	}
	if strings.Contains(strings.ToLower(string(out)), "state = running") {
		return StatusRunning, nil
	}
	return StatusStopped, nil
}

func uid() string {
	out, err := exec.Command("id", "-u").Output()
	if err != nil {
		return "501"
	}
	return strings.TrimSpace(string(out))
}
