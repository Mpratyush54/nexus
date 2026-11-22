// memory_graph.go — the project's memory graph for the desktop Graph view.
//
// Nodes are memories, the cloud sessions (chats) they came from, and the
// code files they touch (code_entities, migration 044). Edges are:
//
//   - memory -> session: "from" (a session:<id> tag or session_id) and
//     "cites" (the project brief's brief-source:<id> tags);
//   - memory -> file and session -> file: the entity_links relation
//     (touched / decided_about / mentions).
//
// Visibility follows memory search and the timeline: memories come from
// SearchMemory as the viewer (own private rows, project-visible rows),
// sessions from ListTimeline or CanReadAgentSession, and a link is kept only
// when its memory or session is in the graph.
package store

import (
	"context"
	"path"
	"sort"
	"strings"
	"time"
)

// Graph node kinds.
const (
	GraphKindMemory  = "memory"
	GraphKindSession = "session"
	GraphKindFile    = "file"

	GraphRelationFrom  = "from"
	GraphRelationCites = "cites"

	// MaxGraphNodes caps one graph; DefaultGraphNodes is used when the
	// caller gives no limit.
	MaxGraphNodes     = 300
	DefaultGraphNodes = 300
	minGraphNodes     = 10
	// maxGraphLinks bounds the links read for one graph.
	maxGraphLinks = 5000
	// maxGraphSessionLookups bounds the per-session reads for sessions a
	// memory cites that are not on the viewer's recent timeline page.
	maxGraphSessionLookups = 60
)

