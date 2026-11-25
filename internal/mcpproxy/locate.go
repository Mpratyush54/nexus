package mcpproxy

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// BinaryName is the file name of the standalone proxy the desktop app ships
// next to Nexus.exe (cmd/nexus-mcp-proxy).
func BinaryName() string {
	if runtime.GOOS == "windows" {
		return "nexus-mcp-proxy.exe"
	}
	return "nexus-mcp-proxy"
}

// DefaultAppDir is the folder of the running executable. Inside the desktop
// app that is Nexus.exe's folder, where nexuscore.dll and the bundled proxy
// live. Empty when it cannot be determined.
func DefaultAppDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Dir(exe)
}

// Locate returns the absolute argv that starts the stdio proxy, in order:
//
//  1. the app's bundled nexus-mcp-proxy in appDir;
//  2. nexus-mcp-proxy on PATH;
//  3. the nexus CLI on PATH (`nexus mcp-proxy`).
//
// The desktop app never needs the CLI: 2 and 3 are fallbacks for a dev or
// CLI-only setup. look nil uses exec.LookPath.
func Locate(appDir string, look func(string) (string, error)) ([]string, error) {
	if look == nil {
		look = exec.LookPath
	}
	if dir := strings.TrimSpace(appDir); dir != "" {
		p := filepath.Join(dir, BinaryName())
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() {
			return []string{absPath(p)}, nil
		}
	}
	// exec.LookPath only returns absolute paths (it refuses "." entries).
	if p, err := look("nexus-mcp-proxy"); err == nil && strings.TrimSpace(p) != "" {
		return []string{p}, nil
	}
	if p, err := look("nexus"); err == nil && strings.TrimSpace(p) != "" {
		return []string{p, "mcp-proxy"}, nil
	}
	where := "the Nexus app folder"
	if strings.TrimSpace(appDir) != "" {
		where = appDir
	}
	return nil, fmt.Errorf("Part of Nexus is missing, so this agent couldn't be connected. Reinstall Nexus to fix it [detail: %s is missing from %s]", BinaryName(), where)
}

func absPath(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}
