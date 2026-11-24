package platform

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// AppDataDir returns the per-user base folder where desktop apps keep their
// settings and state: the folder that holds Cursor/, Code/, Windsurf/ and
// Antigravity/ (VS Code family "User/workspaceStorage" lives under it).
//
//	Windows: %APPDATA%                      (C:\Users\me\AppData\Roaming)
//	darwin:  ~/Library/Application Support
//	linux:   $XDG_CONFIG_HOME or ~/.config
//
// Before this helper, harvester code read %APPDATA% and fell back to
// ~/.config everywhere else, which is right on Linux and wrong on macOS.
func AppDataDir() string {
	home, _ := userHomeDirFunc()
	return appDataDirForGOOS(runtime.GOOS, os.Getenv("APPDATA"), os.Getenv("XDG_CONFIG_HOME"), home)
}

// appDataDirForGOOS is the pure mapping behind AppDataDir, so every OS's
// answer is testable from any OS.
func appDataDirForGOOS(goos, appData, xdgConfig, home string) string {
	switch goos {
	case "windows":
		if strings.TrimSpace(appData) != "" {
			return appData
		}
		if strings.TrimSpace(home) != "" {
			return filepath.Join(home, "AppData", "Roaming")
		}
		return ""
	case "darwin", "ios":
		if strings.TrimSpace(home) == "" {
			return ""
		}
		return filepath.Join(home, "Library", "Application Support")
	default:
		// XDG requires an absolute path; relative values are ignored.
		if strings.HasPrefix(xdgConfig, "/") || (xdgConfig != "" && filepath.IsAbs(xdgConfig)) {
			return xdgConfig
		}
		if strings.TrimSpace(home) == "" {
			return ""
		}
		return filepath.Join(home, ".config")
	}
}
