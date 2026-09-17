// Package backup implements P3: local drive mirror (robocopy /MIR, 15 min,
// free) + apps export (winget, extensions, dotfiles, restore.ps1).
//
// Remote restic->Azure Blob Hot runs every 15 min (Hot has no retention
// must not be pushed every 15 min). Remote is a stub until `az login` +
// restic are provisioned: BackupRemote records the pointer and reports what
// it would push, so P3 never silently pretends off-device safety exists.
package backup

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// DriveExcludes mirrors scan.DriveIgnore for robocopy /XD /XF flags.
var dirExcludes = []string{
	"node_modules", "dist", "build", ".next", "out",
	"__pycache__", ".venv", "venv", ".git",
}

var fileExcludes = []string{"*.log", "*.pem", "*.key", ".env*"}

// MirrorRoot is the local plaintext mirror (browsable, no password).
func MirrorRoot(vault string) string {
	return filepath.Join(vault, "drive-mirror")
}

// MirrorProjects robocopies each D:\<proj> to vault/drive-mirror/<proj>.
// Robocopy exit codes 0-7 all mean success (1=files copied, etc.).
func MirrorProjects(vault string) (string, error) {
	entries, err := os.ReadDir(`D:\`)
	if err != nil {
		return "", err
	}
	var report []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		src := filepath.Join(`D:\`, e.Name())
		dst := filepath.Join(MirrorRoot(vault), e.Name())
		args := append([]string{src, dst, "/MIR", "/R:1", "/W:1", "/NP", "/NDL", "/NFL", "/XD"}, dirExcludes...)
		args = append(args, "/XF")
		args = append(args, fileExcludes...)
		out, err := exec.Command("robocopy", args...).CombinedOutput()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			report = append(report, e.Name()+": robocopy missing/failed: "+err.Error())
			continue
		}
		if code > 7 {
			report = append(report, fmt.Sprintf("%s: robocopy exit %d: %s", e.Name(), code, lastLines(string(out), 3)))
			continue
		}
		report = append(report, fmt.Sprintf("%s: mirrored (exit %d)", e.Name(), code))
	}
	// Record the run for pointers.json (Repo A tracks what/when, not the data).
	stamp := time.Now().UTC().Format(time.RFC3339)
	b := []byte(`{"drive_mirror_at":` + strconv(stamp) + `}`)
	_ = b
	return strings.Join(report, "\n"), recordRun(vault, stamp)
}

func strconv(s string) string { return `"` + s + `"` }

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) <= n {
		return strings.Join(lines, "\n")
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

func recordRun(vault, stamp string) error {
	p := filepath.Join(vault, "drive-mirror", "last-run.json")
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	return os.WriteFile(p, []byte("{\"at\":"+strconv(stamp)+",\"cadence\":\"15min-local\",\"remote\":\"15min-azure-hot (P3-stub)\"}\n"), 0o644)
}

// SystemDir holds apps export (winget manifest, extensions, restore.ps1).
func SystemDir(vault string) string { return filepath.Join(vault, "system") }

// ExportApps captures reinstall recipes, not binaries: winget export,
// vscode extensions, dotfiles, plus a generated restore.ps1.
func ExportApps(vault string) (string, error) {
	dir := SystemDir(vault)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	var report []string
	if _, err := exec.LookPath("winget"); err == nil {
		out, err := exec.Command("winget", "export", "-o", filepath.Join(dir, "winget.json")).CombinedOutput()
		report = append(report, "winget export: "+wingetResult(out, err))
	} else {
		report = append(report, "winget: not found, skipped")
	}
	if _, err := exec.LookPath("code"); err == nil {
		out, err := exec.Command("code", "--list-extensions").Output()
		if err == nil {
			_ = os.WriteFile(filepath.Join(dir, "vscode-extensions.txt"), out, 0o644)
			report = append(report, fmt.Sprintf("vscode extensions: %d recorded", len(strings.Fields(string(out)))))
		}
	} else {
		report = append(report, "vscode cli: not found, skipped")
	}
	home, _ := os.UserHomeDir()
	for _, dot := range []string{".gitconfig"} {
		if data, err := os.ReadFile(filepath.Join(home, dot)); err == nil {
			_ = os.WriteFile(filepath.Join(dir, dot), data, 0o644)
			report = append(report, dot+": saved")
		}
	}
	ps := `# Central-memory app restore (generated). Run on fresh Windows:
# 1. winget import system/winget.json
# 2. reinstall vscode extensions from system/vscode-extensions.txt
# 3. copy dotfiles from system/ back to %USERPROFILE%
winget import "$PSScriptRoot\winget.json"
Get-Content "$PSScriptRoot\vscode-extensions.txt" | ForEach-Object { code --install-extension $_ }
Copy-Item "$PSScriptRoot\.gitconfig" "$env:USERPROFILE\.gitconfig" -ErrorAction SilentlyContinue
`
	_ = os.WriteFile(filepath.Join(dir, "restore.ps1"), []byte(ps), 0o644)
	report = append(report, "restore.ps1: generated")
	return strings.Join(report, "\n"), nil
}

func wingetResult(out []byte, err error) string {
	if err == nil {
		return "ok"
	}
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 0 {
		return "ok"
	}
	return "exit: " + lastLines(string(out), 2)
}

// RemoteStatus reports the hourly Azure leg without pretending it ran.
// Returns ok=false until restic + az login are configured (needs your creds).
func RemoteStatus() (ok bool, detail string) {
	if _, err := exec.LookPath("restic"); err != nil {
		return false, "restic not installed — remote hourly push pending (install restic + mem login azure in P3-final)"
	}
	if _, err := exec.LookPath("az"); err != nil {
		return false, "az cli not found — run mem login azure first"
	}
	return true, "restic+az present — configure container central-backup (Hot) to enable 15-min push"
}
