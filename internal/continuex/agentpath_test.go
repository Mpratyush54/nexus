package continuex

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// macHome lays out a home folder the way the common macOS installers leave
// it. Pure file fixtures, so it runs on every OS.
func macHome(t *testing.T) string {
	home := t.TempDir()
	write(t, home, ".npm-global/bin/codex", nodeScript)
	write(t, home, ".local/bin/claude", "\xcf\xfa\xed\xfe") // Mach-O magic: the native installer
	write(t, home, ".bun/bin/opencode", "\xcf\xfa\xed\xfe")
	write(t, home, ".volta/bin/gemini", "\xcf\xfa\xed\xfe")
	write(t, home, ".nvm/versions/node/v18.20.4/bin/node", "\xcf\xfa\xed\xfe")
	write(t, home, ".nvm/versions/node/v22.11.0/bin/node", "\xcf\xfa\xed\xfe")
	write(t, home, ".nvm/versions/node/v22.11.0/bin/kimi", nodeScript)
	write(t, home, ".nvm/versions/node/v9.11.2/bin/node", "\xcf\xfa\xed\xfe")
	return home
}

func TestUnixAgentDirsCoverMacInstallers(t *testing.T) {
	home := macHome(t)
	env := map[string]string{"BUN_INSTALL": filepath.Join(home, ".bun")}
	dirs := unixAgentDirs(home, func(k string) string { return env[k] })
	want := []string{
		filepath.Join(home, ".local", "bin"),
		filepath.Join(home, ".claude", "local"),
		filepath.Join(home, ".npm-global", "bin"),
		filepath.Join(home, ".bun", "bin"),
		filepath.Join(home, ".volta", "bin"),
		filepath.Join(home, "Library", "pnpm"),
		filepath.Join(home, ".asdf", "shims"),
		filepath.Join(home, ".local", "share", "mise", "shims"),
		filepath.Join(home, ".nvm", "versions", "node", "v22.11.0", "bin"),
		filepath.FromSlash("/opt/homebrew/bin"),
		filepath.FromSlash("/usr/local/bin"),
	}
	joined := "\n" + strings.Join(dirs, "\n") + "\n"
	for _, w := range want {
		if !strings.Contains(joined, "\n"+w+"\n") {
			t.Errorf("missing %s in\n%s", w, joined)
		}
	}
	if strings.Index(joined, filepath.FromSlash("/opt/homebrew/bin")) < strings.Index(joined, filepath.Join(home, ".local", "bin")) {
		t.Error("per-user folders should come before Homebrew")
	}
}

func TestNvmNewestFirstAndDefaultAlias(t *testing.T) {
	home := macHome(t)
	nvm := filepath.Join(home, ".nvm")
	got := nvmBinDirs(nvm)
	if len(got) != 3 || !strings.Contains(got[0], "v22.11.0") || !strings.Contains(got[1], "v18.20.4") || !strings.Contains(got[2], "v9.11.2") {
		t.Fatalf("newest first: %v", got)
	}
	write(t, home, ".nvm/alias/default", "18\n")
	got = nvmBinDirs(nvm)
	if !strings.Contains(got[0], "v18.20.4") || len(got) != 3 {
		t.Fatalf("default alias first: %v", got)
	}
}

func TestLookAgentFindsMacInstallsWithoutShellPATH(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("execute bits and extensionless launchers are macOS/Linux")
	}
	home := macHome(t)
	dirs := unixAgentDirs(home, func(string) string { return "" })
	missing := func(string) (string, error) { return "", errors.New("not on PATH") }
	for agent, rel := range map[string]string{
		"codex":    ".npm-global/bin/codex",
		"claude":   ".local/bin/claude",
		"opencode": ".bun/bin/opencode",
		"gemini":   ".volta/bin/gemini",
		"kimi":     ".nvm/versions/node/v22.11.0/bin/kimi",
	} {
		got, err := lookAgentIn(agent, missing, dirs)
		if err != nil || got != filepath.Join(home, filepath.FromSlash(rel)) {
			t.Errorf("%s: got %q %v", agent, got, err)
		}
	}
}

