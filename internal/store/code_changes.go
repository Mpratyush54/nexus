// code_changes.go — line-level change history (migration 047).
//
// A CodeChange is one hunk of an agent's edit to a file. It is anchored on
// CONTENT: AddedHashes are the LineHash of each added line in order, and
// ContextBefore/ContextAfter the hashes of up to 2 unchanged lines above
// and below the hunk in the file right after the edit. MatchCodeChanges
// finds the changes behind a line by its text (and its neighbours' text),
// so inserted lines above it or edits made outside captured tool calls do
// not lose it.
//
// The changed line range (StartLine..EndLine, 1-based, inclusive) is kept
// as a hint, with short before/after snippets (at most 3 lines each) and
// the file's content hash when the desktop located the hunk (AfterHash).
// Every hunk of one report shares one coordinate snapshot (same AfterHash
// and ObservedAt). PlaceCodeChanges replays the snapshots of a file in
// order and shifts older ranges by the line deltas of newer ones; it is
// the fallback when no content matches. A placed range is Approximate when
// a newer hunk rewrote part of it, when its file was never hashed, or when
// the file's current hash differs from the newest snapshot (the file
// changed outside captured edits).
package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

const (
	// MaxCodeChanges caps the hunks one code-links report may carry.
	MaxCodeChanges = 1000
	// maxFileChanges bounds the hunks replayed for one file (newest kept).
	maxFileChanges = 2000
	// MaxSnippetLines and maxSnippetLineBytes bound before/after snippets.
	MaxSnippetLines     = 3
	maxSnippetLineBytes = 240
	maxToolCallIDBytes  = 200
)

// CodeChange is one stored hunk.
type CodeChange struct {
	ID            string    `json:"id"`
	ProjectID     string    `json:"project_id"`
	EntityID      string    `json:"entity_id"`
	SessionID     string    `json:"session_id"`
	ToolCallID    string    `json:"tool_call_id"`
	HunkIndex     int       `json:"hunk_index"`
	StartLine     int       `json:"start_line"`
	EndLine       int       `json:"end_line"`
	RemovedLines  int       `json:"removed_lines"`
	AddedLines    int       `json:"added_lines"`
	BeforeSnippet string    `json:"before_snippet,omitempty"`
	AfterSnippet  string    `json:"after_snippet,omitempty"`
	AfterHash     string    `json:"after_hash,omitempty"`
	AddedHashes   []string  `json:"added_hashes,omitempty"`
	ContextBefore []string  `json:"context_before,omitempty"`
	ContextAfter  []string  `json:"context_after,omitempty"`
	ChangedAt     time.Time `json:"changed_at"`
	ObservedAt    time.Time `json:"observed_at"`
}

// CodeChangeHit is a stored hunk joined with its file entity and session.
// Session is the joined row; callers check read access themselves.
type CodeChangeHit struct {
	CodeChange
	Path        string
	CurrentHash string
	Session     *AgentSession
}

// PlacedCodeChange is a hunk with its range in the file's latest known
// line numbers.
type PlacedCodeChange struct {
	CodeChangeHit
	CurStart, CurEnd int
	// Overwritten: a newer hunk replaced some of these lines.
	Overwritten bool
	// Approximate: the range may be off (see the package comment).
	Approximate bool
}

// CodeChangeStore is implemented by MemStore and PostgresStore.
type CodeChangeStore interface {
	AddCodeChanges(ctx context.Context, projectID string, changes []*CodeChange) (int, error)
	ListCodeChangesByPath(ctx context.Context, projectID, path string) ([]CodeChangeHit, error)
}

var (
	_ CodeChangeStore = (*MemStore)(nil)
	_ CodeChangeStore = (*PostgresStore)(nil)
)

// ErrBadCodeChange is returned for a hunk with an invalid range or ids.
var ErrBadCodeChange = errors.New("invalid code change")

