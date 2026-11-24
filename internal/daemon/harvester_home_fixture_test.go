package daemon

// End-to-end harvester checks against a fake home directory, written so the
// same test proves the real per-OS layout on Linux, macOS and Windows CI:
//
//   - Claude Code: ~/.claude/projects/<encoded cwd>/<session>.jsonl
//   - Codex:       ~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl (cwd inside)
//   - Cursor:      <AppData>/Cursor/User/workspaceStorage/<hash>/state.vscdb
//     where <AppData> is %APPDATA%, ~/Library/Application Support or
//     ~/.config depending on the OS.

import (
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"central-memory/internal/platform"
)

// fakeHome points every home lookup at a fresh temp dir and returns it.
func fakeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(home); err == nil {
		home = resolved // macOS: /var -> /private/var
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("NEXUS_PROJECT_ROOTS", "")
	if runtime.GOOS == "windows" {
		t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
		t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	} else {
		t.Setenv("APPDATA", "")
		t.Setenv("LOCALAPPDATA", "")
	}
	return home
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// claudeProjectDir encodes an absolute path the way Claude Code names its
// per-project folders: every non-alphanumeric character becomes "-".
func claudeProjectDir(abs string) string {
	var b strings.Builder
	for _, r := range abs {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	return b.String()
}

func fileURI(abs string) string {
	p := filepath.ToSlash(abs)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p // C:/x -> /C:/x
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
}

func jsonPath(p string) string { return strings.ReplaceAll(p, `\`, `\\`) }

func TestHarvesterFakeHomeAllAgents(t *testing.T) {
	home := fakeHome(t)
	ws := filepath.Join(home, "work", "macproj")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}

	// Claude Code transcript.
	claude := filepath.Join(home, ".claude", "projects", claudeProjectDir(ws), "11111111-2222-3333-4444-555555555555.jsonl")
	mustWrite(t, claude,
		`{"type":"user","cwd":"`+jsonPath(ws)+`","message":{"role":"user","content":"claude fixture: use sqlite for the cache"},"timestamp":"2026-11-25T10:00:00Z"}`+"\n"+
			`{"type":"assistant","cwd":"`+jsonPath(ws)+`","message":{"role":"assistant","content":[{"type":"text","text":"claude fixture: agreed, sqlite it is"}]},"timestamp":"2026-11-25T10:00:05Z"}`+"\n")

	// Codex rollout: date-sharded, project only known from the cwd inside.
	codex := filepath.Join(home, ".codex", "sessions", "2026", "11", "25", "rollout-2026-11-25T10-00-00-abc.jsonl")
	mustWrite(t, codex,
		`{"type":"session_meta","payload":{"id":"abc","cwd":"`+jsonPath(ws)+`"}}`+"\n"+
			`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"codex fixture: rename the flag"}]}}`+"\n"+
			`{"type":"event_msg","payload":{"type":"agent_message","message":"codex fixture: renamed"}}`+"\n")

	// Codex rollout for a different project must not be captured.
	mustWrite(t, filepath.Join(home, ".codex", "sessions", "2026", "11", "25", "rollout-other.jsonl"),
		`{"type":"session_meta","payload":{"id":"zzz","cwd":"`+jsonPath(filepath.Join(home, "work", "unrelated"))+`"}}`+"\n"+
			`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"other project"}]}}`+"\n")

	// Cursor workspace DB in the OS's app-data folder, attributed through
	// workspace.json (the hash folder name says nothing about the project).
	cursorWS := filepath.Join(platform.AppDataDir(), "Cursor", "User", "workspaceStorage", "0123456789abcdef")
	mustWrite(t, filepath.Join(cursorWS, "workspace.json"), `{"folder":"`+fileURI(ws)+`"}`)
	cursorDB := filepath.Join(cursorWS, "state.vscdb")
	mustWrite(t, cursorDB, "not really sqlite")

	h, got := collectHarvester(ws)
	files, turns, err := h.ScanAndTailCounted()
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	st := h.Stats()
	byPath := map[string]string{}
	for _, hit := range st.RecentFiles {
		byPath[hit.Path] = hit.Agent
	}
	t.Logf("home=%s appdata=%s files=%d turns=%d agents=%v", home, platform.AppDataDir(), files, turns, st.AgentFiles)
	for p, a := range byPath {
		t.Logf("hit %s -> %s", a, p)
	}

	if byPath[claude] != "claude" {
		t.Errorf("claude transcript not harvested: %s", claude)
	}
	if byPath[codex] != "codex" {
		t.Errorf("codex rollout not harvested: %s", codex)
	}
	if _, ok := byPath[filepath.Join(filepath.Dir(codex), "rollout-other.jsonl")]; ok {
		t.Errorf("unrelated codex rollout was attributed to this workspace")
	}
	if a := byPath[cursorDB]; a != "cursor" && a != "copilot" {
		t.Errorf("cursor workspace DB under %s not tracked (agent=%q)", platform.AppDataDir(), a)
	}

	var claudeTurns, codexTurns int
	for _, ev := range *got {
		c, _ := ev.Payload["content"].(string)
		switch {
		case strings.HasPrefix(c, "claude fixture"):
			claudeTurns++
		case strings.HasPrefix(c, "codex fixture"):
			codexTurns++
		case c == "other project":
			t.Errorf("turn from unrelated project emitted: %+v", ev.Payload)
		}
	}
	if claudeTurns != 2 {
		t.Errorf("claude turns = %d, want 2", claudeTurns)
	}
	if codexTurns != 2 {
		t.Errorf("codex turns = %d, want 2", codexTurns)
	}
}

func TestResolveSourcesUsesOSAppDataDir(t *testing.T) {
	home := fakeHome(t)
	want := map[string]string{
		"darwin": filepath.Join(home, "Library", "Application Support", "Cursor", "User", "workspaceStorage"),
		"linux":  filepath.Join(home, ".config", "Cursor", "User", "workspaceStorage"),
		// windows: APPDATA was pointed into the fake home by fakeHome.
		"windows": filepath.Join(home, "AppData", "Roaming", "Cursor", "User", "workspaceStorage"),
	}[runtime.GOOS]
	if want == "" {
		t.Skip("no expectation for " + runtime.GOOS)
	}
	for _, s := range ResolveSources() {
		for _, d := range s.Dirs {
			if d == want {
				return
			}
		}
	}
	t.Fatalf("ResolveSources has no %s", want)
}

// On macOS and Linux a workspace.json folder is a POSIX path. It used to be
// rewritten with Windows backslashes and never matched.
func TestMatchesTranscriptWorkspaceJSONPosix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX folder URIs")
	}
	home := fakeHome(t)
	ws := filepath.Join(home, "Documents", "posixproj")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	hash := filepath.Join(home, "Library", "Application Support", "Cursor", "User", "workspaceStorage", "feedface")
	mustWrite(t, filepath.Join(hash, "workspace.json"), `{"folder":"`+fileURI(ws)+`"}`)
	db := filepath.Join(hash, "state.vscdb")
	mustWrite(t, db, "x")

	h := NewHarvester(ws, nil)
	if !h.MatchesTranscript(db, false) {
		t.Fatal("workspace.json naming this workspace must match")
	}
	other := NewHarvester(filepath.Join(home, "Documents", "someother"), nil)
	if other.MatchesTranscript(db, false) {
		t.Fatal("workspace.json for another folder must not match")
	}
}
