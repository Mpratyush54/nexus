// code_links.go — memories linked to code (migration 044).
//
// code_entities are a project's files (symbols later), keyed by the
// repo-relative path the desktop reports. entity_links tie an entity to a
// memory or a cloud session:
//
//   - touched / tool_call: an agent edited the file (Edit/Write/apply_patch
//     tool calls captured by the desktop), or a memory came from a session
//     that did;
//   - decided_about / llm: the file is in a harvested memory's
//     files_affected;
//   - mentions: reserved.
//
// observed_hash is the file's content hash when the link was made. When the
// desktop reports a new hash for the file, CONFIRMED memories whose link
// saw a different hash are marked stale (MarkStaleByCodeHash); memories
// whose link saw the same hash are known fresh and are spared the coarser
// path-and-time rule (MarkMemoriesStaleForFilesExcept).
package store

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Code link vocabulary (CHECK constraints in migration 044).
const (
	CodeKindFile   = "file"
	CodeKindSymbol = "symbol"

	LinkSrcMemory  = "memory"
	LinkSrcSession = "session"

	RelationTouched      = "touched"
	RelationDecidedAbout = "decided_about"
	RelationMentions     = "mentions"

	LinkSourceToolCall = "tool_call"
	LinkSourceLLM      = "llm"

	// MaxCodeLinks caps how many links one code-links report may carry.
	MaxCodeLinks = 500
	// maxSourceLinks bounds ListCodeLinksForSource.
	maxSourceLinks = 1000
)

// CodeEntity is one file (or symbol) of a project.
type CodeEntity struct {
	ID          string     `json:"id"`
	ProjectID   string     `json:"project_id"`
	Kind        string     `json:"kind"`
	Path        string     `json:"path"`
	Symbol      string     `json:"symbol,omitempty"`
	ContentHash string     `json:"content_hash,omitempty"`
	LastSeenAt  *time.Time `json:"last_seen_at,omitempty"`
}