// ClipSnippet keeps at most MaxSnippetLines lines of s, each cut to a
// bounded length on a rune boundary; trailing blank lines are dropped.
func ClipSnippet(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > MaxSnippetLines {
		lines = lines[:MaxSnippetLines]
	}
	for i, l := range lines {
		if len(l) > maxSnippetLineBytes {
			cut := maxSnippetLineBytes
			for cut > 0 && !utf8.RuneStart(l[cut]) {
				cut--
			}
			lines[i] = l[:cut] + "…"
		}
	}
	return strings.Join(lines, "\n")
}

// normalizeCodeChange validates c and fills defaults. A missing tool call
// id is derived from the hunk's content so a resend stays idempotent.
func normalizeCodeChange(c *CodeChange) error {
	if c == nil {
		return ErrBadCodeChange
	}
	c.ProjectID = strings.TrimSpace(c.ProjectID)
	c.EntityID = strings.TrimSpace(c.EntityID)
	c.SessionID = strings.TrimSpace(c.SessionID)
	c.ToolCallID = strings.TrimSpace(c.ToolCallID)
	c.AfterHash = normalizeHash(c.AfterHash)
	c.BeforeSnippet = ClipSnippet(c.BeforeSnippet)
	c.AfterSnippet = ClipSnippet(c.AfterSnippet)
	if c.ProjectID == "" || c.EntityID == "" || c.SessionID == "" || c.StartLine < 1 || c.HunkIndex < 0 ||
		c.RemovedLines < 0 || c.AddedLines < 0 {
		return ErrBadCodeChange
	}
	if c.EndLine < c.StartLine {
		c.EndLine = c.StartLine
	}
	if len(c.ToolCallID) > maxToolCallIDBytes {
		c.ToolCallID = c.ToolCallID[:maxToolCallIDBytes]
	}
	if c.ToolCallID == "" {
		sum := sha256.Sum256([]byte(c.BeforeSnippet + "\x00" + c.AfterSnippet))
		c.ToolCallID = "h:" + hex.EncodeToString(sum[:8])
	}
	if c.ChangedAt.IsZero() {
		c.ChangedAt = time.Now()
	}
	c.ChangedAt = c.ChangedAt.UTC()
	c.AddedHashes = cleanLineHashes(c.AddedHashes, MaxAddedHashes, false)
	c.ContextBefore = cleanLineHashes(c.ContextBefore, MaxContextHashes, true)
	c.ContextAfter = cleanLineHashes(c.ContextAfter, MaxContextHashes, false)
	return nil
}

// snapshotKey groups the hunks located against one read of a file.
func snapshotKey(c *CodeChange) string {
	if c.AfterHash != "" {
		return "h:" + c.AfterHash
	}
	return "t:" + c.ObservedAt.UTC().Format(time.RFC3339Nano)
}

// PlaceCodeChanges maps every hunk to the latest known line numbers of its
// file (see the package comment). hits may mix files; each file is replayed
// on its own. The result is in replay order (oldest snapshot first).
func PlaceCodeChanges(hits []CodeChangeHit) []PlacedCodeChange {
	byEntity := map[string][]CodeChangeHit{}
	var entities []string
	for _, h := range hits {
		if _, ok := byEntity[h.EntityID]; !ok {
			entities = append(entities, h.EntityID)
		}
		byEntity[h.EntityID] = append(byEntity[h.EntityID], h)
	}
	var out []PlacedCodeChange
	for _, id := range entities {
		out = append(out, placeFileChanges(byEntity[id])...)
	}
	return out
}

type replaySpan struct {
	oldStart, oldEnd int // replaced lines in the previous snapshot; oldEnd < oldStart for a pure insertion
	newStart, newEnd int // lines in the new snapshot; newEnd < newStart for a pure deletion
	delta            int
}

