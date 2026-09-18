package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

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
		dArgs := []string{"-root", root, "-server", cfg.ServerURL}
		if cfg.Token != "" {
			dArgs = append(dArgs, "-server-token", cfg.Token)
		}
		if err := platform.Install(bin, dArgs); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "installed %s as a login service\n", bin)
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
	for _, name := range []string{"nexus-daemon" + ext, "workspace-daemon" + ext, "daemon" + ext} {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("daemon binary not found next to %s (set NEXUS_DAEMON_BIN)", exe)
}