func TestPathWithDirsAppendsExistingOnce(t *testing.T) {
	home := macHome(t)
	npm := filepath.Join(home, ".npm-global", "bin")
	nvm := filepath.Join(home, ".nvm", "versions", "node", "v22.11.0", "bin")
	sep := string(os.PathListSeparator)
	current := strings.Join([]string{"/usr/bin", "/bin", npm}, sep)
	got := pathWithDirs(current, []string{npm, filepath.Join(home, "absent"), nvm, nvm})
	want := current + sep + nvm
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestInstallFoldersHintNamesThisOS(t *testing.T) {
	hint := installFoldersHint()
	if runtime.GOOS == "darwin" && (!strings.Contains(hint, "Homebrew") || strings.Contains(hint, "scoop")) {
		t.Fatalf("mac hint %q", hint)
	}
	if runtime.GOOS == "windows" && strings.Contains(hint, "Homebrew") {
		t.Fatalf("windows hint %q", hint)
	}
}

// The npm/Homebrew global layout on macOS and Linux: <prefix>/bin/<agent> is
// a symlink into <prefix>/lib/node_modules, the launcher is a
// `#!/usr/bin/env node` script and node sits in <prefix>/bin. The agent is
// found without the shell's PATH, started as is, and node's folder ends up
// on PATH so `env node` resolves.
func TestUnixNpmGlobalSymlinkLayouts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinked launchers and execute bits are macOS/Linux")
	}
	for _, layout := range []struct{ name, prefix, pkg, platform string }{
		{"homebrew npm (darwin arm64)", "opt/homebrew", "opencode-ai", "opencode-darwin-arm64"},
		{"npm-global (linux x64)", ".npm-global", "opencode-ai", "opencode-linux-x64"},
	} {
		root := t.TempDir()
		prefix := filepath.Join(root, filepath.FromSlash(layout.prefix))
		write(t, prefix, "bin/node", "\xcf\xfa\xed\xfe")
		write(t, prefix, "lib/node_modules/"+layout.pkg+"/package.json", `{"name":"`+layout.pkg+`"}`)
		write(t, prefix, "lib/node_modules/"+layout.pkg+"/bin/opencode", nodeScript)
		write(t, prefix, "lib/node_modules/"+layout.pkg+"/node_modules/"+layout.platform+"/bin/opencode", "\xcf\xfa\xed\xfe")
		link := filepath.Join(prefix, "bin", "opencode")
		if err := os.Symlink(filepath.Join("..", "lib", "node_modules", layout.pkg, "bin", "opencode"), link); err != nil {
			t.Fatal(err)
		}
		missing := func(string) (string, error) { return "", errors.New("not on PATH") }
		got, err := lookAgentIn("opencode", missing, []string{filepath.Join(root, "nothing"), filepath.Join(prefix, "bin")})
		if err != nil || got != link {
			t.Fatalf("%s: lookup got %q %v", layout.name, got, err)
		}
		exe, prefixArgs, err := directExecutable(got, "opencode")
		if err != nil || exe != link || len(prefixArgs) != 0 {
			t.Fatalf("%s: start %q %v %v", layout.name, exe, prefixArgs, err)
		}
		path := pathWithDirs("/usr/bin:/bin", []string{filepath.Join(prefix, "bin")})
		if !strings.HasSuffix(path, ":"+filepath.Join(prefix, "bin")) {
			t.Fatalf("%s: node's folder not on PATH: %q", layout.name, path)
		}
	}
}

// pnpm on macOS puts launchers in ~/Library/pnpm (PNPM_HOME); Homebrew
// formulae in /opt/homebrew/bin. Both are searched without the shell's PATH.
func TestUnixAgentDirsIncludePnpmAndHomebrew(t *testing.T) {
	home := t.TempDir()
	dirs := unixAgentDirs(home, func(k string) string {
		if k == "PNPM_HOME" {
			return filepath.Join(home, "Library", "pnpm")
		}
		return ""
	})
	joined := strings.Join(dirs, "\n")
	for _, want := range []string{filepath.Join(home, "Library", "pnpm"), filepath.FromSlash("/opt/homebrew/bin")} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %s", want)
		}
	}
}
