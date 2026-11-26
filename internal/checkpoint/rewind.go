package checkpoint

import (
	"bytes"
	"context"
	"errors"
	"os"
	"sort"
	"strconv"
	"strings"
)

// RewindPlan is what "Restore to before this message" would change.
type RewindPlan struct {
	CheckpointID string       `json:"checkpoint_id"`
	FromIdx      int64        `json:"from_idx"`
	Workspace    string       `json:"workspace"`
	Prompt       string       `json:"prompt,omitempty"`
	Files        []RewindFile `json:"files"`
	// Later is how many turns after the message are rewound with it.
	Later int `json:"later"`
}

// RewindFile is one file the restore changes. Action is "restore" (put the
// earlier text back), "delete" (the agent created it) or "recreate" (the
// agent deleted it). Problem says why a file can't be restored.
type RewindFile struct {
	Path    string `json:"path"`
	Action  string `json:"action"`
	Problem string `json:"problem,omitempty"`
}

// RewindResult is a finished restore.
type RewindResult struct {
	Rewind   Rewind       `json:"rewind"`
	Restored []string     `json:"restored"`
	Failed   []RewindFile `json:"failed,omitempty"`
}

// ErrNoCheckpoint means the message has no checkpoint (it was not sent
// from Nexus to an agent working in a folder, or it was pruned).
var ErrNoCheckpoint = errors.New("There's no checkpoint for that message. Only messages sent from Nexus to an agent in a project folder can be restored")

// Find returns the checkpoint for a message: its saved index ("message
// id"), the checkpoint id or the exchange id. Rewound turns are skipped.
func (s *Store) Find(sessionID, messageID string) (*Checkpoint, error) {
	messageID = strings.TrimSpace(messageID)
	idx, _ := strconv.ParseInt(messageID, 10, 64)
	for _, cp := range s.List(sessionID) {
		if cp.Rewound {
			continue
		}
		if (idx > 0 && cp.UserIdx == idx) || cp.ID == messageID || (cp.ExchangeID != "" && cp.ExchangeID == messageID) {
			return cp, nil
		}
	}
	return nil, ErrNoCheckpoint
}

// Latest returns the chat's newest restorable checkpoint (for /rewind
// without a message).
func (s *Store) Latest(sessionID string) (*Checkpoint, error) {
	list := s.List(sessionID)
	for i := len(list) - 1; i >= 0; i-- {
		if !list[i].Rewound && list[i].UserIdx > 0 {
			return list[i], nil
		}
	}
	return nil, ErrNoCheckpoint
}

// PreviewRewind lists the files a restore to before the message changes.
func (s *Store) PreviewRewind(ctx context.Context, sessionID, messageID string) (*RewindPlan, error) {
	s.op.Lock()
	defer s.op.Unlock()
	plan, _, err := s.plan(ctx, sessionID, messageID)
	return plan, err
}

