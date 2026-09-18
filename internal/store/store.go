package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
)

// OfflineThreshold is how long a workspace may go without a heartbeat
// before it is considered offline. Mirrors the server rule from the
// implementation plan ("marks offline after 90s silence") and the
// SQL predicate used by PostgresStore.GetActiveWorkspace.
const OfflineThreshold = 90 * time.Second

// Store defines the persistent storage operations for Central Memory.
//
// Both MemStore (in-memory, tests/local dev) and PostgresStore (pgx pool,
// production) implement this interface. Subscribe is the real-time fan-out:
// MemStore broadcasts in-process; PostgresStore LISTENs on the 'events'
// channel created by migrations/002_events.up.sql.
type Store interface {
	// Projects
	ResolveProject(ctx context.Context, canonicalURL, rootCommit, folderName string) (*Project, error)
	GetProject(ctx context.Context, id string) (*Project, error)

	// Workspaces
	RegisterWorkspace(ctx context.Context, ws *Workspace) error
	Heartbeat(ctx context.Context, workspaceID string, branch, commitSHA string, isDirty bool) error
	GetActiveWorkspace(ctx context.Context, projectID string) (*Workspace, error)

	// Memory Items
	CreateMemoryItem(ctx context.Context, item *MemoryItem) error
	GetMemoryItem(ctx context.Context, id string) (*MemoryItem, error)
	SearchMemory(ctx context.Context, projectID string, query string, tags []string, limit int) ([]*MemoryItem, error)
	ConfirmMemory(ctx context.Context, id string, confirmedBy string) error

	// Episodes
	CreateEpisode(ctx context.Context, ep *Episode) error
	GetEpisode(ctx context.Context, id string) (*Episode, error)
	SearchEpisodes(ctx context.Context, projectID, errorPattern, query string, limit int) ([]*Episode, error)
	ResolveEpisode(ctx context.Context, id, resolution, verification, resolvedBy string) error

	// Events
	AppendEvent(ctx context.Context, ev *Event) error
	ListEvents(ctx context.Context, projectID string, sinceID int64, limit int) ([]*Event, error)

	// Subscribe returns a channel receiving subsequently appended events for
	// the given project. The returned cancel func unsubscribes and closes the
	// channel. Sends never block the appender: slow subscribers drop events.
	Subscribe(ctx context.Context, projectID string) (<-chan *Event, func(), error)

	// IsProjectMember reports whether userID may access projectID (issues
	// #141, #149): the project creator or an explicit project_members
	// grant. Workspace registration requires membership; it never creates
	// it. The server enforces this on every project-scoped route (403);
	// object routes resolve object → project first.
	IsProjectMember(ctx context.Context, userID, projectID string) (bool, error)

	// ClaimProject records userID as the project creator iff none is set
	// (issue #149): the resolver establishes the first owner, so a fresh
	// project always has exactly one bootstrap member. Never overwrites
	// an existing creator. Returns true when this call claimed it.
	ClaimProject(ctx context.Context, projectID, userID string) (bool, error)

	// GrantMember adds userID as a project member (issue #149). GrantedBy
	// records the granter for audit. Idempotent.
	GrantMember(ctx context.Context, projectID, userID, grantedBy string) error

	// RevokeMember removes a grant. The project creator cannot be revoked
	// (ownership is structural); revoking a non-member is a no-op nil.
	RevokeMember(ctx context.Context, projectID, userID string) error

	// ListMembers returns user IDs with explicit grants on the project.
	ListMembers(ctx context.Context, projectID string) ([]string, error)

	// GetWorkspace fetches one workspace by id (issue #149: heartbeat
	// ownership checks need the row before mutating it).
	GetWorkspace(ctx context.Context, id string) (*Workspace, error)
}

// MemStore is a thread-safe in-memory Store implementation, ideal for unit testing and local development.
type MemStore struct {
	mu         sync.RWMutex
	projects   map[string]*Project
	workspaces map[string]*Workspace
	members    map[string]map[string]bool // projectID -> granted userIDs (issue #149)
	memories   map[string]*MemoryItem
	episodes   map[string]*Episode
	events     []*Event
	eventSeq   int64
	subs       map[int64]*memSubscription
	subSeq     int64
}

