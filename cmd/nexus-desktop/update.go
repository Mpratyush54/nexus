package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"central-memory/internal/buildinfo"
)

// Stable desktop feed (S3). Platform API may 404 until an admin registers a
// release — S3 is the source of truth published by release-desktop.yml tags.
const desktopLatestURL = "https://central-memory-releases.s3.ap-south-1.amazonaws.com/desktop/latest.json"

type desktopManifest struct {
	App        string `json:"app"`
	Version    string `json:"version"`
	Channel    string `json:"channel"`
	Commit     string `json:"commit"`
	InstallURL string `json:"install_url"`
	Artifacts  []struct {
		Name     string `json:"name"`
		Filename string `json:"filename"`
		URL      string `json:"url"`
	} `json:"artifacts"`
}

// updatePhase is shown in the tray version row.
type updatePhase string

const (
	updateIdle       updatePhase = "idle"
	updateChecking   updatePhase = "checking"
	updateAvailable  updatePhase = "available"
	updateDownloading updatePhase = "downloading"
	updateApplying   updatePhase = "applying"
	updateRestarting updatePhase = "restarting"
	updateOK         updatePhase = "ok"
	updateFailed     updatePhase = "failed"
)

type updateStatus struct {
	mu      sync.Mutex
	phase   updatePhase
	detail  string
	remote  string
	errMsg  string
}

var globalUpdate = &updateStatus{phase: updateIdle}

func (s *updateStatus) set(phase updatePhase, detail string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.phase = phase
	s.detail = detail
	if phase != updateFailed {
		s.errMsg = ""
	}
}

func (s *updateStatus) fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.phase = updateFailed
	s.errMsg = err.Error()
	s.detail = "Update failed"
}

func (s *updateStatus) snapshot() (phase updatePhase, detail, remote, errMsg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.phase, s.detail, s.remote, s.errMsg
}

func (s *updateStatus) setRemote(v string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.remote = strings.TrimSpace(v)
}

func versionLabel() string {
	v := strings.TrimPrefix(strings.TrimSpace(buildinfo.Version), "v")
	if v == "" || v == "dev" {
		v = "dev"
	}
	phase, detail, remote, errMsg := globalUpdate.snapshot()
	switch phase {
	case updateChecking:
		return "Version: " + v + " · checking…"
	case updateAvailable:
		if remote != "" {
			return "Version: " + v + " · update " + remote + " available"
		}
		return "Version: " + v + " · update available"
	case updateDownloading:
		return "Version: " + v + " · downloading " + remote + "…"
	case updateApplying:
		return "Version: " + v + " · applying update…"
	case updateRestarting:
		return "Version: " + v + " · restart required"
	case updateOK:
		if detail != "" {
			return "Version: " + v + " · " + detail
		}
		return "Version: " + v + " · up to date"
	case updateFailed:
		msg := errMsg
		if len(msg) > 48 {
			msg = msg[:45] + "…"
		}
		return "Version: " + v + " · update failed: " + msg
	default:
		return "Version: " + v
	}
}

func fetchDesktopManifest(client *http.Client) (*desktopManifest, error) {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	// S3 first — reliable feed from tagged desktop releases.
	m, err := fetchManifestURL(client, desktopLatestURL)
	if err == nil && m != nil && strings.TrimSpace(m.Version) != "" {
		return m, nil
	}
	s3Err := err
	apiBase := strings.TrimRight(firstNonEmpty(os.Getenv("NEXUS_SERVER"), defaultServerURL), "/")
	apiURL := apiBase + "/platform/releases/latest?app=desktop&channel=stable"
	if m, err := fetchManifestURL(client, apiURL); err == nil && m != nil && strings.TrimSpace(m.Version) != "" {
		return m, nil
	}
	if s3Err != nil {
		return nil, s3Err
	}
	return nil, fmt.Errorf("no desktop release manifest")
}