func placeFileChanges(hits []CodeChangeHit) []PlacedCodeChange {
	sort.SliceStable(hits, func(i, j int) bool {
		a, b := hits[i], hits[j]
		if !a.ObservedAt.Equal(b.ObservedAt) {
			return a.ObservedAt.Before(b.ObservedAt)
		}
		if ka, kb := snapshotKey(&a.CodeChange), snapshotKey(&b.CodeChange); ka != kb {
			return ka < kb
		}
		return a.StartLine < b.StartLine
	})
	placed := make([]PlacedCodeChange, 0, len(hits))
	lastKey, lastHash := "", ""
	for i := 0; i < len(hits); {
		key := snapshotKey(&hits[i].CodeChange)
		j := i
		for j < len(hits) && snapshotKey(&hits[j].CodeChange) == key {
			j++
		}
		group := hits[i:j]
		// Old coordinates of each hunk of this snapshot: undo the deltas of
		// the hunks above it.
		spans := make([]replaySpan, 0, len(group))
		cum := 0
		for _, h := range group {
			newStart := h.StartLine
			newEnd := newStart + h.AddedLines - 1
			oldStart := newStart - cum
			spans = append(spans, replaySpan{
				oldStart: oldStart, oldEnd: oldStart + h.RemovedLines - 1,
				newStart: newStart, newEnd: newEnd, delta: h.AddedLines - h.RemovedLines,
			})
			cum += h.AddedLines - h.RemovedLines
		}
		if key != lastKey {
			for k := range placed {
				p := &placed[k]
				start, sHit := mapLine(p.CurStart, spans, false)
				end, eHit := mapLine(p.CurEnd, spans, true)
				if sHit || eHit || spansInside(p.CurStart, p.CurEnd, spans) {
					p.Overwritten = true
				}
				if start < 1 {
					start = 1
				}
				if end < start {
					end = start
				}
				p.CurStart, p.CurEnd = start, end
			}
		}
		for _, h := range group {
			placed = append(placed, PlacedCodeChange{CodeChangeHit: h, CurStart: h.StartLine, CurEnd: h.EndLine})
		}
		lastKey, lastHash = key, hits[i].AfterHash
		i = j
	}
	current := ""
	if len(hits) > 0 {
		current = normalizeHash(hits[len(hits)-1].CurrentHash)
	}
	outside := current != "" && lastHash != "" && current != lastHash
	for k := range placed {
		p := &placed[k]
		p.Approximate = p.Overwritten || outside || p.AfterHash == ""
	}
	return placed
}

// mapLine maps line l of the previous snapshot into the new one. hit is
// true when l was replaced by a span; then it maps to the span's first
// (or, for end, last) new line.
func mapLine(l int, spans []replaySpan, isEnd bool) (int, bool) {
	shift := 0
	for _, sp := range spans {
		switch {
		case sp.oldEnd < sp.oldStart: // pure insertion before oldStart
			if l >= sp.oldStart {
				shift += sp.delta
			}
		case l > sp.oldEnd:
			shift += sp.delta
		case l >= sp.oldStart:
			target := sp.newStart
			if isEnd && sp.newEnd >= sp.newStart {
				target = sp.newEnd
			}
			return target, true
		}
	}
	return l + shift, false
}

// spansInside reports whether a replaced span lies strictly inside
// start..end (both ends survive but lines between were rewritten).
func spansInside(start, end int, spans []replaySpan) bool {
	for _, sp := range spans {
		if sp.oldEnd >= sp.oldStart && sp.oldStart > start && sp.oldEnd < end {
			return true
		}
	}
	return false
}

// ---- MemStore ----

// AddCodeChanges stores hunks (insert, or on a resend of the same tool
// call hunk update its range, snippets and hash; observed_at moves only
// when the hash changed). It returns how many it stored; invalid hunks and
// hunks of entities outside the project are skipped.
func (s *MemStore) AddCodeChanges(ctx context.Context, projectID string, changes []*CodeChange) (int, error) {
	_ = ctx
	projectID = strings.TrimSpace(projectID)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureCodeMapsLocked()
	n := 0
	now := time.Now().UTC()
	for _, c := range changes {
		if c == nil {
			continue
		}
		c.ProjectID = projectID
		if normalizeCodeChange(c) != nil {
			continue
		}
		ok := false
		for _, e := range s.codeEntities {
			if e.ID == c.EntityID && e.ProjectID == projectID {
				ok = true
				break
			}
		}
		if row := s.agentSessions[c.SessionID]; !ok || row == nil || row.ProjectID != projectID {
			continue
		}
		var cur *CodeChange
		for _, x := range s.codeChanges {
			if x.EntityID == c.EntityID && x.SessionID == c.SessionID && x.ToolCallID == c.ToolCallID && x.HunkIndex == c.HunkIndex {
				cur = x
				break
			}
		}
		if cur == nil {
			c.ID = newID("cchg")
			c.ObservedAt = now
			cp := *c
			s.codeChanges = append(s.codeChanges, &cp)
		} else {
			observed := cur.ObservedAt
			if cur.AfterHash != c.AfterHash {
				observed = now
			}
			id := cur.ID
			*cur = *c
			cur.ID, cur.ObservedAt = id, observed
			c.ID, c.ObservedAt = id, observed
		}
		n++
	}
	return n, nil
}

