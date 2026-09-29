// Command nexus-desktop — native Nexus Desktop (Fyne) for Windows / macOS / Linux.
//
// System tray + in-process window (no browser-to-127.0.0.1). Speaks to the
// local daemon via internal/localclient; updater replaces locked binaries.
package main

import (
	"context"
	_ "embed"
	"fmt"
	"log"
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
	"central-memory/internal/desktopui"
	"central-memory/internal/localclient"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/driver/desktop"
)

//go:embed icon.png
var iconPNG []byte

var defaultServerURL = "https://api-nexus.pratyushes.dev"

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	setupDesktopLog()
	detachFromParentConsole()
	release, ok := acquireSingleInstance()
	if !ok {
		log.Println("Nexus Desktop is already running")
		return
	}
	defer release()
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func setupDesktopLog() {
	dir := filepath.Join(os.Getenv("LOCALAPPDATA"), "Nexus", "logs")
	if runtime.GOOS != "windows" {
		if home, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, ".nexus", "logs")
		}
	}
	_ = os.MkdirAll(dir, 0o755)
	f, err := os.OpenFile(filepath.Join(dir, "desktop.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	log.SetOutput(f)
}

func run() error {
	a := app.NewWithID("dev.pratyushes.nexus.desktop")
	a.SetIcon(fyne.NewStaticResource("icon.png", iconPNG))

	var (
		mu         sync.Mutex
		daemonProc *os.Process
	)
	client := localclient.New("")
	win := a.NewWindow("Nexus")

	var shell *desktopui.Shell

	doLogin := func() {
		go func() {
			a.SendNotification(&fyne.Notification{Title: "Nexus", Content: "Opening browser to sign in…"})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			res, err := authbrowser.Login(ctx, authbrowser.Options{
				AppURL:    config.ResolveAppURL(),
				ServerURL: config.ResolveServerURL(defaultServerURL),
			})
			if err != nil {
				a.SendNotification(&fyne.Notification{Title: "Nexus", Content: "Sign-in failed: " + err.Error()})
				return
			}
			a.SendNotification(&fyne.Notification{Title: "Nexus", Content: "Signed in as " + res.Username})
			_ = ensureDaemon(&mu, &daemonProc)
			if shell != nil {
				shell.Refresh()
			}
		}()
	}

	hooks := desktopui.Hooks{
		OnSignIn: doLogin,
		OnSignOut: func() {
			_ = config.ClearCredentials()
			_ = client.Logout()
			a.SendNotification(&fyne.Notification{Title: "Nexus", Content: "Signed out"})
			if shell != nil {
				shell.Refresh()
			}
		},
		OnOpenPortal: func() {
			_ = authbrowser.OpenBrowser(config.ResolveAppURL() + "/app/dashboard")
		},
		OnPickFolder: func() {
			go pickWorkspaceFolder(a, &mu, &daemonProc, func() {
				if shell != nil {
					shell.Refresh()
				}
			})
		},
		OnCheckUpdate: func() {
			go checkAndApplyUpdate(a, &mu, &daemonProc, func() {
				if shell != nil {
					shell.Refresh()
				}
			})
		},
		OnQuit: func() {
			mu.Lock()
			if daemonProc != nil {
				_ = daemonProc.Kill()
				daemonProc = nil
			}
			mu.Unlock()
			_ = stopNexusProcesses(false)
			if shell != nil {
				shell.Stop()
			}
			a.Quit()
		},
		UpdateLabel: versionLabel,
		EnsureDaemon: func() error {
			return ensureDaemon(&mu, &daemonProc)
		},
	}

	shell = desktopui.NewShell(win, client, hooks)
	shell.AttachRefresh(4 * time.Second)

	if desk, ok := a.(desktop.App); ok {
		desk.SetSystemTrayIcon(fyne.NewStaticResource("icon.png", iconPNG))
		desk.SetSystemTrayMenu(fyne.NewMenu("Nexus",
			fyne.NewMenuItem("Open Nexus", func() { shell.Show() }),
			fyne.NewMenuItem("Sign in…", doLogin),
			fyne.NewMenuItem("Open portal", hooks.OnOpenPortal),
			fyne.NewMenuItem("Choose workspace…", hooks.OnPickFolder),
			fyne.NewMenuItem("Scan now", func() {
				go func() {
					if err := client.TriggerHarvest(); err != nil {
						a.SendNotification(&fyne.Notification{Title: "Nexus", Content: "Scan: " + err.Error()})
						return
					}
					a.SendNotification(&fyne.Notification{Title: "Nexus", Content: "Harvest scan requested"})
					shell.Refresh()
				}()
			}),
			fyne.NewMenuItemSeparator(),
			fyne.NewMenuItem("Check for updates…", hooks.OnCheckUpdate),
			fyne.NewMenuItemSeparator(),
			fyne.NewMenuItem("Quit", hooks.OnQuit),
		))
	}

	go func() { _ = installAppShortcuts() }()
	go func() {
		time.Sleep(12 * time.Second)
		silentUpdateCheck(a, func() {
			if shell != nil {
				shell.Refresh()
			}
		})
		t := time.NewTicker(6 * time.Hour)
		defer t.Stop()
		for range t.C {
			silentUpdateCheck(a, func() {
				if shell != nil {
					shell.Refresh()
				}
			})
		}
	}()
	if file, _ := config.LoadFile(); strings.TrimSpace(file.Token) != "" {
		go func() {
			if err := ensureDaemon(&mu, &daemonProc); err != nil {
				log.Printf("ensureDaemon: %v", err)
			}
			shell.Refresh()
		}()
	}

	win.Show()
	a.SendNotification(&fyne.Notification{
		Title:   "Nexus",
		Content: "Nexus Desktop is running — use the tray icon or this window",
	})
	a.Run()
	shell.Stop()
	return nil
}

func checkAndApplyUpdate(a fyne.App, mu *sync.Mutex, daemonProc **os.Process, refresh func()) {
	globalUpdate.set(updateChecking, "Checking for updates")
	refresh()
	a.SendNotification(&fyne.Notification{Title: "Nexus", Content: "Checking for updates…"})
	m, err := fetchDesktopManifest(nil)
	if err != nil {
		globalUpdate.fail(err)
		refresh()
		a.SendNotification(&fyne.Notification{Title: "Nexus", Content: "Update check failed: " + err.Error()})
		return
	}
	globalUpdate.setRemote(m.Version)
	if !versionNewer(m.Version, buildinfo.Version) {
		globalUpdate.set(updateOK, "up to date")
		refresh()
		a.SendNotification(&fyne.Notification{Title: "Nexus", Content: "Up to date (" + normalizeVersion(buildinfo.Version) + ")"})
		return
	}
	globalUpdate.set(updateAvailable, "Update "+m.Version+" available")
	refresh()
	globalUpdate.set(updateDownloading, "Downloading "+m.Version)
	refresh()
	a.SendNotification(&fyne.Notification{Title: "Nexus", Content: "Updating to " + m.Version + "…"})
	globalUpdate.set(updateApplying, "Stopping daemon and replacing binaries")
	refresh()
	if err := applyDesktopUpdate(m, daemonProc, mu); err != nil {
		globalUpdate.fail(err)
		refresh()
		a.SendNotification(&fyne.Notification{Title: "Nexus", Content: "Update failed: " + err.Error()})
		return
	}
	globalUpdate.set(updateRestarting, "Restart required")
	refresh()
	a.SendNotification(&fyne.Notification{Title: "Nexus", Content: "Update " + m.Version + " ready — restarting…"})
	time.Sleep(800 * time.Millisecond)
	os.Exit(0)
}

func silentUpdateCheck(a fyne.App, refresh func()) {
	m, err := fetchDesktopManifest(nil)
	if err != nil || m == nil {
		return
	}
	globalUpdate.setRemote(m.Version)
	if !versionNewer(m.Version, buildinfo.Version) {
		globalUpdate.set(updateOK, "up to date")
		refresh()
		return
	}
	globalUpdate.set(updateAvailable, "Update "+m.Version+" available")
	refresh()
	a.SendNotification(&fyne.Notification{
		Title:   "Nexus",
		Content: "Update " + m.Version + " available — Open Nexus → Check for updates",
	})
}

func ensureDaemon(mu *sync.Mutex, proc **os.Process) error {
	client := localclient.New("")
	if client.Online() {
		return nil
	}
	bin, err := resolveDaemonBinary()
	if err != nil {
		return err
	}
	root := resolveWorkspaceRoot()
	if root == "" {
		return fmt.Errorf("no workspace folder set — Choose workspace…")
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
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if client.Online() {
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
	return ""
}

func pickWorkspaceFolder(a fyne.App, mu *sync.Mutex, proc **os.Process, refresh func()) {
	if runtime.GOOS != "windows" {
		// Fyne folder open dialog is preferred on all platforms; Windows keeps
		// the legacy PowerShell picker until Fyne dialog is wired in-window.
		a.SendNotification(&fyne.Notification{
			Title:   "Nexus",
			Content: "Set workspace from the Open Nexus window (or NEXUS_WORKSPACE)",
		})
		return
	}
	ps := `Add-Type -AssemblyName System.Windows.Forms; $f = New-Object System.Windows.Forms.FolderBrowserDialog; $f.Description = 'Nexus workspace folder'; if ($f.ShowDialog() -eq 'OK') { $f.SelectedPath }`
	out, err := exec.Command("powershell", "-NoProfile", "-Command", ps).Output()
	if err != nil {
		a.SendNotification(&fyne.Notification{Title: "Nexus", Content: "Folder picker failed"})
		return
	}
	path := strings.TrimSpace(string(out))
	if path == "" {
		return
	}
	if err := config.SaveFile(config.File{WorkspaceRoot: path}); err != nil {
		a.SendNotification(&fyne.Notification{Title: "Nexus", Content: "Could not save folder: " + err.Error()})
		return
	}
	mu.Lock()
	if *proc != nil {
		_ = (*proc).Kill()
		*proc = nil
	}
	mu.Unlock()
	if err := ensureDaemon(mu, proc); err != nil {
		a.SendNotification(&fyne.Notification{Title: "Nexus", Content: "Saved " + path + " — daemon: " + err.Error()})
	} else {
		a.SendNotification(&fyne.Notification{Title: "Nexus", Content: "Watching " + path})
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
