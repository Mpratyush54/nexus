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