func fetchManifestURL(client *http.Client, rawURL string) (*desktopManifest, error) {
	resp, err := client.Get(rawURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("manifest %s: %s", rawURL, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var m desktopManifest
	if err := json.Unmarshal(body, &m); err == nil && m.Version != "" {
		if len(m.Artifacts) == 0 {
			// tolerate platforms-shaped S3 docs that still have artifacts[]
		}
		return &m, nil
	}
	var api struct {
		Version string `json:"version"`
		GitSHA  string `json:"git_sha"`
		Artifacts []struct {
			OS       string `json:"os"`
			Arch     string `json:"arch"`
			URL      string `json:"url"`
			Filename string `json:"filename"`
		} `json:"artifacts"`
	}
	if err := json.Unmarshal(body, &api); err != nil {
		return nil, err
	}
	m.Version = api.Version
	m.Commit = api.GitSHA
	m.App = buildinfo.AppDesktop
	for _, a := range api.Artifacts {
		if !strings.EqualFold(a.OS, "windows") || !strings.EqualFold(a.Arch, "amd64") {
			continue
		}
		name := "nexus-desktop"
		low := strings.ToLower(a.Filename + a.URL)
		switch {
		case strings.Contains(low, "daemon"):
			name = "nexus-daemon"
		case strings.Contains(low, "desktop"):
			name = "nexus-desktop"
		case strings.Contains(low, "nexus-windows") || strings.HasSuffix(low, "nexus.exe"):
			name = "nexus"
		}
		m.Artifacts = append(m.Artifacts, struct {
			Name     string `json:"name"`
			Filename string `json:"filename"`
			URL      string `json:"url"`
		}{Name: name, Filename: a.Filename, URL: a.URL})
	}
	return &m, nil
}

func normalizeVersion(v string) string {
	return strings.TrimPrefix(strings.TrimSpace(v), "v")
}

func isCIOrDevVersion(v string) bool {
	v = normalizeVersion(v)
	if v == "" || v == "dev" || v == "unknown" {
		return true
	}
	return strings.HasPrefix(v, "0.0.0-")
}

// versionNewer reports whether remote should replace local.
// CI/SHA builds (0.0.0-*) never count as upgrades over a real semver release.
func versionNewer(remote, local string) bool {
	r := normalizeVersion(remote)
	l := normalizeVersion(local)
	if r == "" || r == l {
		return false
	}
	if isCIOrDevVersion(r) && !isCIOrDevVersion(l) {
		return false
	}
	if isCIOrDevVersion(l) && !isCIOrDevVersion(r) {
		return true
	}
	if isCIOrDevVersion(r) && isCIOrDevVersion(l) {
		return r != l
	}
	cmp, ok := compareSemver(r, l)
	if ok {
		return cmp > 0
	}
	return r != l
}

func compareSemver(a, b string) (int, bool) {
	ap := semverParts(a)
	bp := semverParts(b)
	if ap == nil || bp == nil {
		return 0, false
	}
	for i := 0; i < 3; i++ {
		if ap[i] != bp[i] {
			if ap[i] > bp[i] {
				return 1, true
			}
			return -1, true
		}
	}
	return 0, true
}

func semverParts(v string) []int {
	v = normalizeVersion(v)
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) < 2 {
		return nil
	}
	out := make([]int, 3)
	for i := 0; i < 3 && i < len(parts); i++ {
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			return nil
		}
		out[i] = n
	}
	return out
}

// applyDesktopUpdate downloads artifacts, stops locked binaries, then schedules
// a replace-after-exit script so the running tray binary can be overwritten.
func applyDesktopUpdate(m *desktopManifest, daemonProc **os.Process, mu *sync.Mutex) error {
	if runtime.GOOS != "windows" {
		return fmt.Errorf("auto-update is Windows-only for now")
	}
	binDir := filepath.Join(os.Getenv("LOCALAPPDATA"), "Nexus", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}
	staging := filepath.Join(os.Getenv("LOCALAPPDATA"), "Nexus", "updates", m.Version)
	_ = os.RemoveAll(staging)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return err
	}
	client := &http.Client{Timeout: 3 * time.Minute}
	copied := 0
	var staged []string
	for _, art := range m.Artifacts {
		if art.URL == "" {
			continue
		}
		destName := art.Filename
		switch art.Name {
		case "nexus-desktop":
			destName = "nexus-desktop.exe"
		case "nexus-daemon":
			destName = "nexus-daemon.exe"
		case "nexus":
			destName = "nexus.exe"
		}
		if !strings.HasSuffix(strings.ToLower(destName), ".exe") {
			destName += ".exe"
		}
		stagePath := filepath.Join(staging, destName)
		if err := downloadFile(client, art.URL, stagePath); err != nil {
			return fmt.Errorf("%s: %w", art.Name, err)
		}
		fi, err := os.Stat(stagePath)
		if err != nil || fi.Size() < 1024 {
			return fmt.Errorf("%s: staged file too small or missing", art.Name)
		}
		staged = append(staged, destName)
		copied++
	}
	if copied == 0 {
		return fmt.Errorf("manifest has no downloadable artifacts")
	}

	// Unlock binaries before the replace script runs. nexus-daemon.exe is
	// locked while running — copy would silently leave the old daemon in place.
	stopOwnedDaemon(mu, daemonProc)
	if err := stopNexusProcesses(false); err != nil {
		return fmt.Errorf("could not stop nexus-daemon for update: %w", err)
	}

	selfPID := os.Getpid()
	script := filepath.Join(staging, "apply-update.ps1")
	body := buildApplyUpdateScript(staging, binDir, staged, selfPID, m.Version)
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		return err
	}
	cmd := exec.Command("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-WindowStyle", "Hidden", "-File", script)
	cmd.Dir = staging
	if err := cmd.Start(); err != nil {
		return err
	}
	return nil
}

