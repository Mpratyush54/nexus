package server

// code_changes.go — line-level change history (migration 047).
//
//   - POST /v1/projects/{id}/code-links accepts optional changes: the hunks
//     of the session's edits ({path, tool_call_id, hunk_index, start_line,
//     end_line, removed_lines, added_lines, before_snippet, after_snippet,
//     after_hash, at}). Snippets are at most 3 lines; whole files are never
//     sent.
//   - GET /v1/projects/{id}/why?path=&line= (or start_line/end_line for a
//     range) answers for the app what the MCP tools why / line_history
//     answer for agents, with the same visibility rules as related_to:
//     chats need read access, memories are filtered as the caller.

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"central-memory/internal/mcp"
	"central-memory/internal/store"
)

const (
	// maxChangeMemories caps the decisions shown per change.
	maxChangeMemories = 5
	// sessionMemoryScan is how many of a chat's memories are considered.
	sessionMemoryScan = 50
)

type codeChangeInput struct {
	Path          string    `json:"path"`
	ToolCallID    string    `json:"tool_call_id,omitempty"`
	HunkIndex     int       `json:"hunk_index,omitempty"`
	StartLine     int       `json:"start_line"`
	EndLine       int       `json:"end_line"`
	RemovedLines  int       `json:"removed_lines,omitempty"`
	AddedLines    int       `json:"added_lines,omitempty"`
	BeforeSnippet string    `json:"before_snippet,omitempty"`
	AfterSnippet  string    `json:"after_snippet,omitempty"`
	AfterHash     string    `json:"after_hash,omitempty"`
	At            time.Time `json:"at,omitempty"`
}

// recordCodeChanges stores the hunks of one session's edits. Each hunk's
// file gets an entity and a touched link (when the report did not already
// carry one). Stores without code changes skip them.
func (s *Server) recordCodeChanges(ctx context.Context, cl store.CodeLinkStore, projectID, sessionID string, in []codeChangeInput) (int, int, error) {
	if len(in) == 0 {
		return 0, 0, nil
	}
	ccs, ok := s.Store.(store.CodeChangeStore)
	if !ok {
		return 0, len(in), nil
	}
	entities := map[string]*store.CodeEntity{}
	var batch []*store.CodeChange
	skipped := 0
	for _, c := range in {
		path, ok := store.CleanCodePath(c.Path)
		if !ok || c.StartLine < 1 {
			skipped++
			continue
		}
		key := strings.ToLower(path)
		e := entities[key]
		if e == nil {
			var err error
			e, err = cl.UpsertCodeEntity(ctx, projectID, store.CodeKindFile, path, "")
			if err != nil {
				skipped++
				continue
			}
			entities[key] = e
			if err := cl.AddEntityLink(ctx, &store.EntityLink{
				ProjectID: projectID, EntityID: e.ID, SrcKind: store.LinkSrcSession, SrcID: sessionID,
				Relation: store.RelationTouched, Source: store.LinkSourceToolCall, Confidence: 1,
			}); err != nil && s.Log != nil {
				s.Log.Printf("code changes: link session=%s path=%s: %v", sessionID, path, err)
			}
		}
		batch = append(batch, &store.CodeChange{
			ProjectID: projectID, EntityID: e.ID, SessionID: sessionID,
			ToolCallID: c.ToolCallID, HunkIndex: c.HunkIndex,
			StartLine: c.StartLine, EndLine: c.EndLine,
			RemovedLines: c.RemovedLines, AddedLines: c.AddedLines,
			BeforeSnippet: c.BeforeSnippet, AfterSnippet: c.AfterSnippet,
			AfterHash: c.AfterHash, ChangedAt: c.At,
		})
	}
	n, err := ccs.AddCodeChanges(ctx, projectID, batch)
	if err != nil {
		return n, skipped, err
	}
	return n, skipped + len(batch) - n, nil
}

