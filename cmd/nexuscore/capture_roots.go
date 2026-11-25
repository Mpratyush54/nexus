//go:build nexuscorelib

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"central-memory/internal/config"
)

// Only explicitly approved folders may be monitored. Transcript cwd hints
// and the recent-folder list are suggestions, never authorization.
func captureRoots(_ string) []string {
	saved, err := config.LoadFile()
	if err != nil || saved.CaptureConsentVersion < 1 {
		return nil
	}
	roots, _ := validateCaptureRoots(saved.CaptureWorkspaces, false)
	return roots
}

func validateCaptureRoots(input []string, strict bool) ([]string, error) {
	if len(input) > 20 {
		return nil, fmt.Errorf("choose at most 20 workspaces")
	}
	roots := make([]string, 0, len(input))
	seen := map[string]bool{}
	for _, value := range input {
		path := strings.TrimSpace(value)
		invalid := ""
		if !filepath.IsAbs(path) {
			invalid = "choose an absolute folder path"
		}
		path = filepath.Clean(path)
		if filepath.Dir(path) == path {
			invalid = "choose a project folder, not an entire drive"
		}
		if home, err := os.UserHomeDir(); err == nil && samePath(path, home) {
			invalid = "choose a project folder, not your home directory"
		}
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			invalid = "workspace folder is unavailable: " + path
		}
		if invalid != "" {
			if strict {
				return nil, fmt.Errorf("%s", invalid)
			}
			continue
		}
		key := strings.ToLower(path)
		if !seen[key] {
			roots = append(roots, path)
			seen[key] = true
		}
	}
	return roots, nil
}
