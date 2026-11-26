// Package checkpoint is the safety net for agent edits: before each turn an
// agent runs in a project folder, it snapshots the folder's files, so the
// person can restore everything to before any message ("rewind") and review
// a turn's changes hunk by hunk (Keep or Undo).
//
// Snapshots never touch the person's repository: Git is only asked, read
// only, which files exist (so .gitignore is honoured) and which are
// unchanged from its index. A file identical to the index is recorded by
// its Git blob id (Git already holds those bytes); any other file is copied
// once into a content-addressed blob store under the profile's data folder
// (<data>/checkpoints/blobs), so the next snapshot of an unchanged file
// costs only a stat. Old checkpoints are pruned by count, age and total
// size, and blobs no checkpoint uses are deleted.
package checkpoint

import (
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Checkpoint is the state of a project folder before one turn.
type Checkpoint struct {
	ID        string    `json:"id"`
	SessionID string    `json:"session_id"`
	Workspace string    `json:"workspace"`
	CreatedAt time.Time `json:"created_at"`
	Prompt    string    `json:"prompt,omitempty"`
	// UserIdx is the saved user message's index ("message id"), set once
	// the turn is recorded; ExchangeID is the local exchange file.
	UserIdx    int64  `json:"user_idx,omitempty"`
	ExchangeID string `json:"exchange_id,omitempty"`
	Git        bool   `json:"git,omitempty"`
	Complete   bool   `json:"complete"`
	// Done is set when the turn ended and its changes were measured.
	Done bool `json:"done,omitempty"`
	// Changed lists the files the turn changed (before vs after), and After
	// their state when it ended (Gone for deleted ones).
	Changed []string         `json:"changed,omitempty"`
	After   map[string]Entry `json:"after,omitempty"`
	// Base holds the review's accepted state per file once something was
	// kept; files not in it compare against the snapshot.
	Base map[string]Entry `json:"base,omitempty"`
	// Rewound is set when the chat was restored to this or an earlier
	// message: the turn is no longer shown.
	Rewound bool `json:"rewound,omitempty"`

	before *Manifest
}

// Rewind is one "restore to before this message": messages with an index
// in [FromIdx, CutoffIdx) are hidden, and a divider shows at the cutoff.
type Rewind struct {
	CheckpointID string    `json:"checkpoint_id"`
	FromIdx      int64     `json:"from_idx"`
	CutoffIdx    int64     `json:"cutoff_idx"`
	At           time.Time `json:"at"`
	UndoID       string    `json:"undo_id,omitempty"`
	Files        int       `json:"files"`
	// Hidden are the checkpoints this rewind marked Rewound.
	Hidden []string `json:"hidden,omitempty"`
}

// undoRecord is what a rewind replaced, so its toast can put it back.
type undoRecord struct {
	ID        string           `json:"id"`
	SessionID string           `json:"session_id"`
	Workspace string           `json:"workspace"`
	At        time.Time        `json:"at"`
	Files     map[string]Entry `json:"files"`
	Git       bool             `json:"git,omitempty"`
}

type sessionState struct {
	Rewinds []Rewind     `json:"rewinds,omitempty"`
	Undos   []undoRecord `json:"undos,omitempty"`
}

// Options configures a Store.
type Options struct {
	Limits Limits
	// Pruning: per chat and in total, by age, and by blob bytes.
	MaxPerSession int
	MaxTotal      int
	MaxAge        time.Duration
	MaxBlobBytes  int64
	Now           func() time.Time
}

// Store keeps checkpoints under one folder. It is safe for concurrent use.
type Store struct {
	dir  string
	opts Options

	op        sync.Mutex // Keep, Undo and rewinds, one at a time
	mu        sync.Mutex
	scans     map[string]*Manifest // last snapshot per folder, for stat-only reuse
	lastPrune time.Time
}

// Open returns the store rooted at dir (created on first write).
func Open(dir string, opts Options) *Store {
	if opts.Limits.MaxFiles <= 0 {
		opts.Limits = DefaultLimits
	}
	if opts.MaxPerSession <= 0 {
		opts.MaxPerSession = 60
	}
	if opts.MaxTotal <= 0 {
		opts.MaxTotal = 400
	}
	if opts.MaxAge <= 0 {
		opts.MaxAge = 30 * 24 * time.Hour
	}
	if opts.MaxBlobBytes <= 0 {
		opts.MaxBlobBytes = 1 << 30
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Store{dir: dir, opts: opts, scans: map[string]*Manifest{}}
}

// Dir is the store's folder.
func (s *Store) Dir() string { return s.dir }

func (s *Store) cpDir() string               { return filepath.Join(s.dir, "cps") }
func (s *Store) blobDir() string             { return filepath.Join(s.dir, "blobs") }
func (s *Store) metaPath(id string) string   { return filepath.Join(s.cpDir(), id+".json") }
func (s *Store) beforePath(id string) string { return filepath.Join(s.cpDir(), id+".before.json.gz") }
func (s *Store) sessionPath(id string) string {
	return filepath.Join(s.dir, "sessions", hashName(id)+".json")
}

func hashName(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:16])
}