// memSubscription is one in-process event subscriber.
type memSubscription struct {
	projectID string
	ch        chan *Event
}

// Compile-time guarantee that MemStore satisfies Store.
var _ Store = (*MemStore)(nil)

// NewMemStore returns an initialized in-memory store.
func NewMemStore() *MemStore {
	return &MemStore{
		projects:   make(map[string]*Project),
		workspaces: make(map[string]*Workspace),
		members:    make(map[string]map[string]bool),
		memories:   make(map[string]*MemoryItem),
		episodes:   make(map[string]*Episode),
		events:     make([]*Event, 0),
		subs:       make(map[int64]*memSubscription),
	}
}

// idCounter makes MemStore IDs unique even when many are minted within the
// same clock tick (timestamp-only IDs collided and overwrote rows).
var idCounter atomic.Int64

// newID mints a unique in-memory ID: prefix + nanosecond clock + atomic seq.
func newID(prefix string) string {
	return fmt.Sprintf("%s_%d_%d", prefix, time.Now().UnixNano(), idCounter.Add(1))
}

// NormalizeGitURL strips scheme/user/suffix noise so equivalent remotes
// compare equal, e.g. all of these -> "github.com/mpratyush54/nexus":
//
//	git@github.com:Mpratyush54/nexus.git
//	https://github.com/Mpratyush54/nexus.git
//	ssh://git@github.com/Mpratyush54/nexus
func NormalizeGitURL(raw string) string {
	s := strings.TrimSpace(raw)
	for _, scheme := range []string{"https://", "http://", "ssh://", "git+ssh://", "git://"} {
		s = strings.TrimPrefix(s, scheme)
	}
	// Strip any "user@" (covers "git@host" both bare and post-scheme).
	if i := strings.Index(s, "@"); i >= 0 && (strings.Index(s, "/") == -1 || i < strings.Index(s, "/")) {
		s = s[i+1:]
	}
	// scp-like "host:path" -> "host/path".
	s = strings.Replace(s, ":", "/", 1)
	s = strings.TrimSuffix(s, ".git")
	s = strings.TrimSuffix(s, "/")
	return strings.ToLower(s)
}

func (s *MemStore) ResolveProject(ctx context.Context, canonicalURL, rootCommit, folderName string) (*Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	normURL := NormalizeGitURL(canonicalURL)

	// 1. Check canonical URL first
	if normURL != "" {
		for _, p := range s.projects {
			if NormalizeGitURL(p.CanonicalURL) == normURL {
				return cloneProject(p), nil
			}
		}
	}

	// 2. Check root commit hash
	if rootCommit != "" {
		for _, p := range s.projects {
			if p.RootCommit != "" && p.RootCommit == rootCommit {
				return cloneProject(p), nil
			}
		}
	}

	// 3. Fallback to folder name. Ambiguous by nature: first-registered
	// wins (earliest CreatedAt, ID tie-break), matching the Postgres
	// ORDER BY created_at ASC LIMIT 1 contract (issue #110). Holding the
	// write lock across lookup+insert keeps concurrent first registration
	// atomic in-process (issue #86); the Postgres path reconciles races
	// via INSERT ... ON CONFLICT DO NOTHING + reselect.
	var folderWinner *Project
	for _, p := range s.projects {
		if p.FolderName == folderName {
			if folderWinner == nil || p.CreatedAt.Before(folderWinner.CreatedAt) ||
				(p.CreatedAt.Equal(folderWinner.CreatedAt) && p.ID < folderWinner.ID) {
				folderWinner = p
			}
		}
	}
	if folderWinner != nil {
		return cloneProject(folderWinner), nil
	}

	// 4. Create new project if not resolved
	id := newID("proj")
	storedURL := canonicalURL
	if normURL != "" {
		storedURL = normURL // persist the canonical form, not the raw remote
	}
	p := &Project{
		ID:           id,
		CanonicalURL: storedURL,
		RootCommit:   rootCommit,
		FolderName:   folderName,
		DisplayName:  folderName,
		CreatedAt:    time.Now().UTC(),
	}
	s.projects[id] = p
	return cloneProject(p), nil
}

