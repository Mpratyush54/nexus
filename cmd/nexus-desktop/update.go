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
	"strings"
	"time"

	"central-memory/internal/buildinfo"
)

const desktopLatestURL = "https://central-memory-releases.s3.ap-south-1.amazonaws.com/desktop/latest.json"

type desktopManifest struct {
	App        string `json:"app"`
	Version    string `json:"version"`
	Channel    string `json:"channel"`
	InstallURL string `json:"install_url"`
	Artifacts  []struct {
		Name     string `json:"name"`
		Filename string `json:"filename"`
		URL      string `json:"url"`
	} `json:"artifacts"`
}

func fetchDesktopManifest(client *http.Client) (*desktopManifest, error) {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	// Prefer API registry when available.
	apiBase := strings.TrimRight(firstNonEmpty(os.Getenv("NEXUS_SERVER"), defaultServerURL), "/")
	apiURL := apiBase + "/platform/releases/latest?app=desktop&channel=stable"
	if m, err := fetchManifestURL(client, apiURL); err == nil && m != nil && strings.TrimSpace(m.Version) != "" {
		return m, nil
	}
	return fetchManifestURL(client, desktopLatestURL)
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
	// API returns AppRelease shape; S3 returns desktopManifest.
	var m desktopManifest
	if err := json.Unmarshal(body, &m); err == nil && m.Version != "" {
		return &m, nil
	}
	var api struct {
		Version   string `json:"version"`
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

func versionNewer(remote, local string) bool {
	r := strings.TrimPrefix(strings.TrimSpace(remote), "v")
	l := strings.TrimPrefix(strings.TrimSpace(local), "v")
	if r == "" || l == "" || l == "dev" || strings.HasPrefix(l, "0.0.0-") {
		return r != "" && r != l
	}
	return r != l
}

// applyDesktopUpdate downloads artifacts and schedules a replace-after-exit
// script so the running tray binary can be overwritten on Windows.
func applyDesktopUpdate(m *desktopManifest) error {
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
		stagePath := filepath.Join(staging, destName)
		if err := downloadFile(client, art.URL, stagePath); err != nil {
			return fmt.Errorf("%s: %w", art.Name, err)
		}
		copied++
	}
	if copied == 0 {
		return fmt.Errorf("manifest has no downloadable artifacts")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	script := filepath.Join(staging, "apply-update.cmd")
	// Wait for this process to exit, copy files, relaunch tray.
	body := fmt.Sprintf("@echo off\r\n"+
		"setlocal\r\n"+
		"timeout /t 2 /nobreak >nul\r\n"+
		":wait\r\n"+
		"tasklist /FI \"IMAGENAME eq nexus-desktop.exe\" | find /I \"nexus-desktop.exe\" >nul\r\n"+
		"if not errorlevel 1 (\r\n"+
		"  timeout /t 1 /nobreak >nul\r\n"+
		"  goto wait\r\n"+
		")\r\n"+
		"copy /Y \"%s\\*.exe\" \"%s\\\" >nul\r\n"+
		"start \"\" \"%s\"\r\n"+
		"exit /b 0\r\n", staging, binDir, filepath.Join(binDir, "nexus-desktop.exe"))
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		return err
	}
	cmd := exec.Command("cmd", "/C", "start", "", "/MIN", script)
	cmd.Dir = staging
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = exe // kept for clarity; relaunch uses installed bin path
	return nil
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
	_, err = io.Copy(f, resp.Body)
	return err
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
