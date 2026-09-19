// Command nexus-desktop — Windows tray app for Nexus.
//
// Menu: Sign in (browser → web login → localhost callback), Open dashboard,
// Status page, Start/stop workspace daemon helpers, Quit.
package main

import (
	"context"
	_ "embed"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"central-memory/internal/authbrowser"
	"central-memory/internal/buildinfo"
	"central-memory/internal/config"

	"github.com/gogpu/systray"
)

//go:embed icon.png
var iconPNG []byte

//go:embed icon-dark.png
var iconDarkPNG []byte

var defaultServerURL = "https://api-nexus.pratyushes.dev"

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	setupDesktopLog()
	detachFromParentConsole()
	release, ok := acquireSingleInstance()
	if !ok {
		// Second launch: keep the existing tray instance; do not spawn another.
		log.Println("Nexus Desktop is already running in the system tray")
		return
	}
	defer release()
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func setupDesktopLog() {
	dir := filepath.Join(os.Getenv("LOCALAPPDATA"), "Nexus", "logs")
	_ = os.MkdirAll(dir, 0o755)
	f, err := os.OpenFile(filepath.Join(dir, "desktop.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	log.SetOutput(f)
}

func run() error {
	tray := systray.New()
	var (
		mu         sync.Mutex
		statusItem *systray.MenuItem
		daemonProc *os.Process
	)

	refreshStatus := func() {
		file, _ := config.LoadFile()
		label := "Status: Not signed in"
		tip := "Nexus — not signed in"
		if strings.TrimSpace(file.Token) != "" {
			name := file.Username
			if name == "" {
				name = file.UserID
			}
			label = "Status: Signed in as " + name
			tip = "Nexus — " + name
			if online := daemonOnline(); online {
				label += " · daemon online"
				tip += " (connected)"
			}
		}
		mu.Lock()
		if statusItem != nil {
			statusItem.SetLabel(label)
		}
		mu.Unlock()
		tray.SetTooltip(tip)
	}

	doLogin := func() {
		go func() {
			tray.ShowNotification("Nexus", "Opening browser to sign in…")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			res, err := authbrowser.Login(ctx, authbrowser.Options{
				AppURL:    config.ResolveAppURL(),
				ServerURL: config.ResolveServerURL(defaultServerURL),
			})
			if err != nil {
				tray.ShowNotification("Nexus", "Sign-in failed: "+err.Error())
				return
			}
			tray.ShowNotification("Nexus", "Signed in as "+res.Username)
			refreshStatus()
			ensureDaemon(&mu, &daemonProc)
			refreshStatus()
		}()
	}

	menu := systray.NewMenu()
	statusItem = menu.Add("Status: …", nil)
	statusItem.SetDisabled(true)
	menu.AddSeparator()
	menu.Add("Sign in with browser…", doLogin)
	menu.Add("Sign out", func() {
		_ = config.ClearCredentials()
		tray.ShowNotification("Nexus", "Signed out")
		refreshStatus()
	})
	menu.AddSeparator()
	menu.Add("Open Nexus home", func() {
		_ = authbrowser.OpenBrowser(config.ResolveAppURL() + "/app/dashboard")
	})
	menu.Add("Open local status", func() {
		_ = authbrowser.OpenBrowser("http://127.0.0.1:7272/")
	})
	menu.Add("Start workspace daemon", func() {
		if err := ensureDaemon(&mu, &daemonProc); err != nil {
			tray.ShowNotification("Nexus", "Daemon: "+err.Error())
			return
		}
		tray.ShowNotification("Nexus", "Workspace daemon started")
		refreshStatus()
	})
	menu.Add("Set workspace folder…", func() {
		go pickWorkspaceFolder(tray, &mu, &daemonProc, refreshStatus)
	})
	menu.AddSeparator()
	menu.Add("Check for updates…", func() {
		go checkAndApplyUpdate(tray)
	})
	menu.Add("Install Start Menu + Startup shortcuts", func() {
		go func() {
			if err := installAppShortcuts(); err != nil {
				tray.ShowNotification("Nexus", err.Error())
				return
			}
			tray.ShowNotification("Nexus", "Shortcuts installed — Start Menu → Programs → Nexus")
		}()
	})
	verLabel := "Version: " + strings.TrimPrefix(buildinfo.Version, "v")
	if buildinfo.Version == "" || buildinfo.Version == "dev" {
		verLabel = "Version: dev"
	}
	vi := menu.Add(verLabel, nil)
	vi.SetDisabled(true)
	menu.AddSeparator()
	menu.Add("Quit Nexus Desktop", func() {
		mu.Lock()
		if daemonProc != nil {
			_ = daemonProc.Kill()
			daemonProc = nil
		}
		mu.Unlock()
		tray.Remove()
		os.Exit(0)
	})

	tray.SetIcon(iconPNG).
		SetDarkModeIcon(iconDarkPNG).
		SetTooltip("Nexus").
		SetMenu(menu).
		OnClick(func() {
			file, _ := config.LoadFile()
			if strings.TrimSpace(file.Token) == "" {
				doLogin()
				return
			}
			_ = authbrowser.OpenBrowser(config.ResolveAppURL() + "/app/dashboard")
		}).
		OnDoubleClick(func() {
			_ = authbrowser.OpenBrowser(config.ResolveAppURL() + "/app/dashboard")
		})

	// Show() must run before notifications — otherwise Shell_NotifyIcon fails
	// and Windows may never surface the icon.
	tray.Show()
	refreshStatus()
	tray.ShowNotification("Nexus", "Nexus is running in the system tray — click ^ if the icon is hidden")
	go func() { _ = installAppShortcuts() }()
	go func() {
		for {
			time.Sleep(8 * time.Second)
			refreshStatus()
		}
	}()
	go func() {
		time.Sleep(15 * time.Second)
		silentUpdateCheck(tray)
		t := time.NewTicker(6 * time.Hour)
		defer t.Stop()
		for range t.C {
			silentUpdateCheck(tray)
		}
	}()

	if file, _ := config.LoadFile(); strings.TrimSpace(file.Token) != "" {
		go func() {
			if err := ensureDaemon(&mu, &daemonProc); err != nil {
				log.Printf("ensureDaemon: %v", err)
			}
		}()
	}

	log.Println("tray message loop starting")
	return tray.Run()
}

func checkAndApplyUpdate(tray *systray.SystemTray) {
	tray.ShowNotification("Nexus", "Checking for updates…")
	m, err := fetchDesktopManifest(nil)
	if err != nil {
		tray.ShowNotification("Nexus", "Update check failed: "+err.Error())
		return
	}
	if !versionNewer(m.Version, buildinfo.Version) {
		tray.ShowNotification("Nexus", "Up to date ("+buildinfo.Version+")")
		return
	}
	tray.ShowNotification("Nexus", "Updating to "+m.Version+"…")
	if err := applyDesktopUpdate(m); err != nil {
		tray.ShowNotification("Nexus", "Update failed: "+err.Error())
		return
	}
	tray.ShowNotification("Nexus", "Update downloaded — restarting…")
	time.Sleep(800 * time.Millisecond)
	os.Exit(0)
}

func silentUpdateCheck(tray *systray.SystemTray) {
	m, err := fetchDesktopManifest(nil)
	if err != nil || m == nil {
		return
	}
	if !versionNewer(m.Version, buildinfo.Version) {
		return
	}
	tray.ShowNotification("Nexus", "Update "+m.Version+" available — tray menu → Check for updates")
}

func daemonOnline() bool {
	client := &http.Client{Timeout: 800 * time.Millisecond}
	resp, err := client.Get("http://127.0.0.1:7272/local/status")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200
}

func ensureDaemon(mu *sync.Mutex, proc **os.Process) error {
	if daemonOnline() {
		return nil
	}
	bin, err := resolveDaemonBinary()
	if err != nil {
		return err
	}
	root := resolveWorkspaceRoot()
	if root == "" {
		return fmt.Errorf("no workspace folder set — use tray → Set workspace folder (do not rely on the terminal's current directory)")
	}
	file, _ := config.LoadFile()
	server := firstNonEmpty(file.ServerURL, config.ResolveServerURL(defaultServerURL))
	args := []string{"-root", root, "-server", server}
	if tok := firstNonEmpty(file.Token, config.ResolveToken()); tok != "" {
		args = append(args, "-server-token", tok)
	}
	if uid := firstNonEmpty(file.UserID, config.ResolveUserID()); uid != "" {
		args = append(args, "-user", uid)
	}
	cmd := exec.Command(bin, args...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	configureDaemonCmd(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	mu.Lock()
	*proc = cmd.Process
	mu.Unlock()
	// Wait briefly for proxy.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if daemonOnline() {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("started but status UI not reachable yet (root=%s)", root)
}

func resolveWorkspaceRoot() string {
	file, _ := config.LoadFile()
	if r := strings.TrimSpace(file.WorkspaceRoot); r != "" {
		if st, err := os.Stat(r); err == nil && st.IsDir() {
			return r
		}
	}
	if v := strings.TrimSpace(os.Getenv("NEXUS_WORKSPACE")); v != "" {
		if st, err := os.Stat(v); err == nil && st.IsDir() {
			return v
		}
	}
	// Never fall back to Getwd(): Start Menu / terminal cwd is often the user
	// profile or a drive root and would mint junk portal projects
	// ("\", "Pratyush Mishra", etc.).
	return ""
}

func pickWorkspaceFolder(tray *systray.SystemTray, mu *sync.Mutex, proc **os.Process, refresh func()) {
	if runtime.GOOS != "windows" {
		tray.ShowNotification("Nexus", "Set NEXUS_WORKSPACE or workspace_root in config.json")
		return
	}
	// Native folder picker via PowerShell (no extra GUI deps).
	ps := `Add-Type -AssemblyName System.Windows.Forms; $f = New-Object System.Windows.Forms.FolderBrowserDialog; $f.Description = 'Nexus workspace folder'; if ($f.ShowDialog() -eq 'OK') { $f.SelectedPath }`
	out, err := exec.Command("powershell", "-NoProfile", "-Command", ps).Output()
	if err != nil {
		tray.ShowNotification("Nexus", "Folder picker failed")
		return
	}
	path := strings.TrimSpace(string(out))
	if path == "" {
		return
	}
	if err := config.SaveFile(config.File{WorkspaceRoot: path}); err != nil {
		tray.ShowNotification("Nexus", "Could not save folder: "+err.Error())
		return
	}
	mu.Lock()
	if *proc != nil {
		_ = (*proc).Kill()
		*proc = nil
	}
	mu.Unlock()
	if err := ensureDaemon(mu, proc); err != nil {
		tray.ShowNotification("Nexus", "Saved "+path+" — daemon: "+err.Error())
	} else {
		tray.ShowNotification("Nexus", "Watching "+path)
	}
	refresh()
}

func resolveDaemonBinary() (string, error) {
	if v := strings.TrimSpace(os.Getenv("NEXUS_DAEMON_BIN")); v != "" {
		return v, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(exe)
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	for _, name := range []string{
		"nexus-daemon" + ext,
		"nexus-daemon-windows-amd64" + ext,
	} {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	if base, err := os.UserConfigDir(); err == nil {
		p := filepath.Join(base, "Nexus", "bin", "nexus-daemon"+ext)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("nexus-daemon not found next to desktop app")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
