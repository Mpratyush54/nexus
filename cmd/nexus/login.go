package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"central-memory/internal/authbrowser"
	"central-memory/internal/config"
)

func runLogin(ctx context.Context, cfg Config, args []string, stdout io.Writer) error {
	_ = args
	server := strings.TrimSpace(cfg.ServerURL)
	if server == "" {
		return fmt.Errorf("no server URL (pass --server or set NEXUS_SERVER)")
	}
	appURL := config.ResolveAppURL()
	fmt.Fprintln(stdout, "Opening browser to sign in…")
	fmt.Fprintf(stdout, "  %s/cli/auth\n", appURL)

	res, err := authbrowser.Login(ctx, authbrowser.Options{
		AppURL:    appURL,
		ServerURL: server,
		Timeout:   5 * time.Minute,
	})
	if err != nil {
		return err
	}
	path, _ := config.ConfigPath()
	fmt.Fprintf(stdout, "signed in as %s\n", res.Username)
	fmt.Fprintf(stdout, "credentials saved to %s\n", path)
	fmt.Fprintln(stdout, "next: nexus setup   — or use the tray app (Nexus Desktop)")
	return nil
}

func runLogout(_ context.Context, _ Config, _ []string, stdout io.Writer) error {
	if err := config.ClearCredentials(); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "signed out (token cleared from local config)")
	return nil
}

func runSetup(ctx context.Context, cfg Config, args []string, stdout io.Writer) error {
	_ = args
	file, _ := config.LoadFile()
	if strings.TrimSpace(cfg.Token) == "" && strings.TrimSpace(file.Token) == "" {
		fmt.Fprintln(stdout, "No saved login — opening browser…")
		if err := runLogin(ctx, cfg, nil, stdout); err != nil {
			return err
		}
		cfg.Token = config.ResolveToken()
	} else if strings.TrimSpace(cfg.Token) == "" {
		cfg.Token = strings.TrimSpace(file.Token)
	}

	if err := installBinariesToAppDir(stdout); err != nil {
		fmt.Fprintf(stdout, "note: could not copy binaries into app dir: %v\n", err)
	}

	if err := runDaemonCmd(cfg, []string{"install"}, stdout); err != nil {
		return fmt.Errorf("daemon install: %w", err)
	}

	// Prefer launching the tray app when present.
	if desk, err := resolveDesktopBinary(); err == nil {
		_ = exec.Command(desk).Start()
		fmt.Fprintf(stdout, "started tray app: %s\n", desk)
	}

	statusURL := "http://127.0.0.1:7272/"
	fmt.Fprintln(stdout, "")
	fmt.Fprintln(stdout, "Setup complete.")
	fmt.Fprintf(stdout, "  Tray:       Nexus Desktop (system tray)\n")
	fmt.Fprintf(stdout, "  Status UI:  %s\n", statusURL)
	fmt.Fprintf(stdout, "  Web app:    %s\n", config.ResolveAppURL())
	_ = openBrowser(statusURL)
	return nil
}

func installBinariesToAppDir(stdout io.Writer) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, err = filepath.Abs(exe)
	if err != nil {
		return err
	}
	srcDir := filepath.Dir(exe)
	dstDir, err := nexusBinDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return err
	}
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	pairs := []struct{ src, dst string }{
		{"nexus" + ext, "nexus" + ext},
		{"nexus-windows-amd64" + ext, "nexus" + ext},
		{"nexus-daemon" + ext, "nexus-daemon" + ext},
		{"nexus-daemon-windows-amd64" + ext, "nexus-daemon" + ext},
		{"nexus-desktop" + ext, "nexus-desktop" + ext},
		{"nexus-desktop-windows-amd64" + ext, "nexus-desktop" + ext},
	}
	copied := map[string]bool{}
	for _, p := range pairs {
		src := filepath.Join(srcDir, p.src)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		dst := filepath.Join(dstDir, p.dst)
		if err := copyFile(src, dst); err != nil {
			return err
		}
		if !copied[p.dst] {
			fmt.Fprintf(stdout, "installed %s\n", dst)
			copied[p.dst] = true
		}
	}
	if !copied["nexus"+ext] {
		dst := filepath.Join(dstDir, "nexus"+ext)
		if err := copyFile(exe, dst); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "installed %s\n", dst)
	}
	return nil
}

func resolveDesktopBinary() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(exe)
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	for _, p := range []string{
		filepath.Join(dir, "nexus-desktop"+ext),
		filepath.Join(dir, "nexus-desktop-windows-amd64"+ext),
	} {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	if base, err := os.UserConfigDir(); err == nil {
		p := filepath.Join(base, "Nexus", "bin", "nexus-desktop"+ext)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("desktop app not found")
}

func nexusBinDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "Nexus", "bin"), nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

func openBrowser(url string) error {
	return authbrowser.OpenBrowser(url)
}
