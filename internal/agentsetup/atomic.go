package agentsetup

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// EditOptions control the editing calls (rules files and MCP servers).
type EditOptions struct {
	Home, Workspace string
	// StateDir is a folder Nexus owns (the desktop profile). Backups go to
	// StateDir/backups and servers that are turned off are kept in
	// StateDir/disabled-mcp.json. Empty keeps backups next to the file and
	// allows turning off only agents that have their own on/off setting.
	StateDir string
	// Now is the clock for backup names; nil uses time.Now.
	Now func() time.Time
}

func (o EditOptions) roots() roots { return resolve(ScanOptions{Home: o.Home, Workspace: o.Workspace}) }

func (o EditOptions) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// keepBackups bounds the backups Nexus keeps per file in StateDir.
const keepBackups = 20

// etag identifies one version of a file's bytes ("" when it is missing).
func etag(b []byte, exists bool) string {
	if !exists {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:12])
}

// writeAtomic replaces path with data through a temporary file in the same
// folder and a rename, so a crash or a full disk never leaves a half-written
// config. The file keeps its permissions (new files are 0600).
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("Nexus couldn't create the folder for %s", filepath.Base(path))
	}
	mode := os.FileMode(0o600)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".nexus-*")
	if err != nil {
		return fmt.Errorf("Nexus couldn't write next to %s. Check that the folder isn't read-only", filepath.Base(path))
	}
	name := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(name)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("Nexus couldn't write %s. Check there is free disk space", filepath.Base(path))
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("Nexus couldn't write %s", filepath.Base(path))
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("Nexus couldn't write %s", filepath.Base(path))
	}
	_ = os.Chmod(name, mode)
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("Nexus couldn't replace %s. Close any program that has it open, then try again", filepath.Base(path))
	}
	ok = true
	return nil
}

// backupCopy saves the current bytes of path before it is changed and
// returns where. With a StateDir the copy goes to StateDir/backups (so
// project folders don't fill with backup files) and only the newest
// keepBackups per file are kept; otherwise it sits next to the file.
func (o EditOptions) backupCopy(path string, old []byte) (string, error) {
	var target string
	if strings.TrimSpace(o.StateDir) != "" {
		dir := filepath.Join(o.StateDir, "backups")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", fmt.Errorf("Nexus couldn't create its backup folder")
		}
		sum := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(path))))
		prefix := filepath.Base(path) + "." + hex.EncodeToString(sum[:4]) + "."
		target = filepath.Join(dir, prefix+o.now().Format("20060102-150405")+".bak")
		for i := 1; ; i++ {
			if _, err := os.Lstat(target); os.IsNotExist(err) {
				break
			}
			target = filepath.Join(dir, fmt.Sprintf("%s%s-%d.bak", prefix, o.now().Format("20060102-150405"), i))
		}
		defer pruneBackups(dir, prefix)
	} else {
		b, err := backupPath(path)
		if err != nil {
			return "", err
		}
		target = b
	}
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("Nexus couldn't save a backup of %s, so nothing was changed", filepath.Base(path))
	}
	_, we := f.Write(old)
	ce := f.Close()
	if we != nil || ce != nil {
		_ = os.Remove(target)
		return "", fmt.Errorf("Nexus couldn't save a backup of %s, so nothing was changed", filepath.Base(path))
	}
	return target, nil
}

func pruneBackups(dir, prefix string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), prefix) && strings.HasSuffix(e.Name(), ".bak") {
			names = append(names, e.Name())
		}
	}
	if len(names) <= keepBackups {
		return
	}
	sort.Strings(names) // timestamps sort oldest first
	for _, n := range names[:len(names)-keepBackups] {
		_ = os.Remove(filepath.Join(dir, n))
	}
}
