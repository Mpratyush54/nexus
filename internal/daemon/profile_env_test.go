package daemon

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"central-memory/internal/config"
	"central-memory/internal/platform"
)

// TestProfileEnvironmentKeepsCoreInsideRoot pins the contract the desktop
// shell's NEXUS_PROFILE relies on (desktop/windows/Core/ProfilePaths.cs):
// the shell points USERPROFILE, HOME, APPDATA, LOCALAPPDATA and TEMP at a
// profile root and clears the explicit overrides, and from then on every
// per-user folder the in-process core reads (config, cache, and every
// agent's transcript root) must resolve inside that root.
func TestProfileEnvironmentKeepsCoreInsideRoot(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the profile layout mirrors the Windows per-user folders")
	}
	root := t.TempDir()
	local := filepath.Join(root, "AppData", "Local")
	for key, value := range map[string]string{
		"USERPROFILE":  root,
		"HOME":         root,
		"APPDATA":      filepath.Join(root, "AppData", "Roaming"),
		"LOCALAPPDATA": local,
		"TEMP":         filepath.Join(local, "Temp"),
		"TMP":          filepath.Join(local, "Temp"),
	} {
		t.Setenv(key, value)
	}
	for _, key := range []string{"NEXUS_CONFIG_DIR", "NEXUS_CACHE_DIR", "NEXUS_PROJECT_ROOTS", "CENTRAL_MEMORY_CONFIG_DIR", "CODEX_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME"} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}

	inside := func(what, path string) {
		t.Helper()
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			t.Errorf("%s resolves outside the profile root: %s", what, path)
		}
	}

	if home, err := os.UserHomeDir(); err != nil {
		t.Fatal(err)
	} else {
		inside("home", home)
	}
	if path, err := config.ConfigPath(); err != nil {
		t.Fatal(err)
	} else {
		inside("config.json", path)
	}
	if dir, err := platform.ConfigDir(); err != nil {
		t.Fatal(err)
	} else {
		inside("platform config dir", dir)
	}
	if dir, err := platform.CacheDir(); err != nil {
		t.Fatal(err)
	} else {
		inside("platform cache dir", dir)
	}
	for _, root := range platform.ProjectRoots() {
		inside("project root", root)
	}
	inside("temp", os.TempDir())

	sources := ResolveSources()
	if len(sources) == 0 {
		t.Fatal("no transcript sources resolved")
	}
	for _, src := range sources {
		for _, dir := range src.Dirs {
			inside(src.Agent+" transcripts", dir)
		}
	}
}
