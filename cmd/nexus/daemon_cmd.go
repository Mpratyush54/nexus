package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"central-memory/internal/config"
	"central-memory/internal/platform"
)

func runDaemonCmd(cfg Config, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: nexus daemon install|uninstall|status")
	}
	switch args[0] {
	case "status":
		st, err := platform.Status()
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, st)
		return nil
	case "uninstall":
		if err := platform.Uninstall(); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "daemon service removed")
		return nil
	case "install":
		bin, err := resolveDaemonBinary()
		if err != nil {
			return err
		}
		root, _ := os.Getwd()
		file, _ := config.LoadFile()
		server := firstNonEmpty(cfg.ServerURL, file.ServerURL)
		token := firstNonEmpty(cfg.Token, file.Token)
		userID := firstNonEmpty(config.ResolveUserID(), file.UserID)
		dArgs := []string{"-root", root, "-server", server}
		if token != "" {
			dArgs = append(dArgs, "-server-token", token)
		}
		if userID != "" {
			dArgs = append(dArgs, "-user", userID)
		}
		if err := platform.Install(bin, dArgs); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "installed %s as a login service\n", bin)
		fmt.Fprintln(stdout, "status UI: http://127.0.0.1:7272/")
		return nil
	default:
		return fmt.Errorf("unknown daemon command %q (want install, uninstall, status)", args[0])
	}
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
	candidates := []string{
		filepath.Join(dir, "nexus-daemon"+ext),
		filepath.Join(dir, "nexus-daemon-windows-amd64"+ext),
		filepath.Join(dir, "workspace-daemon"+ext),
		filepath.Join(dir, "daemon"+ext),
	}
	if binDir, err := nexusBinDir(); err == nil {
		candidates = append([]string{
			filepath.Join(binDir, "nexus-daemon"+ext),
		}, candidates...)
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("daemon binary not found next to %s (set NEXUS_DAEMON_BIN)", exe)
}
