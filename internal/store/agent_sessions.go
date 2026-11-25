package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"central-memory/internal/capture"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrStorageFull means file blobs cannot be accepted until the plan has room.
var ErrStorageFull = errors.New("storage full")

// MissingBlobsError is returned when a version cannot become complete
// because the manifest names hashes that are not stored.
type MissingBlobsError struct {
	Missing []string
}

func (e *MissingBlobsError) Error() string {
	return "manifest references missing blobs"
}

// AgentSession is one harness conversation in the cloud (spec 4.2).
type AgentSession struct {
	ID                string     `json:"id"`
	ProjectID         string     `json:"project_id"`
	OwnerUserID       string     `json:"owner_user_id"`
	Harness           string     `json:"harness"`
	NativeID          string     `json:"native_id"`
	OriginMachineID   string     `json:"origin_machine_id,omitempty"`
	WorkspaceRootHint string     `json:"workspace_root_hint,omitempty"`
	Title             string     `json:"title,omitempty"`
	StartedAt         time.Time  `json:"started_at"`
	LastActiveAt      time.Time  `json:"last_active_at"`
	EndedAt           *time.Time `json:"ended_at,omitempty"`
	Visibility        string     `json:"visibility"`
	ParentSessionID   string     `json:"parent_session_id,omitempty"`
	LineageKind       string     `json:"lineage_kind,omitempty"`
	Summary           string     `json:"summary,omitempty"`
	// CodeMergedInto / CodeMergedAt mark a code-only merge (D17); conversations stay separate.
	CodeMergedInto string     `json:"code_merged_into,omitempty"`
	CodeMergedAt   *time.Time `json:"code_merged_at,omitempty"`
	// ParentNativeID is the native id of the session that started this
	// sub-agent run (same project, harness and machine). Upserts set it when
	// given and never clear it. Separate from ParentSessionID (forks).
	ParentNativeID string `json:"parent_native_id,omitempty"`
}

// SessionVersion is one upload of a session. uploading versions are not restorable.
type SessionVersion struct {
	ID          string          `json:"id"`
	SessionID   string          `json:"session_id"`
	Version     int             `json:"version"`
	CreatedAt   time.Time       `json:"created_at"`
	TurnCount   int             `json:"turn_count"`
	ManifestSHA string          `json:"manifest_sha256,omitempty"`
	Manifest    json.RawMessage `json:"manifest,omitempty"`
	UploadedBy  string          `json:"uploaded_by_user_id,omitempty"`
	State       string          `json:"state"`
}

// SessionTurn is one raw turn, including structured tool calls.
type SessionTurn struct {
	SessionID   string          `json:"session_id"`
	Idx         int             `json:"idx"`
	Role        string          `json:"role"`
	At          time.Time       `json:"ts"`
	SourceAt    *time.Time      `json:"source_ts,omitempty"`
	TextPreview string          `json:"text_preview,omitempty"`
	ToolCalls   json.RawMessage `json:"tool_calls"`
	BlobRef     string          `json:"blob_ref,omitempty"`
}

// TimelineItem is one session card on GET /v1/timeline.
type TimelineItem struct {
	SessionID    string    `json:"session_id"`
	ProjectID    string    `json:"project_id"`
	Harness      string    `json:"harness"`
	NativeID     string    `json:"native_id"`
	Title        string    `json:"title,omitempty"`
	Summary      string    `json:"summary,omitempty"`
	Visibility   string    `json:"visibility"`
	OwnerUserID  string    `json:"owner_user_id"`
	LastActiveAt time.Time `json:"last_active_at"`
	TurnCount    int       `json:"turn_count"`
	VersionState string    `json:"version_state,omitempty"`
	MatchPreview string    `json:"match_preview,omitempty"`
	MatchTurnIdx *int      `json:"match_turn_idx,omitempty"`
	MatchRole    string    `json:"match_role,omitempty"`
	// LineageKind is "subagent" for a sub-agent run (listed only with
	// IncludeSubagents). ParentSessionID is the session that started it,
	// when that is known and captured.
	LineageKind     string `json:"lineage_kind,omitempty"`
	ParentSessionID string `json:"parent_session_id,omitempty"`
	// SubagentCount is how many sub-agent runs this chat started.
	SubagentCount int `json:"subagent_count,omitempty"`
}

// TimelineQuery filters GET /v1/timeline.
type TimelineQuery struct {
	UserID    string
	ProjectID string
	Harness   string
	Query     string
	Cursor    string
	Limit     int
	// HideEmpty drops sessions with no turns (turn_count 0).
	HideEmpty bool
	// IncludeSubagents lists sub-agent runs too (IsSubagentRun). By default
	// they are left out before paging, so a page and its cursor count only
	// the chats people had.
	IncludeSubagents bool
}

// SessionGrantOpts is a live or point-in-time share (D15).
// Live=true (default) covers all current and future complete versions.
// Live=false pins the grant to VersionID. Team shares always force Live.
type SessionGrantOpts struct {
	Live      bool   `json:"live"`
	VersionID string `json:"version_id,omitempty"`
}

// AgentCloudStore is the cloud session API (spec 4.2 and 3.5).
type AgentCloudStore interface {
	UpsertAgentSession(ctx context.Context, in *AgentSession) (*AgentSession, error)
	GetAgentSession(ctx context.Context, id string) (*AgentSession, error)
	CanReadAgentSession(ctx context.Context, userID, sessionID string) (bool, error)
	CanReadAgentSessionLive(ctx context.Context, userID, sessionID string) (bool, error)
	GrantAgentSession(ctx context.Context, sessionID, granteeUserID, grantedBy string) error
	GrantAgentSessionOpts(ctx context.Context, sessionID, granteeUserID, grantedBy string, opts SessionGrantOpts) error
	ListVisibleSessionVersions(ctx context.Context, sessionID, userID string) ([]SessionVersion, error)
	ListAgentSessionForks(ctx context.Context, parentSessionID string) ([]AgentSession, error)
	ForkAgentSession(ctx context.Context, parentSessionID, ownerUserID string) (*AgentSession, error)
	// ForkAgentSessionThrough forks with only the turns whose idx is at most
	// throughIdx ("fork from here"); nil copies every turn.
	ForkAgentSessionThrough(ctx context.Context, parentSessionID, ownerUserID string, throughIdx *int) (*AgentSession, error)
	MarkAgentSessionCodeMerged(ctx context.Context, fromSessionID, intoSessionID string) (*AgentSession, error)
	AppendSessionTurn(ctx context.Context, turn SessionTurn) error
	ListSessionTurns(ctx context.Context, sessionID string) ([]SessionTurn, error)
	CreateSessionVersion(ctx context.Context, sessionID, uploadedBy string) (*SessionVersion, error)
	PutBlob(ctx context.Context, projectID, sum, kind, purpose string, body []byte) error
	MissingBlobs(ctx context.Context, projectID string, hashes []string) ([]string, error)
	CompleteSessionVersion(ctx context.Context, sessionID string, version int, manifest json.RawMessage) (*SessionVersion, error)
	SetStorageUsage(ctx context.Context, scope string, used, cap int64) error
	ListTimeline(ctx context.Context, q TimelineQuery) (items []TimelineItem, nextCursor string, err error)
}

type agentGrant struct {
	Grantee   string
	Team      bool
	By        string
	Revoked   bool
	Live      bool
	VersionID string
	CreatedAt time.Time
}

type blobRec struct {
	Size    int64
	Kind    string
	Purpose string
	Body    []byte
}

func nativeKey(projectID, harness, nativeID, machine string) string {
	return projectID + "\x00" + harness + "\x00" + nativeID + "\x00" + machine
}

func blobKey(projectID, sum string) string {
	return projectID + "\x00" + sum
}

func normalizeHash(h string) string {
	h = strings.TrimSpace(strings.ToLower(h))
	h = strings.TrimPrefix(h, "sha256:")
	return h
}

func (m *MemStore) ensureAgentMaps() {
	if m.agentSessions == nil {
		m.agentSessions = map[string]*AgentSession{}
	}
	if m.agentByNative == nil {
		m.agentByNative = map[string]string{}
	}
	if m.sessionVersions == nil {
		m.sessionVersions = map[string][]SessionVersion{}
	}
	if m.blobs == nil {
		m.blobs = map[string]blobRec{}
	}
	if m.sessionTurns == nil {
		m.sessionTurns = map[string][]SessionTurn{}
	}
	if m.agentGrants == nil {
		m.agentGrants = map[string][]agentGrant{}
	}
	if m.storageUsage == nil {
		m.storageUsage = map[string]storageRec{}
	}
}

type storageRec struct {
	Used int64
	Cap  int64
}

