package mcp

// code_links.go — related_to and file_history: memories and chats linked to
// code. Links come from agents' file edits captured by the desktop
// (relation "touched") and from harvested memories' files_affected
// ("decided_about"). The cloud adapter implements CodeLinkReader; stores
// without it fall back to the files_affected match (related_to,
// memory_search) or report the feature unavailable (file_history).

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
)

// ErrCodeLinksUnavailable is returned by a CodeLinkReader whose backing
// store keeps no code links.
var ErrCodeLinksUnavailable = errors.New("code links unavailable")

// CodeChangedNote is shown on links whose file changed after the link.
const CodeChangedNote = "file changed since this was recorded; verify before relying"

// SessionRef is the chat side of a code link.
type SessionRef struct {
	ID           string
	Title        string
	Harness      string
	StartedAt    time.Time
	LastActiveAt time.Time
}

// CodeLink is one memory or session linked to a file. Exactly one of
// Memory and Session is set.
type CodeLink struct {
	Path        string
	Relation    string // touched | decided_about | mentions
	Source      string // tool_call | llm
	Confidence  float32
	LinkedAt    time.Time
	CodeChanged bool // the file's hash differs from the one seen when linked
	Memory      *MemoryItem
	Session     *SessionRef
}

// CodeLinkReader returns the links for a file (suffix match), newest first,
// already filtered to what the caller may read.
type CodeLinkReader interface {
	CodeLinksForFile(ctx context.Context, projectID, path string, limit int) ([]CodeLink, error)
}

func (s *Server) codeLinks(ctx context.Context, projectID, path string, limit int) ([]CodeLink, error) {
	r, ok := s.store.(CodeLinkReader)
	if !ok {
		return nil, ErrCodeLinksUnavailable
	}
	return r.CodeLinksForFile(ctx, projectID, path, limit)
}

