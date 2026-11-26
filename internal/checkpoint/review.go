package checkpoint

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"

	"central-memory/internal/secrets"
)

// Review is a turn's changes still waiting for Keep or Undo: each file's
// review base (the snapshot, plus what was kept) against the folder now.
type Review struct {
	CheckpointID string       `json:"checkpoint_id"`
	Workspace    string       `json:"workspace"`
	Total        int          `json:"total"`   // files the turn changed
	Pending      int          `json:"pending"` // still differing from the base
	Added        int          `json:"added"`
	Removed      int          `json:"removed"`
	Files        []ReviewFile `json:"files"`
}

// ReviewFile is one changed file. Version identifies the exact base and
// current text the hunks were computed from; Keep/Undo of a hunk must
// send it back, so a file that changed since is never patched blindly.
type ReviewFile struct {
	Path      string `json:"path"`
	Status    string `json:"status"` // added, deleted, modified
	Binary    bool   `json:"binary,omitempty"`
	Sensitive bool   `json:"sensitive,omitempty"`
	TooLarge  bool   `json:"too_large,omitempty"`
	Notice    string `json:"notice,omitempty"`
	Added     int    `json:"added"`
	Removed   int    `json:"removed"`
	Hunks     []Hunk `json:"hunks,omitempty"`
	Version   string `json:"version"`
	CanUndo   bool   `json:"can_undo"`
}

// maxReviewText is the largest file shown as hunks; bigger ones are kept
// or undone whole.
const maxReviewText = 1 << 20

// ErrStale means the file changed after the review was shown.
var ErrStale = errors.New("This file changed since the review opened. The review has been refreshed")

type fileState struct {
	path          string
	base, cur     []byte
	baseOK, curOK bool // exists
	baseErr       error
	baseEntry     Entry
	full          string
}

func (s *Store) fileState(cp *Checkpoint, before *Manifest, path string) (fileState, error) {
	full, err := safePath(cp.Workspace, path)
	if err != nil {
		return fileState{}, err
	}
	st := fileState{path: path, full: full}
	base, ok := cp.Base[path]
	if !ok {
		if e, had := before.Files[path]; had {
			base = e
		} else {
			base = Entry{Gone: true}
		}
	}
	st.baseEntry = base
	if !base.Gone {
		st.baseOK = true
		st.base, st.baseErr = s.read(cp.Workspace, base)
	}
	if b, err := os.ReadFile(full); err == nil {
		st.cur, st.curOK = b, true
	} else if !os.IsNotExist(err) {
		return st, err
	}
	return st, nil
}

func (f fileState) same() bool {
	if f.baseErr != nil {
		return false
	}
	return f.baseOK == f.curOK && bytes.Equal(f.base, f.cur)
}

