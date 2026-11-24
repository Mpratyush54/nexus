package platform

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// A macOS app started from Finder, the Dock or at login inherits launchd's
// PATH, /usr/bin:/bin:/usr/sbin:/sbin, not the user's shell PATH. Agent CLIs
// (claude, codex, opencode, cursor-agent, gemini) and the node they run on
// live in Homebrew, npm or per-user folders, so exec.LookPath would report
// every agent as missing although it works in Terminal.
//
// AugmentGUIPath appends the usual install folders that exist on this
// machine to PATH. Folders already on PATH keep their position, so a PATH
// inherited from a terminal still wins. It changes nothing off macOS.
func AugmentGUIPath() {
	if runtime.GOOS != "darwin" {
		return
	}
	home, _ := userHomeDirFunc()
	next := augmentedPath(os.Getenv("PATH"), guiPathCandidates(home), dirExists)
	if next != os.Getenv("PATH") {
		_ = os.Setenv("PATH", next)
	}
}

func guiPathCandidates(home string) []string {
	out := []string{"/opt/homebrew/bin", "/opt/homebrew/sbin", "/usr/local/bin", "/opt/local/bin"}
	if strings.TrimSpace(home) == "" {
		return out
	}
	for _, rel := range []string{
		".local/bin",      // claude native installer, cursor-agent, pipx
		".claude/local",   // older claude local installs
		".npm-global/bin", // npm prefix ~/.npm-global
		".bun/bin",        // bun global installs
		".volta/bin",      // volta node shims
		".opencode/bin",   // opencode installer
		".cargo/bin",      // rust-built CLIs
		"go/bin",          // go install
		"Library/pnpm",    // pnpm global
		".deno/bin",       // deno
	} {
		out = append(out, filepath.Join(home, filepath.FromSlash(rel)))
	}
	return out
}

func augmentedPath(current string, candidates []string, exists func(string) bool) string {
	have := map[string]bool{}
	parts := []string{}
	for _, p := range filepath.SplitList(current) {
		if p == "" {
			continue
		}
		parts = append(parts, p)
		have[filepath.Clean(p)] = true
	}
	for _, c := range candidates {
		if have[filepath.Clean(c)] || !exists(c) {
			continue
		}
		parts = append(parts, c)
		have[filepath.Clean(c)] = true
	}
	return strings.Join(parts, string(os.PathListSeparator))
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}
