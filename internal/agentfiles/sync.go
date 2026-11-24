package agentfiles

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// File actions reported by SyncFile.
const (
	ActionCreated   = "created"
	ActionUpdated   = "updated"
	ActionUnchanged = "unchanged"
	ActionSkipped   = "skipped"
	ActionError     = "error"
)

// FileResult reports what SyncFile did to one file.
type FileResult struct {
	File   string `json:"file"` // claude | agents | gemini (or base name)
	Path   string `json:"path"`
	Action string `json:"action"`
	Reason string `json:"reason,omitempty"`
	Err    error  `json:"-"`
}

// ErrChangedDuringSync means the file changed between read and rename;
// the write is abandoned and retried on the next sync.
var ErrChangedDuringSync = errors.New("agentfiles: file changed during sync")

// SyncWorkspace applies body to every file listed in s under root. It does
// nothing when s is disabled. body is Render's output ("" = nothing to say).
func SyncWorkspace(root string, s Settings, body string) []FileResult {
	s = s.Normalize()
	if !s.Enabled {
		return nil
	}
	st, err := os.Stat(root)
	if err != nil || !st.IsDir() {
		return []FileResult{{Path: root, Action: ActionError, Reason: "workspace folder not found", Err: err}}
	}
	out := make([]FileResult, 0, len(s.Files))
	for _, id := range s.Files {
		r := SyncFile(filepath.Join(root, FileName(id)), body, s.CreateMissing)
		r.File = id
		out = append(out, r)
	}
	return out
}

// SyncFile writes body into the managed section of path.
//
//   - A missing file is created only when createMissing is true (and body
//     is not empty).
//   - Symlinks, non-regular files, files over MaxFileBytes, non-UTF-8 files
//     and files with malformed markers are skipped untouched.
//   - Empty body: an existing section is replaced with a placeholder; a file
//     without markers is left alone.
//   - Nothing is written when the result is unchanged; otherwise the file is
//     replaced atomically (temp file + rename in the same directory), and the
//     rename is abandoned if the file changed after it was read.
func SyncFile(path, body string, createMissing bool) FileResult {
	res := FileResult{File: filepath.Base(path), Path: path}
	skip := func(action, reason string, err error) FileResult {
		res.Action, res.Reason, res.Err = action, reason, err
		return res
	}
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		if !createMissing {
			return skip(ActionSkipped, "file does not exist (create_missing is off)", nil)
		}
		if body == "" {
			return skip(ActionSkipped, "nothing to write", nil)
		}
		out, _, err := Merge(nil, body)
		if err != nil {
			return skip(ActionError, err.Error(), err)
		}
		guard := func() error {
			if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
				return ErrChangedDuringSync
			}
			return nil
		}
		if err := writeAtomic(path, out, 0o644, guard); err != nil {
			return skip(ActionError, err.Error(), err)
		}
		res.Action = ActionCreated
		return res
	}
	if err != nil {
		return skip(ActionError, err.Error(), err)
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return skip(ActionSkipped, "file is a symlink", nil)
	}
	if !info.Mode().IsRegular() {
		return skip(ActionSkipped, "not a regular file", nil)
	}
	if info.Size() > MaxFileBytes {
		return skip(ActionSkipped, ErrTooLarge.Error(), ErrTooLarge)
	}
	existing, err := os.ReadFile(path)
	if err != nil {
		return skip(ActionError, err.Error(), err)
	}
	if body == "" {
		if !HasMarkers(existing) {
			return skip(ActionUnchanged, "nothing to write", nil)
		}
		body = emptyBody
	}
	out, changed, err := Merge(existing, body)
	if err != nil {
		return skip(ActionSkipped, err.Error(), err)
	}
	if !changed {
		res.Action = ActionUnchanged
		return res
	}
	size, mod := info.Size(), info.ModTime()
	guard := func() error {
		cur, err := os.Lstat(path)
		if err != nil || cur.Size() != size || !cur.ModTime().Equal(mod) || !cur.Mode().IsRegular() {
			return ErrChangedDuringSync
		}
		return nil
	}
	if err := writeAtomic(path, out, info.Mode().Perm(), guard); err != nil {
		return skip(ActionError, err.Error(), err)
	}
	res.Action = ActionUpdated
	return res
}

// writeAtomic writes data to a temp file in path's directory and renames it
// over path. guard (optional) runs just before the rename; an error aborts.
func writeAtomic(path string, data []byte, perm fs.FileMode, guard func() error) (err error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".nexus-*.tmp")
	if err != nil {
		return fmt.Errorf("agentfiles: temp file: %w", err)
	}
	name := tmp.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(name)
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("agentfiles: write: %w", err)
	}
	if err = tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("agentfiles: sync: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("agentfiles: close: %w", err)
	}
	if perm != 0 {
		_ = os.Chmod(name, perm)
	}
	if guard != nil {
		if err = guard(); err != nil {
			return err
		}
	}
	if err = os.Rename(name, path); err != nil {
		return fmt.Errorf("agentfiles: rename: %w", err)
	}
	return nil
}

// SyncReport summarises one workspace sync (returned to the core).
type SyncReport struct {
	Root        string       `json:"root"`
	Enabled     bool         `json:"enabled"`
	Skipped     string       `json:"skipped,omitempty"` // disabled | unchanged | not connected | ...
	Fingerprint string       `json:"fingerprint,omitempty"`
	BriefFound  bool         `json:"brief_found"`
	Memories    int          `json:"memories"`
	Files       []FileResult `json:"files,omitempty"`
	At          time.Time    `json:"at"`
}