func newID(now time.Time) string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return now.UTC().Format("20060102T150405.000000000") + "-" + hex.EncodeToString(b[:])
}

// Take snapshots the folder before a turn. The returned checkpoint is
// saved; call Finish when the turn ends.
func (s *Store) Take(ctx context.Context, sessionID, workspace, prompt string) (*Checkpoint, error) {
	workspace, err := filepath.Abs(strings.TrimSpace(workspace))
	if err != nil || workspace == "" {
		return nil, errors.New("no project folder")
	}
	if st, err := os.Stat(workspace); err != nil || !st.IsDir() {
		return nil, errors.New("the project folder is not on this computer")
	}
	man, err := s.scan(ctx, workspace)
	if err != nil {
		return nil, err
	}
	now := s.opts.Now()
	if len(prompt) > 240 {
		prompt = prompt[:240]
	}
	cp := &Checkpoint{ID: newID(now), SessionID: sessionID, Workspace: workspace, CreatedAt: now.UTC(), Prompt: prompt,
		Git: man.Git, Complete: man.Complete, before: man}
	if err := s.writeBefore(cp.ID, man); err != nil {
		return nil, err
	}
	if err := s.saveMeta(cp); err != nil {
		return nil, err
	}
	return cp, nil
}

// Finish measures what the turn changed and records it.
func (s *Store) Finish(ctx context.Context, id string) (*Checkpoint, error) {
	cp, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	before, err := s.Before(cp)
	if err != nil {
		return nil, err
	}
	after, err := s.scan(ctx, cp.Workspace)
	if err != nil {
		return nil, err
	}
	cp.Changed, cp.After = changedPaths(before, after)
	cp.Done = true
	if err := s.saveMeta(cp); err != nil {
		return nil, err
	}
	s.maybePrune()
	return cp, nil
}

// changedPaths lists files that differ between two snapshots, with their
// state in `after`.
func changedPaths(before, after *Manifest) ([]string, map[string]Entry) {
	out := map[string]Entry{}
	var paths []string
	for p, b := range before.Files {
		a, ok := after.Files[p]
		if !ok {
			if after.Complete {
				paths = append(paths, p)
				out[p] = Entry{Gone: true}
			}
			continue
		}
		if !sameContent(b, a) {
			paths = append(paths, p)
			out[p] = a
		}
	}
	for p, a := range after.Files {
		if _, ok := before.Files[p]; !ok && before.Complete {
			paths = append(paths, p)
			out[p] = a
		}
	}
	sort.Strings(paths)
	return paths, out
}

func sameContent(a, b Entry) bool {
	if a.Gone || b.Gone {
		return a.Gone == b.Gone
	}
	if a.ID != "" && b.ID != "" {
		return a.ID == b.ID
	}
	return a.Size == b.Size && a.MTime == b.MTime
}

// Link ties a checkpoint to the saved user message.
func (s *Store) Link(id string, userIdx int64, exchangeID string) error {
	cp, err := s.Get(id)
	if err != nil {
		return err
	}
	cp.UserIdx, cp.ExchangeID = userIdx, exchangeID
	return s.saveMeta(cp)
}

// Get loads a checkpoint's metadata.
func (s *Store) Get(id string) (*Checkpoint, error) {
	if !validID(id) {
		return nil, errors.New("That checkpoint is no longer available")
	}
	data, err := os.ReadFile(s.metaPath(id))
	if err != nil {
		return nil, errors.New("That checkpoint is no longer available")
	}
	var cp Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return nil, err
	}
	return &cp, nil
}

func validID(id string) bool {
	if id == "" || len(id) > 80 {
		return false
	}
	for _, r := range id {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '.' || r == '-' || r == '_') {
			return false
		}
	}
	return !strings.Contains(id, "..")
}

// List returns a chat's checkpoints, oldest first.
func (s *Store) List(sessionID string) []*Checkpoint {
	var out []*Checkpoint
	for _, cp := range s.all() {
		if cp.SessionID == sessionID {
			out = append(out, cp)
		}
	}
	return out
}

