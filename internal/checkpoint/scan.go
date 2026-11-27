package checkpoint

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"central-memory/internal/hiddenproc"
)

// Entry is one file in a snapshot.
type Entry struct {
	Size  int64 `json:"s"`
	MTime int64 `json:"m,omitempty"`
	// ID names the saved content: "s:<sha256>" in the store's blobs, or
	// "g:<sha1>" for a file identical to the Git index (Git already has it).
	ID string `json:"id,omitempty"`
	// Skip says why the content was not saved ("large", "budget", "unreadable").
	Skip string `json:"skip,omitempty"`
	// Gone marks a file that does not exist (used in review bases).
	Gone bool `json:"gone,omitempty"`
}

// Saved reports whether the entry's content can be restored.
func (e Entry) Saved() bool { return e.ID != "" }

// Manifest is a folder's files at one moment.
type Manifest struct {
	Git      bool             `json:"git,omitempty"`
	Complete bool             `json:"complete"`
	Files    map[string]Entry `json:"files"`
}

// Limits bound what one snapshot reads and copies.
type Limits struct {
	MaxFiles     int   // files listed per snapshot
	MaxFileBytes int64 // larger files are listed, not saved
	MaxNewBytes  int64 // bytes copied into the store per snapshot
}

// DefaultLimits are sized for source folders.
var DefaultLimits = Limits{MaxFiles: 20000, MaxFileBytes: 8 << 20, MaxNewBytes: 256 << 20}

// skipDir lists folders never snapshotted when the folder is not a Git
// repo (in a repo, .gitignore decides).
func skipDir(name string) bool {
	switch strings.ToLower(name) {
	case ".git", "node_modules", "vendor", ".venv", "venv", "__pycache__", ".next", "dist", "build", "bin", "obj", "target", ".cache", ".idea", ".vs":
		return true
	}
	return false
}

// listFiles returns the folder's files relative to root (slash paths),
// the Git index blob for tracked ones, and whether the list is complete.
func listFiles(ctx context.Context, root string, maxFiles int) (paths []string, index map[string]string, git bool, complete bool) {
	if tracked, ok := gitLines(ctx, root, "ls-files", "-s", "-z"); ok {
		index = map[string]string{}
		seen := map[string]bool{}
		for _, line := range tracked {
			// "<mode> <sha> <stage>\t<path>"
			tab := strings.IndexByte(line, '\t')
			if tab < 0 {
				continue
			}
			meta := strings.Fields(line[:tab])
			path := line[tab+1:]
			if len(meta) == 3 && meta[2] == "0" && !strings.HasPrefix(meta[0], "120") && !strings.HasPrefix(meta[0], "160") {
				index[path] = meta[1]
			}
			if !seen[path] {
				seen[path] = true
				paths = append(paths, path)
			}
		}
		if others, ok := gitLines(ctx, root, "ls-files", "-z", "--others", "--exclude-standard"); ok {
			for _, path := range others {
				if !seen[path] {
					seen[path] = true
					paths = append(paths, path)
				}
			}
		}
		complete = true
		clean := paths[:0]
		for _, p := range paths {
			if !validRel(p) {
				continue
			}
			if len(clean) >= maxFiles {
				complete = false
				break
			}
			clean = append(clean, p)
		}
		sort.Strings(clean)
		return clean, index, true, complete
	}
	complete = true
	visited := 0
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			complete = false
			return nil
		}
		visited++
		if ctx.Err() != nil || visited > 4*maxFiles || len(paths) >= maxFiles {
			complete = false
			return fs.SkipAll
		}
		if d.IsDir() {
			if path != root && skipDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if rel, err := filepath.Rel(root, path); err == nil {
			paths = append(paths, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(paths)
	return paths, nil, false, complete
}

// gitLines runs a read-only git command in dir and splits its NUL output.
// GIT_OPTIONAL_LOCKS=0 keeps git from refreshing (writing) the index.
func gitLines(ctx context.Context, dir string, args ...string) ([]string, bool) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "core.quotepath=off"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	hiddenproc.Configure(cmd)
	out, err := cmd.Output()
	if err != nil {
		return nil, false
	}
	var lines []string
	for _, s := range strings.Split(string(out), "\x00") {
		if s != "" {
			lines = append(lines, s)
		}
	}
	return lines, true
}

// gitBlob reads a blob from the Git object store of the repo at dir.
func gitBlob(dir, sha string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "cat-file", "blob", sha)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	hiddenproc.Configure(cmd)
	out, err := cmd.Output()
	if err != nil {
		return nil, errors.New("Git no longer has this version of the file")
	}
	return out, nil
}

// validRel accepts a clean relative path inside the folder.
func validRel(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || filepath.IsAbs(p) || strings.Contains(p, "\\") || strings.Contains(p, "\x00") {
		return false
	}
	if len(p) >= 2 && p[1] == ':' {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

// safePath joins a relative path to the folder, refusing anything that
// leaves it or goes through a link.
func safePath(root, rel string) (string, error) {
	if !validRel(rel) {
		return "", fmt.Errorf("%q is not a path inside the project folder", rel)
	}
	full := filepath.Join(root, filepath.FromSlash(rel))
	parts := strings.Split(rel, "/")
	at := root
	for _, part := range parts[:len(parts)-1] {
		at = filepath.Join(at, part)
		if info, err := os.Lstat(at); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("%s goes through a link; Nexus won't change it", rel)
		}
	}
	if info, err := os.Lstat(full); err == nil && !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file; Nexus won't change it", rel)
	}
	return full, nil
}

func gitBlobID(content []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(content))
	h.Write(content)
	return hex.EncodeToString(h.Sum(nil))
}

func storeID(content []byte) string {
	sum := sha256.Sum256(content)
	return "s:" + hex.EncodeToString(sum[:])
}

// matches reports whether content is exactly what the entry saved.
func matches(e Entry, content []byte) bool {
	switch {
	case strings.HasPrefix(e.ID, "s:"):
		return storeID(content) == e.ID
	case strings.HasPrefix(e.ID, "g:"):
		return gitBlobID(content) == e.ID[2:]
	}
	return false
}

// isBinary is Git's rule: a NUL byte in the first 8000 bytes.
func isBinary(b []byte) bool {
	if len(b) > 8000 {
		b = b[:8000]
	}
	return bytes.IndexByte(b, 0) >= 0
}