func buildApplyUpdateScript(staging, binDir string, staged []string, selfPID int, version string) string {
	var b strings.Builder
	b.WriteString("$ErrorActionPreference = 'Stop'\r\n")
	b.WriteString(fmt.Sprintf("$staging = '%s'\r\n", psQuote(staging)))
	b.WriteString(fmt.Sprintf("$binDir = '%s'\r\n", psQuote(binDir)))
	b.WriteString(fmt.Sprintf("$selfPid = %d\r\n", selfPID))
	b.WriteString(fmt.Sprintf("$version = '%s'\r\n", psQuote(version)))
	b.WriteString("$log = Join-Path $env:LOCALAPPDATA 'Nexus\\logs\\update.log'\r\n")
	b.WriteString("New-Item -ItemType Directory -Force -Path (Split-Path $log) | Out-Null\r\n")
	b.WriteString("function Log([string]$m) { Add-Content -Path $log -Value (\"[{0}] {1}\" -f (Get-Date -Format o), $m) }\r\n")
	b.WriteString("Log \"apply-update start version=$version\"\r\n")
	b.WriteString("# Wait for tray process to exit (unlocks nexus-desktop.exe)\r\n")
	b.WriteString("for ($i = 0; $i -lt 90; $i++) {\r\n")
	b.WriteString("  if (-not (Get-Process -Id $selfPid -ErrorAction SilentlyContinue)) { break }\r\n")
	b.WriteString("  Start-Sleep -Milliseconds 400\r\n")
	b.WriteString("}\r\n")
	b.WriteString("Start-Sleep -Milliseconds 500\r\n")
	b.WriteString("# Force-stop anything still holding bin locks\r\n")
	b.WriteString("foreach ($name in @('nexus-desktop','nexus-daemon','nexus')) {\r\n")
	b.WriteString("  Get-Process -Name $name -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue\r\n")
	b.WriteString("}\r\n")
	b.WriteString("Start-Sleep -Milliseconds 800\r\n")
	b.WriteString("$files = @(")
	for i, f := range staged {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString("'" + psQuote(f) + "'")
	}
	b.WriteString(")\r\n")
	b.WriteString("$ok = $true\r\n")
	b.WriteString("foreach ($f in $files) {\r\n")
	b.WriteString("  $src = Join-Path $staging $f\r\n")
	b.WriteString("  $dst = Join-Path $binDir $f\r\n")
	b.WriteString("  if (-not (Test-Path $src)) { Log \"missing staged $f\"; $ok = $false; continue }\r\n")
	b.WriteString("  $want = (Get-Item $src).Length\r\n")
	b.WriteString("  $replaced = $false\r\n")
	b.WriteString("  for ($t = 0; $t -lt 20; $t++) {\r\n")
	b.WriteString("    try {\r\n")
	b.WriteString("      if (Test-Path $dst) {\r\n")
	b.WriteString("        $bak = \"$dst.bak\"\r\n")
	b.WriteString("        Remove-Item -Force $bak -ErrorAction SilentlyContinue\r\n")
	b.WriteString("        Move-Item -Force $dst $bak\r\n")
	b.WriteString("      }\r\n")
	b.WriteString("      Copy-Item -Force $src $dst\r\n")
	b.WriteString("      $got = (Get-Item $dst).Length\r\n")
	b.WriteString("      if ($got -ne $want) { throw \"size mismatch $got vs $want\" }\r\n")
	b.WriteString("      Remove-Item -Force \"$dst.bak\" -ErrorAction SilentlyContinue\r\n")
	b.WriteString("      Log \"replaced $f ($got bytes)\"\r\n")
	b.WriteString("      $replaced = $true\r\n")
	b.WriteString("      break\r\n")
	b.WriteString("    } catch {\r\n")
	b.WriteString("      Log \"retry $f : $_\"\r\n")
	b.WriteString("      Start-Sleep -Milliseconds 500\r\n")
	b.WriteString("      Get-Process -Name 'nexus-daemon','nexus-desktop','nexus' -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue\r\n")
	b.WriteString("    }\r\n")
	b.WriteString("  }\r\n")
	b.WriteString("  if (-not $replaced) { Log \"FAILED $f\"; $ok = $false }\r\n")
	b.WriteString("}\r\n")
	b.WriteString("if (-not $ok) { Log 'update incomplete'; exit 1 }\r\n")
	b.WriteString("# Verify daemon reports something when asked\r\n")
	b.WriteString("$daemon = Join-Path $binDir 'nexus-daemon.exe'\r\n")
	b.WriteString("if (Test-Path $daemon) {\r\n")
	b.WriteString("  try {\r\n")
	b.WriteString("    $vout = & $daemon version 2>&1 | Out-String\r\n")
	b.WriteString("    Log \"daemon version: $vout\"\r\n")
	b.WriteString("    if ($vout -notmatch [regex]::Escape($version) -and $version -notmatch '^0\\.0\\.0-') {\r\n")
	b.WriteString("      Log \"WARNING: daemon version string did not include $version\"\r\n")
	b.WriteString("    }\r\n")
	b.WriteString("  } catch { Log \"daemon version check: $_\" }\r\n")
	b.WriteString("}\r\n")
	b.WriteString("$desktop = Join-Path $binDir 'nexus-desktop.exe'\r\n")
	b.WriteString("Log 'relaunching desktop'\r\n")
	b.WriteString("Start-Process -FilePath $desktop -WorkingDirectory $binDir\r\n")
	b.WriteString("Log 'apply-update done'\r\n")
	b.WriteString("exit 0\r\n")
	return b.String()
}