func (s *Store) all() []*Checkpoint {
	names, _ := filepath.Glob(filepath.Join(s.cpDir(), "*.json"))
	var out []*Checkpoint
	for _, name := range names {
		if strings.HasSuffix(name, ".before.json.gz") {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			continue
		}
		var cp Checkpoint
		if json.Unmarshal(data, &cp) == nil && cp.ID != "" {
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Before loads the snapshot a checkpoint took.
func (s *Store) Before(cp *Checkpoint) (*Manifest, error) {
	if cp.before != nil {
		return cp.before, nil
	}
	f, err := os.Open(s.beforePath(cp.ID))
	if err != nil {
		return nil, errors.New("That checkpoint is no longer available")
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.NewDecoder(zr).Decode(&m); err != nil {
		return nil, err
	}
	if m.Files == nil {
		m.Files = map[string]Entry{}
	}
	cp.before = &m
	return &m, nil
}

func (s *Store) saveMeta(cp *Checkpoint) error {
	data, err := json.Marshal(cp)
	if err != nil {
		return err
	}
	return writeAtomic(s.metaPath(cp.ID), data)
}

func (s *Store) writeBefore(id string, m *Manifest) error {
	if err := os.MkdirAll(s.cpDir(), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.cpDir(), ".pending-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	zw, _ := gzip.NewWriterLevel(tmp, gzip.BestSpeed)
	if err := json.NewEncoder(zw).Encode(m); err != nil {
		tmp.Close()
		return err
	}
	if err := zw.Close(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.beforePath(id))
}

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".pending-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// ---- snapshots -----------------------------------------------------------

// scan lists the folder and saves the content of every file that changed
// since the last scan (by size and modification time).
func (s *Store) scan(ctx context.Context, root string) (*Manifest, error) {
	paths, index, git, complete := listFiles(ctx, root, s.opts.Limits.MaxFiles)
	s.mu.Lock()
	prev := s.scans[root]
	s.mu.Unlock()
	man := &Manifest{Git: git, Complete: complete, Files: make(map[string]Entry, len(paths))}
	budget := s.opts.Limits.MaxNewBytes
	for _, p := range paths {
		if ctx.Err() != nil {
			man.Complete = false
			break
		}
		full := filepath.Join(root, filepath.FromSlash(p))
		info, err := os.Lstat(full)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		e := Entry{Size: info.Size(), MTime: info.ModTime().UnixNano()}
		if prev != nil {
			if old, ok := prev.Files[p]; ok && old.Size == e.Size && old.MTime == e.MTime && old.ID != "" {
				// A Git reference stays valid only while the index still has it.
				if !strings.HasPrefix(old.ID, "g:") || index[p] == old.ID[2:] {
					man.Files[p] = old
					continue
				}
			}
		}
		if e.Size > s.opts.Limits.MaxFileBytes {
			e.Skip = "large"
			man.Files[p] = e
			continue
		}
		content, err := os.ReadFile(full)
		if err != nil {
			e.Skip = "unreadable"
			man.Files[p] = e
			continue
		}
		if sha, ok := index[p]; ok && gitBlobID(content) == sha {
			e.ID = "g:" + sha
			man.Files[p] = e
			continue
		}
		id := storeID(content)
		if !s.hasBlob(id) {
			if budget < int64(len(content)) {
				e.Skip = "budget"
				man.Files[p] = e
				continue
			}
			if err := s.putBlob(id, content); err != nil {
				e.Skip = "unreadable"
				man.Files[p] = e
				continue
			}
			budget -= int64(len(content))
		}
		e.ID = id
		man.Files[p] = e
	}
	s.mu.Lock()
	s.scans[root] = man
	s.mu.Unlock()
	return man, nil
}

func (s *Store) blobPath(id string) string {
	h := strings.TrimPrefix(id, "s:")
	if len(h) < 4 {
		return filepath.Join(s.blobDir(), "invalid")
	}
	return filepath.Join(s.blobDir(), h[:2], h)
}

func (s *Store) hasBlob(id string) bool {
	_, err := os.Stat(s.blobPath(id))
	return err == nil
}

func (s *Store) putBlob(id string, content []byte) error {
	if s.hasBlob(id) {
		return nil
	}
	return writeAtomic(s.blobPath(id), content)
}

// read returns an entry's saved content.
func (s *Store) read(workspace string, e Entry) ([]byte, error) {
	switch {
	case strings.HasPrefix(e.ID, "s:"):
		b, err := os.ReadFile(s.blobPath(e.ID))
		if err != nil {
			return nil, errors.New("the saved copy is missing")
		}
		return b, nil
	case strings.HasPrefix(e.ID, "g:"):
		return gitBlob(workspace, e.ID[2:])
	}
	switch e.Skip {
	case "large":
		return nil, errors.New("too large to keep a copy")
	case "budget":
		return nil, errors.New("not copied: the snapshot size limit was reached")
	}
	return nil, errors.New("no copy was saved")
}

// ---- chat state (rewinds) --------------------------------------------------

func (s *Store) loadSession(sessionID string) sessionState {
	var st sessionState
	if data, err := os.ReadFile(s.sessionPath(sessionID)); err == nil {
		_ = json.Unmarshal(data, &st)
	}
	return st
}

func (s *Store) saveSession(sessionID string, st sessionState) error {
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return writeAtomic(s.sessionPath(sessionID), data)
}

// Rewinds lists a chat's restores, oldest first.
func (s *Store) Rewinds(sessionID string) []Rewind {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadSession(sessionID).Rewinds
}

// ---- pruning -----------------------------------------------------------------

func (s *Store) maybePrune() {
	s.mu.Lock()
	due := s.opts.Now().Sub(s.lastPrune) > 10*time.Minute
	s.mu.Unlock()
	if !due {
		// Still prune when a chat is over its count.
		if len(s.all()) <= s.opts.MaxTotal {
			return
		}
	}
	_ = s.Prune()
}

// Prune drops checkpoints beyond the count, age and size limits and deletes
// blobs nothing references.
func (s *Store) Prune() error {
	s.mu.Lock()
	s.lastPrune = s.opts.Now()
	s.mu.Unlock()
	all := s.all()
	now := s.opts.Now()
	keep := make([]*Checkpoint, 0, len(all))
	perSession := map[string]int{}
	// Newest first, so counts keep the latest.
	for i := len(all) - 1; i >= 0; i-- {
		cp := all[i]
		perSession[cp.SessionID]++
		if perSession[cp.SessionID] > s.opts.MaxPerSession || len(keep) >= s.opts.MaxTotal || now.Sub(cp.CreatedAt) > s.opts.MaxAge {
			s.drop(cp.ID)
			continue
		}
		keep = append(keep, cp)
	}
	for {
		refs, undoRefs := s.references(keep)
		size := s.gcBlobs(refs, undoRefs)
		if size <= s.opts.MaxBlobBytes || len(keep) <= 1 {
			return nil
		}
		// Over the size limit: drop the oldest kept checkpoint and retry.
		oldest := keep[len(keep)-1]
		keep = keep[:len(keep)-1]
		s.drop(oldest.ID)
	}
}

func (s *Store) drop(id string) {
	_ = os.Remove(s.metaPath(id))
	_ = os.Remove(s.beforePath(id))
}

// references collects every stored blob a kept checkpoint or undo uses.
func (s *Store) references(keep []*Checkpoint) (map[string]bool, map[string]bool) {
	refs := map[string]bool{}
	add := func(e Entry) {
		if strings.HasPrefix(e.ID, "s:") {
			refs[e.ID] = true
		}
	}
	for _, cp := range keep {
		if before, err := s.Before(cp); err == nil {
			for _, e := range before.Files {
				add(e)
			}
		}
		for _, e := range cp.After {
			add(e)
		}
		for _, e := range cp.Base {
			add(e)
		}
	}
	// Blobs the last scans saw stay too: the next snapshot reuses them.
	s.mu.Lock()
	for _, m := range s.scans {
		for _, e := range m.Files {
			add(e)
		}
	}
	s.mu.Unlock()
	undo := map[string]bool{}
	sessions, _ := filepath.Glob(filepath.Join(s.dir, "sessions", "*.json"))
	for _, name := range sessions {
		var st sessionState
		if data, err := os.ReadFile(name); err == nil && json.Unmarshal(data, &st) == nil {
			for _, u := range st.Undos {
				for _, e := range u.Files {
					if strings.HasPrefix(e.ID, "s:") {
						undo[e.ID] = true
					}
				}
			}
		}
	}
	return refs, undo
}

// gcBlobs deletes unreferenced blobs and returns the bytes still stored.
func (s *Store) gcBlobs(refs, undo map[string]bool) int64 {
	var size int64
	cutoff := s.opts.Now().Add(-2 * time.Minute)
	_ = filepath.WalkDir(s.blobDir(), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		id := "s:" + d.Name()
		info, err := d.Info()
		if err != nil {
			return nil
		}
		// A blob written moments ago may belong to a snapshot being taken.
		if !refs[id] && !undo[id] && info.ModTime().Before(cutoff) {
			_ = os.Remove(path)
			return nil
		}
		size += info.Size()
		return nil
	})
	return size
}
