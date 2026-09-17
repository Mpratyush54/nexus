// Package install makes mem installable on Windows: copies the binary to
// %LOCALAPPDATA%\central-memory, adds it to the user PATH, registers the
// two Task Scheduler jobs (15-min local backup, hourly sync+remote), and
// drops a Start Menu shortcut that opens the dashboard. Everything supports
// --dry-run preview. Uninstall reverses it (vault data is never touched).
package install

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"central-memory/internal/deps"
)

// Dir is %LOCALAPPDATA%\central-memory.
func Dir() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, "AppData", "Local")
	}
	return filepath.Join(base, "central-memory")
}

func exePath() string { return filepath.Join(Dir(), "mem.exe") }

// Install performs (or previews with dryRun) the setup. With withDeps it
// also installs missing external tools (az, restic) via winget first, so a
// fresh machine ends fully provisioned from one command.
func Install(vault string, dryRun, withDeps bool) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if withDeps {
		fmt.Println("== dependencies ==")
		for _, line := range deps.Ensure(dryRun) {
			fmt.Println(" ", line)
		}
	}
	steps := []struct {
		name string
		run  func() error
	}{
		{"copy binary to " + exePath(), func() error {
			if err := os.MkdirAll(Dir(), 0o755); err != nil {
				return err
			}
			data, err := os.ReadFile(self)
			if err != nil {
				return err
			}
			return os.WriteFile(exePath(), data, 0o755)
		}},
		{"add install dir to user PATH", addToPath},
		{"schedule CentralMemory15 (backup, every 15 min)", func() error {
			// Remote push no-ops honestly until `mem login azure` configures it,
			// so bundling it here is safe from day one (Hot tier: no penalty).
			return schtasks("/create", "/tn", "CentralMemory15", "/f",
				"/sc", "minute", "/mo", "15",
				"/tr", fmt.Sprintf(`"%s" backup --projects`, exePath()))
		}},
		{"schedule CentralMemoryRemote15 (remote push, every 15 min)", func() error {
			return schtasks("/create", "/tn", "CentralMemoryRemote15", "/f",
				"/sc", "minute", "/mo", "15",
				"/tr", fmt.Sprintf(`"%s" remote push`, exePath()))
		}},
		{"schedule CentralMemoryHourly (sync, hourly)", func() error {
			return schtasks("/create", "/tn", "CentralMemoryHourly", "/f",
				"/sc", "hourly", "/mo", "1",
				"/tr", fmt.Sprintf(`"%s" sync`, exePath()))
		}},
		{"start-menu shortcut (dashboard)", shortcut},
	}
	_ = vault
	for _, s := range steps {
		fmt.Printf("[%s] %s\n", map[bool]string{true: "preview", false: "run"}[dryRun], s.name)
		if dryRun {
			continue
		}
		if err := s.run(); err != nil {
			return fmt.Errorf("%s: %w", s.name, err)
		}
	}
	if dryRun {
		fmt.Println("dry-run only — nothing changed. Re-run without --dry-run to install.")
		return nil
	}
	fmt.Println("installed. Open a NEW terminal, then: mem status / mem ui")
	return nil
}

// Uninstall removes tasks, shortcut, and binary. Vault is preserved.
func Uninstall(dryRun bool) error {
	for _, tn := range []string{"CentralMemory15", "CentralMemoryRemote15", "CentralMemoryHourly"} {
		fmt.Printf("[%s] delete task %s\n", mode(dryRun), tn)
		if !dryRun {
			_ = schtasks("/delete", "/tn", tn, "/f")
		}
	}
	fmt.Printf("[%s] remove %s\n", mode(dryRun), Dir())
	if !dryRun {
		_ = os.RemoveAll(Dir())
	}
	fmt.Println("uninstalled (vault data kept). Remove the PATH entry manually if desired: " + Dir())
	return nil
}

func mode(dry bool) string {
	if dry {
		return "preview"
	}
	return "run"
}

func schtasks(args ...string) error {
	out, err := exec.Command("schtasks", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func addToPath() error {
	dir := Dir()
	cur := os.Getenv("PATH")
	for _, p := range strings.Split(cur, ";") {
		if strings.EqualFold(strings.TrimRight(p, `\`), strings.TrimRight(dir, `\`)) {
			return nil
		}
	}
	// setx truncates at 1024 chars — check first.
	if len(cur+";"+dir) > 1000 {
		return fmt.Errorf("PATH too long for setx; add %s manually", dir)
	}
	out, err := exec.Command("setx", "PATH", cur+";"+dir).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func shortcut() error {
	start := filepath.Join(os.Getenv("APPDATA"), `Microsoft\Windows\Start Menu\Programs\Central Memory`)
	if err := os.MkdirAll(start, 0o755); err != nil {
		return err
	}
	lnk := filepath.Join(start, "Central Memory Dashboard.lnk")
	ps := fmt.Sprintf(`$s=(New-Object -ComObject WScript.Shell).CreateShortcut(%q);$s.TargetPath=%q;$s.Arguments="ui";$s.Save()`,
		lnk, exePath())
	out, err := exec.Command("powershell", "-NoProfile", "-Command", ps).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