// EntityLink ties a code entity to a memory or a session.
type EntityLink struct {
	ID           string    `json:"id"`
	ProjectID    string    `json:"project_id"`
	EntityID     string    `json:"entity_id"`
	SrcKind      string    `json:"src_kind"`
	SrcID        string    `json:"src_id"`
	Relation     string    `json:"relation"`
	Source       string    `json:"source"`
	Confidence   float32   `json:"confidence"`
	ObservedHash string    `json:"observed_hash,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// CodeLinkHit is one link joined with its entity and, for lookups by path,
// the linked memory or session. Memory is set only when the memory is
// active and visible to the viewer in ctx (memory links to anything else
// are dropped). Session is the joined session row; callers check session
// read access themselves.
type CodeLinkHit struct {
	EntityID     string
	Path         string
	Symbol       string
	CurrentHash  string
	SrcKind      string
	SrcID        string
	Relation     string
	Source       string
	Confidence   float32
	ObservedHash string
	LinkedAt     time.Time
	Memory       *MemoryItem
	Session      *AgentSession
}

// CodeChanged reports whether the file changed since the link was made:
// both hashes are known and differ.
func (h CodeLinkHit) CodeChanged() bool {
	return h.ObservedHash != "" && h.CurrentHash != "" && h.ObservedHash != h.CurrentHash
}

// CodeLinkStore is implemented by MemStore and PostgresStore.
type CodeLinkStore interface {
	UpsertCodeEntity(ctx context.Context, projectID, kind, path, symbol string) (*CodeEntity, error)
	AddEntityLink(ctx context.Context, l *EntityLink) error
	ListCodeLinksByPath(ctx context.Context, projectID, path string, limit int) ([]CodeLinkHit, error)
	ListCodeLinksForSource(ctx context.Context, projectID, srcKind, srcID string) ([]CodeLinkHit, error)
	SetCodeEntityHash(ctx context.Context, projectID, path, hash string, at time.Time) (int, error)
	MarkStaleByCodeHash(ctx context.Context, projectID string, paths []string, at time.Time) (marked, fresh []string, err error)
	MarkMemoriesStaleForFilesExcept(ctx context.Context, projectID string, paths []string, at time.Time, except map[string]bool) ([]string, error)
	FindAgentSessionByNativeID(ctx context.Context, projectID, nativeID string) (string, error)
}

var (
	_ CodeLinkStore = (*MemStore)(nil)
	_ CodeLinkStore = (*PostgresStore)(nil)
)

// ErrBadCodeLink is returned for links with an invalid kind/relation/source.
var ErrBadCodeLink = errors.New("invalid code link")

// CleanCodePath makes a path usable as a code entity path: forward slashes,
// no "./" prefix, no doubled or trailing slash. Case is kept. Absolute
// paths and paths with ".." segments are rejected (ok=false): entity paths
// are repo-relative.
func CleanCodePath(p string) (string, bool) {
	p = strings.TrimSpace(strings.ReplaceAll(p, `\`, "/"))
	for strings.Contains(p, "//") {
		p = strings.ReplaceAll(p, "//", "/")
	}
	for strings.HasPrefix(p, "./") {
		p = p[2:]
	}
	p = strings.TrimSuffix(p, "/")
	if p == "" || p == "." || strings.HasPrefix(p, "/") || isDrivePath(p) {
		return "", false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return "", false
		}
	}
	return p, true
}

func isDrivePath(p string) bool {
	if len(p) < 2 || p[1] != ':' {
		return false
	}
	c := p[0]
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func validLinkKinds(l *EntityLink) bool {
	switch l.SrcKind {
	case LinkSrcMemory, LinkSrcSession:
	default:
		return false
	}
	switch l.Relation {
	case RelationTouched, RelationDecidedAbout, RelationMentions:
	default:
		return false
	}
	switch l.Source {
	case LinkSourceToolCall, LinkSourceLLM:
	default:
		return false
	}
	return true
}

func normalizeLink(l *EntityLink) error {
	if l == nil {
		return ErrBadCodeLink
	}
	l.ProjectID = strings.TrimSpace(l.ProjectID)
	l.EntityID = strings.TrimSpace(l.EntityID)
	l.SrcID = strings.TrimSpace(l.SrcID)
	l.SrcKind = strings.ToLower(strings.TrimSpace(l.SrcKind))
	l.Relation = strings.ToLower(strings.TrimSpace(l.Relation))
	l.Source = strings.ToLower(strings.TrimSpace(l.Source))
	l.ObservedHash = normalizeHash(l.ObservedHash)
	if l.ProjectID == "" || l.EntityID == "" || l.SrcID == "" || !validLinkKinds(l) {
		return ErrBadCodeLink
	}
	if l.Confidence <= 0 || l.Confidence > 1 {
		l.Confidence = 1
	}
	return nil
}

// cleanHashPaths cleans and lower-cases paths for hash lookups.
func cleanHashPaths(paths []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		c, ok := CleanCodePath(p)
		if !ok {
			continue
		}
		c = strings.ToLower(c)
		if seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
		if len(out) >= MaxStalePaths {
			break
		}
	}
	return out
}

// splitHashVerdicts turns (memory, observed, current) rows into the memories
// whose code changed (any link mismatches) and those known fresh (a link
// matches and none mismatches).
func splitHashVerdicts(rows [][3]string) (stale, fresh []string) {
	bad := map[string]bool{}
	good := map[string]bool{}
	var order []string
	for _, r := range rows {
		id, observed, current := r[0], normalizeHash(r[1]), normalizeHash(r[2])
		if id == "" || observed == "" || current == "" {
			continue
		}
		if !bad[id] && !good[id] {
			order = append(order, id)
		}
		if observed == current {
			good[id] = true
		} else {
			bad[id] = true
		}
	}
	for _, id := range order {
		if bad[id] {
			stale = append(stale, id)
		} else if good[id] {
			fresh = append(fresh, id)
		}
	}
	return stale, fresh
}

func sortHitsNewestFirst(hits []CodeLinkHit) {
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].LinkedAt.After(hits[j].LinkedAt) })
}

// ---- MemStore ----

type memCodeLink struct {
	EntityLink
	seq int64
}

func codeEntityKey(projectID, kind, path, symbol string) string {
	return projectID + "\x00" + kind + "\x00" + path + "\x00" + symbol
}

func codeLinkKey(l *EntityLink) string {
	return l.EntityID + "\x00" + l.SrcKind + "\x00" + l.SrcID + "\x00" + l.Relation
}

func (s *MemStore) ensureCodeMapsLocked() {
	if s.codeEntities == nil {
		s.codeEntities = map[string]*CodeEntity{}
	}
	if s.codeLinks == nil {
		s.codeLinks = map[string]*memCodeLink{}
	}
}

// findCodeEntityLocked matches an entity case-insensitively (paths from
// the LLM and the desktop may differ in case on Windows).
func (s *MemStore) findCodeEntityLocked(projectID, kind, path, symbol string) *CodeEntity {
	if e := s.codeEntities[codeEntityKey(projectID, kind, path, symbol)]; e != nil {
		return e
	}
	for _, e := range s.codeEntities {
		if e.ProjectID == projectID && e.Kind == kind && e.Symbol == symbol && strings.EqualFold(e.Path, path) {
			return e
		}
	}
	return nil
}

// UpsertCodeEntity returns the entity for (kind, path, symbol), creating it
// when missing. An existing entity whose path differs only in case is
// reused.
func (s *MemStore) UpsertCodeEntity(ctx context.Context, projectID, kind, path, symbol string) (*CodeEntity, error) {
	_ = ctx
	projectID = strings.TrimSpace(projectID)
	kind = strings.ToLower(strings.TrimSpace(kind))
	symbol = strings.TrimSpace(symbol)
	clean, ok := CleanCodePath(path)
	if projectID == "" || !ok || (kind != CodeKindFile && kind != CodeKindSymbol) {
		return nil, ErrBadCodeLink
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureCodeMapsLocked()
	if e := s.findCodeEntityLocked(projectID, kind, clean, symbol); e != nil {
		cp := *e
		return &cp, nil
	}
	e := &CodeEntity{ID: newID("cent"), ProjectID: projectID, Kind: kind, Path: clean, Symbol: symbol}
	s.codeEntities[codeEntityKey(projectID, kind, clean, symbol)] = e
	cp := *e
	return &cp, nil
}

// AddEntityLink inserts l, or on a repeat keeps the first created_at and
// takes a newer non-empty observed hash and the higher confidence.
func (s *MemStore) AddEntityLink(ctx context.Context, l *EntityLink) error {
	_ = ctx
	if err := normalizeLink(l); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureCodeMapsLocked()
	found := false
	for _, e := range s.codeEntities {
		if e.ID == l.EntityID && e.ProjectID == l.ProjectID {
			found = true
			break
		}
	}
	if !found {
		return ErrNotFound
	}
	key := codeLinkKey(l)
	if cur := s.codeLinks[key]; cur != nil {
		if l.ObservedHash != "" {
			cur.ObservedHash = l.ObservedHash
		}
		if l.Confidence > cur.Confidence {
			cur.Confidence = l.Confidence
		}
		*l = cur.EntityLink
		return nil
	}
	l.ID = newID("elink")
	l.CreatedAt = time.Now().UTC()
	s.codeLinks[key] = &memCodeLink{EntityLink: *l, seq: idCounter.Add(1)}
	return nil
}

// memLinkVisibleLocked mirrors SearchMemory's filters for one memory.
func (s *MemStore) memLinkVisibleLocked(m *MemoryItem, projectID, viewerID string, isMember bool) bool {
	if m == nil || !memoryVisibleToProject(m, projectID) || m.SupersededBy != "" {
		return false
	}
	if m.Status != StatusConfirmed && m.Status != StatusProposed {
		return false
	}
	if viewerID == "" {
		vis := NormalizeVisibility(m.Visibility)
		return vis == VisibilityProject || vis == VisibilityPublic
	}
	return s.memoryVisibleLocked(m, viewerID, isMember)
}

func (s *MemStore) hitLocked(e *CodeEntity, l *memCodeLink) CodeLinkHit {
	return CodeLinkHit{
		EntityID: e.ID, Path: e.Path, Symbol: e.Symbol, CurrentHash: e.ContentHash,
		SrcKind: l.SrcKind, SrcID: l.SrcID, Relation: l.Relation, Source: l.Source,
		Confidence: l.Confidence, ObservedHash: l.ObservedHash, LinkedAt: l.CreatedAt,
	}
}

// ListCodeLinksByPath returns links of entities whose path matches path
// (FilePathMatches: suffix at a segment boundary), newest first, with the
// visible memory or the session joined.
func (s *MemStore) ListCodeLinksByPath(ctx context.Context, projectID, path string, limit int) ([]CodeLinkHit, error) {
	if NormalizeFilePath(path) == "" || strings.TrimSpace(projectID) == "" {
		return []CodeLinkHit{}, nil
	}
	if limit <= 0 {
		limit = 50
	}
	viewerID := ViewerFrom(ctx)
	isMember := false
	if viewerID != "" {
		ok, err := s.IsProjectMember(ctx, viewerID, projectID)
		if err != nil {
			return nil, err
		}
		isMember = ok
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	type ranked struct {
		hit CodeLinkHit
		seq int64
	}
	var all []ranked
	for _, l := range s.codeLinks {
		if l.ProjectID != projectID {
			continue
		}
		var e *CodeEntity
		for _, cand := range s.codeEntities {
			if cand.ID == l.EntityID {
				e = cand
				break
			}
		}
		if e == nil || !FilePathMatches(e.Path, path) {
			continue
		}
		h := s.hitLocked(e, l)
		switch l.SrcKind {
		case LinkSrcMemory:
			m := s.memories[l.SrcID]
			if !s.memLinkVisibleLocked(m, projectID, viewerID, isMember) {
				continue
			}
			h.Memory = cloneMemoryItem(m)
		case LinkSrcSession:
			if row := s.agentSessions[l.SrcID]; row != nil && row.ProjectID == projectID {
				cp := *row
				h.Session = &cp
			}
		}
		all = append(all, ranked{hit: h, seq: l.seq})
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].hit.LinkedAt.Equal(all[j].hit.LinkedAt) {
			return all[i].seq > all[j].seq
		}
		return all[i].hit.LinkedAt.After(all[j].hit.LinkedAt)
	})
	out := make([]CodeLinkHit, 0, limit)
	for _, r := range all {
		if len(out) >= limit {
			break
		}
		out = append(out, r.hit)
	}
	return out, nil
}

// ListCodeLinksForSource returns the links of one memory or session (its
// paths), newest first. Memory/Session are not joined.
func (s *MemStore) ListCodeLinksForSource(ctx context.Context, projectID, srcKind, srcID string) ([]CodeLinkHit, error) {
	_ = ctx
	srcID = strings.TrimSpace(srcID)
	srcKind = strings.ToLower(strings.TrimSpace(srcKind))
	s.mu.RLock()
	defer s.mu.RUnlock()
	type ranked struct {
		hit CodeLinkHit
		seq int64
	}
	var all []ranked
	for _, l := range s.codeLinks {
		if l.ProjectID != projectID || l.SrcKind != srcKind || l.SrcID != srcID {
			continue
		}
		for _, e := range s.codeEntities {
			if e.ID == l.EntityID {
				all = append(all, ranked{hit: s.hitLocked(e, l), seq: l.seq})
				break
			}
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].seq > all[j].seq })
	out := make([]CodeLinkHit, 0, len(all))
	for i, r := range all {
		if i >= maxSourceLinks {
			break
		}
		out = append(out, r.hit)
	}
	return out, nil
}

// SetCodeEntityHash records the current content hash of the file entity at
// path (case-insensitive). It returns how many entities it updated; files
// no memory or session was ever linked to have no entity and are skipped.
func (s *MemStore) SetCodeEntityHash(ctx context.Context, projectID, path, hash string, at time.Time) (int, error) {
	_ = ctx
	clean, ok := CleanCodePath(path)
	hash = normalizeHash(hash)
	if !ok || hash == "" || strings.TrimSpace(projectID) == "" {
		return 0, nil
	}
	if at.IsZero() {
		at = time.Now()
	}
	at = at.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, e := range s.codeEntities {
		if e.ProjectID == projectID && e.Kind == CodeKindFile && strings.EqualFold(e.Path, clean) {
			e.ContentHash = hash
			stamp := at
			e.LastSeenAt = &stamp
			n++
		}
	}
	return n, nil
}

// MarkStaleByCodeHash applies the hash rule to the file entities at paths:
// a CONFIRMED memory linked with an observed hash that differs from the
// entity's current hash is marked stale (first stamp kept). fresh lists
// memories whose links all saw the current hash.
func (s *MemStore) MarkStaleByCodeHash(ctx context.Context, projectID string, paths []string, at time.Time) ([]string, []string, error) {
	_ = ctx
	lower := cleanHashPaths(paths)
	if strings.TrimSpace(projectID) == "" || len(lower) == 0 {
		return []string{}, []string{}, nil
	}
	want := map[string]bool{}
	for _, p := range lower {
		want[p] = true
	}
	if at.IsZero() {
		at = time.Now()
	}
	at = at.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	var rows [][3]string
	for _, l := range s.codeLinks {
		if l.ProjectID != projectID || l.SrcKind != LinkSrcMemory {
			continue
		}
		for _, e := range s.codeEntities {
			if e.ID == l.EntityID && e.Kind == CodeKindFile && want[strings.ToLower(e.Path)] {
				rows = append(rows, [3]string{l.SrcID, l.ObservedHash, e.ContentHash})
				break
			}
		}
	}
	stale, fresh := splitHashVerdicts(rows)
	marked := []string{}
	for _, id := range stale {
		m := s.memories[id]
		if m == nil || m.Status != StatusConfirmed || m.StaleSince != nil || m.SupersededBy != "" {
			continue
		}
		stamp := at
		m.StaleSince = &stamp
		marked = append(marked, id)
	}
	if fresh == nil {
		fresh = []string{}
	}
	return marked, fresh, nil
}

// FindAgentSessionByNativeID returns the cloud id of the project's most
// recently active session with that native id.
func (s *MemStore) FindAgentSessionByNativeID(ctx context.Context, projectID, nativeID string) (string, error) {
	_ = ctx
	nativeID = strings.TrimSpace(nativeID)
	if nativeID == "" {
		return "", ErrNotFound
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var best *AgentSession
	for _, row := range s.agentSessions {
		if row.ProjectID != projectID || row.NativeID != nativeID {
			continue
		}
		if best == nil || row.LastActiveAt.After(best.LastActiveAt) {
			best = row
		}
	}
	if best == nil {
		return "", ErrNotFound
	}
	return best.ID, nil
}

// ---- PostgresStore ----

// UpsertCodeEntity returns the entity for (kind, path, symbol), creating it
// when missing. An existing entity whose path differs only in case is
// reused.
func (s *PostgresStore) UpsertCodeEntity(ctx context.Context, projectID, kind, path, symbol string) (*CodeEntity, error) {
	projectID = strings.TrimSpace(projectID)
	kind = strings.ToLower(strings.TrimSpace(kind))
	symbol = strings.TrimSpace(symbol)
	clean, ok := CleanCodePath(path)
	if !looksLikeUUID(projectID) || !ok || (kind != CodeKindFile && kind != CodeKindSymbol) {
		return nil, ErrBadCodeLink
	}
	const cols = `id::text, project_id::text, kind, path, COALESCE(symbol, ''), COALESCE(content_hash, ''), last_seen_at`
	scan := func(row interface{ Scan(...any) error }) (*CodeEntity, error) {
		var e CodeEntity
		if err := row.Scan(&e.ID, &e.ProjectID, &e.Kind, &e.Path, &e.Symbol, &e.ContentHash, &e.LastSeenAt); err != nil {
			return nil, err
		}
		return &e, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+cols+` FROM code_entities
		  WHERE project_id = $1::uuid AND kind = $2 AND lower(path) = lower($3)
		    AND COALESCE(symbol, '') = $4
		  ORDER BY (path = $3) DESC LIMIT 1`,
		projectID, kind, clean, symbol)
	if err != nil {
		return nil, err
	}
	var found *CodeEntity
	if rows.Next() {
		found, err = scan(rows)
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if found != nil {
		return found, nil
	}
	// The no-op update makes RETURNING yield the row on a concurrent insert.
	return scan(s.pool.QueryRow(ctx,
		`INSERT INTO code_entities (project_id, kind, path, symbol)
		 VALUES ($1::uuid, $2, $3, NULLIF($4, ''))
		 ON CONFLICT (project_id, kind, path, (COALESCE(symbol, ''))) DO UPDATE SET path = EXCLUDED.path
		 RETURNING `+cols,
		projectID, kind, clean, symbol))
}

// AddEntityLink inserts l, or on a repeat keeps the first created_at and
// takes a newer non-empty observed hash and the higher confidence.
func (s *PostgresStore) AddEntityLink(ctx context.Context, l *EntityLink) error {
	if err := normalizeLink(l); err != nil {
		return err
	}
	if !looksLikeUUID(l.ProjectID) || !looksLikeUUID(l.EntityID) || !looksLikeUUID(l.SrcID) {
		return ErrBadCodeLink
	}
	var observed *string
	err := s.pool.QueryRow(ctx,
		`INSERT INTO entity_links (project_id, entity_id, src_kind, src_id, relation, source, confidence, observed_hash)
		 SELECT $1::uuid, e.id, $3::text, $4::text, $5::text, $6::text, $7::real, NULLIF($8::text, '')
		   FROM code_entities e WHERE e.id = $2::uuid AND e.project_id = $1::uuid
		 ON CONFLICT (entity_id, src_kind, src_id, relation) DO UPDATE
		   SET observed_hash = COALESCE(EXCLUDED.observed_hash, entity_links.observed_hash),
		       confidence = GREATEST(entity_links.confidence, EXCLUDED.confidence)
		 RETURNING id::text, confidence, observed_hash, created_at`,
		l.ProjectID, l.EntityID, l.SrcKind, l.SrcID, l.Relation, l.Source, l.Confidence, l.ObservedHash,
	).Scan(&l.ID, &l.Confidence, &observed, &l.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	l.ObservedHash = ""
	if observed != nil {
		l.ObservedHash = *observed
	}
	return nil
}

const codeLinkHitColumns = `e.id::text, e.path, COALESCE(e.symbol, ''), COALESCE(e.content_hash, ''),
	l.src_kind, l.src_id, l.relation, l.source, l.confidence, COALESCE(l.observed_hash, ''), l.created_at`

// ListCodeLinksByPath returns links of entities whose path matches path
// (FilePathMatches), newest first, with the visible memory or the session
// joined. A basename LIKE prefilters in SQL; the exact rule runs in Go.
func (s *PostgresStore) ListCodeLinksByPath(ctx context.Context, projectID, path string, limit int) ([]CodeLinkHit, error) {
	norm := NormalizeFilePath(path)
	if norm == "" || !looksLikeUUID(projectID) {
		return []CodeLinkHit{}, nil
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+codeLinkHitColumns+`,
		        s.id::text, COALESCE(s.title, ''), COALESCE(s.harness, ''), COALESCE(s.native_id, ''),
		        s.started_at, s.last_active_at, COALESCE(s.summary, ''), COALESCE(s.owner_user_id::text, ''),
		        COALESCE(s.visibility, '')
		   FROM code_entities e
		   JOIN entity_links l ON l.entity_id = e.id
		   LEFT JOIN agent_sessions s ON l.src_kind = 'session' AND s.id = l.src_id::uuid
		  WHERE e.project_id = $1::uuid
		    AND lower(replace(e.path, '\', '/')) LIKE '%' || $2
		  ORDER BY l.created_at DESC
		  LIMIT $3`,
		projectID, fileBaseName(norm), limit*8)
	if err != nil {
		return nil, err
	}
	var hits []CodeLinkHit
	for rows.Next() {
		var h CodeLinkHit
		var sid *string
		var title, harness, native, summary, owner, vis string
		var started, last *time.Time
		if err := rows.Scan(&h.EntityID, &h.Path, &h.Symbol, &h.CurrentHash,
			&h.SrcKind, &h.SrcID, &h.Relation, &h.Source, &h.Confidence, &h.ObservedHash, &h.LinkedAt,
			&sid, &title, &harness, &native, &started, &last, &summary, &owner, &vis); err != nil {
			rows.Close()
			return nil, err
		}
		if !FilePathMatches(h.Path, path) {
			continue
		}
		if sid != nil {
			sess := &AgentSession{ID: *sid, ProjectID: projectID, Title: title, Harness: harness,
				NativeID: native, Summary: summary, OwnerUserID: owner, Visibility: vis}
			if started != nil {
				sess.StartedAt = *started
			}
			if last != nil {
				sess.LastActiveAt = *last
			}
			h.Session = sess
		}
		hits = append(hits, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var memIDs []string
	for _, h := range hits {
		if h.SrcKind == LinkSrcMemory {
			memIDs = append(memIDs, h.SrcID)
		}
	}
	mems := map[string]*MemoryItem{}
	if len(memIDs) > 0 {
		mr, err := s.pool.Query(ctx,
			`SELECT `+memoryColumns+` FROM memory_items
			  WHERE id = ANY($1::uuid[])
			    AND (project_id = $2::uuid OR (project_id IS NULL AND level = 'organization'))
			    AND status IN ('CONFIRMED','PROPOSED')
			    AND superseded_by IS NULL`,
			memIDs, projectID)
		if err != nil {
			return nil, err
		}
		var cands []*MemoryItem
		for mr.Next() {
			m, err := scanMemoryItem(mr)
			if err != nil {
				mr.Close()
				return nil, err
			}
			cands = append(cands, m)
		}
		mr.Close()
		if err := mr.Err(); err != nil {
			return nil, err
		}
		visible, err := s.viewerVisibleMemories(ctx, projectID, cands, len(cands))
		if err != nil {
			return nil, err
		}
		for _, m := range visible {
			mems[m.ID] = m
		}
	}
	out := make([]CodeLinkHit, 0, limit)
	for _, h := range hits {
		if len(out) >= limit {
			break
		}
		if h.SrcKind == LinkSrcMemory {
			m := mems[h.SrcID]
			if m == nil {
				continue
			}
			h.Memory = m
		}
		out = append(out, h)
	}
	sortHitsNewestFirst(out)
	return out, nil
}

// ListCodeLinksForSource returns the links of one memory or session (its
// paths), newest first. Memory/Session are not joined.
func (s *PostgresStore) ListCodeLinksForSource(ctx context.Context, projectID, srcKind, srcID string) ([]CodeLinkHit, error) {
	srcID = strings.TrimSpace(srcID)
	if !looksLikeUUID(projectID) || !looksLikeUUID(srcID) {
		return []CodeLinkHit{}, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+codeLinkHitColumns+`
		   FROM entity_links l JOIN code_entities e ON e.id = l.entity_id
		  WHERE l.project_id = $1::uuid AND l.src_kind = $2 AND l.src_id = $3
		  ORDER BY l.created_at DESC
		  LIMIT $4`,
		projectID, strings.ToLower(strings.TrimSpace(srcKind)), srcID, maxSourceLinks)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CodeLinkHit{}
	for rows.Next() {
		var h CodeLinkHit
		if err := rows.Scan(&h.EntityID, &h.Path, &h.Symbol, &h.CurrentHash,
			&h.SrcKind, &h.SrcID, &h.Relation, &h.Source, &h.Confidence, &h.ObservedHash, &h.LinkedAt); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// SetCodeEntityHash records the current content hash of the file entity at
// path (case-insensitive). Files without an entity are skipped.
func (s *PostgresStore) SetCodeEntityHash(ctx context.Context, projectID, path, hash string, at time.Time) (int, error) {
	clean, ok := CleanCodePath(path)
	hash = normalizeHash(hash)
	if !ok || hash == "" || !looksLikeUUID(projectID) {
		return 0, nil
	}
	if at.IsZero() {
		at = time.Now()
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE code_entities SET content_hash = $3, last_seen_at = $4
		  WHERE project_id = $1::uuid AND kind = 'file' AND lower(path) = lower($2)`,
		projectID, clean, hash, at.UTC())
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// MarkStaleByCodeHash applies the hash rule (see MemStore).
func (s *PostgresStore) MarkStaleByCodeHash(ctx context.Context, projectID string, paths []string, at time.Time) ([]string, []string, error) {
	lower := cleanHashPaths(paths)
	if !looksLikeUUID(projectID) || len(lower) == 0 {
		return []string{}, []string{}, nil
	}
	if at.IsZero() {
		at = time.Now()
	}
	at = at.UTC()
	rows, err := s.pool.Query(ctx,
		`SELECT l.src_id, COALESCE(l.observed_hash, ''), COALESCE(e.content_hash, '')
		   FROM code_entities e JOIN entity_links l ON l.entity_id = e.id
		  WHERE e.project_id = $1::uuid AND e.kind = 'file'
		    AND lower(e.path) = ANY($2::text[])
		    AND l.src_kind = 'memory'
		    AND COALESCE(l.observed_hash, '') <> ''
		    AND COALESCE(e.content_hash, '') <> ''`,
		projectID, lower)
	if err != nil {
		return nil, nil, err
	}
	var verdicts [][3]string
	for rows.Next() {
		var r [3]string
		if err := rows.Scan(&r[0], &r[1], &r[2]); err != nil {
			rows.Close()
			return nil, nil, err
		}
		verdicts = append(verdicts, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	stale, fresh := splitHashVerdicts(verdicts)
	if fresh == nil {
		fresh = []string{}
	}
	marked := []string{}
	if len(stale) == 0 {
		return marked, fresh, nil
	}
	out, err := s.pool.Query(ctx,
		`UPDATE memory_items SET stale_since = $2
		  WHERE id = ANY($1::uuid[]) AND project_id = $3::uuid AND stale_since IS NULL
		    AND status = 'CONFIRMED' AND superseded_by IS NULL
		  RETURNING id::text`, stale, at, projectID)
	if err != nil {
		return nil, nil, err
	}
	defer out.Close()
	for out.Next() {
		var id string
		if err := out.Scan(&id); err != nil {
			return nil, nil, err
		}
		marked = append(marked, id)
	}
	return marked, fresh, out.Err()
}

// FindAgentSessionByNativeID returns the cloud id of the project's most
// recently active session with that native id.
func (s *PostgresStore) FindAgentSessionByNativeID(ctx context.Context, projectID, nativeID string) (string, error) {
	nativeID = strings.TrimSpace(nativeID)
	if nativeID == "" || !looksLikeUUID(projectID) {
		return "", ErrNotFound
	}
	var id string
	err := s.pool.QueryRow(ctx,
		`SELECT id::text FROM agent_sessions
		  WHERE project_id = $1::uuid AND native_id = $2
		  ORDER BY last_active_at DESC LIMIT 1`, projectID, nativeID).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	return id, nil
}