func psQuote(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

func stopOwnedDaemon(mu *sync.Mutex, daemonProc **os.Process) {
	if mu == nil || daemonProc == nil {
		return
	}
	mu.Lock()
	p := *daemonProc
	*daemonProc = nil
	mu.Unlock()
	if p != nil {
		_ = p.Kill()
		_, _ = p.Wait()
	}
}

// stopNexusProcesses stops daemon (and optionally desktop) so binaries unlock.
func stopNexusProcesses(includeDesktop bool) error {
	if runtime.GOOS != "windows" {
		return nil
	}
	names := []string{"nexus-daemon"}
	if includeDesktop {
		names = append(names, "nexus-desktop")
	}
	for _, name := range names {
		_ = exec.Command("taskkill", "/F", "/IM", name+".exe", "/T").Run()
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		still := false
		for _, name := range names {
			out, _ := exec.Command("tasklist", "/FI", "IMAGENAME eq "+name+".exe", "/NH").CombinedOutput()
			if strings.Contains(strings.ToLower(string(out)), strings.ToLower(name+".exe")) {
				still = true
				_ = exec.Command("taskkill", "/F", "/IM", name+".exe", "/T").Run()
			}
		}
		if !still {
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("nexus-daemon still running after stop attempts")
}

func downloadFile(client *http.Client, rawURL, dest string) error {
	resp, err := client.Get(rawURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("download %s: %s", rawURL, resp.Status)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	n, err := io.Copy(f, resp.Body)
	if err != nil {
		return err
	}
	if n < 1024 {
		return fmt.Errorf("download too small (%d bytes)", n)
	}
	return nil
}

func installAppShortcuts() error {
	if runtime.GOOS != "windows" {
		return fmt.Errorf("shortcuts are Windows-only")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	binDir := filepath.Dir(exe)
	ps := fmt.Sprintf(`
$ErrorActionPreference='Stop'
$exe='%s'
$bin='%s'
$wsh=New-Object -ComObject WScript.Shell
$startup=[Environment]::GetFolderPath('Startup')
$sc=$wsh.CreateShortcut((Join-Path $startup 'Nexus Desktop.lnk'))
$sc.TargetPath=$exe; $sc.WorkingDirectory=$bin; $sc.Description='Nexus Desktop'; $sc.Save()
$programs=Join-Path ([Environment]::GetFolderPath('StartMenu')) 'Programs\Nexus'
New-Item -ItemType Directory -Force -Path $programs | Out-Null
$sc2=$wsh.CreateShortcut((Join-Path $programs 'Nexus Desktop.lnk'))
$sc2.TargetPath=$exe; $sc2.WorkingDirectory=$bin; $sc2.Description='Nexus Desktop'; $sc2.Save()
`, strings.ReplaceAll(exe, "'", "''"), strings.ReplaceAll(binDir, "'", "''"))
	out, err := exec.Command("powershell", "-NoProfile", "-Command", ps).CombinedOutput()
	if err != nil {
		return fmt.Errorf("shortcut install: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}
