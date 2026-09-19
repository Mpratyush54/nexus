package adapters

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuditRegistryExpectedAgents(t *testing.T) {
	want := []string{"claude", "opencode", "cursor", "codex", "antigravity", "copilot", "codeium", "kimi", "windsurf", "gemini", "grok", "commandcode", "cagent", "zcode", "deepseek", "hermes"}
	got := map[string]bool{}
	for _, a := range Registry() {
		got[a.Name()] = true
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("Registry() missing adapter %q (got %v)", w, got)
		}
	}
	if len(Registry()) != len(want) {
		t.Errorf("Registry() len = %d, want %d", len(Registry()), len(want))
	}
}

func TestAuditKindOfTable(t *testing.T) {
	cases := []struct{ path, want string }{
		{"a/b/plan.md", "plan"},
		{"a/b/PLAN.MD", "config"}, // ext is case-sensitive: ".MD" != ".md"
		{"a/b/s.json", "session"},
		{"a/b/s.jsonl", "session"},
		{"a/b/s.db", "session"},
		{"a/b/s.vscdb", "session"},
		{"a/b/notes.txt", "config"},
		{"a/b/noext", "config"},
		{"a/b/settings.json.bak", "config"},
	}
	for _, tc := range cases {
		if got := kindOf(tc.path); got != tc.want {
			t.Errorf("kindOf(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestAuditLeafDirOfTable(t *testing.T) {
	// Explicit roots: deterministic on every GOOS (issue #147). Without
	// NEXUS_PROJECT_ROOTS the result is home-/platform-dependent and
	// cannot be pinned portably.
	root := t.TempDir()
	t.Setenv("NEXUS_PROJECT_ROOTS", root)
	cases := []struct{ leaf, want string }{
		{"", ""},
		{"global", ""},
		{"myproj", filepath.Join(root, "myproj")},
		{"a/b", filepath.Join(root, "a", "b")},
		{"a/b/c", filepath.Join(root, "a", "b", "c")},
	}
	for _, tc := range cases {
		if got := leafDirOf(tc.leaf); got != tc.want {
			t.Errorf("leafDirOf(%q) = %q, want %q", tc.leaf, got, tc.want)
		}
	}
}

func TestAuditSuffixTable(t *testing.T) {
	if got := suffix(""); got != "" {
		t.Errorf("suffix(%q) = %q, want empty", "", got)
	}
	if got := suffix("a/b"); got != " for project a/b" {
		t.Errorf("suffix project = %q", got)
	}
}

func TestAuditErrNoTargetMessage(t *testing.T) {
	err := errNoTarget{project: "a/b", target: `D:\a\b`}
	msg := err.Error()
	if !strings.Contains(msg, "a/b") || !strings.Contains(msg, `D:\a\b`) || !strings.Contains(msg, "same absolute path") {
		t.Errorf("errNoTarget message missing context: %q", msg)
	}
}

func TestAuditWriteManifestCreatesFile(t *testing.T) {
	vault := t.TempDir()
	agentDir := filepath.Join(vault, "agents", "testagent")
	if err := writeManifest(vault, "testagent", nil); err != nil {
		t.Fatalf("writeManifest: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(agentDir, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("manifest not JSON: %v", err)
	}
	if m["agent"] != "testagent" {
		t.Errorf("manifest agent = %v", m["agent"])
	}
}

func TestAuditGenericAdapterNameRawIndexPaths(t *testing.T) {
	g := genericAdapter{name: "unittest"}
	if g.Name() != "unittest" {
		t.Errorf("Name() = %q", g.Name())
	}
	vault := t.TempDir()
	if want := filepath.Join(vault, "agents", "unittest", "raw"); g.rawDir(vault) != want {
		t.Errorf("rawDir = %q want %q", g.rawDir(vault), want)
	}
	if want := filepath.Join(vault, "agents", "unittest", "index.json"); g.indexPath(vault) != want {
		t.Errorf("indexPath = %q want %q", g.indexPath(vault), want)
	}
	if got := g.Classify(Artifact{NativePath: filepath.Join("x", "session.json")}); got != ClassifyPath(filepath.Join("x", "session.json")) {
		t.Errorf("Classify should delegate to ClassifyPath")
	}
}

func TestAuditRootsExpandsEnvAndHome(t *testing.T) {
	t.Setenv("AUDIT_TEST_ROOT_XYZ", t.TempDir())
	g := genericAdapter{name: "r", agentDirs: []string{"someDir"}, absRoots: []string{"$AUDIT_TEST_ROOT_XYZ", "", "$DEFINITELY_UNSET_VAR_12345____/sub"}}
	roots := g.roots()
	found := false
	for _, r := range roots {
		if strings.Contains(r, "AUDIT_TEST_ROOT_XYZ") {
			t.Errorf("absRoot was not expanded: %q", r)
		}
		if r == os.Getenv("AUDIT_TEST_ROOT_XYZ") {
			found = true
		}
	}
	if !found {
		t.Errorf("expanded absRoot not in roots: %v", roots)
	}
}

func TestAuditDiscoverEmptyRoots(t *testing.T) {
	g := genericAdapter{name: "empty", agentDirs: nil, projectDot: nil, absRoots: []string{filepath.Join(t.TempDir(), "does-not-exist")}, maxBytes: 50 << 20}
	arts, err := g.Discover()
	if err != nil {
		t.Fatalf("Discover with missing roots should not error, got %v", err)
	}
	if len(arts) != 0 {
		t.Errorf("Discover with missing roots = %d artifacts, want 0", len(arts))
	}
}

func TestAuditDiscoverFindsBackupSkipsNever(t *testing.T) {
	clearAuditResolveCache(t)
	// Sources outside /tmp (issue #147): ClassifyPath ignores /tmp/
	// segments and Linux TempDir lives under /tmp.
	root := mksrc(t)
	if err := os.WriteFile(filepath.Join(root, "keep.json"), []byte(`{"cwd":"D:\\nowhere"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "my-secret-stuff.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	g := genericAdapter{name: "d", absRoots: []string{root}, maxBytes: 50 << 20}
	arts, err := g.Discover()
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, a := range arts {
		names[filepath.Base(a.NativePath)] = true
		if a.Agent != "d" {
			t.Errorf("Discover agent = %q want d", a.Agent)
		}
	}
	if !names["keep.json"] {
		t.Errorf("Discover should find keep.json, got %v", names)
	}
	if names["my-secret-stuff.json"] {
		t.Errorf("Discover should skip NEVER file my-secret-stuff.json")
	}
}

func TestAuditExportWritesIndexAndManifest(t *testing.T) {
	clearAuditResolveCache(t)
	root := mksrc(t)
	content := []byte("hello export")
	if err := os.WriteFile(filepath.Join(root, "s.json"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	vault := t.TempDir()
	g := genericAdapter{name: "expagent", absRoots: []string{root}, maxBytes: 50 << 20}
	if err := g.Export(vault); err != nil {
		t.Fatalf("Export: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(vault, "agents", "expagent", "index.json"))
	if err != nil {
		t.Fatalf("index.json missing: %v", err)
	}
	var idx struct {
		Agent string     `json:"agent"`
		Files []Artifact `json:"files"`
	}
	if err := json.Unmarshal(data, &idx); err != nil {
		t.Fatalf("index.json bad JSON: %v", err)
	}
	if idx.Agent != "expagent" {
		t.Errorf("index agent = %q", idx.Agent)
	}
	if len(idx.Files) != 1 {
		t.Fatalf("index files = %d, want 1: %+v", len(idx.Files), idx.Files)
	}
	if idx.Files[0].Kind != "session" {
		t.Errorf("exported kind = %q want session", idx.Files[0].Kind)
	}
	if _, err := os.Stat(idx.Files[0].RawPath); err != nil {
		t.Errorf("raw copy missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(vault, "agents", "expagent", "manifest.json")); err != nil {
		t.Errorf("manifest.json missing: %v", err)
	}
}

func TestAuditExportSkipsLargeFiles(t *testing.T) {
	clearAuditResolveCache(t)
	root := mksrc(t)
	if err := os.WriteFile(filepath.Join(root, "big.json"), []byte("1234567890"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "small.json"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	vault := t.TempDir()
	g := genericAdapter{name: "largeagent", absRoots: []string{root}, maxBytes: 5}
	if err := g.Export(vault); err != nil {
		t.Fatalf("Export: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(vault, "agents", "largeagent", "index.json"))
	var idx struct {
		Files        []Artifact `json:"files"`
		SkippedLarge []Artifact `json:"skipped_large"`
	}
	if err := json.Unmarshal(data, &idx); err != nil {
		t.Fatal(err)
	}
	if len(idx.Files) != 1 || filepath.Base(idx.Files[0].NativePath) != "small.json" {
		t.Errorf("expected only small.json copied, got %+v", idx.Files)
	}
	if len(idx.SkippedLarge) != 1 || filepath.Base(idx.SkippedLarge[0].NativePath) != "big.json" {
		t.Errorf("expected big.json in skipped_large, got %+v", idx.SkippedLarge)
	}
}

// Error propagation: Export to a path that cannot be a vault must fail.
func TestAuditExportErrorPropagation(t *testing.T) {
	clearAuditResolveCache(t)
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "s.json"), []byte("x"), 0o644)
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("file, not dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	g := genericAdapter{name: "erragent", absRoots: []string{root}, maxBytes: 50 << 20}
	// vault/agents/erragent collides with an existing file -> MkdirAll/WriteFile must error.
	vault := filepath.Join(blocker, "sub")
	if err := g.Export(vault); err == nil {
		t.Errorf("Export with unwritable vault %q should return error, got nil (error swallowed)", vault)
	}
}

// Normalize error propagation: vault that is a file must fail.
func TestAuditNormalizeErrorPropagation(t *testing.T) {
	g := genericAdapter{name: "normerr", absRoots: []string{t.TempDir()}, maxBytes: 50 << 20}
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("file"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := g.Normalize(filepath.Join(blocker, "sub")); err == nil {
		t.Errorf("Normalize with unwritable vault should return error, got nil")
	}
}

func TestAuditNormalizeWritesSessionsAndTranscript(t *testing.T) {
	clearAuditResolveCache(t)
	root := mksrc(t)
	_ = os.WriteFile(filepath.Join(root, "a.json"), []byte("{}"), 0o644)
	vault := t.TempDir()
	g := genericAdapter{name: "normagent", absRoots: []string{root}, maxBytes: 50 << 20}
	if err := g.Normalize(vault); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	ndir := filepath.Join(vault, "agents", "normagent", "normalized")
	if _, err := os.Stat(filepath.Join(ndir, "sessions.jsonl")); err != nil {
		t.Errorf("sessions.jsonl missing: %v", err)
	}
	md, err := os.ReadFile(filepath.Join(ndir, "transcript.md"))
	if err != nil {
		t.Fatalf("transcript.md missing: %v", err)
	}
	if !strings.Contains(string(md), "normagent") {
		t.Errorf("transcript.md should mention agent name: %q", md)
	}
}

func TestAuditRestoreMissingIndexErrors(t *testing.T) {
	g := genericAdapter{name: "noexport"}
	if err := g.Restore(t.TempDir(), "", ""); err == nil {
		t.Errorf("Restore with no index.json should error, got nil")
	} else if !strings.Contains(err.Error(), "nothing exported") {
		t.Errorf("Restore missing-index error should mention 'nothing exported', got %q", err)
	}
}

func TestAuditRestoreRejectsMissingAbsolutePath(t *testing.T) {
	g := genericAdapter{name: "r"}
	err := g.Restore(t.TempDir(), "definitely-not-a-real-project-xyz123", "")
	if err == nil {
		t.Errorf("Restore with missing D:\\<project> should return errNoTarget, got nil")
	}
	if _, ok := err.(errNoTarget); ok {
		// correct type
	} else if err != nil && strings.Contains(err.Error(), "same absolute path") {
		// acceptable: wrapped errNoTarget message
	} else {
		t.Errorf("Restore error type = %T (%v), want errNoTarget", err, err)
	}
}

func TestAuditRestoreRoundTrip(t *testing.T) {
	clearAuditResolveCache(t)
	root := mksrc(t)
	native := filepath.Join(root, "note.json")
	orig := []byte(`{"hello":1}`)
	if err := os.WriteFile(native, orig, 0o644); err != nil {
		t.Fatal(err)
	}
	vault := t.TempDir()
	g := genericAdapter{name: "rtagent", absRoots: []string{root}, maxBytes: 50 << 20}
	if err := g.Export(vault); err != nil {
		t.Fatalf("Export: %v", err)
	}
	// Corrupt native, restore with empty project (all files).
	if err := os.WriteFile(native, []byte("corrupted"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := g.Restore(vault, "", ""); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	got, _ := os.ReadFile(native)
	if string(got) != string(orig) {
		t.Errorf("Restore round-trip = %q want %q", got, orig)
	}
	// Pre-restore backup should exist.
	matches, _ := filepath.Glob(native + ".pre-restore-*")
	if len(matches) == 0 {
		t.Errorf("Restore should leave <path>.pre-restore-TIMESTAMP backup")
	}
}

func TestAuditRestoreCorruptIndexErrors(t *testing.T) {
	vault := t.TempDir()
	g := genericAdapter{name: "corruptidx"}
	_ = os.MkdirAll(filepath.Join(vault, "agents", "corruptidx"), 0o755)
	_ = os.WriteFile(filepath.Join(vault, "agents", "corruptidx", "index.json"), []byte("not json{"), 0o644)
	if err := g.Restore(vault, "", ""); err == nil {
		t.Errorf("Restore with corrupt index.json should error, got nil")
	}
}
