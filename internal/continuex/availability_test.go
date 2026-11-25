package continuex

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestMissingHarnessReturnsInstallInstructionsWithoutLaunching(t *testing.T) {
	for _, agent := range []string{"codex", "opencode", "cursor", "claude", "gemini", "copilot"} {
		called := 0
		status := detectHarness(agent, func(string) (string, error) { called++; return "", errors.New("not installed") })
		if status.Installed || status.Problem == "" || status.InstallURL == "" || called != 1 {
			t.Fatalf("%s: %+v", agent, status)
		}
	}
}

func TestCodexNpmShimResolvesNativeExecutableWithoutShell(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows npm layout")
	}
	dir := t.TempDir()
	native := filepath.Join(dir, "node_modules", "@openai", "codex", "node_modules", "@openai", "codex-win32-x64", "vendor", "x86_64-pc-windows-msvc", "codex", "codex.exe")
	if err := os.MkdirAll(filepath.Dir(native), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(native, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	resolved, prefix, err := directExecutable(filepath.Join(dir, "codex.cmd"), "codex")
	if err != nil || resolved != native || len(prefix) != 0 {
		t.Fatalf("resolved=%q prefix=%v err=%v", resolved, prefix, err)
	}
}

func TestCodexResumeUsesThreadIDNotRolloutFilename(t *testing.T) {
	const id = "01a14557-98bf-71a2-a9d6-b532e7e0c2bc"
	plan, err := Plan("codex", "open_in_agent", "rollout-2026-10-16T21-02-11-"+id+".jsonl", "")
	if err != nil || plan.Argv[2] != id {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
}
