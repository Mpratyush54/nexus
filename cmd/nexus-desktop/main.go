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
	"central-memory/internal/config"

	"github.com/gogpu/systray"
)

//go:embed icon.png
var iconPNG []byte

var defaultServerURL = "https://api-nexus.pratyushes.dev"

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	if err := run(); err != nil {
		log.Fatal(err)
	}
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

	refreshStatus()
	tray.ShowNotification("Nexus", "Nexus is running in the system tray")
	go func() {
		for {
			time.Sleep(8 * time.Second)
			refreshStatus()
		}
	}()

	if file, _ := config.LoadFile(); strings.TrimSpace(file.Token) != "" {
		go func() { _ = ensureDaemon(&mu, &daemonProc) }()
	}

	tray.Show()
	return tray.Run()
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
		return v
	}
	root, _ := os.Getwd()
	return root
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