// linkedMemories returns the memories linked to path (newest link first,
// deduplicated), or ErrCodeLinksUnavailable.
func (s *Server) linkedMemories(ctx context.Context, projectID, path string, tags []string, limit int) ([]*MemoryItem, error) {
	links, err := s.codeLinks(ctx, projectID, path, limit*4)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := make([]*MemoryItem, 0, limit)
	for _, l := range links {
		if l.Memory == nil || seen[memoryIdentity(l.Memory)] || !memoryHasAnyTag(l.Memory, tags) {
			continue
		}
		seen[memoryIdentity(l.Memory)] = true
		out = append(out, l.Memory)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

type fileToolArgs struct {
	File      string `json:"file"`
	Limit     int    `json:"limit"`
	ProjectID string `json:"project_id"`
}

func (s *Server) fileToolArgs(raw json.RawMessage) (fileToolArgs, *RPCError) {
	var a fileToolArgs
	if rpcErr := decodeArgs(raw, &a); rpcErr != nil {
		return a, rpcErr
	}
	a.File = strings.TrimSpace(a.File)
	if a.File == "" {
		return a, invalidParams("missing required field: file")
	}
	if strings.TrimSpace(a.ProjectID) == "" {
		a.ProjectID = s.cfg.ProjectID
	}
	if a.Limit <= 0 {
		a.Limit = 20
	}
	if a.Limit > 50 {
		a.Limit = 50
	}
	return a, nil
}

func dateOf(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02")
}

func addUnique(list []string, v string) []string {
	if v == "" {
		return list
	}
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

// handleRelatedTo: decisions/memories and chats linked to a file, newest
// first. Each memory carries its relation(s), source chat, date and stale
// flag; each chat its title and dates.
func (s *Server) handleRelatedTo(ctx context.Context, raw json.RawMessage) (any, *RPCError) {
	a, rpcErr := s.fileToolArgs(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	links, err := s.codeLinks(ctx, a.ProjectID, a.File, a.Limit*4)
	if err != nil && !errors.Is(err, ErrCodeLinksUnavailable) {
		return nil, &RPCError{Code: ErrInternal, Message: "related_to failed: " + err.Error()}
	}

	type entry struct {
		row       map[string]any
		at        time.Time
		relations []string
		paths     []string
	}
	var order []string
	entries := map[string]*entry{}
	add := func(key string, at time.Time, relation, path string, build func() map[string]any) *entry {
		e := entries[key]
		if e == nil {
			e = &entry{row: build(), at: at}
			entries[key] = e
			order = append(order, key)
		}
		if at.After(e.at) {
			e.at = at
		}
		e.relations = addUnique(e.relations, relation)
		e.paths = addUnique(e.paths, path)
		return e
	}
	for _, l := range links {
		switch {
		case l.Memory != nil:
			m := l.Memory
			e := add("m:"+memoryIdentity(m), l.LinkedAt, l.Relation, l.Path, func() map[string]any {
				row := map[string]any{
					"kind": "memory", "id": m.ID, "key": m.Key, "content": m.Content,
					"level": m.Level, "status": publicMemoryWord(m.Status), "link_source": l.Source,
				}
				if d := itemDate(m); d != "" {
					row["date"] = d
				}
				if src := itemSource(m); src != "" {
					row["source"] = src
				}
				return row
			})
			if m.StaleSince != nil {
				e.row["stale"] = true
				e.row["note"] = StaleNote
			} else if l.CodeChanged {
				e.row["stale"] = true
				e.row["note"] = CodeChangedNote
			}
		case l.Session != nil:
			sess := l.Session
			add("s:"+sess.ID, l.LinkedAt, l.Relation, l.Path, func() map[string]any {
				row := map[string]any{"kind": "session", "session_id": sess.ID, "source": "session:" + sess.ID}
				if sess.Title != "" {
					row["title"] = sess.Title
				}
				if sess.Harness != "" {
					row["harness"] = sess.Harness
				}
				if d := dateOf(sess.LastActiveAt); d != "" {
					row["date"] = d
				}
				return row
			})
		}
	}
	// Memories whose files_affected name the file but that carry no link
	// (written before links existed, or by a store without them).
	linked := len(order)
	mems, ferr := s.searchByFilesAffected(ctx, a.ProjectID, a.File, nil, a.Limit)
	if ferr != nil {
		return nil, &RPCError{Code: ErrInternal, Message: "related_to failed: " + ferr.Error()}
	}
	{
		for _, m := range mems {
			m := m
			at := m.UpdatedAt
			if at.IsZero() {
				at = m.CreatedAt
			}
			e := add("m:"+memoryIdentity(m), at, "decided_about", a.File, func() map[string]any {
				row := map[string]any{
					"kind": "memory", "id": m.ID, "key": m.Key, "content": m.Content,
					"level": m.Level, "status": publicMemoryWord(m.Status), "link_source": "files_affected",
				}
				if d := itemDate(m); d != "" {
					row["date"] = d
				}
				if src := itemSource(m); src != "" {
					row["source"] = src
				}
				return row
			})
			if m.StaleSince != nil {
				e.row["stale"] = true
				e.row["note"] = StaleNote
			}
		}
	}
	match := "links"
	switch {
	case linked == 0:
		match = "files_affected"
	case len(order) > linked:
		match = "links+files_affected"
	}
	sort.SliceStable(order, func(i, j int) bool { return entries[order[i]].at.After(entries[order[j]].at) })
	items := make([]map[string]any, 0, len(order))
	for _, k := range order {
		if len(items) >= a.Limit {
			break
		}
		e := entries[k]
		e.row["relation"] = strings.Join(e.relations, ",")
		e.row["paths"] = e.paths
		if !e.at.IsZero() {
			e.row["linked_at"] = e.at.UTC().Format(time.RFC3339)
		}
		items = append(items, e.row)
	}
	return map[string]any{"file": a.File, "items": items, "count": len(items), "match": match}, nil
}

// handleFileHistory: the chats that touched a file, newest first.
func (s *Server) handleFileHistory(ctx context.Context, raw json.RawMessage) (any, *RPCError) {
	a, rpcErr := s.fileToolArgs(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	links, err := s.codeLinks(ctx, a.ProjectID, a.File, a.Limit*8)
	if errors.Is(err, ErrCodeLinksUnavailable) {
		return nil, &RPCError{Code: ErrInternal, Message: "file history needs Nexus cloud; code links unavailable"}
	}
	if err != nil {
		return nil, &RPCError{Code: ErrInternal, Message: "file_history failed: " + err.Error()}
	}
	type touch struct {
		ref         *SessionRef
		first, last time.Time
		paths       []string
		relations   []string
	}
	var order []string
	byID := map[string]*touch{}
	for _, l := range links {
		if l.Session == nil || l.Session.ID == "" {
			continue
		}
		t := byID[l.Session.ID]
		if t == nil {
			t = &touch{ref: l.Session, first: l.LinkedAt, last: l.LinkedAt}
			byID[l.Session.ID] = t
			order = append(order, l.Session.ID)
		}
		if l.LinkedAt.Before(t.first) {
			t.first = l.LinkedAt
		}
		if l.LinkedAt.After(t.last) {
			t.last = l.LinkedAt
		}
		t.paths = addUnique(t.paths, l.Path)
		t.relations = addUnique(t.relations, l.Relation)
	}
	sort.SliceStable(order, func(i, j int) bool { return byID[order[i]].last.After(byID[order[j]].last) })
	sessions := make([]map[string]any, 0, len(order))
	for _, id := range order {
		if len(sessions) >= a.Limit {
			break
		}
		t := byID[id]
		row := map[string]any{
			"session_id": id, "source": "session:" + id,
			"relation": strings.Join(t.relations, ","), "paths": t.paths,
			"touched_at": t.first.UTC().Format(time.RFC3339),
		}
		if t.ref.Title != "" {
			row["title"] = t.ref.Title
		}
		if t.ref.Harness != "" {
			row["harness"] = t.ref.Harness
		}
		if d := dateOf(t.ref.StartedAt); d != "" {
			row["started"] = d
		}
		if d := dateOf(t.ref.LastActiveAt); d != "" {
			row["last_active"] = d
		}
		sessions = append(sessions, row)
	}
	return map[string]any{"file": a.File, "sessions": sessions, "count": len(sessions)}, nil
}
