package agentrun

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"central-memory/internal/checkpoint"
)

// Checkpoints: before the agent gets a message, the chat's project folder
// is snapshotted (internal/checkpoint); when the turn ends, what it changed
// is measured, and once the turn is saved the snapshot is tied to the saved
// user message, so "Restore to before this message" and the turn's
// "Review changes" card can find it.

// snapshotTimeout bounds the snapshot before a turn; a folder too big to
// snapshot in time gets no checkpoint rather than a slow reply.
const snapshotTimeout = 20 * time.Second

// Checkpoints is the store, or nil when checkpoints are off.
func (m *Manager) Checkpoints() *checkpoint.Store { return m.cfg.Checkpoints }

func (m *Manager) takeCheckpoint(s *session, req SendRequest) {
	store := m.cfg.Checkpoints
	if store == nil || strings.TrimSpace(req.Workspace) == "" {
		return
	}
	m.mu.Lock()
	t := s.turn
	m.mu.Unlock()
	if t == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), snapshotTimeout)
	defer cancel()
	cp, err := store.Take(ctx, s.id, req.Workspace, req.Text)
	if err != nil {
		return
	}
	m.mu.Lock()
	if s.turn == t {
		t.checkpoint = cp.ID
	}
	m.mu.Unlock()
}

// finishCheckpoint measures the turn's changes; it returns how many files
// changed (-1 without a checkpoint).
func (m *Manager) finishCheckpoint(t *turn) int {
	store := m.cfg.Checkpoints
	if store == nil || t == nil || t.checkpoint == "" {
		return -1
	}
	ctx, cancel := context.WithTimeout(context.Background(), snapshotTimeout)
	defer cancel()
	cp, err := store.Finish(ctx, t.checkpoint)
	if err != nil {
		return -1
	}
	return len(cp.Changed)
}

// linkCheckpoint ties the snapshot to the saved user message and adds
// checkpoint_id / checkpoint_files to the recorded result for the UI.
func (m *Manager) linkCheckpoint(t *turn, result any, changed int) any {
	store := m.cfg.Checkpoints
	if store == nil || t == nil || t.checkpoint == "" {
		return result
	}
	var fields map[string]any
	switch r := result.(type) {
	case map[string]any:
		fields = r
	default:
		if raw, err := json.Marshal(result); err == nil {
			_ = json.Unmarshal(raw, &fields)
		}
	}
	if fields == nil {
		fields = map[string]any{}
	}
	idx := int64Of(fields["user_idx"])
	_ = store.Link(t.checkpoint, idx, strOf(fields["exchange_id"]))
	fields["checkpoint_id"] = t.checkpoint
	if changed >= 0 {
		fields["checkpoint_files"] = changed
	}
	return fields
}

func int64Of(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	case json.Number:
		i, _ := n.Int64()
		return i
	case string:
		i, _ := strconv.ParseInt(n, 10, 64)
		return i
	}
	return 0
}

var errNoCheckpoints = errors.New("Checkpoints are off in this copy of Nexus")

// busyLocked reports whether the chat's agent is working (or about to):
// restoring files under it would race its edits.
func (m *Manager) busyLocked(sessionID string) bool {
	s := m.sessions[sessionID]
	return s != nil && (s.turn != nil || s.queued != nil)
}

var errRewindBusy = errors.New("Wait for this reply to finish, or press Stop, then restore")

// PreviewRewind lists the files RewindTo would change. An empty messageID
// means the chat's latest message that has a checkpoint.
func (m *Manager) PreviewRewind(sessionID, messageID string) (*checkpoint.RewindPlan, error) {
	store := m.cfg.Checkpoints
	if store == nil {
		return nil, errNoCheckpoints
	}
	messageID, err := m.rewindTarget(sessionID, messageID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	return store.PreviewRewind(ctx, sessionID, messageID)
}

// RewindTo restores the chat's project folder to how it was before the
// message (its saved index, a checkpoint id or an exchange id; "" is the
// latest one) and remembers the rewind so the chat hides the messages from
// there on. The agent's own conversation is not changed: the next message
// continues it. This is the entry point for "Restore to before this
// message" and /rewind.
func (m *Manager) RewindTo(sessionID, messageID string) (*checkpoint.RewindResult, error) {
	store := m.cfg.Checkpoints
	if store == nil {
		return nil, errNoCheckpoints
	}
	messageID, err := m.rewindTarget(sessionID, messageID)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	busy := m.busyLocked(sessionID)
	m.mu.Unlock()
	if busy {
		return nil, errRewindBusy
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	res, err := store.RewindTo(ctx, sessionID, messageID)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.emitLocked(map[string]any{"type": "live.rewound", "session_id": sessionID, "rewind": res.Rewind})
	m.mu.Unlock()
	return res, nil
}

// UndoRewind puts back what a rewind replaced.
func (m *Manager) UndoRewind(sessionID, undoID string) ([]string, error) {
	store := m.cfg.Checkpoints
	if store == nil {
		return nil, errNoCheckpoints
	}
	m.mu.Lock()
	busy := m.busyLocked(sessionID)
	m.mu.Unlock()
	if busy {
		return nil, errRewindBusy
	}
	put, err := store.UndoRewind(sessionID, undoID)
	if err == nil {
		m.mu.Lock()
		m.emitLocked(map[string]any{"type": "live.rewound", "session_id": sessionID, "undone": undoID})
		m.mu.Unlock()
	}
	return put, err
}

func (m *Manager) rewindTarget(sessionID, messageID string) (string, error) {
	messageID = strings.TrimSpace(messageID)
	if messageID != "" {
		return messageID, nil
	}
	cp, err := m.cfg.Checkpoints.Latest(sessionID)
	if err != nil {
		return "", err
	}
	return cp.ID, nil
}
