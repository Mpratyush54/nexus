package continuex

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"central-memory/internal/secrets"
)

// FileDiff describes observed workspace changes during one request, regardless
// of which harness/tool wrote the file. It never uses HEAD as the before state:
// pre-existing user edits must not be presented as this reply's changes.
type FileDiff struct {
	Path   string `json:"path"`
	Status string `json:"status"`
	Diff   string `json:"diff,omitempty"`
	Notice string `json:"notice,omitempty"`
}

type workspaceFile struct {
	text, fingerprint, notice string
	preview                   bool
}
type workspaceBaseline struct {
	root     string
	files    map[string]workspaceFile
	complete bool
}

const maxWorkspacePaths = 4000
const maxWorkspacePreviewBytes = 8 << 20

func captureWorkspaceBaseline(ctx context.Context, root string) workspaceBaseline {
	root, _ = filepath.Abs(root)
	paths, complete := workspacePreviewPaths(ctx, root)
	state := workspaceBaseline{root: root, files: make(map[string]workspaceFile), complete: complete}
	budget := maxWorkspacePreviewBytes
	for _, path := range paths {
		full := filepath.Join(root, filepath.FromSlash(path))
		info, err := os.Lstat(full)
		if err != nil {
			if !os.IsNotExist(err) {
				state.complete = false
			}
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}
		file := workspaceFile{fingerprint: fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano())}
		switch {
		case secrets.DetectSecretPath(path):
			file.notice = "Diff hidden: sensitive file."
		case info.Size() > maxEditPreview:
			file.notice = "Diff preview omitted: file exceeds 128 KB."
		case info.Size() > int64(budget):
			file.notice = "Diff preview omitted: workspace preview budget reached."
		default:
			if safe, ok := safeActivityPath(root, path); ok {
				if text, ok := readEditPreview(safe); ok {
					file.text, file.preview = text, true
					budget -= len(text)
				} else {
					file.notice = "Diff preview unavailable: binary or unreadable file."
				}
			} else {
				file.notice = "Diff preview unavailable: path is outside the workspace or follows a link."
			}
		}
		state.files[path] = file
	}
	return state
}

// Two bounded scans per request, no shell and no per-token/per-tool Git
// processes. Git's ignore rules exclude dependencies and generated outputs.
func workspacePreviewPaths(ctx context.Context, root string) ([]string, bool) {
	gitCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	cmd := exec.CommandContext(gitCtx, "git", "ls-files", "--cached", "--others", "--exclude-standard", "-z")
	hideConsole(cmd)
	cmd.Dir = root
	var output cappedOutput
	cmd.Stdout = &output
	if cmd.Run() == nil && !output.truncated {
		seen := map[string]bool{}
		paths := []string{}
		complete := true
		for _, path := range strings.Split(output.String(), "\x00") {
			if path == "" || seen[path] {
				continue
			}
			seen[path] = true
			skip := false
			parts := strings.Split(filepath.ToSlash(path), "/")
			for _, part := range parts[:len(parts)-1] {
				if skipPreviewDirectory(part) {
					skip = true
					break
				}
			}
			if skip {
				continue
			}
			if len(paths) >= maxWorkspacePaths {
				complete = false
				break
			}
			if filepath.IsAbs(path) || path == ".." || strings.HasPrefix(path, "../") {
				continue
			}
			paths = append(paths, filepath.ToSlash(path))
		}
		sort.Strings(paths)
		return paths, complete
	}
	paths := []string{}
	complete := true
	visited := 0
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			complete = false
			return nil
		}
		visited++
		if ctx.Err() != nil || visited > 20000 || len(paths) >= maxWorkspacePaths {
			complete = false
			return fs.SkipAll
		}
		if entry.IsDir() {
			if path != root && skipPreviewDirectory(entry.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err == nil {
			paths = append(paths, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(paths)
	return paths, complete
}

func skipPreviewDirectory(name string) bool {
	switch strings.ToLower(name) {
	case ".git", "node_modules", "vendor", ".venv", "venv", "__pycache__", ".next", "dist", "build", "bin", "obj", "target", ".cache":
		return true
	}
	return false
}

func (before workspaceBaseline) changes(after workspaceBaseline) []FileDiff {
	paths := map[string]bool{}
	for path := range before.files {
		paths[path] = true
	}
	for path := range after.files {
		paths[path] = true
	}
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)
	changes := []FileDiff{}
	for _, path := range ordered {
		old, hadOld := before.files[path]
		now, hasNow := after.files[path]
		if (!hadOld && !before.complete) || (!hasNow && !after.complete) {
			continue
		}
		if hadOld && hasNow {
			if old.preview && now.preview {
				if old.text == now.text {
					continue
				}
			} else if old.fingerprint == now.fingerprint {
				continue
			}
		}
		change := FileDiff{Path: path, Status: "modified"}
		if !hadOld {
			change.Status = "added"
			old.preview = true
		}
		if !hasNow {
			change.Status = "deleted"
			now.preview = true
		}
		if old.preview && now.preview {
			change.Diff = editPreview(path, old.text, now.text)
			if strings.HasPrefix(change.Diff, "Diff preview") {
				change.Notice, change.Diff = change.Diff, ""
			}
		} else {
			change.Notice = old.notice
			if change.Notice == "" {
				change.Notice = now.notice
			}
			if change.Notice == "" {
				change.Notice = "Diff preview unavailable."
			}
		}
		changes = append(changes, change)
		if len(changes) >= 100 {
			break
		}
	}
	return changes
}
