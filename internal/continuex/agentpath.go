package continuex

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// A Mac app opened from Finder, the Dock or a login item does not get the
// PATH of the user's shell: it starts with /usr/bin:/bin:/usr/sbin:/sbin.
// Agents installed with Homebrew, npm (global prefix or ~/.npm-global), bun,
// Volta, nvm, fnm, asdf, mise or the Claude installer then look "not
// installed", and npm launchers that start with `#!/usr/bin/env node` fail
// because node isn't on that PATH either. unixAgentDirs lists those folders
// and ensureAgentPATH appends the ones that exist to this process's PATH once,
// so lookups, `env node` and anything the agent itself starts all see them.

// unixAgentDirs are the install folders for agent CLIs (and node) on macOS
// and Linux, most specific first. env reads environment variables.
func unixAgentDirs(home string, env func(string) string) []string {
	var dirs []string
	add := func(parts ...string) {
		if len(parts) == 0 || parts[0] == "" {
			return
		}
		dirs = append(dirs, filepath.Join(parts...))
	}
	if v := env("NPM_CONFIG_PREFIX"); v != "" {
		add(v, "bin")
	}
	if v := env("PNPM_HOME"); v != "" {
		add(v)
	}
	if v := env("BUN_INSTALL"); v != "" {
		add(v, "bin")
	}
	if v := env("VOLTA_HOME"); v != "" {
		add(v, "bin")
	}
	if home != "" {
		add(home, ".local", "bin") // Claude, Kimi and pipx installers
		add(home, ".claude", "local")
		add(home, ".npm-global", "bin")
		add(home, ".npm-packages", "bin")
		add(home, ".bun", "bin")
		add(home, ".volta", "bin")
		add(home, ".opencode", "bin")
		add(home, ".kimi-code", "bin")
		add(home, ".cargo", "bin")
		add(home, ".yarn", "bin")
		add(home, "Library", "pnpm")
		add(home, ".local", "share", "pnpm")
		add(home, ".asdf", "shims")
		add(home, ".local", "share", "mise", "shims")
		add(home, ".local", "share", "fnm", "aliases", "default", "bin")
		add(home, "Library", "Application Support", "fnm", "aliases", "default", "bin")
	}
	nvm := env("NVM_DIR")
	if nvm == "" && home != "" {
		nvm = filepath.Join(home, ".nvm")
	}
	if nvm != "" {
		dirs = append(dirs, nvmBinDirs(nvm)...)
	}
	add("/opt/homebrew/bin")
	add("/usr/local/bin")
	add("/opt/local/bin") // MacPorts
	return dirs
}

// nvmBinDirs returns <nvm>/versions/node/<v>/bin, the default alias's
// version first, then newest first.
func nvmBinDirs(nvm string) []string {
	matches, _ := filepath.Glob(filepath.Join(nvm, "versions", "node", "v*", "bin"))
	sort.SliceStable(matches, func(i, j int) bool {
		return versionLess(filepath.Base(filepath.Dir(matches[j])), filepath.Base(filepath.Dir(matches[i])))
	})
	if raw, err := os.ReadFile(filepath.Join(nvm, "alias", "default")); err == nil {
		want := strings.TrimPrefix(strings.TrimSpace(string(raw)), "v")
		for i, m := range matches {
			v := strings.TrimPrefix(filepath.Base(filepath.Dir(m)), "v")
			if want != "" && (v == want || strings.HasPrefix(v, want+".")) {
				return append([]string{m}, append(append([]string{}, matches[:i]...), matches[i+1:]...)...)
			}
		}
	}
	return matches
}

// versionLess compares "v20.11.1"-style names numerically.
func versionLess(a, b string) bool {
	pa := strings.Split(strings.TrimPrefix(a, "v"), ".")
	pb := strings.Split(strings.TrimPrefix(b, "v"), ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			return x < y
		}
	}
	return false
}

// pathWithDirs appends the dirs that exist and are not already listed.
func pathWithDirs(current string, dirs []string) string {
	seen := map[string]bool{}
	parts := filepath.SplitList(current)
	for _, p := range parts {
		seen[filepath.Clean(p)] = true
	}
	for _, d := range dirs {
		d = filepath.Clean(d)
		if seen[d] {
			continue
		}
		if info, err := os.Stat(d); err != nil || !info.IsDir() {
			continue
		}
		seen[d] = true
		parts = append(parts, d)
	}
	return strings.Join(parts, string(os.PathListSeparator))
}

var agentPATHOnce sync.Once

// ensureAgentPATH widens PATH on macOS and Linux (see above). Windows apps
// inherit the user's PATH, so it does nothing there.
func ensureAgentPATH() {
	if runtime.GOOS == "windows" {
		return
	}
	agentPATHOnce.Do(func() {
		home, _ := os.UserHomeDir()
		current := os.Getenv("PATH")
		if next := pathWithDirs(current, unixAgentDirs(home, os.Getenv)); next != current {
			_ = os.Setenv("PATH", next)
		}
	})
}

// installFoldersHint names the usual install folders for this OS.
func installFoldersHint() string {
	switch runtime.GOOS {
	case "windows":
		return "npm, pnpm, bun, scoop, Volta, Chocolatey"
	case "darwin":
		return "Homebrew, npm, pnpm, bun, Volta, nvm, ~/.local/bin"
	default:
		return "npm, pnpm, bun, Volta, nvm, ~/.local/bin"
	}
}