// GraphNode is one node of the memory graph. ID is kind-prefixed
// ("memory:<id>", "session:<id>", "file:<path>"); Ref is the memory or
// session id, or the file path.
type GraphNode struct {
	ID        string     `json:"id"`
	Kind      string     `json:"kind"`
	Ref       string     `json:"ref"`
	Label     string     `json:"label"`
	Sub       string     `json:"sub,omitempty"`
	Stale     bool       `json:"stale"`
	Pinned    bool       `json:"pinned,omitempty"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

// GraphEdge connects two node ids.
type GraphEdge struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Relation string `json:"relation"`
}

// ProjectGraph is GET /v1/projects/{id}/graph.
type ProjectGraph struct {
	ProjectID string      `json:"project_id"`
	Nodes     []GraphNode `json:"nodes"`
	Edges     []GraphEdge `json:"edges"`
	// Truncated is set when visible memories, sessions or files were left
	// out to stay under the node cap.
	Truncated bool `json:"truncated"`
}

// ClampGraphLimit maps a requested node count to [10, MaxGraphNodes].
func ClampGraphLimit(n int) int {
	if n <= 0 {
		return DefaultGraphNodes
	}
	if n < minGraphNodes {
		return minGraphNodes
	}
	if n > MaxGraphNodes {
		return MaxGraphNodes
	}
	return n
}

// ProjectLinkLister lists a project's code links (CodeLinkStore).
type ProjectLinkLister interface {
	ListProjectCodeLinks(ctx context.Context, projectID string, limit int) ([]CodeLinkHit, error)
}

// GraphSources are the stores LoadProjectGraph reads. Sessions and Links
// may be nil (store without agent sessions or code links).
type GraphSources struct {
	Memories interface {
		SearchMemory(ctx context.Context, projectID, query string, tags []string, limit int) ([]*MemoryItem, error)
	}
	Sessions AgentCloudStore
	Links    ProjectLinkLister
}

// LoadProjectGraph builds the viewer's graph of a project, at most limit
// nodes (ClampGraphLimit). The caller has checked project membership.
func LoadProjectGraph(ctx context.Context, src GraphSources, viewerID, projectID string, limit int) (*ProjectGraph, error) {
	limit = ClampGraphLimit(limit)
	g := &ProjectGraph{ProjectID: projectID, Nodes: []GraphNode{}, Edges: []GraphEdge{}}
	if src.Memories == nil {
		return g, nil
	}
	vctx := WithViewer(ctx, viewerID)
	mems, err := src.Memories.SearchMemory(vctx, projectID, "", nil, limit*2)
	if err != nil {
		return nil, err
	}
	// Pinned rows (and the brief) rank first whatever their confidence, so
	// fetch them on their own.
	pinned, err := src.Memories.SearchMemory(vctx, projectID, "", []string{pinnedTag, ProjectBriefTag}, MaxGraphNodes)
	if err != nil {
		return nil, err
	}
	memByID := map[string]*MemoryItem{}
	for _, list := range [][]*MemoryItem{pinned, mems} {
		for _, m := range list {
			if m != nil && m.ID != "" && memByID[m.ID] == nil {
				memByID[m.ID] = m
			}
		}
	}
	mems = mems[:0:0]
	for _, m := range memByID {
		mems = append(mems, m)
	}

	sessByID := map[string]*AgentSession{}
	if src.Sessions != nil {
		items, _, err := src.Sessions.ListTimeline(ctx, TimelineQuery{UserID: viewerID, ProjectID: projectID, Limit: 100})
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			if it.ProjectID != projectID {
				continue
			}
			sessByID[it.SessionID] = &AgentSession{ID: it.SessionID, ProjectID: it.ProjectID, Harness: it.Harness,
				NativeID: it.NativeID, Title: it.Title, Summary: it.Summary, LastActiveAt: it.LastActiveAt}
		}
		// Sessions memories cite that are not on the recent page.
		lookups := 0
		for _, m := range rankGraphMemories(mems) {
			for _, ref := range memorySessionRefs(m) {
				if sessByID[ref.id] != nil || lookups >= maxGraphSessionLookups {
					continue
				}
				if _, tried := sessByID[ref.id]; tried {
					continue
				}
				lookups++
				sessByID[ref.id] = nil
				if ok, err := src.Sessions.CanReadAgentSession(ctx, viewerID, ref.id); err != nil || !ok {
					continue
				}
				row, err := src.Sessions.GetAgentSession(ctx, ref.id)
				if err != nil || row == nil || row.ProjectID != projectID {
					continue
				}
				cp := *row
				sessByID[ref.id] = &cp
			}
		}
		for id, row := range sessByID {
			if row == nil {
				delete(sessByID, id)
			}
		}
	}

	var links []CodeLinkHit
	if src.Links != nil {
		links, err = src.Links.ListProjectCodeLinks(ctx, projectID, maxGraphLinks)
		if err != nil {
			return nil, err
		}
	}
	// Keep only links whose memory or session the viewer sees.
	kept := links[:0:0]
	filesAvail := map[string]bool{}
	for _, l := range links {
		switch l.SrcKind {
		case LinkSrcMemory:
			if memByID[l.SrcID] == nil {
				continue
			}
		case LinkSrcSession:
			if sessByID[l.SrcID] == nil {
				continue
			}
		default:
			continue
		}
		kept = append(kept, l)
		filesAvail[graphFileKey(l.Path)] = true
	}
	links = kept

	// Budget: memories ~45%, sessions ~20%, files the rest; unused shares
	// flow to the others.
	memRank := rankGraphMemories(mems)
	sessQ, fileQ := limit*20/100, limit-limit*45/100-limit*20/100
	fileGuess := minInt(len(filesAvail), fileQ)
	sTake := minInt(len(sessByID), sessQ)
	mTake := minInt(len(memRank), limit-sTake-fileGuess)
	sTake = minInt(len(sessByID), limit-mTake-fileGuess)
	selMem := memRank[:mTake]

	// Sessions the selected memories cite first, then the most recent.
	cited := map[string]bool{}
	for _, m := range selMem {
		for _, ref := range memorySessionRefs(m) {
			cited[ref.id] = true
		}
	}
	sessList := make([]*AgentSession, 0, len(sessByID))
	for _, row := range sessByID {
		sessList = append(sessList, row)
	}
	sort.SliceStable(sessList, func(i, j int) bool {
		a, b := sessList[i], sessList[j]
		if cited[a.ID] != cited[b.ID] {
			return cited[a.ID]
		}
		if !a.LastActiveAt.Equal(b.LastActiveAt) {
			return a.LastActiveAt.After(b.LastActiveAt)
		}
		return a.ID < b.ID
	})
	selSess := sessList[:sTake]

	inMem := map[string]bool{}
	for _, m := range selMem {
		inMem[m.ID] = true
	}
	inSess := map[string]bool{}
	for _, row := range selSess {
		inSess[row.ID] = true
	}

	// Files ranked by links to the selected memories and sessions.
	type fileAgg struct {
		key, path string
		count     int
		latest    time.Time
		changed   bool
	}
	files := map[string]*fileAgg{}
	for _, l := range links {
		if (l.SrcKind == LinkSrcMemory && !inMem[l.SrcID]) || (l.SrcKind == LinkSrcSession && !inSess[l.SrcID]) {
			continue
		}
		key := graphFileKey(l.Path)
		f := files[key]
		if f == nil {
			f = &fileAgg{key: key, path: l.Path}
			files[key] = f
		}
		f.count++
		if l.LinkedAt.After(f.latest) {
			f.latest = l.LinkedAt
		}
		if l.SrcKind == LinkSrcMemory && l.CodeChanged() {
			f.changed = true
		}
	}
	fileList := make([]*fileAgg, 0, len(files))
	for _, f := range files {
		fileList = append(fileList, f)
	}
	sort.SliceStable(fileList, func(i, j int) bool {
		a, b := fileList[i], fileList[j]
		if a.count != b.count {
			return a.count > b.count
		}
		if !a.latest.Equal(b.latest) {
			return a.latest.After(b.latest)
		}
		return a.key < b.key
	})
	fTake := minInt(len(fileList), limit-mTake-sTake)
	selFiles := fileList[:fTake]
	inFile := map[string]bool{}
	for _, f := range selFiles {
		inFile[f.key] = true
	}
	g.Truncated = mTake < len(memRank) || sTake < len(sessByID) || fTake < len(fileList) || len(filesAvail) > len(files)

	for _, m := range selMem {
		at := m.UpdatedAt
		if at.IsZero() {
			at = m.CreatedAt
		}
		g.Nodes = append(g.Nodes, GraphNode{
			ID: graphMemoryID(m.ID), Kind: GraphKindMemory, Ref: m.ID,
			Label: graphMemoryLabel(m), Sub: graphMemorySub(m),
			Stale: m.StaleSince != nil, Pinned: graphPinned(m), UpdatedAt: graphTime(at),
		})
	}
	for _, row := range selSess {
		g.Nodes = append(g.Nodes, GraphNode{
			ID: graphSessionID(row.ID), Kind: GraphKindSession, Ref: row.ID,
			Label: graphSessionLabel(row), Sub: graphSessionSub(row), UpdatedAt: graphTime(row.LastActiveAt),
		})
	}
	for _, f := range selFiles {
		g.Nodes = append(g.Nodes, GraphNode{
			ID: graphFileID(f.key), Kind: GraphKindFile, Ref: f.path,
			Label: path.Base(f.path), Sub: f.path, Stale: f.changed, UpdatedAt: graphTime(f.latest),
		})
	}

	seen := map[GraphEdge]bool{}
	add := func(e GraphEdge) {
		if !seen[e] {
			seen[e] = true
			g.Edges = append(g.Edges, e)
		}
	}
	for _, m := range selMem {
		for _, ref := range memorySessionRefs(m) {
			if inSess[ref.id] {
				add(GraphEdge{From: graphMemoryID(m.ID), To: graphSessionID(ref.id), Relation: ref.relation})
			}
		}
	}
	for _, l := range links {
		key := graphFileKey(l.Path)
		if !inFile[key] {
			continue
		}
		switch {
		case l.SrcKind == LinkSrcMemory && inMem[l.SrcID]:
			add(GraphEdge{From: graphMemoryID(l.SrcID), To: graphFileID(key), Relation: l.Relation})
		case l.SrcKind == LinkSrcSession && inSess[l.SrcID]:
			add(GraphEdge{From: graphSessionID(l.SrcID), To: graphFileID(key), Relation: l.Relation})
		}
	}
	return g, nil
}

type graphSessionRef struct{ id, relation string }

// memorySessionRefs lists the sessions a memory came from or cites.
func memorySessionRefs(m *MemoryItem) []graphSessionRef {
	if m == nil {
		return nil
	}
	var out []graphSessionRef
	seen := map[string]bool{}
	add := func(id, rel string) {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, graphSessionRef{id: id, relation: rel})
	}
	add(m.SessionID, GraphRelationFrom)
	for _, t := range m.Tags {
		switch {
		case strings.HasPrefix(t, "session:"):
			add(strings.TrimPrefix(t, "session:"), GraphRelationFrom)
		case strings.HasPrefix(t, ProjectBriefSourceTag):
			add(strings.TrimPrefix(t, ProjectBriefSourceTag), GraphRelationCites)
		}
	}
	return out
}

// rankGraphMemories orders memories pinned first, then by confidence, then
// most recently updated.
func rankGraphMemories(mems []*MemoryItem) []*MemoryItem {
	out := make([]*MemoryItem, 0, len(mems))
	for _, m := range mems {
		if m != nil && m.ID != "" {
			out = append(out, m)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if pa, pb := graphPinned(a), graphPinned(b); pa != pb {
			return pa
		}
		if a.Confidence != b.Confidence {
			return a.Confidence > b.Confidence
		}
		if !a.UpdatedAt.Equal(b.UpdatedAt) {
			return a.UpdatedAt.After(b.UpdatedAt)
		}
		return a.ID < b.ID
	})
	return out
}

func graphPinned(m *MemoryItem) bool {
	for _, t := range m.Tags {
		if t == pinnedTag || t == ProjectBriefTag {
			return true
		}
	}
	return MemoryPinned(m.ID)
}

func graphMemoryID(id string) string  { return GraphKindMemory + ":" + id }
func graphSessionID(id string) string { return GraphKindSession + ":" + id }
func graphFileID(key string) string   { return GraphKindFile + ":" + key }

// graphFileKey folds a path's case and slashes so a file and its symbols
// (and Windows case variants) are one node.
func graphFileKey(p string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(p), `\`, "/"))
}

func graphTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

func graphMemoryLabel(m *MemoryItem) string {
	if k := strings.TrimSpace(m.Key); k != "" {
		return graphClip(k, 80)
	}
	return graphClip(m.Content, 80)
}

func graphMemorySub(m *MemoryItem) string {
	kind := strings.TrimSpace(m.Category)
	if kind == "" {
		kind = strings.TrimSpace(m.Scope)
	}
	body := graphClip(m.Content, 240)
	if kind == "" {
		return body
	}
	return kind + " · " + body
}

func graphSessionLabel(s *AgentSession) string {
	if t := strings.TrimSpace(s.Title); t != "" {
		return graphClip(t, 80)
	}
	h := strings.TrimSpace(s.Harness)
	if h == "" {
		h = "chat"
	}
	n := strings.TrimSpace(s.NativeID)
	if len(n) > 8 {
		n = n[:8]
	}
	if n == "" {
		return h
	}
	return h + " " + n
}

func graphSessionSub(s *AgentSession) string {
	h := strings.TrimSpace(s.Harness)
	sum := graphClip(s.Summary, 200)
	switch {
	case h == "":
		return sum
	case sum == "":
		return h
	}
	return h + " · " + sum
}

// graphClip collapses whitespace and cuts s to n runes.
func graphClip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n-1])) + "…"
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ---- project links ----

// ListProjectCodeLinks returns the project's links, newest first, at most
// limit (capped at maxGraphLinks). Memory/Session are not joined.
func (s *MemStore) ListProjectCodeLinks(ctx context.Context, projectID string, limit int) ([]CodeLinkHit, error) {
	_ = ctx
	if limit <= 0 || limit > maxGraphLinks {
		limit = maxGraphLinks
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	entities := map[string]*CodeEntity{}
	for _, e := range s.codeEntities {
		if e.ProjectID == projectID {
			entities[e.ID] = e
		}
	}
	type ranked struct {
		hit CodeLinkHit
		seq int64
	}
	var all []ranked
	for _, l := range s.codeLinks {
		if l.ProjectID != projectID {
			continue
		}
		if e := entities[l.EntityID]; e != nil {
			all = append(all, ranked{hit: s.hitLocked(e, l), seq: l.seq})
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].hit.LinkedAt.Equal(all[j].hit.LinkedAt) {
			return all[i].seq > all[j].seq
		}
		return all[i].hit.LinkedAt.After(all[j].hit.LinkedAt)
	})
	out := make([]CodeLinkHit, 0, minInt(len(all), limit))
	for _, r := range all {
		if len(out) >= limit {
			break
		}
		out = append(out, r.hit)
	}
	return out, nil
}

// ListProjectCodeLinks is the Postgres form (see MemStore).
func (s *PostgresStore) ListProjectCodeLinks(ctx context.Context, projectID string, limit int) ([]CodeLinkHit, error) {
	if !looksLikeUUID(projectID) {
		return []CodeLinkHit{}, nil
	}
	if limit <= 0 || limit > maxGraphLinks {
		limit = maxGraphLinks
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+codeLinkHitColumns+`
		   FROM entity_links l JOIN code_entities e ON e.id = l.entity_id
		  WHERE l.project_id = $1::uuid
		  ORDER BY l.created_at DESC
		  LIMIT $2`, projectID, limit)
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

var (
	_ ProjectLinkLister = (*MemStore)(nil)
	_ ProjectLinkLister = (*PostgresStore)(nil)
)
