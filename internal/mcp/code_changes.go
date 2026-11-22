package mcp

// code_changes.go — why and line_history: line-level change history. The
// desktop reports each hunk of an agent's file edit (Edit/MultiEdit/Write/
// apply_patch ...) with content anchors (hashes of the added lines and of
// the lines around them), its line range as a hint and short before/after
// snippets. Questions are asked by CONTENT: the text of the line (and its
// neighbours) as it is now, so inserted lines above it or edits made
// outside captured tool calls do not break the answer; the line number is
// only a fallback. The cloud adapter joins the chat that made the change
// and the memories (decisions) harvested from it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ApproximateNote explains approximate=true on a change.
const (
	ApproximateNote = "line numbers may have shifted since; check the snippet"
	OverwrittenNote = "a later edit rewrote some of these lines"
)

// CodeChange is one captured hunk with its range in today's line numbers.
type CodeChange struct {
	Path                       string
	SessionID                  string
	ToolCallID                 string
	StartLine, EndLine         int // current (shifted) range
	RecordedStart, RecordedEnd int // range when it was recorded
	RemovedLines, AddedLines   int
	Before, After              string
	ChangedAt                  time.Time
	Approximate                bool
	Overwritten                bool
	// Match says how the change was found: exact (the line and its
	// neighbours), context (the line, with a neighbour near the change),
	// hash (the line alone), line (line-number placement, approximate);
	// "" for a whole-file listing.
	Match   string
	Session *SessionRef
	// Memories are the decisions/memories harvested from that chat, the
	// ones naming the file first.
	Memories []*MemoryItem
}

// LineQuery selects lines of a file. Texts are the current text of the
// lines (preferred); Prev/Next the lines just outside them (nil when
// unknown); StartLine..EndLine the line numbers (0 = open), a hint when
// Texts are given and the selection otherwise.
type LineQuery struct {
	Texts              []string
	Prev, Next         *string
	StartLine, EndLine int
}

// CodeChangeReader returns the hunks of a file (suffix match) behind the
// queried lines, best match first and newest first within a match,
// filtered to what the caller may read.
type CodeChangeReader interface {
	CodeChangesForFile(ctx context.Context, projectID, path string, q LineQuery, limit int) ([]CodeChange, error)
}

func lineRange(a, b int) string {
	if b <= a {
		return fmt.Sprintf("%d", a)
	}
	return fmt.Sprintf("%d-%d", a, b)
}

// CodeChangeRow renders one change for agents and the app.
func CodeChangeRow(c CodeChange) map[string]any {
	row := map[string]any{
		"path": c.Path, "lines": lineRange(c.StartLine, c.EndLine),
		"start_line": c.StartLine, "end_line": c.EndLine,
		"change":      fmt.Sprintf("+%d -%d", c.AddedLines, c.RemovedLines),
		"session_id":  c.SessionID,
		"source":      "session:" + c.SessionID,
		"approximate": c.Approximate,
	}
	if c.Match != "" {
		row["match"] = c.Match
	}
	if c.RecordedStart != c.StartLine || c.RecordedEnd != c.EndLine {
		row["recorded_lines"] = lineRange(c.RecordedStart, c.RecordedEnd)
	}
	if c.Before != "" {
		row["before"] = c.Before
	}
	if c.After != "" {
		row["after"] = c.After
	}
	if c.ToolCallID != "" {
		row["tool_call_id"] = c.ToolCallID
	}
	if !c.ChangedAt.IsZero() {
		row["changed_at"] = c.ChangedAt.UTC().Format(time.RFC3339)
		row["date"] = dateOf(c.ChangedAt)
	}
	switch {
	case c.Overwritten:
		row["overwritten"] = true
		row["note"] = OverwrittenNote
	case c.Approximate:
		row["note"] = ApproximateNote
	}
	if c.Session != nil {
		if c.Session.Title != "" {
			row["title"] = c.Session.Title
		}
		if c.Session.Harness != "" {
			row["agent"] = c.Session.Harness
		}
	}
	decisions := make([]map[string]any, 0, len(c.Memories))
	for _, m := range c.Memories {
		if m == nil {
			continue
		}
		d := map[string]any{
			"id": m.ID, "key": m.Key, "content": m.Content, "level": m.Level,
			"status": publicMemoryWord(m.Status),
		}
		if m.Scope != "" {
			d["scope"] = m.Scope
		}
		if dt := itemDate(m); dt != "" {
			d["date"] = dt
		}
		if m.StaleSince != nil {
			d["stale"] = true
			d["note"] = StaleNote
		}
		decisions = append(decisions, d)
	}
	row["decisions"] = decisions
	return row
}

// CodeChangeRows renders changes in order.
func CodeChangeRows(changes []CodeChange) []map[string]any {
	out := make([]map[string]any, 0, len(changes))
	for _, c := range changes {
		out = append(out, CodeChangeRow(c))
	}
	return out
}

