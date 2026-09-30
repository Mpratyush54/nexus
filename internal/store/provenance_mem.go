package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

func (m *MemStore) InsertFileOperation(ctx context.Context, op FileOperation) error {
	_ = ctx
	if strings.TrimSpace(op.ProjectID) == "" || strings.TrimSpace(op.SessionID) == "" {
		return fmt.Errorf("file operation requires project_id and session_id")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if op.ID == "" {
		op.ID = newID("fop")
	}
	if op.CreatedAt.IsZero() {
		op.CreatedAt = time.Now().UTC()
	}
	m.fileOps = append(m.fileOps, op)
	return nil
}

func (m *MemStore) InsertToolExecution(ctx context.Context, exec ToolExecution) error {
	_ = ctx
	if strings.TrimSpace(exec.ProjectID) == "" || strings.TrimSpace(exec.SessionID) == "" {
		return fmt.Errorf("tool execution requires project_id and session_id")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if exec.ID == "" {
		exec.ID = newID("tex")
	}
	if exec.CreatedAt.IsZero() {
		exec.CreatedAt = time.Now().UTC()
	}
	m.toolExecs = append(m.toolExecs, exec)
	return nil
}

func (m *MemStore) InsertBatchFileOperations(ctx context.Context, ops []FileOperation) error {
	for _, op := range ops {
		if err := m.InsertFileOperation(ctx, op); err != nil {
			return err
		}
	}
	return nil
}

func (m *MemStore) ListFileOperations(ctx context.Context, sessionID string, opts FileOpListOpts) ([]FileOperation, error) {
	_ = ctx
	m.mu.RLock()
	defer m.mu.RUnlock()
	limit := opts.Limit
	if limit <= 0 {
		limit = 500
	}
	var out []FileOperation
	for _, op := range m.fileOps {
		if op.SessionID != sessionID {
			continue
		}
		if opts.OpType != "" && op.OpType != opts.OpType {
			continue
		}
		if opts.FilePath != "" && op.FilePath != opts.FilePath {
			continue
		}
		out = append(out, op)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (m *MemStore) ListToolExecutions(ctx context.Context, sessionID string) ([]ToolExecution, error) {
	_ = ctx
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []ToolExecution
	for _, te := range m.toolExecs {
		if te.SessionID == sessionID {
			out = append(out, te)
		}
	}
	return out, nil
}

func (m *MemStore) UpsertSessionSnapshot(ctx context.Context, snap *SessionSnapshot) error {
	_ = ctx
	if snap == nil {
		return fmt.Errorf("snapshot is nil")
	}
	if strings.TrimSpace(snap.SessionID) == "" || strings.TrimSpace(snap.ProjectID) == "" {
		return fmt.Errorf("snapshot requires session_id and project_id")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	max := 0
	for _, s := range m.snapshots {
		if s.SessionID == snap.SessionID && s.SnapshotVersion > max {
			max = s.SnapshotVersion
		}
	}
	snap.SnapshotVersion = max + 1
	if snap.ID == "" {
		snap.ID = newID("snap")
	}
	now := time.Now().UTC()
	snap.CreatedAt = now
	snap.UpdatedAt = now
	// First insert records the uploader. A later upsert cannot claim an
	// ownerless session or replace an existing owner.
	if m.sessionOwners == nil {
		m.sessionOwners = make(map[string]sessionContentOwner)
	}
	if row, ok := m.sessionOwners[snap.SessionID]; ok {
		if row.OwnerUserID != "" {
			snap.OwnerUserID = row.OwnerUserID
		} else {
			snap.OwnerUserID = ""
		}
	} else {
		m.sessionOwners[snap.SessionID] = sessionContentOwner{
			ProjectID: snap.ProjectID, OwnerUserID: snap.OwnerUserID,
		}
	}
	cp := *snap
	m.snapshots = append(m.snapshots, cp)
	return nil
}

func (m *MemStore) GetLatestSnapshot(ctx context.Context, sessionID string) (*SessionSnapshot, error) {
	_ = ctx
	m.mu.RLock()
	defer m.mu.RUnlock()
	var best *SessionSnapshot
	for i := range m.snapshots {
		s := &m.snapshots[i]
		if s.SessionID != sessionID {
			continue
		}
		if best == nil || s.SnapshotVersion > best.SnapshotVersion {
			cp := *s
			best = &cp
		}
	}
	if best == nil {
		return nil, ErrNotFound
	}
	return best, nil
}

func (m *MemStore) ListSnapshotsForProject(ctx context.Context, projectID string, limit int) ([]SessionSnapshot, error) {
	_ = ctx
	if limit <= 0 {
		limit = 50
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	latest := map[string]SessionSnapshot{}
	for _, s := range m.snapshots {
		if s.ProjectID != projectID {
			continue
		}
		cur, ok := latest[s.SessionID]
		if !ok || s.SnapshotVersion > cur.SnapshotVersion {
			cp := s
			cp.UncommittedDiff = nil
			cp.TranscriptPayload = nil
			cp.ArtifactsBundle = nil
			latest[s.SessionID] = cp
		}
	}
	out := make([]SessionSnapshot, 0, len(latest))
	for _, s := range latest {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *MemStore) PruneOldSnapshots(ctx context.Context, sessionID string, keepN int) error {
	_ = ctx
	if keepN <= 0 {
		keepN = 5
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var forSession []SessionSnapshot
	var other []SessionSnapshot
	for _, s := range m.snapshots {
		if s.SessionID == sessionID {
			forSession = append(forSession, s)
		} else {
			other = append(other, s)
		}
	}
	sort.Slice(forSession, func(i, j int) bool {
		return forSession[i].SnapshotVersion > forSession[j].SnapshotVersion
	})
	if len(forSession) > keepN {
		forSession = forSession[:keepN]
	}
	m.snapshots = append(other, forSession...)
	return nil
}

// ListSnapshotSessionIDs returns distinct session IDs that have snapshots (tests/maintenance).
func (m *MemStore) ListSnapshotSessionIDs() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	seen := map[string]bool{}
	var out []string
	for _, s := range m.snapshots {
		if !seen[s.SessionID] {
			seen[s.SessionID] = true
			out = append(out, s.SessionID)
		}
	}
	return out
}

var _ ProvenanceStore = (*MemStore)(nil)
var _ ProvenanceStore = (*PostgresStore)(nil)