// plan returns the restore plan and the checkpoints it rewinds.
func (s *Store) plan(ctx context.Context, sessionID, messageID string) (*RewindPlan, []*Checkpoint, error) {
	target, err := s.Find(sessionID, messageID)
	if err != nil {
		return nil, nil, err
	}
	before, err := s.Before(target)
	if err != nil {
		return nil, nil, err
	}
	// Every later turn in the same folder: the files they changed.
	var rewound []*Checkpoint
	touched := map[string]bool{}
	for _, cp := range s.List(sessionID) {
		if cp.Rewound || cp.Workspace != target.Workspace || cp.CreatedAt.Before(target.CreatedAt) {
			continue
		}
		if !cp.Done {
			// A turn that never finished (the agent stopped): measure now.
			if done, err := s.Finish(ctx, cp.ID); err == nil {
				cp = done
			}
		}
		rewound = append(rewound, cp)
		for _, p := range cp.Changed {
			touched[p] = true
		}
	}
	paths := make([]string, 0, len(touched))
	for p := range touched {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	plan := &RewindPlan{CheckpointID: target.ID, FromIdx: target.UserIdx, Workspace: target.Workspace, Prompt: target.Prompt,
		Files: []RewindFile{}, Later: len(rewound) - 1}
	for _, p := range paths {
		full, err := safePath(target.Workspace, p)
		if err != nil {
			plan.Files = append(plan.Files, RewindFile{Path: p, Action: "restore", Problem: err.Error()})
			continue
		}
		cur, curErr := os.ReadFile(full)
		exists := curErr == nil
		want, had := before.Files[p]
		switch {
		case !had && !exists:
			continue
		case !had:
			plan.Files = append(plan.Files, RewindFile{Path: p, Action: "delete"})
		case !want.Saved():
			plan.Files = append(plan.Files, RewindFile{Path: p, Action: "restore", Problem: skipReason(want)})
		case exists && matches(want, cur):
			continue
		case !exists:
			plan.Files = append(plan.Files, RewindFile{Path: p, Action: "recreate"})
		default:
			plan.Files = append(plan.Files, RewindFile{Path: p, Action: "restore"})
		}
	}
	return plan, rewound, nil
}

func skipReason(e Entry) string {
	switch e.Skip {
	case "large":
		return "Too large: Nexus didn't keep a copy"
	case "budget":
		return "Not copied: the checkpoint size limit was reached"
	}
	return "Nexus couldn't read this file when the checkpoint was taken"
}

// RewindTo restores the project folder to how it was before the message
// (messageID: the saved user message index, a checkpoint id or an exchange
// id). Files later turns changed go back; files they created are deleted.
// What it replaces is kept so UndoRewind can put it back, and the chat
// remembers the rewind so its messages from there on are hidden.
func (s *Store) RewindTo(ctx context.Context, sessionID, messageID string) (*RewindResult, error) {
	s.op.Lock()
	defer s.op.Unlock()
	plan, rewound, err := s.plan(ctx, sessionID, messageID)
	if err != nil {
		return nil, err
	}
	target := rewound[0]
	before, err := s.Before(target)
	if err != nil {
		return nil, err
	}
	now := s.opts.Now()
	undo := undoRecord{ID: newID(now), SessionID: sessionID, Workspace: target.Workspace, At: now.UTC(), Files: map[string]Entry{}}
	res := &RewindResult{Restored: []string{}}
	for _, f := range plan.Files {
		if f.Problem != "" {
			res.Failed = append(res.Failed, f)
			continue
		}
		full, err := safePath(target.Workspace, f.Path)
		if err != nil {
			res.Failed = append(res.Failed, RewindFile{Path: f.Path, Action: f.Action, Problem: err.Error()})
			continue
		}
		// Keep what is there now, for Undo.
		if cur, err := os.ReadFile(full); err == nil {
			id := storeID(cur)
			if err := s.putBlob(id, cur); err != nil {
				res.Failed = append(res.Failed, RewindFile{Path: f.Path, Action: f.Action, Problem: "couldn't keep a copy for Undo"})
				continue
			}
			undo.Files[f.Path] = Entry{ID: id, Size: int64(len(cur))}
		} else {
			undo.Files[f.Path] = Entry{Gone: true}
		}
		if f.Action == "delete" {
			err = removeFile(full)
		} else {
			var content []byte
			content, err = s.read(target.Workspace, before.Files[f.Path])
			if err == nil {
				err = writeFile(full, content)
			}
		}
		if err != nil {
			res.Failed = append(res.Failed, RewindFile{Path: f.Path, Action: f.Action, Problem: err.Error()})
			delete(undo.Files, f.Path)
			continue
		}
		res.Restored = append(res.Restored, f.Path)
	}
	rw := Rewind{CheckpointID: target.ID, FromIdx: target.UserIdx, CutoffIdx: now.UnixNano() / 10, At: now.UTC(), Files: len(res.Restored)}
	for _, cp := range rewound {
		cp.Rewound = true
		if err := s.saveMeta(cp); err == nil {
			rw.Hidden = append(rw.Hidden, cp.ID)
		}
	}
	s.mu.Lock()
	st := s.loadSession(sessionID)
	rw.UndoID = undo.ID
	st.Undos = append(st.Undos, undo)
	if len(st.Undos) > 10 {
		st.Undos = st.Undos[len(st.Undos)-10:]
	}
	st.Rewinds = append(st.Rewinds, rw)
	err = s.saveSession(sessionID, st)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	res.Rewind = rw
	return res, nil
}

// UndoRewind puts back what a rewind replaced and shows its messages again.
func (s *Store) UndoRewind(sessionID, undoID string) ([]string, error) {
	s.op.Lock()
	defer s.op.Unlock()
	s.mu.Lock()
	st := s.loadSession(sessionID)
	s.mu.Unlock()
	var rec *undoRecord
	for i := range st.Undos {
		if st.Undos[i].ID == undoID {
			rec = &st.Undos[i]
		}
	}
	if rec == nil || undoID == "" {
		return nil, errors.New("That restore can no longer be undone")
	}
	var put []string
	var problems []string
	for p, e := range rec.Files {
		full, err := safePath(rec.Workspace, p)
		if err != nil {
			problems = append(problems, p+": "+err.Error())
			continue
		}
		if e.Gone {
			err = removeFile(full)
		} else {
			var content []byte
			content, err = s.read(rec.Workspace, e)
			if err == nil {
				if cur, rerr := os.ReadFile(full); rerr == nil && bytes.Equal(cur, content) {
					continue
				}
				err = writeFile(full, content)
			}
		}
		if err != nil {
			problems = append(problems, p+": "+err.Error())
			continue
		}
		put = append(put, p)
	}
	sort.Strings(put)
	s.mu.Lock()
	st = s.loadSession(sessionID)
	var hidden []string
	kept := st.Rewinds[:0]
	for _, rw := range st.Rewinds {
		if rw.UndoID == undoID {
			hidden = append(hidden, rw.Hidden...)
			continue
		}
		kept = append(kept, rw)
	}
	st.Rewinds = kept
	undos := st.Undos[:0]
	for _, u := range st.Undos {
		if u.ID != undoID {
			undos = append(undos, u)
		}
	}
	st.Undos = undos
	err := s.saveSession(sessionID, st)
	s.mu.Unlock()
	for _, id := range hidden {
		if cp, gerr := s.Get(id); gerr == nil {
			cp.Rewound = false
			_ = s.saveMeta(cp)
		}
	}
	if err != nil {
		return put, err
	}
	if len(problems) > 0 {
		return put, errors.New("Some files could not be put back: " + joinLimit(problems, 3))
	}
	return put, nil
}