func (s *Server) codeChanges(ctx context.Context, projectID, path string, q LineQuery, limit int) ([]CodeChange, *RPCError) {
	r, ok := s.store.(CodeChangeReader)
	if !ok {
		return nil, &RPCError{Code: ErrInternal, Message: "line history needs Nexus cloud; code changes unavailable"}
	}
	changes, err := r.CodeChangesForFile(ctx, projectID, path, q, limit)
	if errors.Is(err, ErrCodeLinksUnavailable) {
		return nil, &RPCError{Code: ErrInternal, Message: "line history needs Nexus cloud; code changes unavailable"}
	}
	if err != nil {
		return nil, &RPCError{Code: ErrInternal, Message: "line history failed: " + err.Error()}
	}
	return changes, nil
}

type lineToolArgs struct {
	File      string   `json:"file"`
	Line      int      `json:"line"`
	LineText  *string  `json:"line_text"`
	Context   []string `json:"context"`
	Lines     []string `json:"lines"`
	StartLine int      `json:"start_line"`
	EndLine   int      `json:"end_line"`
	Limit     int      `json:"limit"`
	ProjectID string   `json:"project_id"`
}

func (s *Server) lineToolArgs(raw json.RawMessage, defLimit, maxLimit int) (lineToolArgs, *RPCError) {
	var a lineToolArgs
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
		a.Limit = defLimit
	}
	if a.Limit > maxLimit {
		a.Limit = maxLimit
	}
	return a, nil
}

// WhyQuery builds the query for one line: its text (nil when not given),
// its neighbours ([prev, next]; one entry is prev) and its line number.
func WhyQuery(text *string, context []string, line int) LineQuery {
	q := LineQuery{StartLine: line, EndLine: line}
	if text != nil {
		q.Texts = []string{*text}
		if len(context) > 0 {
			p := context[0]
			q.Prev = &p
		}
		if len(context) > 1 {
			n := context[1]
			q.Next = &n
		}
	}
	return q
}

// WhyResult is the why answer for one line (shared with the REST route).
func WhyResult(file string, line int, changes []CodeChange) map[string]any {
	out := map[string]any{"file": file, "changes": CodeChangeRows(changes), "count": len(changes)}
	if line > 0 {
		out["line"] = line
	}
	if len(changes) == 0 {
		out["note"] = "no captured agent edit covers this line; try related_to for decisions about the file"
	}
	return out
}

// handleWhy: the most recent captured edits covering one line, newest
// first, each with its chat and the decisions harvested from that chat.
func (s *Server) handleWhy(ctx context.Context, raw json.RawMessage) (any, *RPCError) {
	a, rpcErr := s.lineToolArgs(raw, 5, 20)
	if rpcErr != nil {
		return nil, rpcErr
	}
	if a.Line < 0 || (a.Line == 0 && a.LineText == nil) {
		return nil, invalidParams("missing required field: line_text (the line's current text) or line (1-based)")
	}
	if len(a.Context) > 2 {
		return nil, invalidParams("context takes at most 2 lines: [previous, next]")
	}
	changes, rpcErr := s.codeChanges(ctx, a.ProjectID, a.File, WhyQuery(a.LineText, a.Context, a.Line), a.Limit)
	if rpcErr != nil {
		return nil, rpcErr
	}
	return WhyResult(a.File, a.Line, changes), nil
}

// handleLineHistory: every captured edit in a line range (or the whole
// file), newest first.
func (s *Server) handleLineHistory(ctx context.Context, raw json.RawMessage) (any, *RPCError) {
	a, rpcErr := s.lineToolArgs(raw, 30, 100)
	if rpcErr != nil {
		return nil, rpcErr
	}
	if a.StartLine < 0 || a.EndLine < 0 || (a.EndLine > 0 && a.StartLine > a.EndLine) {
		return nil, invalidParams("invalid line range")
	}
	if len(a.Lines) > 500 {
		return nil, invalidParams("lines takes at most 500 lines")
	}
	q := LineQuery{Texts: a.Lines, StartLine: a.StartLine, EndLine: a.EndLine}
	if len(a.Lines) > 0 && a.StartLine > 0 && a.EndLine == 0 {
		q.EndLine = a.StartLine + len(a.Lines) - 1
	}
	changes, rpcErr := s.codeChanges(ctx, a.ProjectID, a.File, q, a.Limit)
	if rpcErr != nil {
		return nil, rpcErr
	}
	out := map[string]any{"file": a.File, "changes": CodeChangeRows(changes), "count": len(changes)}
	if a.StartLine > 0 {
		out["start_line"] = a.StartLine
	}
	if a.EndLine > 0 {
		out["end_line"] = a.EndLine
	}
	return out, nil
}