func (m *MemStore) UpsertAgentSession(ctx context.Context, in *AgentSession) (*AgentSession, error) {
	_ = ctx
	if in == nil {
		return nil, errors.New("store: session is nil")
	}
	in.ProjectID = strings.TrimSpace(in.ProjectID)
	in.OwnerUserID = strings.TrimSpace(in.OwnerUserID)
	in.Harness = strings.TrimSpace(in.Harness)
	in.NativeID = strings.TrimSpace(in.NativeID)
	in.OriginMachineID = strings.TrimSpace(in.OriginMachineID)
	if in.ProjectID == "" || in.OwnerUserID == "" || in.Harness == "" || in.NativeID == "" {
		return nil, errors.New("store: agent session requires project, owner, harness, and native_id")
	}
	if in.Visibility == "" {
		in.Visibility = "private"
	}
	if in.Visibility != "private" && in.Visibility != "team" {
		return nil, errors.New("store: visibility must be private or team")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureAgentMaps()
	key := nativeKey(in.ProjectID, in.Harness, in.NativeID, in.OriginMachineID)
	if id, ok := m.agentByNative[key]; ok {
		cur := m.agentSessions[id]
		if cur.OwnerUserID != in.OwnerUserID {
			return nil, fmt.Errorf("store: session owned by someone else: %w", ErrConflict)
		}
		// A placeholder ("Captured codex session") never replaces a real or
		// derived title: captures resend it on every upsert.
		if t := strings.TrimSpace(in.Title); t != "" && (!IsPlaceholderTitle(t) || IsPlaceholderTitle(cur.Title)) {
			cur.Title = t
		}
		if strings.TrimSpace(in.Summary) != "" {
			cur.Summary = strings.TrimSpace(in.Summary)
		}
		if in.WorkspaceRootHint != "" {
			cur.WorkspaceRootHint = in.WorkspaceRootHint
		}
		if p := strings.TrimSpace(in.ParentNativeID); p != "" {
			cur.ParentNativeID = p
		}
		cp := *cur
		return &cp, nil
	}
	now := time.Now().UTC()
	id := newID("asess")
	row := &AgentSession{
		ID: id, ProjectID: in.ProjectID, OwnerUserID: in.OwnerUserID,
		Harness: in.Harness, NativeID: in.NativeID, OriginMachineID: in.OriginMachineID,
		WorkspaceRootHint: in.WorkspaceRootHint, Title: strings.TrimSpace(in.Title),
		StartedAt: now, LastActiveAt: now, Visibility: in.Visibility,
		ParentSessionID: in.ParentSessionID, LineageKind: in.LineageKind,
		Summary: strings.TrimSpace(in.Summary), ParentNativeID: strings.TrimSpace(in.ParentNativeID),
	}
	m.agentSessions[id] = row
	m.agentByNative[key] = id
	cp := *row
	return &cp, nil
}

func (m *MemStore) GetAgentSession(ctx context.Context, id string) (*AgentSession, error) {
	_ = ctx
	m.mu.RLock()
	defer m.mu.RUnlock()
	row := m.agentSessions[strings.TrimSpace(id)]
	if row == nil {
		return nil, ErrNotFound
	}
	cp := *row
	return &cp, nil
}

func (m *MemStore) CanReadAgentSession(ctx context.Context, userID, sessionID string) (bool, error) {
	_ = ctx
	userID = strings.TrimSpace(userID)
	m.mu.RLock()
	defer m.mu.RUnlock()
	row := m.agentSessions[strings.TrimSpace(sessionID)]
	if row == nil {
		return false, ErrNotFound
	}
	return m.agentVisibleLocked(userID, row), nil
}

func (m *MemStore) CanReadAgentSessionLive(ctx context.Context, userID, sessionID string) (bool, error) {
	_ = ctx
	userID = strings.TrimSpace(userID)
	m.mu.RLock()
	defer m.mu.RUnlock()
	row := m.agentSessions[strings.TrimSpace(sessionID)]
	if row == nil {
		return false, ErrNotFound
	}
	return m.agentLiveVisibleLocked(userID, row), nil
}

func (m *MemStore) agentVisibleLocked(userID string, row *AgentSession) bool {
	if row.OwnerUserID == userID {
		return true
	}
	for _, g := range m.agentGrants[row.ID] {
		if g.Revoked {
			continue
		}
		if g.Grantee != "" && g.Grantee == userID {
			return true
		}
	}
	if row.Visibility == "team" && m.projectMemberLocked(userID, row.ProjectID) {
		return true
	}
	return false
}

func (m *MemStore) agentLiveVisibleLocked(userID string, row *AgentSession) bool {
	if row.OwnerUserID == userID {
		return true
	}
	for _, g := range m.agentGrants[row.ID] {
		if g.Revoked || g.Grantee == "" || g.Grantee != userID {
			continue
		}
		// A grant without a pin predates point-in-time sharing and remains live.
		if g.Live || g.VersionID == "" {
			return true
		}
	}
	if row.Visibility == "team" && m.projectMemberLocked(userID, row.ProjectID) {
		return true
	}
	return false
}

func (m *MemStore) GrantAgentSession(ctx context.Context, sessionID, granteeUserID, grantedBy string) error {
	return m.GrantAgentSessionOpts(ctx, sessionID, granteeUserID, grantedBy, SessionGrantOpts{Live: true})
}

func normalizeGrantOpts(opts SessionGrantOpts) (SessionGrantOpts, error) {
	opts.VersionID = strings.TrimSpace(opts.VersionID)
	if !opts.Live && opts.VersionID == "" {
		return opts, errors.New("store: point-in-time grant requires version_id")
	}
	if opts.Live {
		// Live grants ignore a pinned version; keep VersionID empty for clarity.
		opts.VersionID = ""
	}
	return opts, nil
}

func (m *MemStore) GrantAgentSessionOpts(ctx context.Context, sessionID, granteeUserID, grantedBy string, opts SessionGrantOpts) error {
	_ = ctx
	sessionID = strings.TrimSpace(sessionID)
	granteeUserID = strings.TrimSpace(granteeUserID)
	if sessionID == "" || granteeUserID == "" {
		return errors.New("store: grant requires session and user")
	}
	opts, err := normalizeGrantOpts(opts)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureAgentMaps()
	if m.agentSessions[sessionID] == nil {
		return ErrNotFound
	}
	if opts.VersionID != "" {
		found := false
		for _, v := range m.sessionVersions[sessionID] {
			if v.ID == opts.VersionID {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("store: version %s: %w", opts.VersionID, ErrNotFound)
		}
	}
	for i, g := range m.agentGrants[sessionID] {
		if !g.Revoked && g.Grantee == granteeUserID {
			m.agentGrants[sessionID][i].Live = opts.Live
			m.agentGrants[sessionID][i].VersionID = opts.VersionID
			m.agentGrants[sessionID][i].By = grantedBy
			return nil
		}
	}
	m.agentGrants[sessionID] = append(m.agentGrants[sessionID], agentGrant{
		Grantee: granteeUserID, By: grantedBy, Live: opts.Live, VersionID: opts.VersionID,
		CreatedAt: time.Now().UTC(),
	})
	return nil
}

func versionReadable(state string) bool {
	return state == "complete" || state == "transcript_only"
}

func (m *MemStore) ListVisibleSessionVersions(ctx context.Context, sessionID, userID string) ([]SessionVersion, error) {
	_ = ctx
	sessionID = strings.TrimSpace(sessionID)
	userID = strings.TrimSpace(userID)
	m.mu.RLock()
	defer m.mu.RUnlock()
	row := m.agentSessions[sessionID]
	if row == nil {
		return nil, ErrNotFound
	}
	if !m.agentVisibleLocked(userID, row) {
		return []SessionVersion{}, nil
	}
	versions := m.sessionVersions[sessionID]
	if row.OwnerUserID == userID || row.Visibility == "team" {
		return filterCompleteVersions(versions, ""), nil
	}
	pin := ""
	live := false
	for _, g := range m.agentGrants[sessionID] {
		if g.Revoked || g.Grantee != userID {
			continue
		}
		if g.Live {
			live = true
			pin = ""
			break
		}
		pin = g.VersionID
	}
	if live || pin == "" && row.OwnerUserID == userID {
		return filterCompleteVersions(versions, ""), nil
	}
	if pin == "" {
		// Grant without pin defaults to live for legacy rows.
		return filterCompleteVersions(versions, ""), nil
	}
	return filterCompleteVersions(versions, pin), nil
}

func filterCompleteVersions(versions []SessionVersion, onlyID string) []SessionVersion {
	var out []SessionVersion
	for _, v := range versions {
		if !versionReadable(v.State) {
			continue
		}
		if onlyID != "" && v.ID != onlyID {
			continue
		}
		cp := v
		cp.Manifest = append([]byte(nil), v.Manifest...)
		out = append(out, cp)
	}
	if out == nil {
		out = []SessionVersion{}
	}
	return out
}

func (m *MemStore) ListAgentSessionForks(ctx context.Context, parentSessionID string) ([]AgentSession, error) {
	_ = ctx
	parentSessionID = strings.TrimSpace(parentSessionID)
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.agentSessions[parentSessionID] == nil {
		return nil, ErrNotFound
	}
	var out []AgentSession
	for _, row := range m.agentSessions {
		if row == nil || row.ParentSessionID != parentSessionID {
			continue
		}
		out = append(out, *row)
	}
	if out == nil {
		out = []AgentSession{}
	}
	return out, nil
}

func (m *MemStore) ForkAgentSession(ctx context.Context, parentSessionID, ownerUserID string) (*AgentSession, error) {
	return m.ForkAgentSessionThrough(ctx, parentSessionID, ownerUserID, nil)
}

func (m *MemStore) ForkAgentSessionThrough(ctx context.Context, parentSessionID, ownerUserID string, throughIdx *int) (*AgentSession, error) {
	_ = ctx
	parentSessionID = strings.TrimSpace(parentSessionID)
	ownerUserID = strings.TrimSpace(ownerUserID)
	if parentSessionID == "" || ownerUserID == "" {
		return nil, errors.New("store: fork requires parent session and owner")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureAgentMaps()
	parent := m.agentSessions[parentSessionID]
	if parent == nil {
		return nil, ErrNotFound
	}
	now := time.Now().UTC()
	id := newID("asess")
	native := parent.NativeID + "-fork-" + id[len(id)-6:]
	row := &AgentSession{
		ID: id, ProjectID: parent.ProjectID, OwnerUserID: ownerUserID,
		Harness: parent.Harness, NativeID: native, OriginMachineID: parent.OriginMachineID,
		WorkspaceRootHint: parent.WorkspaceRootHint, Title: parent.Title,
		StartedAt: now, LastActiveAt: now, Visibility: "private",
		ParentSessionID: parent.ID, LineageKind: "fork",
		Summary: parent.Summary,
	}
	key := nativeKey(row.ProjectID, row.Harness, row.NativeID, row.OriginMachineID)
	m.agentSessions[id] = row
	m.agentByNative[key] = id
	for _, original := range m.sessionTurns[parent.ID] {
		if throughIdx != nil && original.Idx > *throughIdx {
			continue
		}
		copyTurn := original
		copyTurn.SessionID = id
		copyTurn.ToolCalls = append(json.RawMessage(nil), original.ToolCalls...)
		m.sessionTurns[id] = append(m.sessionTurns[id], copyTurn)
	}
	cp := *row
	return &cp, nil
}

// MarkAgentSessionCodeMerged records a code-only merge marker on the source
// fork's lineage (D17). No new session is created and turns are unchanged.
func (m *MemStore) MarkAgentSessionCodeMerged(ctx context.Context, fromSessionID, intoSessionID string) (*AgentSession, error) {
	_ = ctx
	fromSessionID = strings.TrimSpace(fromSessionID)
	intoSessionID = strings.TrimSpace(intoSessionID)
	if fromSessionID == "" || intoSessionID == "" {
		return nil, errors.New("store: code merge requires from and into session ids")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureAgentMaps()
	from := m.agentSessions[fromSessionID]
	into := m.agentSessions[intoSessionID]
	if from == nil || into == nil {
		return nil, ErrNotFound
	}
	now := time.Now().UTC()
	from.CodeMergedInto = into.ID
	from.CodeMergedAt = &now
	if from.LineageKind == "" || from.LineageKind == "fork" {
		from.LineageKind = "fork"
	}
	cp := *from
	return &cp, nil
}

func (m *MemStore) AppendSessionTurn(ctx context.Context, turn SessionTurn) error {
	_ = ctx
	turn.SessionID = strings.TrimSpace(turn.SessionID)
	turn.Role = strings.TrimSpace(turn.Role)
	if turn.SessionID == "" || turn.Role == "" {
		return errors.New("store: turn requires session and role")
	}
	if len(turn.ToolCalls) == 0 {
		turn.ToolCalls = json.RawMessage("[]")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureAgentMaps()
	row := m.agentSessions[turn.SessionID]
	if row == nil {
		return ErrNotFound
	}
	if turn.At.IsZero() {
		turn.At = time.Now().UTC()
	}
	turns := m.sessionTurns[turn.SessionID]
	for i, existing := range turns {
		if existing.Idx == turn.Idx {
			if turn.SourceAt != nil && existing.SourceAt == nil {
				turns[i].SourceAt = turn.SourceAt
				// Same as Postgres: a fully dated history dates the chat.
				var latest time.Time
				complete := true
				for _, t := range turns {
					if t.SourceAt == nil {
						complete = false
						break
					}
					if t.SourceAt.After(latest) {
						latest = *t.SourceAt
					}
				}
				if complete && !latest.IsZero() {
					row.LastActiveAt = latest
				}
			}
			return fmt.Errorf("store: turn %d: %w", turn.Idx, ErrConflict)
		}
	}
	m.sessionTurns[turn.SessionID] = append(turns, turn)
	// A captured chat with a placeholder title takes its first request.
	if turn.Role == "user" && IsPlaceholderTitle(row.Title) {
		if derived := TitleFromRequest(turn.TextPreview); derived != "" {
			row.Title = derived
		}
	}
	activity := turn.At
	if turn.SourceAt != nil && !turn.SourceAt.IsZero() {
		activity = *turn.SourceAt
	}
	if len(turns) == 0 || activity.After(row.LastActiveAt) {
		row.LastActiveAt = activity
	}
	return nil
}

func (m *MemStore) ListSessionTurns(ctx context.Context, sessionID string) ([]SessionTurn, error) {
	_ = ctx
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.agentSessions[sessionID] == nil {
		return nil, ErrNotFound
	}
	out := append([]SessionTurn(nil), m.sessionTurns[sessionID]...)
	if out == nil {
		out = []SessionTurn{}
	}
	return out, nil
}

func (m *MemStore) CreateSessionVersion(ctx context.Context, sessionID, uploadedBy string) (*SessionVersion, error) {
	_ = ctx
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureAgentMaps()
	if m.agentSessions[sessionID] == nil {
		return nil, ErrNotFound
	}
	next := 1
	for _, v := range m.sessionVersions[sessionID] {
		if v.Version >= next {
			next = v.Version + 1
		}
	}
	v := SessionVersion{
		ID: newID("sver"), SessionID: sessionID, Version: next,
		CreatedAt: time.Now().UTC(), UploadedBy: uploadedBy, State: "uploading",
	}
	m.sessionVersions[sessionID] = append(m.sessionVersions[sessionID], v)
	return &v, nil
}

func (m *MemStore) PutBlob(ctx context.Context, projectID, sum, kind, purpose string, body []byte) error {
	_ = ctx
	projectID = strings.TrimSpace(projectID)
	sum = normalizeHash(sum)
	if projectID == "" || sum == "" {
		return errors.New("store: blob requires project and sha256")
	}
	got := sha256.Sum256(body)
	if hex.EncodeToString(got[:]) != sum {
		return errors.New("store: blob bytes do not match sha256")
	}
	if kind == "" {
		kind = "plain"
	}
	if purpose == "" {
		purpose = "file"
	}
	if purpose != "file" && purpose != "transcript" {
		return errors.New("store: blob purpose must be file or transcript")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureAgentMaps()
	used, cap, scopeKey := m.resolveBlobStorageLocked(projectID)
	if purpose == "file" && cap > 0 && used+int64(len(body)) > cap {
		return fmt.Errorf("store: %w", ErrStorageFull)
	}
	m.blobs[blobKey(projectID, sum)] = blobRec{Size: int64(len(body)), Kind: kind, Purpose: purpose, Body: append([]byte(nil), body...)}
	if purpose == "file" && (cap > 0 || used > 0) {
		used += int64(len(body))
		m.storageUsage[scopeKey] = storageRec{Used: used, Cap: cap}
	}
	return nil
}

// resolveBlobStorageLocked returns used/cap and the map key to update.
func (m *MemStore) resolveBlobStorageLocked(projectID string) (used, cap int64, scopeKey string) {
	scopeKey = projectID
	if rec, ok := m.storageUsage[projectID]; ok {
		used, cap = rec.Used, rec.Cap
	}
	owner := ""
	if p := m.projects[projectID]; p != nil {
		owner = strings.TrimSpace(p.CreatedBy)
	}
	if owner != "" {
		userScope := "user:" + owner
		if rec, ok := m.storageUsage[userScope]; ok && (cap <= 0 || !mapHasStorage(m.storageUsage, projectID)) {
			used, cap = rec.Used, rec.Cap
			scopeKey = userScope
		}
	}
	if rec, ok := m.storageUsage["user"]; ok && cap <= 0 {
		used, cap = rec.Used, rec.Cap
		scopeKey = "user"
	}
	if cap <= 0 {
		if owner != "" {
			cap = m.planStorageCapLocked(OwnerUser, owner)
			if scopeKey == projectID && owner != "" {
				scopeKey = "user:" + owner
			}
		} else {
			cap = PlanStorageBytes(PlanFree)
		}
		if existing, ok := m.storageUsage[scopeKey]; ok {
			used = existing.Used
		}
	}
	return used, cap, scopeKey
}

func mapHasStorage(m map[string]storageRec, key string) bool {
	_, ok := m[key]
	return ok
}

func (m *MemStore) MissingBlobs(ctx context.Context, projectID string, hashes []string) ([]string, error) {
	_ = ctx
	m.mu.RLock()
	defer m.mu.RUnlock()
	var missing []string
	for _, h := range hashes {
		h = normalizeHash(h)
		if h == "" {
			continue
		}
		if _, ok := m.blobs[blobKey(projectID, h)]; !ok {
			missing = append(missing, h)
		}
	}
	if missing == nil {
		missing = []string{}
	}
	return missing, nil
}

func (m *MemStore) CompleteSessionVersion(ctx context.Context, sessionID string, version int, manifest json.RawMessage) (*SessionVersion, error) {
	_ = ctx
	refs, fileRefs, err := manifestBlobRefs(manifest)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureAgentMaps()
	sess := m.agentSessions[sessionID]
	if sess == nil {
		return nil, ErrNotFound
	}
	idx := -1
	for i := range m.sessionVersions[sessionID] {
		if m.sessionVersions[sessionID][i].Version == version {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil, ErrNotFound
	}
	cur := m.sessionVersions[sessionID][idx]
	if cur.State == "complete" || cur.State == "transcript_only" {
		return nil, fmt.Errorf("store: version is immutable: %w", ErrConflict)
	}
	var missing []string
	for _, h := range refs {
		if _, ok := m.blobs[blobKey(sess.ProjectID, h)]; !ok {
			missing = append(missing, h)
		}
	}
	if len(missing) > 0 {
		return nil, &MissingBlobsError{Missing: missing}
	}
	state := "complete"
	used, cap, _ := m.resolveBlobStorageLocked(sess.ProjectID)
	if capture.UsageState(used, cap) == "full" {
		if fileRefs > 0 {
			return nil, fmt.Errorf("store: %w", ErrStorageFull)
		}
		state = "transcript_only"
	}
	sum := sha256.Sum256(manifest)
	cur.State = state
	cur.Manifest = append(json.RawMessage(nil), manifest...)
	cur.ManifestSHA = hex.EncodeToString(sum[:])
	m.sessionVersions[sessionID][idx] = cur
	sess.LastActiveAt = time.Now().UTC()
	return &cur, nil
}

func (m *MemStore) SetStorageUsage(ctx context.Context, scope string, used, cap int64) error {
	_ = ctx
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return errors.New("store: storage scope is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureAgentMaps()
	m.storageUsage[scope] = storageRec{Used: used, Cap: cap}
	return nil
}

// timelineTurnPreview is a chat's fallback summary from one user turn: the
// request without harness wrapper, whitespace collapsed, cut at 260 chars.
// "" when the turn is only wrapper.
func timelineTurnPreview(raw string) string {
	text := strings.Join(strings.Fields(requestText(raw)), " ")
	chars := []rune(text)
	if len(chars) > 260 {
		return string(chars[:260]) + "…"
	}
	return text
}

// timelineFallbackTurns is how many leading user turns may supply the
// fallback summary; envelope-only turns are skipped.
const timelineFallbackTurns = 3

// timelineFirstRequestPreview is the preview of the first of the leading
// user turns that still has text once its harness wrapper is removed.
func timelineFirstRequestPreview(raw []string) string {
	for i, text := range raw {
		if i >= timelineFallbackTurns {
			break
		}
		if p := timelineTurnPreview(text); p != "" {
			return p
		}
	}
	return ""
}

func (m *MemStore) ListTimeline(ctx context.Context, q TimelineQuery) ([]TimelineItem, string, error) {
	_ = ctx
	if q.Limit <= 0 || q.Limit > 100 {
		q.Limit = 30
	}
	var cursorAt time.Time
	cursorID := ""
	if q.Cursor != "" {
		i := strings.LastIndex(q.Cursor, "|")
		if i <= 0 {
			return nil, "", errors.New("store: bad timeline cursor")
		}
		var err error
		cursorAt, err = time.Parse(time.RFC3339Nano, q.Cursor[:i])
		if err != nil {
			return nil, "", errors.New("store: bad timeline cursor")
		}
		cursorID = q.Cursor[i+1:]
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var items []TimelineItem
	for _, row := range m.agentSessions {
		if row == nil || !m.agentVisibleLocked(q.UserID, row) {
			continue
		}
		if q.ProjectID != "" && row.ProjectID != q.ProjectID {
			continue
		}
		if q.Harness != "" && row.Harness != q.Harness {
			continue
		}
		subagent := IsSubagentRun(row.NativeID, row.Title, row.ParentNativeID)
		if subagent && !q.IncludeSubagents {
			continue
		}
		var matchTurn *SessionTurn
		live := m.agentLiveVisibleLocked(q.UserID, row)
		if q.Query != "" {
			needle := strings.ToLower(q.Query)
			matched := strings.Contains(strings.ToLower(row.Title+" "+row.NativeID), needle)
			if live && strings.Contains(strings.ToLower(row.Summary), needle) {
				matched = true
			}
			for _, turn := range m.sessionTurns[row.ID] {
				if !live {
					break
				}
				if (turn.Role == "user" || turn.Role == "assistant") && strings.Contains(strings.ToLower(turn.TextPreview), needle) {
					matched = true
					copy := turn
					matchTurn = &copy
					break
				}
			}
			if !matched {
				continue
			}
		}
		if !cursorAt.IsZero() {
			if row.LastActiveAt.After(cursorAt) {
				continue
			}
			if row.LastActiveAt.Equal(cursorAt) && row.ID >= cursorID {
				continue
			}
		}
		item := TimelineItem{
			SessionID: row.ID, ProjectID: row.ProjectID, Harness: row.Harness,
			NativeID: row.NativeID, Title: row.Title, Summary: row.Summary,
			Visibility: row.Visibility, OwnerUserID: row.OwnerUserID, LastActiveAt: row.LastActiveAt,
		}
		if subagent {
			item.LineageKind = "subagent"
		}
		for _, other := range m.agentSessions {
			if other == nil || other.ProjectID != row.ProjectID || other.Harness != row.Harness ||
				other.OriginMachineID != row.OriginMachineID || other.OwnerUserID != row.OwnerUserID {
				continue
			}
			if row.ParentNativeID != "" && other.NativeID == row.ParentNativeID {
				item.ParentSessionID = other.ID
			}
			if other.ParentNativeID == row.NativeID {
				item.SubagentCount++
			}
		}
		if !live {
			item.Summary = ""
		}
		if matchTurn != nil {
			idx := matchTurn.Idx
			item.MatchTurnIdx, item.MatchRole = &idx, matchTurn.Role
			item.MatchPreview = TimelineMatchPreview(matchTurn.TextPreview, q.Query)
		}
		// A version-limited grantee sees neither summary nor turn previews:
		// both describe the chat as it is now, not the shared version.
		if live && (strings.TrimSpace(item.Summary) == "" || IsPlaceholderTitle(item.Title)) {
			var users []SessionTurn
			for _, turn := range m.sessionTurns[row.ID] {
				if turn.Role == "user" && strings.TrimSpace(turn.TextPreview) != "" {
					users = append(users, turn)
				}
			}
			sort.SliceStable(users, func(i, j int) bool { return users[i].Idx < users[j].Idx })
			previews := make([]string, 0, len(users))
			for _, turn := range users {
				previews = append(previews, turn.TextPreview)
			}
			if strings.TrimSpace(item.Summary) == "" {
				item.Summary = timelineFirstRequestPreview(previews)
			}
			item.Title = PresentTitle(item.Title, previews)
		}
		var best *SessionVersion
		for i := range m.sessionVersions[row.ID] {
			v := &m.sessionVersions[row.ID][i]
			if v.State != "complete" && v.State != "transcript_only" {
				continue
			}
			if best == nil || v.Version > best.Version {
				best = v
			}
		}
		if best != nil {
			item.TurnCount = best.TurnCount
			item.VersionState = best.State
		}
		// Mirrored chats have turns but often no version: count the turns.
		if n := len(m.sessionTurns[row.ID]); n > item.TurnCount {
			item.TurnCount = n
		}
		if q.HideEmpty && item.TurnCount == 0 {
			continue
		}
		items = append(items, item)
	}
	// Newest first, then id descending so the cursor is stable.
	for i := 0; i < len(items); i++ {
		for j := i + 1; j < len(items); j++ {
			if items[j].LastActiveAt.After(items[i].LastActiveAt) ||
				(items[j].LastActiveAt.Equal(items[i].LastActiveAt) && items[j].SessionID > items[i].SessionID) {
				items[i], items[j] = items[j], items[i]
			}
		}
	}
	next := ""
	if len(items) > q.Limit {
		items = items[:q.Limit]
		last := items[len(items)-1]
		next = last.LastActiveAt.UTC().Format(time.RFC3339Nano) + "|" + last.SessionID
	}
	if items == nil {
		items = []TimelineItem{}
	}
	return items, next, nil
}

type manifestShape struct {
	Transcript struct {
		Blob string `json:"blob"`
	} `json:"transcript"`
	HarnessState []struct {
		Blob string `json:"blob"`
	} `json:"harness_state"`
	Git struct {
		Bundle string `json:"bundle"`
	} `json:"git"`
	Files []struct {
		Blob string `json:"blob"`
	} `json:"files"`
	Secrets []struct {
		Blob string `json:"blob"`
	} `json:"secrets"`
}

func manifestBlobRefs(raw json.RawMessage) (refs []string, fileRefs int, err error) {
	if len(raw) == 0 {
		return nil, 0, errors.New("store: manifest is required")
	}
	var doc manifestShape
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, 0, fmt.Errorf("store: manifest: %w", err)
	}
	add := func(h string, file bool) {
		h = normalizeHash(h)
		if h == "" || h == "null" {
			return
		}
		refs = append(refs, h)
		if file {
			fileRefs++
		}
	}
	add(doc.Transcript.Blob, false)
	for _, h := range doc.HarnessState {
		add(h.Blob, false)
	}
	add(doc.Git.Bundle, false)
	for _, f := range doc.Files {
		add(f.Blob, true)
	}
	for _, s := range doc.Secrets {
		add(s.Blob, true)
	}
	return refs, fileRefs, nil
}

func (s *PostgresStore) UpsertAgentSession(ctx context.Context, in *AgentSession) (*AgentSession, error) {
	if in == nil || !looksLikeUUID(in.ProjectID) || !looksLikeUUID(in.OwnerUserID) {
		return nil, errors.New("store: agent session ids must be uuids")
	}
	in.Harness = strings.TrimSpace(in.Harness)
	in.NativeID = strings.TrimSpace(in.NativeID)
	in.OriginMachineID = strings.TrimSpace(in.OriginMachineID)
	if in.Harness == "" || in.NativeID == "" {
		return nil, errors.New("store: harness and native_id are required")
	}
	if in.Visibility == "" {
		in.Visibility = "private"
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO agent_sessions (
			project_id, owner_user_id, harness, native_id, origin_machine_id,
			workspace_root_hint, title, visibility, summary, parent_native_id
		) VALUES (
			$1::uuid, $2::uuid, $3, $4, $5, NULLIF($6,''), NULLIF($7,''), $8, NULLIF($9,''), NULLIF($12,'')
		)
		ON CONFLICT (project_id, harness, native_id, origin_machine_id) DO UPDATE
		-- A placeholder ($10) never replaces a real or derived title:
		-- captures resend "Captured <harness> session" on every upsert.
		SET title = CASE WHEN $10::bool AND NULLIF(agent_sessions.title, '') IS NOT NULL
		                      AND agent_sessions.title !~* $11 THEN agent_sessions.title
		                 ELSE COALESCE(NULLIF(EXCLUDED.title, ''), agent_sessions.title) END,
		    summary = COALESCE(NULLIF(EXCLUDED.summary, ''), agent_sessions.summary),
		    -- Set when the daemon knows it (lazy backfill); never cleared.
		    parent_native_id = COALESCE(EXCLUDED.parent_native_id, agent_sessions.parent_native_id)
		WHERE agent_sessions.owner_user_id = EXCLUDED.owner_user_id
		RETURNING id::text, project_id::text, COALESCE(owner_user_id::text,''), harness, native_id,
		          origin_machine_id, COALESCE(workspace_root_hint,''), COALESCE(title,''),
		          started_at, last_active_at, ended_at, visibility,
		          COALESCE(parent_session_id::text,''), COALESCE(lineage_kind,''), COALESCE(summary,'')`,
		in.ProjectID, in.OwnerUserID, in.Harness, in.NativeID, in.OriginMachineID,
		in.WorkspaceRootHint, in.Title, in.Visibility, in.Summary,
		IsPlaceholderTitle(in.Title), placeholderTitlePattern, strings.TrimSpace(in.ParentNativeID))
	out, err := scanAgentSession(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("store: session owned by someone else: %w", ErrConflict)
	}
	if err != nil {
		return nil, err
	}
	out.ParentNativeID = strings.TrimSpace(in.ParentNativeID)
	return out, nil
}

func scanAgentSession(row pgx.Row) (*AgentSession, error) {
	var a AgentSession
	var ended *time.Time
	if err := row.Scan(&a.ID, &a.ProjectID, &a.OwnerUserID, &a.Harness, &a.NativeID, &a.OriginMachineID,
		&a.WorkspaceRootHint, &a.Title, &a.StartedAt, &a.LastActiveAt, &ended, &a.Visibility,
		&a.ParentSessionID, &a.LineageKind, &a.Summary); err != nil {
		return nil, err
	}
	a.EndedAt = ended
	return &a, nil
}

func (s *PostgresStore) GetAgentSession(ctx context.Context, id string) (*AgentSession, error) {
	if !looksLikeUUID(id) {
		return nil, ErrNotFound
	}
	row := s.pool.QueryRow(ctx, `
		SELECT id::text, project_id::text, COALESCE(owner_user_id::text,''), harness, native_id,
		       origin_machine_id, COALESCE(workspace_root_hint,''), COALESCE(title,''),
		       started_at, last_active_at, ended_at, visibility,
		       COALESCE(parent_session_id::text,''), COALESCE(lineage_kind,''), COALESCE(summary,'')
		FROM agent_sessions WHERE id = $1::uuid`, id)
	out, err := scanAgentSession(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return out, err
}

func (s *PostgresStore) CanReadAgentSession(ctx context.Context, userID, sessionID string) (bool, error) {
	if !looksLikeUUID(sessionID) || !looksLikeUUID(userID) {
		return false, nil
	}
	var ok bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (
		  SELECT 1 FROM agent_sessions s
		  WHERE s.id = $2::uuid AND (
		    s.owner_user_id = $1::uuid
		    OR EXISTS (
		      SELECT 1 FROM session_grants g
		      WHERE g.session_id = s.id AND g.revoked_at IS NULL AND g.grantee_user_id = $1::uuid
		    )
		    OR (
		      s.visibility = 'team' AND (
		        EXISTS (SELECT 1 FROM projects p WHERE p.id = s.project_id AND p.created_by = $1::uuid)
		        OR EXISTS (SELECT 1 FROM project_members pm WHERE pm.project_id = s.project_id AND pm.user_id = $1::uuid)
		        OR EXISTS (
		          SELECT 1 FROM projects p
		          JOIN organization_members om ON om.org_id = p.org_id
		          WHERE p.id = s.project_id AND om.user_id = $1::uuid AND om.role IN ('ADMIN','OWNER')
		        )
		      )
		    )
		  )
		)`, userID, sessionID).Scan(&ok)
	return ok, err
}

func (s *PostgresStore) CanReadAgentSessionLive(ctx context.Context, userID, sessionID string) (bool, error) {
	if !looksLikeUUID(sessionID) || !looksLikeUUID(userID) {
		return false, nil
	}
	var ok bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (
		  SELECT 1 FROM agent_sessions s
		  WHERE s.id = $2::uuid AND (
		    s.owner_user_id = $1::uuid
		    OR EXISTS (
		      SELECT 1 FROM session_grants g
		      WHERE g.session_id = s.id AND g.revoked_at IS NULL AND g.grantee_user_id = $1::uuid
		        AND g.live
		    )
		    OR (
		      s.visibility = 'team' AND (
		        EXISTS (SELECT 1 FROM projects p WHERE p.id = s.project_id AND p.created_by = $1::uuid)
		        OR EXISTS (SELECT 1 FROM project_members pm WHERE pm.project_id = s.project_id AND pm.user_id = $1::uuid)
		        OR EXISTS (
		          SELECT 1 FROM projects p
		          JOIN organization_members om ON om.org_id = p.org_id
		          WHERE p.id = s.project_id AND om.user_id = $1::uuid AND om.role IN ('ADMIN','OWNER')
		        )
		      )
		    )
		  )
		)`, userID, sessionID).Scan(&ok)
	return ok, err
}

func (s *PostgresStore) GrantAgentSession(ctx context.Context, sessionID, granteeUserID, grantedBy string) error {
	return s.GrantAgentSessionOpts(ctx, sessionID, granteeUserID, grantedBy, SessionGrantOpts{Live: true})
}

func (s *PostgresStore) GrantAgentSessionOpts(ctx context.Context, sessionID, granteeUserID, grantedBy string, opts SessionGrantOpts) error {
	if !looksLikeUUID(sessionID) || !looksLikeUUID(granteeUserID) {
		return errors.New("store: grant ids must be uuids")
	}
	opts, err := normalizeGrantOpts(opts)
	if err != nil {
		return err
	}
	if opts.VersionID != "" && !looksLikeUUID(opts.VersionID) {
		return errors.New("store: version_id must be a uuid")
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO session_grants (session_id, grantee_user_id, granted_by, live, version_id)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5::uuid)
		ON CONFLICT (session_id, grantee_user_id) WHERE revoked_at IS NULL AND grantee_user_id IS NOT NULL
		DO UPDATE SET live = EXCLUDED.live, version_id = EXCLUDED.version_id, granted_by = EXCLUDED.granted_by`,
		sessionID, granteeUserID, nullUUIDStrict(grantedBy), opts.Live, nullUUIDStrict(opts.VersionID))
	return err
}

func (s *PostgresStore) ListVisibleSessionVersions(ctx context.Context, sessionID, userID string) ([]SessionVersion, error) {
	if !looksLikeUUID(sessionID) || !looksLikeUUID(userID) {
		return nil, ErrNotFound
	}
	ok, err := s.CanReadAgentSession(ctx, userID, sessionID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return []SessionVersion{}, nil
	}
	var owner string
	var visibility string
	if err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(owner_user_id::text,''), visibility
		FROM agent_sessions WHERE id = $1::uuid`, sessionID).Scan(&owner, &visibility); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	onlyID := ""
	if owner != userID && visibility != "team" {
		var live bool
		var versionID *string
		err := s.pool.QueryRow(ctx, `
			SELECT live, version_id::text
			FROM session_grants
			WHERE session_id = $1::uuid AND grantee_user_id = $2::uuid AND revoked_at IS NULL
			LIMIT 1`, sessionID, userID).Scan(&live, &versionID)
		if errors.Is(err, pgx.ErrNoRows) {
			// Team membership path: all complete versions.
		} else if err != nil {
			return nil, err
		} else if !live && versionID != nil && *versionID != "" {
			onlyID = *versionID
		}
	}
	var rows pgx.Rows
	if onlyID == "" {
		rows, err = s.pool.Query(ctx, `
			SELECT id::text, session_id::text, version, created_at, turn_count,
			       COALESCE(manifest_sha256,''), manifest, COALESCE(uploaded_by_user_id::text,''), state
			FROM session_versions
			WHERE session_id = $1::uuid
			  AND state IN ('complete', 'transcript_only')
			ORDER BY version`, sessionID)
	} else {
		rows, err = s.pool.Query(ctx, `
			SELECT id::text, session_id::text, version, created_at, turn_count,
			       COALESCE(manifest_sha256,''), manifest, COALESCE(uploaded_by_user_id::text,''), state
			FROM session_versions
			WHERE session_id = $1::uuid
			  AND state IN ('complete', 'transcript_only')
			  AND id = $2::uuid
			ORDER BY version`, sessionID, onlyID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionVersion
	for rows.Next() {
		v, err := scanSessionVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	if out == nil {
		out = []SessionVersion{}
	}
	return out, rows.Err()
}

func (s *PostgresStore) ListAgentSessionForks(ctx context.Context, parentSessionID string) ([]AgentSession, error) {
	if !looksLikeUUID(parentSessionID) {
		return nil, ErrNotFound
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id::text, project_id::text, COALESCE(owner_user_id::text,''), harness, native_id,
		       origin_machine_id, COALESCE(workspace_root_hint,''), COALESCE(title,''),
		       started_at, last_active_at, ended_at, visibility,
		       COALESCE(parent_session_id::text,''), COALESCE(lineage_kind,''), COALESCE(summary,'')
		FROM agent_sessions
		WHERE parent_session_id = $1::uuid
		ORDER BY started_at`, parentSessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AgentSession
	for rows.Next() {
		row, err := scanAgentSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *row)
	}
	if out == nil {
		out = []AgentSession{}
	}
	return out, rows.Err()
}

func (s *PostgresStore) ForkAgentSession(ctx context.Context, parentSessionID, ownerUserID string) (*AgentSession, error) {
	return s.ForkAgentSessionThrough(ctx, parentSessionID, ownerUserID, nil)
}

func (s *PostgresStore) ForkAgentSessionThrough(ctx context.Context, parentSessionID, ownerUserID string, throughIdx *int) (*AgentSession, error) {
	if !looksLikeUUID(parentSessionID) || !looksLikeUUID(ownerUserID) {
		return nil, errors.New("store: fork ids must be uuids")
	}
	parent, err := s.GetAgentSession(ctx, parentSessionID)
	if err != nil {
		return nil, err
	}
	suffix := fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000)
	native := parent.NativeID + "-fork-" + suffix
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	row := tx.QueryRow(ctx, `
		INSERT INTO agent_sessions (
			project_id, owner_user_id, harness, native_id, origin_machine_id,
			workspace_root_hint, title, visibility, summary, parent_session_id, lineage_kind
		) VALUES (
			$1::uuid, $2::uuid, $3, $4, $5, NULLIF($6,''), NULLIF($7,''), 'private', NULLIF($8,''),
			$9::uuid, 'fork'
		)
		RETURNING id::text, project_id::text, COALESCE(owner_user_id::text,''), harness, native_id,
		          origin_machine_id, COALESCE(workspace_root_hint,''), COALESCE(title,''),
		          started_at, last_active_at, ended_at, visibility,
		          COALESCE(parent_session_id::text,''), COALESCE(lineage_kind,''), COALESCE(summary,'')`,
		parent.ProjectID, ownerUserID, parent.Harness, native, parent.OriginMachineID,
		parent.WorkspaceRootHint, parent.Title, parent.Summary, parent.ID)
	child, err := scanAgentSession(row)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO session_turns (session_id, idx, role, ts, text_preview, tool_calls, blob_ref)
		SELECT $1::uuid, idx, role, ts, text_preview, tool_calls, blob_ref
		FROM session_turns WHERE session_id = $2::uuid AND ($3::bigint IS NULL OR idx <= $3::bigint)`, child.ID, parent.ID, throughIdx)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return child, nil
}

// MarkAgentSessionCodeMerged records a code-only merge on the source session.
// Without a dedicated column yet, the marker is appended to summary and also
// returned on CodeMergedInto / CodeMergedAt for API consumers.
func (s *PostgresStore) MarkAgentSessionCodeMerged(ctx context.Context, fromSessionID, intoSessionID string) (*AgentSession, error) {
	fromSessionID = strings.TrimSpace(fromSessionID)
	intoSessionID = strings.TrimSpace(intoSessionID)
	if !looksLikeUUID(fromSessionID) || !looksLikeUUID(intoSessionID) {
		return nil, errors.New("store: code merge requires session uuids")
	}
	from, err := s.GetAgentSession(ctx, fromSessionID)
	if err != nil {
		return nil, err
	}
	if _, err := s.GetAgentSession(ctx, intoSessionID); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	marker := fmt.Sprintf("code merged into %s at %s", intoSessionID, now.Format(time.RFC3339))
	summary := strings.TrimSpace(from.Summary)
	if summary == "" {
		summary = marker
	} else if !strings.Contains(summary, "code merged into "+intoSessionID) {
		summary = summary + " · " + marker
	}
	_, err = s.pool.Exec(ctx, `UPDATE agent_sessions SET summary = $2 WHERE id = $1::uuid`, fromSessionID, summary)
	if err != nil {
		return nil, err
	}
	from.Summary = summary
	from.CodeMergedInto = intoSessionID
	from.CodeMergedAt = &now
	return from, nil
}

func (s *PostgresStore) AppendSessionTurn(ctx context.Context, turn SessionTurn) error {
	if !looksLikeUUID(turn.SessionID) || strings.TrimSpace(turn.Role) == "" {
		return errors.New("store: turn requires session and role")
	}
	if len(turn.ToolCalls) == 0 {
		turn.ToolCalls = json.RawMessage("[]")
	}
	if turn.At.IsZero() {
		turn.At = time.Now().UTC()
	}
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO session_turns (session_id, idx, role, ts, text_preview, tool_calls, blob_ref, source_ts)
		VALUES ($1::uuid, $2, $3, $4, NULLIF($5,''), $6::jsonb, NULLIF($7,''), $8)`,
		turn.SessionID, turn.Idx, turn.Role, turn.At, turn.TextPreview, string(turn.ToolCalls), turn.BlobRef, turn.SourceAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			if turn.SourceAt != nil {
				repaired, repairErr := s.pool.Exec(ctx, `UPDATE session_turns SET source_ts = $3
					WHERE session_id = $1::uuid AND idx = $2 AND source_ts IS NULL`, turn.SessionID, turn.Idx, turn.SourceAt)
				if repairErr != nil {
					return repairErr
				}
				// Once a re-sent history has every turn dated, date the chat
				// from them; an import stamped it with the upload time.
				if repaired.RowsAffected() > 0 {
					_, _ = s.pool.Exec(ctx, `UPDATE agent_sessions s
						SET last_active_at = t.latest
						FROM (SELECT MAX(source_ts) AS latest FROM session_turns WHERE session_id = $1::uuid) t
						WHERE s.id = $1::uuid AND t.latest IS NOT NULL
						  AND NOT EXISTS (SELECT 1 FROM session_turns WHERE session_id = $1::uuid AND source_ts IS NULL)`,
						turn.SessionID)
				}
			}
			return ErrConflict
		}
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrConflict
	}
	// Activity is when the message was written (source time), not when it
	// was uploaded. The first turn replaces the insert-time default; later
	// turns only move it forward, so out-of-order imports cannot rewind it.
	activity := turn.At
	if turn.SourceAt != nil && !turn.SourceAt.IsZero() {
		activity = *turn.SourceAt
	}
	_, _ = s.pool.Exec(ctx, `UPDATE agent_sessions
		SET last_active_at = CASE WHEN last_active_at = started_at THEN $2
		                          ELSE GREATEST(last_active_at, $2) END
		WHERE id = $1::uuid`, turn.SessionID, activity)
	// A captured chat with a placeholder title takes its first request.
	if turn.Role == "user" {
		if derived := TitleFromRequest(turn.TextPreview); derived != "" {
			_, _ = s.pool.Exec(ctx, `UPDATE agent_sessions SET title = $2
				WHERE id = $1::uuid AND (NULLIF(BTRIM(title), '') IS NULL OR title ~* $3)`,
				turn.SessionID, derived, placeholderTitlePattern)
		}
	}
	return nil
}

func (s *PostgresStore) ListSessionTurns(ctx context.Context, sessionID string) ([]SessionTurn, error) {
	if !looksLikeUUID(sessionID) {
		return nil, ErrNotFound
	}
	rows, err := s.pool.Query(ctx, `
		SELECT session_id::text, idx, role, ts, COALESCE(text_preview,''), tool_calls, COALESCE(blob_ref,''), source_ts
		FROM session_turns WHERE session_id = $1::uuid ORDER BY idx`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionTurn
	for rows.Next() {
		var t SessionTurn
		var calls []byte
		if err := rows.Scan(&t.SessionID, &t.Idx, &t.Role, &t.At, &t.TextPreview, &calls, &t.BlobRef, &t.SourceAt); err != nil {
			return nil, err
		}
		t.ToolCalls = calls
		out = append(out, t)
	}
	if out == nil {
		out = []SessionTurn{}
	}
	return out, rows.Err()
}

func (s *PostgresStore) CreateSessionVersion(ctx context.Context, sessionID, uploadedBy string) (*SessionVersion, error) {
	if !looksLikeUUID(sessionID) {
		return nil, ErrNotFound
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO session_versions (session_id, version, uploaded_by_user_id, state)
		SELECT $1::uuid, COALESCE(MAX(version), 0) + 1, $2, 'uploading'
		FROM session_versions WHERE session_id = $1::uuid
		RETURNING id::text, session_id::text, version, created_at, turn_count,
		          COALESCE(manifest_sha256,''), manifest, COALESCE(uploaded_by_user_id::text,''), state`,
		sessionID, nullUUIDStrict(uploadedBy))
	return scanSessionVersion(row)
}

func scanSessionVersion(row pgx.Row) (*SessionVersion, error) {
	var v SessionVersion
	var manifest []byte
	if err := row.Scan(&v.ID, &v.SessionID, &v.Version, &v.CreatedAt, &v.TurnCount, &v.ManifestSHA, &manifest, &v.UploadedBy, &v.State); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	v.Manifest = manifest
	return &v, nil
}

func (s *PostgresStore) PutBlob(ctx context.Context, projectID, sum, kind, purpose string, body []byte) error {
	projectID = strings.TrimSpace(projectID)
	sum = normalizeHash(sum)
	if !looksLikeUUID(projectID) || sum == "" {
		return errors.New("store: blob requires project uuid and sha256")
	}
	got := sha256.Sum256(body)
	if hex.EncodeToString(got[:]) != sum {
		return errors.New("store: blob bytes do not match sha256")
	}
	if kind == "" {
		kind = "plain"
	}
	if purpose == "" {
		purpose = "file"
	}
	if purpose != "file" {
		_, err := s.pool.Exec(ctx, `
			INSERT INTO blobs (project_id, sha256, size, kind, purpose, verified_at, body)
			VALUES ($1::uuid, $2, $3, $4, $5, now(), $6)
			ON CONFLICT (project_id, sha256) DO UPDATE
			SET size = EXCLUDED.size, kind = EXCLUDED.kind, purpose = EXCLUDED.purpose,
			    verified_at = now(), body = EXCLUDED.body`,
			projectID, sum, len(body), kind, purpose, body)
		return err
	}

	scope, _, cap, err := s.fileStorageScope(ctx, projectID)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		INSERT INTO storage_usage (plan_scope, bytes_used, bytes_cap, state)
		VALUES ($1, 0, $2, $3)
		ON CONFLICT (plan_scope) DO NOTHING`, scope, cap, capture.UsageState(0, cap)); err != nil {
		return err
	}
	var used, storedCap int64
	if err := tx.QueryRow(ctx, `
		SELECT bytes_used, bytes_cap FROM storage_usage WHERE plan_scope = $1 FOR UPDATE`, scope).Scan(&used, &storedCap); err != nil {
		return err
	}
	if storedCap <= 0 {
		storedCap = cap
	}
	var oldSize int64
	oldPurpose := ""
	err = tx.QueryRow(ctx, `
		SELECT size, purpose FROM blobs WHERE project_id = $1::uuid AND sha256 = $2`,
		projectID, sum).Scan(&oldSize, &oldPurpose)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	delta := int64(len(body))
	if oldPurpose == "file" {
		delta -= oldSize
	}
	if storedCap > 0 && delta > 0 && used+delta > storedCap {
		return fmt.Errorf("store: %w", ErrStorageFull)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO blobs (project_id, sha256, size, kind, purpose, verified_at, body)
		VALUES ($1::uuid, $2, $3, $4, $5, now(), $6)
		ON CONFLICT (project_id, sha256) DO UPDATE
		SET size = EXCLUDED.size, kind = EXCLUDED.kind, purpose = EXCLUDED.purpose,
		    verified_at = now(), body = EXCLUDED.body`,
		projectID, sum, len(body), kind, purpose, body); err != nil {
		return err
	}
	if delta != 0 {
		used += delta
		if used < 0 {
			used = 0
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE storage_usage SET bytes_used = $2, bytes_cap = $3, state = $4 WHERE plan_scope = $1`,
		scope, used, storedCap, capture.UsageState(used, storedCap)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) fileStorageScope(ctx context.Context, projectID string) (string, int64, int64, error) {
	owner := ""
	_ = s.pool.QueryRow(ctx, `SELECT COALESCE(created_by::text, '') FROM projects WHERE id = $1::uuid`, projectID).Scan(&owner)
	candidates := []string{"project:" + projectID, projectID}
	if owner != "" {
		candidates = append(candidates, "user:"+owner)
	}
	for _, scope := range candidates {
		var used, cap int64
		err := s.pool.QueryRow(ctx, `
			SELECT bytes_used, bytes_cap FROM storage_usage WHERE plan_scope = $1`, scope).Scan(&used, &cap)
		if err == nil {
			return scope, used, cap, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return "", 0, 0, err
		}
	}
	scope := "project:" + projectID
	if owner != "" {
		scope = "user:" + owner
	}
	cap, err := s.planCapForScope(ctx, scope)
	if err != nil {
		return "", 0, 0, err
	}
	if cap <= 0 {
		cap = PlanStorageBytes(PlanFree)
	}
	return scope, 0, cap, nil
}

func (s *PostgresStore) MissingBlobs(ctx context.Context, projectID string, hashes []string) ([]string, error) {
	if !looksLikeUUID(projectID) {
		return nil, errors.New("store: project id must be a uuid")
	}
	var missing []string
	for _, h := range hashes {
		h = normalizeHash(h)
		if h == "" {
			continue
		}
		var ok bool
		if err := s.pool.QueryRow(ctx, `
			SELECT EXISTS(SELECT 1 FROM blobs WHERE project_id = $1::uuid AND sha256 = $2 AND verified_at IS NOT NULL)`,
			projectID, h).Scan(&ok); err != nil {
			return nil, err
		}
		if !ok {
			missing = append(missing, h)
		}
	}
	if missing == nil {
		missing = []string{}
	}
	return missing, nil
}

func (s *PostgresStore) CompleteSessionVersion(ctx context.Context, sessionID string, version int, manifest json.RawMessage) (*SessionVersion, error) {
	refs, fileRefs, err := manifestBlobRefs(manifest)
	if err != nil {
		return nil, err
	}
	sess, err := s.GetAgentSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	missing, err := s.MissingBlobs(ctx, sess.ProjectID, refs)
	if err != nil {
		return nil, err
	}
	if len(missing) > 0 && !(len(missing) == 1 && missing[0] == "") {
		if len(missing) > 0 {
			return nil, &MissingBlobsError{Missing: missing}
		}
	}
	state := "complete"
	var usage string
	_ = s.pool.QueryRow(ctx, `SELECT state FROM storage_usage WHERE plan_scope = $1`, "project:"+sess.ProjectID).Scan(&usage)
	if usage == "full" {
		if fileRefs > 0 {
			return nil, fmt.Errorf("store: %w", ErrStorageFull)
		}
		state = "transcript_only"
	}
	sum := sha256.Sum256(manifest)
	row := s.pool.QueryRow(ctx, `
		UPDATE session_versions
		SET state = $3, manifest = $4::jsonb, manifest_sha256 = $5
		WHERE session_id = $1::uuid AND version = $2 AND state = 'uploading'
		RETURNING id::text, session_id::text, version, created_at, turn_count,
		          COALESCE(manifest_sha256,''), manifest, COALESCE(uploaded_by_user_id::text,''), state`,
		sessionID, version, state, string(manifest), hex.EncodeToString(sum[:]))
	out, err := scanSessionVersion(row)
	if errors.Is(err, ErrNotFound) {
		return nil, fmt.Errorf("store: version is immutable: %w", ErrConflict)
	}
	return out, err
}

func (s *PostgresStore) SetStorageUsage(ctx context.Context, scope string, used, cap int64) error {
	state := capture.UsageState(used, cap)
	_, err := s.pool.Exec(ctx, `
		INSERT INTO storage_usage (plan_scope, bytes_used, bytes_cap, state)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (plan_scope) DO UPDATE
		SET bytes_used = EXCLUDED.bytes_used, bytes_cap = EXCLUDED.bytes_cap, state = EXCLUDED.state`,
		scope, used, cap, state)
	return err
}

func (s *PostgresStore) ListTimeline(ctx context.Context, q TimelineQuery) ([]TimelineItem, string, error) {
	if !looksLikeUUID(q.UserID) {
		return []TimelineItem{}, "", nil
	}
	if q.ProjectID != "" && !looksLikeUUID(q.ProjectID) {
		return []TimelineItem{}, "", nil
	}
	if q.Limit <= 0 || q.Limit > 100 {
		q.Limit = 30
	}
	var cursorAt any
	cursorID := ""
	if q.Cursor != "" {
		i := strings.LastIndex(q.Cursor, "|")
		if i <= 0 {
			return nil, "", errors.New("store: bad timeline cursor")
		}
		t, err := time.Parse(time.RFC3339Nano, q.Cursor[:i])
		if err != nil {
			return nil, "", errors.New("store: bad timeline cursor")
		}
		cursorAt = t
		cursorID = q.Cursor[i+1:]
	}
	// The project filter compares uuids so the (project_id, last_active_at)
	// index applies. The page is chosen first (inner SELECT … LIMIT) and only
	// those rows pay for the preview, turn-count and match subqueries.
	//
	// Text search (q) matches title, summary, native id or any user/assistant
	// turn. A q of three or more characters collects matching sessions once,
	// from the trigram index on LOWER(text_preview) (migration 045), as a
	// materialized set the session filter probes by hash. An EXISTS under the
	// OR cannot be turned into a join, so the planner ran it per candidate
	// session: every turn of every visible chat read and lower()ed until the
	// page filled, which a cold cache made seconds long. Shorter q has no
	// trigram to use; it keeps the per-session probe, which stops as soon as
	// the page is full (short needles match almost every chat).
	// LIKE pattern for q, lower()ed in SQL like the index expression.
	pattern := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q.Query) + "%"
	turnMatch := `EXISTS (
		       SELECT 1 FROM session_turns m
		       WHERE m.session_id = s.id AND m.role IN ('user', 'assistant')
		         AND LOWER(m.text_preview) LIKE LOWER($10)
		     )`
	with := ""
	if len([]rune(q.Query)) >= 3 {
		with = `WITH turn_hits AS MATERIALIZED (
		  SELECT DISTINCT m.session_id FROM session_turns m
		  WHERE m.role IN ('user', 'assistant') AND LOWER(m.text_preview) LIKE LOWER($10)
		)`
		turnMatch = `s.id IN (SELECT session_id FROM turn_hits)`
	}
	rows, err := s.pool.Query(ctx, with+`
		SELECT s.id::text, s.project_id::text, s.harness, s.native_id, COALESCE(s.title,''),
		       -- A version-limited grantee (can_read_live false) sees neither the
		       -- summary nor turn previews: both describe the chat as it is now.
		       CASE WHEN s.can_read_live THEN COALESCE(s.summary,'') ELSE '' END,
		       -- Fallback preview and title source, only when there is no
		       -- summary or the title is a placeholder: the raw first user
		       -- turns. Go removes harness wrapper and skips envelope-only
		       -- turns (timelineFirstRequestPreview, PresentTitle).
		       CASE WHEN s.can_read_live AND (NULLIF(s.summary,'') IS NULL OR NULLIF(BTRIM(s.title),'') IS NULL OR s.title ~* $9) THEN ARRAY(
		         SELECT LEFT(t.text_preview, 4000)
		         FROM session_turns t WHERE t.session_id = s.id AND t.role = 'user'
		           AND NULLIF(BTRIM(t.text_preview), '') IS NOT NULL
		         ORDER BY t.idx LIMIT 3
		       ) ELSE ARRAY[]::text[] END, s.visibility, COALESCE(s.owner_user_id::text,''), s.last_active_at,
		       GREATEST(COALESCE((
		         SELECT v.turn_count FROM session_versions v
		         WHERE v.session_id = s.id AND v.state IN ('complete','transcript_only')
		         ORDER BY v.version DESC LIMIT 1
		       ), 0), (
		         -- Mirrored chats have turns but often no version. Index-only
		         -- count on the (session_id, idx) primary key, output rows only.
		         SELECT COUNT(*) FROM session_turns t WHERE t.session_id = s.id
		       )::int),
		       COALESCE((
		         SELECT v.state FROM session_versions v
		         WHERE v.session_id = s.id AND v.state IN ('complete','transcript_only')
		         ORDER BY v.version DESC LIMIT 1
		       ), ''), COALESCE(hit.preview, ''), hit.idx, COALESCE(hit.role, ''),
		       s.is_subagent,
		       -- Sub-agent links (migration 051): the parent by its key, and
		       -- how many sub-agent runs this chat started. Same owner only.
		       COALESCE((
		         SELECT p.id::text FROM agent_sessions p
		         WHERE s.parent_native_id IS NOT NULL AND p.project_id = s.project_id
		           AND p.harness = s.harness AND p.native_id = s.parent_native_id
		           AND p.origin_machine_id = s.origin_machine_id
		           AND p.owner_user_id IS NOT DISTINCT FROM s.owner_user_id
		         LIMIT 1
		       ), ''),
		       (
		         SELECT COUNT(*) FROM agent_sessions c
		         WHERE c.parent_native_id = s.native_id AND c.project_id = s.project_id
		           AND c.harness = s.harness AND c.origin_machine_id = s.origin_machine_id
		           AND c.owner_user_id IS NOT DISTINCT FROM s.owner_user_id
		       )::int
		FROM (
		  SELECT s.id, s.project_id, s.harness, s.native_id, s.title, s.summary,
		         s.visibility, s.owner_user_id, s.last_active_at,
		         s.origin_machine_id, s.parent_native_id,
		         `+subagentSQL(11, 12, 13)+` AS is_subagent,
		         acl.can_read_live
		  FROM agent_sessions s
		  -- Live read: owner, a live grant, or team access. A grant pinned to a
		  -- version lists the chat but not its live summary or turns.
		  CROSS JOIN LATERAL (SELECT (
		    s.owner_user_id = $1::uuid
		    OR EXISTS (SELECT 1 FROM session_grants g WHERE g.session_id = s.id
		      AND g.revoked_at IS NULL AND g.grantee_user_id = $1::uuid AND g.live)
		    OR (s.visibility = 'team' AND (
		      EXISTS (SELECT 1 FROM projects p WHERE p.id = s.project_id AND p.created_by = $1::uuid)
		      OR EXISTS (SELECT 1 FROM project_members pm WHERE pm.project_id = s.project_id AND pm.user_id = $1::uuid)
		      OR EXISTS (SELECT 1 FROM projects p JOIN organization_members om ON om.org_id = p.org_id
		        WHERE p.id = s.project_id AND om.user_id = $1::uuid AND om.role IN ('ADMIN','OWNER'))
		    ))
		  ) AS can_read_live) acl
		  WHERE (
		    s.owner_user_id = $1::uuid
		    OR EXISTS (
		      SELECT 1 FROM session_grants g
		      WHERE g.session_id = s.id AND g.revoked_at IS NULL AND g.grantee_user_id = $1::uuid
		    )
		    OR (
		      s.visibility = 'team' AND (
		        EXISTS (SELECT 1 FROM projects p WHERE p.id = s.project_id AND p.created_by = $1::uuid)
		        OR EXISTS (SELECT 1 FROM project_members pm WHERE pm.project_id = s.project_id AND pm.user_id = $1::uuid)
		        OR EXISTS (SELECT 1 FROM projects p JOIN organization_members om ON om.org_id = p.org_id
		          WHERE p.id = s.project_id AND om.user_id = $1::uuid AND om.role IN ('ADMIN','OWNER'))
		      )
		    )
		  )
		  AND ($2 = '' OR s.project_id = NULLIF($2, '')::uuid)
		  AND ($3 = '' OR s.harness = $3)
		  -- Sub-agent runs are left out before LIMIT, so the page and its
		  -- cursor count only listed rows.
		  AND ($14::bool OR NOT `+subagentSQL(11, 12, 13)+`)
		  AND ($7 = '' OR STRPOS(LOWER(s.title), LOWER($7)) > 0
		       OR (acl.can_read_live AND STRPOS(LOWER(s.summary), LOWER($7)) > 0)
		       OR STRPOS(LOWER(s.native_id), LOWER($7)) > 0
		       OR (acl.can_read_live AND `+turnMatch+`))
		  AND ($4::timestamptz IS NULL OR (s.last_active_at, s.id::text) < ($4::timestamptz, $5))
		  AND (NOT $8::bool
		    OR EXISTS (SELECT 1 FROM session_turns t WHERE t.session_id = s.id)
		    OR EXISTS (SELECT 1 FROM session_versions v
		               WHERE v.session_id = s.id AND v.state IN ('complete','transcript_only') AND v.turn_count > 0))
		  ORDER BY s.last_active_at DESC, s.id DESC
		  LIMIT $6
		) s
		-- The first matching turn of each listed chat (by primary key).
		LEFT JOIN LATERAL (
		  SELECT t.idx, t.role,
		    t.text_preview AS preview
		  FROM session_turns t
		  WHERE t.session_id = s.id AND s.can_read_live AND $7 <> '' AND t.role IN ('user', 'assistant')
		    AND STRPOS(LOWER(t.text_preview), LOWER($7)) > 0
		  ORDER BY t.idx LIMIT 1
		) hit ON TRUE
		ORDER BY s.last_active_at DESC, s.id DESC`,
		q.UserID, q.ProjectID, q.Harness, cursorAt, cursorID, q.Limit+1, q.Query, q.HideEmpty,
		placeholderTitlePattern, pattern,
		subagentNativeIDPattern, subagentNativeMarkerPattern, subagentTitlePattern, q.IncludeSubagents)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var items []TimelineItem
	for rows.Next() {
		var it TimelineItem
		var firstTurns []string
		var subagent bool
		if err := rows.Scan(&it.SessionID, &it.ProjectID, &it.Harness, &it.NativeID, &it.Title, &it.Summary, &firstTurns,
			&it.Visibility, &it.OwnerUserID, &it.LastActiveAt, &it.TurnCount, &it.VersionState, &it.MatchPreview, &it.MatchTurnIdx, &it.MatchRole,
			&subagent, &it.ParentSessionID, &it.SubagentCount); err != nil {
			return nil, "", err
		}
		if subagent {
			it.LineageKind = "subagent"
		}
		if it.Summary == "" {
			it.Summary = timelineFirstRequestPreview(firstTurns)
		}
		it.Title = PresentTitle(it.Title, firstTurns)
		// Same excerpt rules as every other store: centred on the match,
		// cut at word boundaries, marked with … where text was cut.
		if it.MatchPreview != "" {
			it.MatchPreview = TimelineMatchPreview(it.MatchPreview, q.Query)
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(items) > q.Limit {
		items = items[:q.Limit]
		last := items[len(items)-1]
		next = last.LastActiveAt.UTC().Format(time.RFC3339Nano) + "|" + last.SessionID
	}
	if items == nil {
		items = []TimelineItem{}
	}
	return items, next, nil
}

// TimelineMatchPreview centers the excerpt on the match instead of returning
// the start of a long message where the searched phrase may not be visible.
func TimelineMatchPreview(text, query string) string {
	text = strings.Join(strings.Fields(text), " ")
	lower := strings.ToLower(text)
	at := strings.Index(lower, strings.ToLower(query))
	start := 0
	if at > 0 {
		start = utf8.RuneCountInString(lower[:at]) - 70
	}
	if start < 0 {
		start = 0
	}
	runes := []rune(text)
	if start > len(runes) {
		start = 0
	}
	end := start + 260
	if end > len(runes) {
		end = len(runes)
	}
	// Snap a cut edge to a word boundary so the excerpt never starts or
	// ends mid-word (a long unbroken token keeps the hard cut).
	if start > 0 {
		for i := start; i < end && i-start < 20; i++ {
			if runes[i-1] == ' ' {
				start = i
				break
			}
		}
	}
	if end < len(runes) {
		for i := end; i > start && end-i < 20; i-- {
			if runes[i] == ' ' {
				end = i
				break
			}
		}
	}
	preview := strings.TrimSpace(string(runes[start:end]))
	if start > 0 {
		preview = "…" + preview
	}
	if end < len(runes) {
		preview += "…"
	}
	return preview
}

var _ AgentCloudStore = (*MemStore)(nil)
var _ AgentCloudStore = (*PostgresStore)(nil)