// ListCodeChangesByPath returns the hunks of the file entities whose path
// matches path (FilePathMatches), with the session row joined, oldest
// first (at most maxFileChanges per call, newest kept).
func (s *MemStore) ListCodeChangesByPath(ctx context.Context, projectID, path string) ([]CodeChangeHit, error) {
	_ = ctx
	if NormalizeFilePath(path) == "" || strings.TrimSpace(projectID) == "" {
		return []CodeChangeHit{}, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	entities := map[string]*CodeEntity{}
	for _, e := range s.codeEntities {
		if e.ProjectID == projectID && e.Kind == CodeKindFile && FilePathMatches(e.Path, path) {
			entities[e.ID] = e
		}
	}
	out := []CodeChangeHit{}
	for _, c := range s.codeChanges {
		e := entities[c.EntityID]
		if e == nil || c.ProjectID != projectID {
			continue
		}
		h := CodeChangeHit{CodeChange: *c, Path: e.Path, CurrentHash: e.ContentHash}
		if row := s.agentSessions[c.SessionID]; row != nil {
			cp := *row
			h.Session = &cp
		}
		out = append(out, h)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ObservedAt.Before(out[j].ObservedAt) })
	if len(out) > maxFileChanges {
		out = out[len(out)-maxFileChanges:]
	}
	return out, nil
}

// ---- PostgresStore ----