func (s *MemStore) GetProject(ctx context.Context, id string) (*Project, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.projects[id]
	if !ok {
		return nil, ErrNotFound
	}
	return cloneProject(p), nil
}

func (s *MemStore) RegisterWorkspace(ctx context.Context, ws *Workspace) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(ws.ProjectID) == "" {
		return errors.New("store: workspace project id is required")
	}
	if _, ok := s.projects[ws.ProjectID]; !ok {
		return fmt.Errorf("store: workspace project %s: %w", ws.ProjectID, ErrNotFound)
	}
	if strings.TrimSpace(ws.MachineID) == "" || strings.TrimSpace(ws.Path) == "" {
		return errors.New("store: workspace machine_id and path are required")
	}
	// Upsert on (machine_id, path) like Postgres ON CONFLICT (issue #87):
	// re-registration refreshes only mutable liveness/git fields. Identity
	// and ownership (project, user, designated-processor) are preserved so
	// a caller that knows a machine/path cannot rebind the workspace to
	// another project/user.
	for _, existing := range s.workspaces {
		if existing.MachineID == ws.MachineID && existing.Path == ws.Path {
			existing.Branch = ws.Branch
			existing.CommitSHA = ws.CommitSHA
			existing.IsDirty = ws.IsDirty
			existing.DaemonURL = ws.DaemonURL
			existing.LastSeen = time.Now().UTC()
			existing.IsOnline = true
			ws.ID = existing.ID
			ws.ProjectID = existing.ProjectID
			ws.UserID = existing.UserID
			ws.IsDesignatedProcessor = existing.IsDesignatedProcessor
			ws.CreatedAt = existing.CreatedAt
			ws.LastSeen = existing.LastSeen
			ws.IsOnline = true
			return nil
		}
	}
	if ws.ID == "" {
		ws.ID = newID("ws")
	}
	now := time.Now().UTC()
	ws.LastSeen = now
	ws.IsOnline = true
	// Designation is server-managed (issue #149): registration never
	// designates — only the election path may. A client-supplied true
	// would otherwise forge processor ownership at insert.
	ws.IsDesignatedProcessor = false
	stored := *ws
	if stored.CreatedAt.IsZero() {
		stored.CreatedAt = now
	}
	s.workspaces[stored.ID] = &stored
	ws.CreatedAt = stored.CreatedAt
	return nil
}

// GetWorkspace fetches one workspace by id (issue #149: heartbeat
// ownership checks need the row before mutating it).
func (s *MemStore) GetWorkspace(ctx context.Context, id string) (*Workspace, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ws, ok := s.workspaces[id]
	if !ok {
		return nil, ErrNotFound
	}
	return cloneWorkspace(ws), nil
}

func (s *MemStore) Heartbeat(ctx context.Context, workspaceID string, branch, commitSHA string, isDirty bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ws, ok := s.workspaces[workspaceID]
	if !ok {
		return ErrNotFound
	}
	ws.Branch = branch
	ws.CommitSHA = commitSHA
	ws.IsDirty = isDirty
	ws.IsOnline = true
	ws.LastSeen = time.Now().UTC()
	return nil
}

func (s *MemStore) GetActiveWorkspace(ctx context.Context, projectID string) (*Workspace, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var mostRecent *Workspace
	threshold := time.Now().UTC().Add(-OfflineThreshold)

	for _, ws := range s.workspaces {
		// Inclusive 90s boundary (issue #131): exact silence == 90s is
		// still online, matching IsOnlineAt/IsStaleAt/MarkStaleOffline.
		if ws.ProjectID == projectID && ws.IsOnline && !ws.LastSeen.Before(threshold) {
			if mostRecent == nil || ws.LastSeen.After(mostRecent.LastSeen) {
				mostRecent = ws
			}
		}
	}
	if mostRecent == nil {
		return nil, ErrNotFound
	}
	return cloneWorkspace(mostRecent), nil
}