func (f fileState) version() string {
	h := sha256.New()
	h.Write([]byte(f.baseEntry.ID))
	if f.baseEntry.Gone {
		h.Write([]byte{0})
	}
	h.Write([]byte{1})
	if f.curOK {
		h.Write(f.cur)
	} else {
		h.Write([]byte{2})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// Review lists what a finished turn changed that is not yet kept or undone.
func (s *Store) Review(id string) (*Review, error) {
	s.op.Lock()
	defer s.op.Unlock()
	return s.review(id)
}

func (s *Store) review(id string) (*Review, error) {
	cp, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	before, err := s.Before(cp)
	if err != nil {
		return nil, err
	}
	r := &Review{CheckpointID: cp.ID, Workspace: cp.Workspace, Total: len(cp.Changed), Files: []ReviewFile{}}
	for _, path := range cp.Changed {
		st, err := s.fileState(cp, before, path)
		if err != nil {
			continue
		}
		if st.same() {
			continue
		}
		f := ReviewFile{Path: path, Status: "modified", Version: st.version(), CanUndo: st.baseErr == nil}
		switch {
		case !st.baseOK:
			f.Status = "added"
		case !st.curOK:
			f.Status = "deleted"
		}
		switch {
		case st.baseErr != nil:
			f.Notice = "Nexus has no copy of the earlier version (" + st.baseErr.Error() + "), so this file can only be kept."
		case isBinary(st.base) || isBinary(st.cur):
			f.Binary = true
			f.Notice = "Binary file. Keep or undo it as a whole."
		case secrets.DetectSecretPath(path):
			f.Sensitive = true
			f.Notice = "Changes hidden: this file may hold secrets. Keep or undo it as a whole."
		case len(st.base) > maxReviewText || len(st.cur) > maxReviewText:
			f.TooLarge = true
			f.Notice = "This file is too large to show line by line. Keep or undo it as a whole."
		default:
			f.Hunks = Hunks(string(st.base), string(st.cur))
			for _, h := range f.Hunks {
				f.Added += h.Added
				f.Removed += h.Removed
			}
		}
		r.Added += f.Added
		r.Removed += f.Removed
		r.Files = append(r.Files, f)
	}
	r.Pending = len(r.Files)
	return r, nil
}

// Keep accepts changes: one hunk (hunk >= 0), one file (hunk < 0), or every
// pending file (path ""). Kept changes are no longer offered for Undo.
func (s *Store) Keep(id, path string, hunk int, version string) (*Review, error) {
	return s.decide(id, path, hunk, version, true)
}

// Undo reverts changes in the folder: one hunk, one file, or every pending
// file (path ""), back to the review base.
func (s *Store) Undo(id, path string, hunk int, version string) (*Review, error) {
	return s.decide(id, path, hunk, version, false)
}

func (s *Store) decide(id, path string, hunk int, version string, keep bool) (*Review, error) {
	s.op.Lock()
	defer s.op.Unlock()
	cp, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	before, err := s.Before(cp)
	if err != nil {
		return nil, err
	}
	paths := []string{path}
	if path == "" {
		paths = cp.Changed
		hunk = -1
	} else if !contains(cp.Changed, path) {
		return nil, errors.New("That file isn't part of this turn's changes")
	}
	var problems []string
	changedBase := false
	for _, p := range paths {
		st, err := s.fileState(cp, before, p)
		if err != nil {
			problems = append(problems, p+": "+err.Error())
			continue
		}
		if st.same() {
			continue
		}
		if path != "" && version != "" && st.version() != version {
			return nil, ErrStale
		}
		if hunk >= 0 && (st.baseErr != nil || isBinary(st.base) || isBinary(st.cur)) {
			return nil, errors.New("This file can only be kept or undone as a whole")
		}
		if keep {
			var next []byte
			gone := false
			if hunk >= 0 {
				text, ok := ApplyHunk(string(st.base), string(st.cur), hunk)
				if !ok {
					return nil, ErrStale
				}
				next = []byte(text)
				// Keeping the last hunk of a deleted file settles it as deleted.
				gone = !st.curOK && len(next) == 0
			} else if st.curOK {
				next = st.cur
			} else {
				gone = true
			}
			if cp.Base == nil {
				cp.Base = map[string]Entry{}
			}
			if gone {
				cp.Base[p] = Entry{Gone: true}
			} else {
				bid := storeID(next)
				if err := s.putBlob(bid, next); err != nil {
					problems = append(problems, p+": "+err.Error())
					continue
				}
				cp.Base[p] = Entry{ID: bid, Size: int64(len(next))}
			}
			changedBase = true
			continue
		}
		if st.baseErr != nil {
			problems = append(problems, p+": "+st.baseErr.Error())
			continue
		}
		if hunk >= 0 {
			text, ok := RevertHunk(string(st.base), string(st.cur), hunk)
			if !ok {
				return nil, ErrStale
			}
			if !st.baseOK && text == "" {
				err = removeFile(st.full)
			} else {
				err = writeFile(st.full, []byte(text))
			}
		} else if !st.baseOK {
			err = removeFile(st.full)
		} else {
			err = writeFile(st.full, st.base)
		}
		if err != nil {
			problems = append(problems, p+": "+err.Error())
		}
	}
	if changedBase {
		if err := s.saveMeta(cp); err != nil {
			return nil, err
		}
	}
	r, err := s.review(id)
	if err != nil {
		return nil, err
	}
	if len(problems) > 0 {
		return r, errors.New("Some files could not be changed: " + joinLimit(problems, 3))
	}
	return r, nil
}

func writeFile(full string, content []byte) error {
	mode := os.FileMode(0o644)
	if info, err := os.Stat(full); err == nil {
		mode = info.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, content, mode)
}

func removeFile(full string) error {
	if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func joinLimit(list []string, n int) string {
	out := ""
	for i, s := range list {
		if i == n {
			out += "; and " + itoa(len(list)-n) + " more"
			break
		}
		if i > 0 {
			out += "; "
		}
		out += s
	}
	return out
}
