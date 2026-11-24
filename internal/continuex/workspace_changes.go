package continuex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// workspaceSnapshot records bounded working-tree state without reading Git's
// history or including ignored files. It lets an in-app reply show paths that
// changed while the agent ran, including files that were already dirty.
func workspaceSnapshot(root string) map[string]string {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain=v1", "-z", "--untracked-files=all")
	hideConsole(cmd)
	cmd.Dir = root
	output, err := cmd.Output()
	if err != nil || len(output) > 2<<20 {
		return nil
	}
	state := make(map[string]string)
	parts := strings.Split(string(output), "\x00")
	for i := 0; i < len(parts) && len(state) < 500; i++ {
		entry := parts[i]
		if len(entry) < 4 || entry[2] != ' ' {
			continue
		}
		path := entry[3:]
		if strings.ContainsAny(entry[:2], "RC") && i+1 < len(parts) {
			i++ // porcelain -z includes the old path after a rename/copy
		}
		if path == "" || filepath.IsAbs(path) || strings.HasPrefix(path, "..") {
			continue
		}
		state[path] = workspaceFingerprint(filepath.Join(root, path))
	}
	return state
}

func workspaceFingerprint(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return "missing"
	}
	if !info.Mode().IsRegular() {
		return info.Mode().String()
	}
	if info.Size() > 8<<20 {
		return info.ModTime().UTC().String() + ":large"
	}
	file, err := os.Open(path)
	if err != nil {
		return "unreadable"
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "unreadable"
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func workspaceChanges(before, after map[string]string) []string {
	if before == nil || after == nil {
		return nil
	}
	changed := make([]string, 0)
	for path, now := range after {
		if prior, ok := before[path]; !ok || prior != now {
			changed = append(changed, path)
		}
	}
	for path := range before {
		if _, ok := after[path]; !ok {
			changed = append(changed, path)
		}
	}
	sort.Strings(changed)
	return changed
}