func (s *MemStore) CreateMemoryItem(ctx context.Context, item *MemoryItem) error {
	if err := validateMemoryItemForCreate(item); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	stored := cloneMemoryItem(item)
	if stored.ID == "" {
		stored.ID = newID("mem")
	}
	if stored.Confidence == 0 {
		stored.Confidence = 1.0
	}
	if stored.Status == "" {
		stored.Status = StatusProposed
	}
	if stored.Level == "" {
		stored.Level = LevelProject
	}
	if stored.Scope == "" {
		stored.Scope = "fact"
	}
	if stored.Tags == nil {
		stored.Tags = []string{}
	}
	now := time.Now().UTC()
	stored.CreatedAt = now
	stored.UpdatedAt = now
	s.memories[stored.ID] = stored
	*item = *cloneMemoryItem(stored)
	return nil
}

func (s *MemStore) GetMemoryItem(ctx context.Context, id string) (*MemoryItem, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.memories[id]
	if !ok {
		return nil, ErrNotFound
	}
	return cloneMemoryItem(item), nil
}

// ConfirmMemory flips PROPOSED -> CONFIRMED (issue #89 DAG). Re-confirming a
// CONFIRMED row is idempotent; terminal states (REJECTED, SUPERSEDED) and
// unknown ids fail instead of resurrecting rows.
func (s *MemStore) ConfirmMemory(ctx context.Context, id string, confirmedBy string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.memories[id]
	if !ok {
		return ErrNotFound
	}
	switch item.Status {
	case StatusProposed:
		item.Status = StatusConfirmed
	case StatusConfirmed:
		// idempotent re-confirm
	default:
		return errMemoryConflict(item.Status, StatusConfirmed)
	}
	item.ConfirmedBy = confirmedBy
	item.UpdatedAt = time.Now().UTC()
	return nil
}

// memoryVisibleToProject encodes the scope-isolation rule (issue #102):
// NULL-project rows are visible only when explicitly organization-level.
// Personal/session/ephemeral rows with no project never leak globally.
func memoryVisibleToProject(m *MemoryItem, projectID string) bool {
	if m.ProjectID != "" {
		return m.ProjectID == projectID
	}
	return m.Level == LevelOrganization
}

func (s *MemStore) SearchMemory(ctx context.Context, projectID string, query string, tags []string, limit int) ([]*MemoryItem, error) {
	effective := limit
	if effective <= 0 {
		effective = 20
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	var results []*MemoryItem
	terms := strings.Fields(strings.ToLower(query))

	for _, item := range s.memories {
		if !memoryVisibleToProject(item, projectID) {
			continue
		}
		if item.Status != StatusConfirmed && item.Status != StatusProposed {
			continue
		}
		if len(tags) > 0 {
			hit := false
			set := make(map[string]struct{}, len(item.Tags))
			for _, t := range item.Tags {
				set[t] = struct{}{}
			}
			for _, t := range tags {
				if _, ok := set[t]; ok {
					hit = true
					break
				}
			}
			if !hit {
				continue
			}
		}

		// Simple term match fallback
		matched := false
		combined := strings.ToLower(item.Key + " " + item.Content + " " + strings.Join(item.Tags, " "))
		for _, t := range terms {
			if strings.Contains(combined, t) {
				matched = true
				break
			}
		}

		if matched || len(terms) == 0 {
			results = append(results, cloneMemoryItem(item))
		}
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].Confidence == results[j].Confidence {
			if results[i].CreatedAt.Equal(results[j].CreatedAt) {
				return results[i].ID < results[j].ID
			}
			return results[i].CreatedAt.After(results[j].CreatedAt)
		}
		return results[i].Confidence > results[j].Confidence
	})
	if len(results) > effective {
		results = results[:effective]
	}
	return results, nil
}

