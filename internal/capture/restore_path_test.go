package capture

import (
	"os"
	"path/filepath"
	"testing"

	"central-memory/internal/blobs"
)

func TestRestoreTreeRejectsEscapingPaths(t *testing.T) {
	store := blobs.NewByteaStore()
	body := []byte("payload")
	sum := blobs.HashBytes(body)
	if _, err := store.Put(sum, body); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"../escape.txt", "a/../../escape.txt", `..\escape.txt`, "/escape.txt"} {
		t.Run(rel, func(t *testing.T) {
			dest := t.TempDir()
			man := &Manifest{Files: []FileRecord{{Path: rel, SHA256: sum, Size: int64(len(body))}}}
			if _, err := RestoreTree(dest, man, store); err == nil {
				t.Fatalf("expected rejection for %q", rel)
			}
			outside := filepath.Join(filepath.Dir(dest), "escape.txt")
			if _, err := os.Stat(outside); err == nil {
				t.Fatalf("restore escaped destination to %s", outside)
			}
		})
	}
}

func TestRestoreTreeRejectsSymlinkedParent(t *testing.T) {
	dest := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dest, "link")); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}
	store := blobs.NewByteaStore()
	body := []byte("payload")
	sum := blobs.HashBytes(body)
	if _, err := store.Put(sum, body); err != nil {
		t.Fatal(err)
	}
	man := &Manifest{Files: []FileRecord{{Path: "link/escape.txt", SHA256: sum, Size: int64(len(body))}}}
	if _, err := RestoreTree(dest, man, store); err == nil {
		t.Fatal("expected rejection for symlinked parent")
	}
	if _, err := os.Stat(filepath.Join(outside, "escape.txt")); err == nil {
		t.Fatal("restore wrote through a symlinked parent")
	}
}

func TestRestoreTreeRejectsEscapingRebuildDir(t *testing.T) {
	store := blobs.NewByteaStore()
	man := &Manifest{Rebuild: []RebuildRecipe{{
		Rule:    "default:node",
		Dir:     "../outside",
		Command: "go version",
	}}}
	rep, err := RestoreTree(t.TempDir(), man, store)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Rebuilds) != 1 || rep.Rebuilds[0].Ran || rep.Rebuilds[0].Err == "" {
		t.Fatalf("unsafe rebuild was not rejected: %+v", rep.Rebuilds)
	}
}