// CodeChangesForFile serves why / line_history (mcp.CodeChangeReader).
func (a agentMCPStore) CodeChangesForFile(ctx context.Context, projectID, path string, startLine, endLine, limit int) ([]mcp.CodeChange, error) {
	ccs, ok := a.Store.(store.CodeChangeStore)
	if !ok {
		return nil, mcp.ErrCodeLinksUnavailable
	}
	if limit <= 0 {
		limit = 20
	}
	hits, err := ccs.ListCodeChangesByPath(ctx, projectID, path)
	if err != nil {
		return nil, err
	}
	placed := store.PlaceCodeChanges(hits)
	sort.SliceStable(placed, func(i, j int) bool {
		x, y := placed[i], placed[j]
		if !x.ChangedAt.Equal(y.ChangedAt) {
			return x.ChangedAt.After(y.ChangedAt)
		}
		if !x.ObservedAt.Equal(y.ObservedAt) {
			return x.ObservedAt.After(y.ObservedAt)
		}
		return x.HunkIndex > y.HunkIndex
	})
	cs, hasCloud := a.Store.(store.AgentCloudStore)
	readable := map[string]bool{}
	canRead := func(id string) bool {
		if v, ok := readable[id]; ok {
			return v
		}
		ok := false
		if hasCloud {
			ok, _ = cs.CanReadAgentSession(ctx, a.UserID, id)
		}
		readable[id] = ok
		return ok
	}
	out := make([]mcp.CodeChange, 0, limit)
	for _, p := range placed {
		if len(out) >= limit {
			break
		}
		if startLine > 0 && p.CurEnd < startLine {
			continue
		}
		if endLine > 0 && p.CurStart > endLine {
			continue
		}
		if !canRead(p.SessionID) {
			continue
		}
		c := mcp.CodeChange{
			Path: p.Path, SessionID: p.SessionID, ToolCallID: p.ToolCallID,
			StartLine: p.CurStart, EndLine: p.CurEnd, RecordedStart: p.StartLine, RecordedEnd: p.EndLine,
			RemovedLines: p.RemovedLines, AddedLines: p.AddedLines,
			Before: p.BeforeSnippet, After: p.AfterSnippet, ChangedAt: p.ChangedAt,
			Approximate: p.Approximate, Overwritten: p.Overwritten,
		}
		if p.Session != nil {
			c.Session = &mcp.SessionRef{ID: p.Session.ID, Title: p.Session.Title, Harness: p.Session.Harness,
				StartedAt: p.Session.StartedAt, LastActiveAt: p.Session.LastActiveAt}
		}
		out = append(out, c)
	}
	// The decisions behind each change: memories harvested from its chat,
	// the ones naming the file first.
	memsBySession := map[string][]*store.MemoryItem{}
	for i := range out {
		sid := out[i].SessionID
		mems, seen := memsBySession[sid]
		if !seen {
			mems, _ = a.Store.SearchMemory(a.viewerCtx(ctx), projectID, "", []string{"session:" + sid}, sessionMemoryScan)
			memsBySession[sid] = mems
		}
		out[i].Memories = rankChangeMemories(mems, out[i].Path)
	}
	return out, nil
}

// rankChangeMemories orders a chat's memories for one changed file: the
// ones naming the file, then decisions, then the rest (each newest first),
// at most maxChangeMemories.
func rankChangeMemories(mems []*store.MemoryItem, path string) []*mcp.MemoryItem {
	type ranked struct {
		m    *store.MemoryItem
		rank int
	}
	var list []ranked
	for _, m := range mems {
		if m == nil || m.SupersededBy != "" {
			continue
		}
		r := 2
		switch {
		case memoryNamesPath(m, path):
			r = 0
		case strings.EqualFold(m.Scope, "decision") || strings.EqualFold(m.Category, "decision"):
			r = 1
		}
		list = append(list, ranked{m, r})
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].rank != list[j].rank {
			return list[i].rank < list[j].rank
		}
		return list[i].m.CreatedAt.After(list[j].m.CreatedAt)
	})
	out := make([]*mcp.MemoryItem, 0, maxChangeMemories)
	for _, r := range list {
		if len(out) >= maxChangeMemories {
			break
		}
		out = append(out, storeToMCPMemory(r.m))
	}
	return out
}

// handleWhy: GET /v1/projects/{id}/why?path=&line= — the captured edits
// covering a line (or start_line..end_line), newest first.
func (s *Server) handleWhy(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.PathValue("id"))
	if !s.authorizePermission(w, r, projectID, store.PermMemoryRead) {
		return
	}
	q := r.URL.Query()
	path := strings.TrimSpace(q.Get("path"))
	if path == "" {
		writeError(w, http.StatusBadRequest, "path required")
		return
	}
	num := func(k string) (int, bool) {
		v := strings.TrimSpace(q.Get(k))
		if v == "" {
			return 0, true
		}
		n, err := strconv.Atoi(v)
		return n, err == nil && n >= 0
	}
	line, ok1 := num("line")
	start, ok2 := num("start_line")
	end, ok3 := num("end_line")
	limit, ok4 := num("limit")
	if !ok1 || !ok2 || !ok3 || !ok4 || (end > 0 && start > end) {
		writeError(w, http.StatusBadRequest, "invalid line")
		return
	}
	if line > 0 {
		start, end = line, line
	}
	if limit <= 0 {
		limit = 20
		if line > 0 {
			limit = 5
		}
	}
	if limit > 100 {
		limit = 100
	}
	if _, ok := s.Store.(store.CodeChangeStore); !ok {
		writeError(w, http.StatusNotImplemented, "line history unavailable")
		return
	}
	adapter := agentMCPStore{Store: s.Store, UserID: authSubject(r)}
	changes, err := adapter.CodeChangesForFile(r.Context(), projectID, path, start, end, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "why failed: "+err.Error())
		return
	}
	var out map[string]any
	if line > 0 {
		out = mcp.WhyResult(path, line, changes)
	} else {
		out = map[string]any{"file": path, "changes": mcp.CodeChangeRows(changes), "count": len(changes)}
	}
	writeJSON(w, http.StatusOK, out)
}