// SearchMemoryVector ranks memories by cosine similarity (issue #165).
// Mirrors PostgresStore.SearchMemoryVector: CONFIRMED only, confidence > 0.3,
// non-empty embeddings. Used by local MCP/mem wiring and tests so vector
// search works without Postgres.
func (s *MemStore) SearchMemoryVector(ctx context.Context, projectID string, queryVec []float32, limit int) ([]*MemoryItem, error) {
	if len(queryVec) == 0 {
		return nil, fmt.Errorf("store: vector search needs a query embedding (use text search when there is none)")
	}
	if err := ValidateEmbeddingDim(queryVec); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 20
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	type scored struct {
		item *MemoryItem
		sim  float64
	}
	var ranked []scored
	for _, item := range s.memories {
		if !memoryVisibleToProject(item, projectID) {
			continue
		}
		if item.Status != StatusConfirmed {
			continue
		}
		if item.Confidence <= 0.3 {
			continue
		}
		if len(item.Embedding) == 0 {
			continue
		}
		if sim := CosineSimilarity(item.Embedding, queryVec); sim > 0 {
			ranked = append(ranked, scored{item, sim})
		}
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].sim == ranked[j].sim {
			return ranked[i].item.ID < ranked[j].item.ID
		}
		return ranked[i].sim > ranked[j].sim
	})
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	out := make([]*MemoryItem, 0, len(ranked))
	for _, r := range ranked {
		out = append(out, cloneMemoryItem(r.item))
	}
	return out, nil
}

func (s *MemStore) CreateEpisode(ctx context.Context, ep *Episode) error {
	if strings.TrimSpace(ep.Title) == "" {
		return errors.New("store: episode title is required")
	}
	if strings.TrimSpace(ep.EpisodeType) == "" {
		return errors.New("store: episode_type is required (want bug_fix|feature|refactor|incident|investigation|onboarding)")
	}
	if err := ValidateEpisodeType(ep.EpisodeType); err != nil {
		return err
	}
	if err := ValidateEmbeddingDim(ep.Embedding); err != nil {
		return err
	}
	status := ep.Status
	if strings.TrimSpace(status) == "" {
		status = "OPEN"
	} else {
		status = strings.ToUpper(strings.TrimSpace(status))
		if err := ValidateEpisodeStatus(status); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	stored := cloneEpisode(ep)
	if stored.ID == "" {
		stored.ID = newID("ep")
	}
	stored.EpisodeType = strings.ToLower(strings.TrimSpace(stored.EpisodeType))
	stored.Status = status
	if stored.Tags == nil {
		stored.Tags = []string{}
	}
	if stored.FilesInvolved == nil {
		stored.FilesInvolved = []string{}
	}
	if stored.ErrorPatterns == nil {
		stored.ErrorPatterns = []string{}
	}
	stored.OpenedAt = time.Now().UTC()
	s.episodes[stored.ID] = stored
	*ep = *cloneEpisode(stored)
	return nil
}

func (s *MemStore) GetEpisode(ctx context.Context, id string) (*Episode, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ep, ok := s.episodes[id]
	if !ok {
		return nil, ErrNotFound
	}
	return cloneEpisode(ep), nil
}

// ResolveEpisode closes an open arc (issue #103). Only OPEN and
// INVESTIGATING arcs resolve; RESOLVED/WONT_FIX are terminal and unknown
// ids report ErrNotFound. Re-resolution fails instead of overwriting.
func (s *MemStore) ResolveEpisode(ctx context.Context, id, resolution, verification, resolvedBy string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ep, ok := s.episodes[id]
	if !ok {
		return ErrNotFound
	}
	if ep.Status != "OPEN" && ep.Status != "INVESTIGATING" {
		return fmt.Errorf("store: episode %s is %s, only OPEN/INVESTIGATING resolve: %w", id, ep.Status, ErrConflict)
	}
	ep.Status = "RESOLVED"
	ep.Resolution = resolution
	ep.Verification = verification
	ep.ResolvedBy = resolvedBy
	now := time.Now().UTC()
	ep.ResolvedAt = now
	return nil
}

func (s *MemStore) SearchEpisodes(ctx context.Context, projectID, errorPattern, query string, limit int) ([]*Episode, error) {
	effective := limit
	if effective <= 0 {
		effective = 20
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	var results []*Episode
	lowErr := strings.ToLower(errorPattern)
	lowQ := strings.ToLower(query)

	for _, ep := range s.episodes {
		if ep.ProjectID != projectID {
			continue
		}
		match := false
		if lowErr != "" {
			for _, epPattern := range ep.ErrorPatterns {
				if strings.Contains(strings.ToLower(epPattern), lowErr) {
					match = true
					break
				}
			}
		}
		if !match && lowQ != "" {
			narrative := strings.ToLower(ep.Title + " " + ep.Trigger + " " + ep.Investigation + " " + ep.RootCause + " " + ep.Resolution)
			if strings.Contains(narrative, lowQ) {
				match = true
			}
		}
		if match || (lowErr == "" && lowQ == "") {
			results = append(results, cloneEpisode(ep))
		}
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].OpenedAt.Equal(results[j].OpenedAt) {
			return results[i].ID > results[j].ID
		}
		return results[i].OpenedAt.After(results[j].OpenedAt)
	})
	if len(results) > effective {
		results = results[:effective]
	}
	return results, nil
}

