package store

import (
	"fmt"
	"path"
	"strings"
)

// ValidProjectFolderName rejects names that minted junk portal projects
// when a daemon/tray resolved from a drive root, user profile, or empty cwd.
func ValidProjectFolderName(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		return false
	}
	if name == `\` || name == `/` {
		return false
	}
	// Windows drive roots ("C:", "D:") or UNC noise.
	if len(name) == 2 && name[1] == ':' {
		return false
	}
	if strings.ContainsAny(name, `/\`) {
		return false
	}
	// Common false positives when Getwd() is the user profile.
	low := strings.ToLower(name)
	if low == "users" || low == "desktop" || low == "documents" || low == "downloads" {
		return false
	}
	return true
}

// SanitizeProjectFolderName returns a usable folder label or an error when
// the only identity signal is a junk name (no git URL / root commit).
func SanitizeProjectFolderName(canonicalURL, rootCommit, folderName string) (string, error) {
	folderName = strings.TrimSpace(folderName)
	if ValidProjectFolderName(folderName) {
		return folderName, nil
	}
	if u := strings.TrimSpace(canonicalURL); u != "" {
		base := path.Base(strings.TrimSuffix(NormalizeGitURL(u), ".git"))
		if ValidProjectFolderName(base) {
			return base, nil
		}
		return "git-project", nil
	}
	if c := strings.TrimSpace(rootCommit); c != "" {
		if len(c) > 8 {
			c = c[:8]
		}
		return "commit-" + c, nil
	}
	return "", fmt.Errorf("invalid folder_name %q (set a real workspace folder, not a drive root or home directory)", folderName)
}
