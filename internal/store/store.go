package store

import (
	"context"
	"errors"
	"fmt"
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
}

// MemStore is a thread-safe in-memory Store implementation, ideal for unit testing and local development.
type MemStore struct {
	mu         sync.RWMutex
	projects   map[string]*Project
	workspaces map[string]*Workspace
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
				return p, nil
			}
		}
	}

	// 2. Check root commit hash
	if rootCommit != "" {
		for _, p := range s.projects {
			if p.RootCommit != "" && p.RootCommit == rootCommit {
				return p, nil
			}
		}
	}

	// 3. Fallback to folder name
	for _, p := range s.projects {
		if p.FolderName == folderName {
			return p, nil
		}
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
	return p, nil
}

func (s *MemStore) GetProject(ctx context.Context, id string) (*Project, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.projects[id]
	if !ok {
		return nil, ErrNotFound
	}
	return p, nil
}

func (s *MemStore) RegisterWorkspace(ctx context.Context, ws *Workspace) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ws.ID == "" {
		ws.ID = newID("ws")
	}
	ws.LastSeen = time.Now().UTC()
	ws.IsOnline = true
	s.workspaces[ws.ID] = ws
	return nil
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
		if ws.ProjectID == projectID && ws.IsOnline && ws.LastSeen.After(threshold) {
			if mostRecent == nil || ws.LastSeen.After(mostRecent.LastSeen) {
				mostRecent = ws
			}
		}
	}
	if mostRecent == nil {
		return nil, ErrNotFound
	}
	return mostRecent, nil
}

func (s *MemStore) CreateMemoryItem(ctx context.Context, item *MemoryItem) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if item.ID == "" {
		item.ID = newID("mem")
	}
	if item.Confidence == 0 {
		item.Confidence = 1.0
	}
	if item.Status == "" {
		item.Status = "PROPOSED"
	}
	if item.Level == "" {
		item.Level = "project"
	}
	item.CreatedAt = time.Now().UTC()
	item.UpdatedAt = item.CreatedAt
	s.memories[item.ID] = item
	return nil
}

func (s *MemStore) GetMemoryItem(ctx context.Context, id string) (*MemoryItem, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.memories[id]
	if !ok {
		return nil, ErrNotFound
	}
	return item, nil
}

func (s *MemStore) ConfirmMemory(ctx context.Context, id string, confirmedBy string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.memories[id]
	if !ok {
		return ErrNotFound
	}
	item.Status = "CONFIRMED"
	item.ConfirmedBy = confirmedBy
	item.UpdatedAt = time.Now().UTC()
	return nil
}

func (s *MemStore) SearchMemory(ctx context.Context, projectID string, query string, tags []string, limit int) ([]*MemoryItem, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var results []*MemoryItem
	terms := strings.Fields(strings.ToLower(query))

	for _, item := range s.memories {
		if item.ProjectID != "" {
			if item.ProjectID != projectID {
				continue
			}
		} else if item.Level != "organization" {
			// NULL-project rows are globally readable only on the
			// org tier; personal/session/project rows stay hidden.
			continue
		}
		if item.Status != "CONFIRMED" && item.Status != "PROPOSED" {
			continue
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
			results = append(results, item)
		}
		if limit > 0 && len(results) >= limit {
			break
		}
	}
	return results, nil
}

func (s *MemStore) CreateEpisode(ctx context.Context, ep *Episode) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ep.ID == "" {
		ep.ID = newID("ep")
	}
	if ep.Status == "" {
		ep.Status = "OPEN"
	}
	ep.OpenedAt = time.Now().UTC()
	s.episodes[ep.ID] = ep
	return nil
}

func (s *MemStore) GetEpisode(ctx context.Context, id string) (*Episode, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ep, ok := s.episodes[id]
	if !ok {
		return nil, ErrNotFound
	}
	return ep, nil
}

func (s *MemStore) ResolveEpisode(ctx context.Context, id, resolution, verification, resolvedBy string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ep, ok := s.episodes[id]
	if !ok {
		return ErrNotFound
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
			narrative := strings.ToLower(ep.Title + " " + ep.Trigger + " " + ep.RootCause + " " + ep.Resolution)
			if strings.Contains(narrative, lowQ) {
				match = true
			}
		}
		if match || (lowErr == "" && lowQ == "") {
			results = append(results, ep)
		}
		if limit > 0 && len(results) >= limit {
			break
		}
	}
	return results, nil
}

func (s *MemStore) AppendEvent(ctx context.Context, ev *Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.eventSeq++
	ev.ID = s.eventSeq
	ev.CreatedAt = time.Now().UTC()
	s.events = append(s.events, ev)
	// Fan out to in-process subscribers; never block the appender.
	for _, sub := range s.subs {
		if sub.projectID != "" && sub.projectID != ev.ProjectID {
			continue
		}
		select {
		case sub.ch <- ev:
		default: // slow subscriber: drop, it can catch up via ListEvents
		}
	}
	return nil
}

func (s *MemStore) ListEvents(ctx context.Context, projectID string, sinceID int64, limit int) ([]*Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var results []*Event
	for _, ev := range s.events {
		if ev.ProjectID == projectID && ev.ID > sinceID {
			results = append(results, ev)
			if limit > 0 && len(results) >= limit {
				break
			}
		}
	}
	return results, nil
}

// Subscribe registers an in-process subscriber for subsequently appended
// events of one project. Buffered (64) + non-blocking send: a lagging
// reader drops events and can backfill with ListEvents.
func (s *MemStore) Subscribe(ctx context.Context, projectID string) (<-chan *Event, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subSeq++
	id := s.subSeq
	ch := make(chan *Event, 64)
	s.subs[id] = &memSubscription{projectID: projectID, ch: ch}
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if _, ok := s.subs[id]; ok {
				delete(s.subs, id)
				close(ch)
			}
		})
	}
	// Context cancellation also unsubscribes.
	go func() {
		<-ctx.Done()
		cancel()
	}()
	return ch, cancel, nil
}