func (s *MemStore) AppendEvent(ctx context.Context, ev *Event) error {
	if err := ValidateEvent(ev); err != nil {
		return err
	}
	stored := cloneEvent(ev)
	if stored.Payload == nil {
		// Match Postgres (column NOT NULL marshals nil to '{}'): a nil
		// payload round-trips as an empty map (issue #110).
		stored.Payload = make(map[string]any)
	}
	s.mu.Lock()
	s.eventSeq++
	stored.ID = s.eventSeq
	stored.CreatedAt = time.Now().UTC()
	s.events = append(s.events, stored)
	// Snapshot subscribers under RLock and send after unlocking (issue
	// #119): fan-out sends must never hold the store lock.
	s.mu.Unlock()

	ev.ID = stored.ID
	ev.CreatedAt = stored.CreatedAt
	if ev.Payload == nil {
		ev.Payload = make(map[string]any)
	}

	s.mu.RLock()
	type subSnap struct {
		id  int64
		sub *memSubscription
	}
	subs := make([]subSnap, 0, len(s.subs))
	for id, sub := range s.subs {
		subs = append(subs, subSnap{id: id, sub: sub})
	}
	s.mu.RUnlock()
	// Fan out to in-process subscribers; never block the appender.
	// Each send re-checks membership under RLock: cancel() deletes +
	// closes the channel under the write lock, so the existence check
	// and the send are mutually exclusive with close — a snapshot taken
	// before cancel can never send on a closed channel (issue #131).
	for _, sn := range subs {
		if sn.sub.projectID != "" && sn.sub.projectID != stored.ProjectID {
			continue
		}
		s.mu.RLock()
		_, ok := s.subs[sn.id]
		if ok {
			select {
			case sn.sub.ch <- cloneEvent(stored):
			default: // slow subscriber: drop, it can catch up via ListEvents
			}
		}
		s.mu.RUnlock()
	}
	return nil
}

func (s *MemStore) ListEvents(ctx context.Context, projectID string, sinceID int64, limit int) ([]*Event, error) {
	effective := ClampEventsLimit(limit)
	s.mu.RLock()
	defer s.mu.RUnlock()

	var results []*Event
	for _, ev := range s.events {
		if ev.ProjectID == projectID && ev.ID > sinceID {
			results = append(results, cloneEvent(ev))
			if len(results) >= effective {
				break
			}
		}
	}
	return results, nil
}

// Subscribe registers an in-process subscriber for subsequently appended
// events of one project. Buffered (64) + non-blocking send: a lagging
// reader drops events and can backfill with ListEvents.
//
// Cancel semantics (issue #110): the cancel func closes a dedicated done
// channel that the reaper goroutine selects on, so the goroutine terminates
// promptly when cancel is invoked — it does not linger until the parent
// context fires.
func (s *MemStore) Subscribe(ctx context.Context, projectID string) (<-chan *Event, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subSeq++
	id := s.subSeq
	ch := make(chan *Event, 64)
	s.subs[id] = &memSubscription{projectID: projectID, ch: ch}
	var once sync.Once
	done := make(chan struct{})
	cancel := func() {
		once.Do(func() {
			close(done)
			s.mu.Lock()
			defer s.mu.Unlock()
			if _, ok := s.subs[id]; ok {
				delete(s.subs, id)
				close(ch)
			}
		})
	}
	// Context cancellation also unsubscribes; the goroutine exits on
	// whichever fires first (cancel or parent ctx).
	go func() {
		select {
		case <-ctx.Done():
			cancel()
		case <-done:
		}
	}()
	return ch, cancel, nil
}
