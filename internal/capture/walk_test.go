package capture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"central-memory/internal/blobs"
)

func TestCaptureWalkRestoreSmallFiles(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, root, "README.md", "# hi\n")
	mustWrite(t, root, "src/main.go", "package main\n")

	store := blobs.NewByteaStore()
	man, err := CaptureWalk(root, store)
	if err != nil {
		t.Fatal(err)
	}
	if man.NewUploads != 2 {
		t.Fatalf("new uploads = %d want 2", man.NewUploads)
	}
	if len(man.Files) != 2 {
		t.Fatalf("files = %+v", man.Files)
	}
	if len(man.Refuse) != 0 || len(man.Rebuild) != 0 {
		t.Fatalf("unexpected refuse/rebuild: %+v", man)
	}

	dest := t.TempDir()
	rep, err := RestoreTree(dest, man, store)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Verified != 2 {
		t.Fatalf("verified = %d", rep.Verified)
	}
	got, err := os.ReadFile(filepath.Join(dest, "src", "main.go"))
	if err != nil || string(got) != "package main\n" {
		t.Fatalf("restored main.go: %q %v", got, err)
	}
}

func TestCaptureWalkIgnoresNodeModulesRebuildRecipe(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, root, "package.json", `{"name":"demo","version":"1.0.0"}`+"\n")
	mustWrite(t, root, "package-lock.json", `{"lockfileVersion":3,"packages":{}}`+"\n")
	mustWrite(t, root, "index.js", "console.log('ok')\n")
	mustWrite(t, root, "node_modules/leftpad/index.js", "module.exports=1\n")

	store := blobs.NewByteaStore()
	man, err := CaptureWalk(root, store)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range man.Files {
		if strings.HasPrefix(f.Path, "node_modules") {
			t.Fatalf("node_modules should be excluded: %s", f.Path)
		}
	}
	if len(man.Exclude) == 0 {
		t.Fatal("expected exclude for node_modules")
	}
	foundNode := false
	for _, e := range man.Exclude {
		if strings.HasPrefix(e, "node_modules") {
			foundNode = true
			break
		}
	}
	if !foundNode {
		t.Fatalf("exclude = %v", man.Exclude)
	}
	if len(man.Rebuild) != 1 || man.Rebuild[0].Rule != "default:node" {
		t.Fatalf("rebuild = %+v", man.Rebuild)
	}
	if man.Rebuild[0].Lockfile != "package-lock.json" {
		t.Fatalf("lockfile = %s", man.Rebuild[0].Lockfile)
	}
	if man.Rebuild[0].Command != "npm install --frozen-lockfile" {
		t.Fatalf("command = %s", man.Rebuild[0].Command)
	}

	dest := t.TempDir()
	rep, err := RestoreTree(dest, man, store)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Verified != len(man.Files) {
		t.Fatalf("verified = %d files = %d", rep.Verified, len(man.Files))
	}
	if len(rep.Rebuilds) != 1 {
		t.Fatalf("rebuilds = %+v", rep.Rebuilds)
	}
	// npm is present in this environment; allow either ran or planned.
	if rep.Rebuilds[0].Command != "npm install --frozen-lockfile" {
		t.Fatalf("rebuild command = %s", rep.Rebuilds[0].Command)
	}
	if _, err := os.Stat(filepath.Join(dest, "node_modules")); err == nil {
		// install may have created node_modules; that is fine
	}
	// Non-regenerable files must match after restore regardless of rebuild.
	body, err := os.ReadFile(filepath.Join(dest, "index.js"))
	if err != nil || string(body) != "console.log('ok')\n" {
		t.Fatalf("index.js: %q %v", body, err)
	}
}

func TestCaptureWalkRefuseHugeViaSizeField(t *testing.T) {
	store := blobs.NewByteaStore()
	entries := []FileEntry{
		{Rel: "ok.txt", Size: 3},
		{Rel: "huge.bin", Size: (10 << 30) + 1},
	}
	markers := Markers{}
	man, err := captureFromEntries("/unused", entries, markers, store, func(rel string) ([]byte, error) {
		if rel == "huge.bin" {
			t.Fatal("refused file must not be read")
		}
		return []byte("ok\n"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(man.Refuse) != 1 || man.Refuse[0] != "huge.bin" {
		t.Fatalf("refuse = %v", man.Refuse)
	}
	if len(man.Files) != 1 || man.Files[0].Path != "ok.txt" {
		t.Fatalf("files = %+v", man.Files)
	}
	if man.NewUploads != 1 {
		t.Fatalf("uploads = %d", man.NewUploads)
	}
}

func TestCaptureWalkDedupSecondCaptureZeroUploads(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, root, "a.txt", "alpha\n")
	mustWrite(t, root, "b.txt", "beta\n")

	store := blobs.NewByteaStore()
	first, err := CaptureWalk(root, store)
	if err != nil {
		t.Fatal(err)
	}
	if first.NewUploads != 2 {
		t.Fatalf("first uploads = %d", first.NewUploads)
	}
	second, err := CaptureWalk(root, store)
	if err != nil {
		t.Fatal(err)
	}
	if second.NewUploads != 0 {
		t.Fatalf("second uploads = %d want 0 (dedup)", second.NewUploads)
	}
	if store.Len() != 2 {
		t.Fatalf("store len = %d", store.Len())
	}
	if len(second.Files) != 2 {
		t.Fatalf("second still lists files: %+v", second.Files)
	}
}

func TestRestoreTreeHashMismatch(t *testing.T) {
	sum := blobs.HashBytes([]byte("good"))
	fake := &corruptStore{sum: sum, body: []byte("evil")}
	man := &Manifest{Files: []FileRecord{{Path: "x.txt", SHA256: sum, Size: 4}}}
	dest := t.TempDir()
	if _, err := RestoreTree(dest, man, fake); err == nil {
		t.Fatal("expected hash mismatch")
	}
}

type corruptStore struct {
	sum  string
	body []byte
}

func (c *corruptStore) Put(string, []byte) (bool, error) { return false, nil }
func (c *corruptStore) Get(string) ([]byte, error)       { return append([]byte(nil), c.body...), nil }
func (c *corruptStore) Has(string) bool                  { return true }
func (c *corruptStore) Len() int                         { return 1 }

func TestRestoreTreeMissingToolRecordsCommand(t *testing.T) {
	store := blobs.NewByteaStore()
	body := []byte(`{"name":"x"}`)
	sum := blobs.HashBytes(body)
	if _, err := store.Put(sum, body); err != nil {
		t.Fatal(err)
	}
	man := &Manifest{
		Files: []FileRecord{{Path: "package.json", SHA256: sum, Size: int64(len(body))}},
		Rebuild: []RebuildRecipe{{
			Rule:     "default:node",
			Dir:      ".",
			Lockfile: "package-lock.json",
			Command:  "this-tool-does-not-exist-xyz install --frozen-lockfile",
		}},
	}
	dest := t.TempDir()
	rep, err := RestoreTree(dest, man, store)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Verified != 1 {
		t.Fatalf("verified = %d", rep.Verified)
	}
	if len(rep.Rebuilds) != 1 || rep.Rebuilds[0].Ran {
		t.Fatalf("rebuilds = %+v", rep.Rebuilds)
	}
	if !strings.Contains(rep.Rebuilds[0].Note, "not found") {
		t.Fatalf("note = %s", rep.Rebuilds[0].Note)
	}
	if rep.Rebuilds[0].Command != man.Rebuild[0].Command {
		t.Fatalf("command = %s", rep.Rebuilds[0].Command)
	}
}

func mustWrite(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
