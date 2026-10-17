//go:build nexuscorelib

package main

import (
	"os"
	"path/filepath"
	"strings"

	"central-memory/internal/config"
	"central-memory/internal/daemon"
)

// captureRoots maps local Codex history back to the repositories that
// produced it. Session metadata is a hint, never a command: only existing
// Git workspaces are auto-added, and the selected root always comes first.
func captureRoots(primary string) []string {
	roots := []string{primary}
	seenPaths := map[string]bool{strings.ToLower(filepath.Clean(primary)): true}
	seenRepos := map[string]bool{}
	repoKey := func(path string) string {
		origin, root := daemon.FingerprintOf(path)
		if origin == "" || root == "" {
			return ""
		}
		return strings.ToLower(origin + "|" + root)
	}
	if key := repoKey(primary); key != "" {
		seenRepos[key] = true
	}
	add := func(path string) {
		if len(roots) >= 20 || !filepath.IsAbs(path) {
			return
		}
		path = filepath.Clean(path)
		if seenPaths[strings.ToLower(path)] {
			return
		}
		if home, err := os.UserHomeDir(); err == nil && samePath(path, home) {
			return
		}
		if filepath.Dir(path) == path {
			return // drive or filesystem root
		}
		if st, err := os.Stat(path); err != nil || !st.IsDir() {
			return
		}
		if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
			return
		}
		key := repoKey(path)
		if key != "" && seenRepos[key] {
			return // another checkout/worktree of an already captured repo
		}
		seenPaths[strings.ToLower(path)] = true
		if key != "" {
			seenRepos[key] = true
		}
		roots = append(roots, path)
	}
	if saved, err := config.LoadFile(); err == nil {
		for _, path := range saved.RecentWorkspaces {
			add(path)
		}
	}
	codexHome := strings.TrimSpace(os.Getenv("CODEX_HOME"))
	if codexHome == "" {
		if home, err := os.UserHomeDir(); err == nil {
			codexHome = filepath.Join(home, ".codex")
		}
	}
	if codexHome == "" {
		return roots
	}
	for _, name := range []string{"sessions", "archived_sessions"} {
		count := 0
		_ = filepath.Walk(filepath.Join(codexHome, name), func(path string, info os.FileInfo, err error) error {
			if err != nil || info == nil || info.IsDir() || !strings.EqualFold(filepath.Ext(path), ".jsonl") {
				return nil
			}
			count++
			if count > 10000 || len(roots) >= 20 {
				return filepath.SkipAll
			}
			add(daemon.PeekTranscriptCwd(path))
			return nil
		})
	}
	return roots
}