// AddCodeChanges is the Postgres form (see MemStore). The entity and the
// session must belong to projectID (INSERT … SELECT … JOIN).
func (s *PostgresStore) AddCodeChanges(ctx context.Context, projectID string, changes []*CodeChange) (int, error) {
	if !looksLikeUUID(projectID) {
		return 0, nil
	}
	batch := &pgx.Batch{}
	var queued []*CodeChange
	for _, c := range changes {
		if c == nil {
			continue
		}
		c.ProjectID = projectID
		if normalizeCodeChange(c) != nil || !looksLikeUUID(c.EntityID) || !looksLikeUUID(c.SessionID) {
			continue
		}
		batch.Queue(
			`INSERT INTO code_changes (project_id, entity_id, session_id, tool_call_id, hunk_index,
			        start_line, end_line, removed_lines, added_lines, before_snippet, after_snippet,
			        after_hash, changed_at, added_hashes, context_before, context_after)
			 SELECT $1::uuid, e.id, a.id, $4::text, $5::int, $6::int, $7::int, $8::int, $9::int,
			        $10::text, $11::text, NULLIF($12::text, ''), $13::timestamptz,
			        $14::text[], $15::text[], $16::text[]
			   FROM code_entities e JOIN agent_sessions a ON a.id = $3::uuid AND a.project_id = $1::uuid
			  WHERE e.id = $2::uuid AND e.project_id = $1::uuid
			 ON CONFLICT (entity_id, session_id, tool_call_id, hunk_index) DO UPDATE SET
			    start_line = EXCLUDED.start_line, end_line = EXCLUDED.end_line,
			    removed_lines = EXCLUDED.removed_lines, added_lines = EXCLUDED.added_lines,
			    before_snippet = EXCLUDED.before_snippet, after_snippet = EXCLUDED.after_snippet,
			    changed_at = EXCLUDED.changed_at,
			    added_hashes = EXCLUDED.added_hashes, context_before = EXCLUDED.context_before,
			    context_after = EXCLUDED.context_after,
			    observed_at = CASE WHEN code_changes.after_hash IS DISTINCT FROM EXCLUDED.after_hash
			                       THEN now() ELSE code_changes.observed_at END,
			    after_hash = EXCLUDED.after_hash
			 RETURNING id::text, observed_at`,
			projectID, c.EntityID, c.SessionID, c.ToolCallID, c.HunkIndex,
			c.StartLine, c.EndLine, c.RemovedLines, c.AddedLines, c.BeforeSnippet, c.AfterSnippet,
			c.AfterHash, c.ChangedAt, nonNilStrings(c.AddedHashes), nonNilStrings(c.ContextBefore), nonNilStrings(c.ContextAfter))
		queued = append(queued, c)
	}
	if len(queued) == 0 {
		return 0, nil
	}
	br := s.pool.SendBatch(ctx, batch)
	defer br.Close()
	n := 0
	for _, c := range queued {
		err := br.QueryRow().Scan(&c.ID, &c.ObservedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// ListCodeChangesByPath is the Postgres form (see MemStore). A basename
// LIKE prefilters in SQL; the exact path rule runs in Go.
func (s *PostgresStore) ListCodeChangesByPath(ctx context.Context, projectID, path string) ([]CodeChangeHit, error) {
	norm := NormalizeFilePath(path)
	if norm == "" || !looksLikeUUID(projectID) {
		return []CodeChangeHit{}, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT c.id::text, c.entity_id::text, c.session_id::text, c.tool_call_id, c.hunk_index,
		        c.start_line, c.end_line, c.removed_lines, c.added_lines, c.before_snippet, c.after_snippet,
		        COALESCE(c.after_hash, ''), c.changed_at, c.observed_at,
		        c.added_hashes, c.context_before, c.context_after,
		        e.path, COALESCE(e.content_hash, ''),
		        COALESCE(s.title, ''), COALESCE(s.harness, ''), COALESCE(s.native_id, ''),
		        s.started_at, s.last_active_at, COALESCE(s.owner_user_id::text, ''), COALESCE(s.visibility, '')
		   FROM code_changes c
		   JOIN code_entities e ON e.id = c.entity_id
		   JOIN agent_sessions s ON s.id = c.session_id
		  WHERE c.project_id = $1::uuid AND e.kind = 'file'
		    AND lower(replace(e.path, '\', '/')) LIKE '%' || $2
		  ORDER BY c.observed_at DESC
		  LIMIT $3`,
		projectID, fileBaseName(norm), maxFileChanges*2)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CodeChangeHit{}
	for rows.Next() {
		var h CodeChangeHit
		var title, harness, native, owner, vis string
		var started, last *time.Time
		if err := rows.Scan(&h.ID, &h.EntityID, &h.SessionID, &h.ToolCallID, &h.HunkIndex,
			&h.StartLine, &h.EndLine, &h.RemovedLines, &h.AddedLines, &h.BeforeSnippet, &h.AfterSnippet,
			&h.AfterHash, &h.ChangedAt, &h.ObservedAt, &h.AddedHashes, &h.ContextBefore, &h.ContextAfter,
			&h.Path, &h.CurrentHash,
			&title, &harness, &native, &started, &last, &owner, &vis); err != nil {
			return nil, err
		}
		if !FilePathMatches(h.Path, path) {
			continue
		}
		h.ProjectID = projectID
		sess := &AgentSession{ID: h.SessionID, ProjectID: projectID, Title: title, Harness: harness,
			NativeID: native, OwnerUserID: owner, Visibility: vis}
		if started != nil {
			sess.StartedAt = *started
		}
		if last != nil {
			sess.LastActiveAt = *last
		}
		h.Session = sess
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) > maxFileChanges {
		out = out[:maxFileChanges]
	}
	// Oldest first, as MemStore returns them.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// nonNilStrings makes a nil slice an empty array for Postgres.
func nonNilStrings(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}
