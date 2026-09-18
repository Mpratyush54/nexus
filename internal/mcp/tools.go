package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// ReflectionHint is piggybacked onto every memory_search response (Layer 4
// passive extraction). It costs nothing — the agent is already reading the
// response — and the agent is free to ignore it. The Transcript Harvester
// (Layer 2) is the safety net that catches everything regardless.
const ReflectionHint = "If you've made decisions or learned facts in this session not shown above, call memory_write to record them."

// maxFileBytes caps file_read output (1 MiB, mirroring the daemon sandbox).
const maxFileBytes = 1 << 20

// MemoryItem is the MCP layer's view of a memory row: the fields tools
// read and write. It intentionally mirrors (a subset of) the store model's
// shape without importing the store package, so this package keeps building
// while internal/store evolves (pgx/pgvector wiring in progress).
type MemoryItem struct {
	ID             string
	ProjectID      string
	UserID         string
	SessionID      string
	Key            string
	Content        string
	ContextSnippet string
	Level          string // organization | project | personal | session | ephemeral
	Scope          string // fact | preference | decision | constraint | pattern | episode_summary
	Tags           []string
	Confidence     float32
	Status         string // PROPOSED | CONFIRMED | REJECTED | SUPERSEDED
	Source         string
	// Embedding carries the write-time vector (issue #76, 1536-dim,
	// L2-normalized) for the store adapter to persist to pgvector.
	Embedding []float32
}

// Episode is the MCP layer's view of an episode row (subset, see above).
type Episode struct {
	ID            string
	ProjectID     string
	Title         string
	EpisodeType   string // bug_fix | feature | refactor | incident | investigation | onboarding
	Trigger       string
	RootCause     string
	Resolution    string
	Verification  string
	Tags          []string
	FilesInvolved []string
	ErrorPatterns []string
	Status        string // OPEN | INVESTIGATING | RESOLVED | WONT_FIX
}

// Workspace is the MCP layer's view of a workspace row (subset, see above).
type Workspace struct {
	Branch    string
	CommitSHA string
	IsDirty   bool
	Path      string
}

// Project is the MCP layer's view of a project row (subset, see above).
type Project struct {
	DisplayName string
	FolderName  string
}

// Store is the narrow persistence surface the MCP server needs, expressed
// in the local DTO types above. Production wiring is a thin adapter mapping
// store.Store <-> Store (same method shapes, field-for-field copies); tests
// and local dev inject any in-memory fake. The MCP layer never imports a
// concrete store: widen this interface only when a new tool genuinely needs
// another operation.
type Store interface {
	SearchMemory(ctx context.Context, projectID string, query string, tags []string, limit int) ([]*MemoryItem, error)
	CreateMemoryItem(ctx context.Context, item *MemoryItem) error
	SearchEpisodes(ctx context.Context, projectID, errorPattern, query string, limit int) ([]*Episode, error)
	CreateEpisode(ctx context.Context, ep *Episode) error
	GetActiveWorkspace(ctx context.Context, projectID string) (*Workspace, error)
	GetProject(ctx context.Context, id string) (*Project, error)
}

// Tool describes one MCP tool for tools/list.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func schema(required []string, props map[string]any) map[string]any {
	return map[string]any{
		"type": "object", "properties": props, "required": required,
		"additionalProperties": true,
	}
}

// ListTools returns the Phase 1.4 tool catalog. Note the absence of any
// sampling tool — that omission is a locked decision, not a gap.
func ListTools() []Tool {
	str := map[string]any{"type": "string"}
	strArr := map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	num := map[string]any{"type": "integer"}

	return []Tool{
		{
			Name:        "memory_search",
			Description: "Search confirmed project memories. Returns an inference-ready XML context block plus a voluntary reflection hint.",
			InputSchema: schema([]string{"query"}, map[string]any{
				"query": str, "tags": strArr, "level": str, "limit": num, "project_id": str,
			}),
		},
		{
			Name:        "memory_write",
			Description: "Record a fact, decision, preference, constraint, or pattern as a PROPOSED memory. Content must be natural language, 20-2000 chars. level=personal requires user_id; level=session requires session_id.",
			InputSchema: schema([]string{"key", "content"}, map[string]any{
				"key": str, "content": str, "scope": str, "level": str,
				"tags": strArr, "context_snippet": str, "project_id": str,
				"user_id": str, "session_id": str,
			}),
		},
		{
			Name:        "memory_reflect",
			Description: "Voluntary — call when finishing a task or before ending a session to summarize decisions, facts, and preferences from this session. Never required; the daemon harvests transcripts regardless.",
			InputSchema: schema(nil, map[string]any{
				"summary": str,
				"memories": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"key": str, "content": str, "scope": str, "level": str, "tags": strArr,
						},
						"required": []string{"key", "content"},
					},
				},
			}),
		},
		{
			Name:        "episode_search",
			Description: "Find past bug/incident/feature episodes by error pattern, description, involved file, or status.",
			InputSchema: schema(nil, map[string]any{
				"query": str, "error_pattern": str, "file": str, "status": str,
				"project_id": str, "limit": num,
			}),
		},
		{
			Name:        "episode_report",
			Description: "Open a new episode for an active bug, incident, or investigation.",
			InputSchema: schema([]string{"title", "episode_type"}, map[string]any{
				"title": str, "episode_type": str, "trigger": str, "tags": strArr, "project_id": str,
			}),
		},
		{
			Name:        "workspace_info",
			Description: "Return the current project name, branch, commit, dirty status, and path.",
			InputSchema: schema(nil, map[string]any{}),
		},
		{
			Name:        "file_read",
			Description: "Read a file through the workspace sandbox (path must stay inside the workspace root, max 1MB). Reads are logged as events by the daemon.",
			InputSchema: schema([]string{"path"}, map[string]any{"path": str}),
		},
		{
			Name:        "file_write",
			Description: "Write a file through the workspace sandbox (path must stay inside the workspace root). Writes are logged as events by the daemon.",
			InputSchema: schema([]string{"path", "content"}, map[string]any{"path": str, "content": str}),
		},
	}
}

// CallTool dispatches one tools/call by name. Unknown names are
// ErrMethodNotFound (-32601); bad arguments are ErrInvalidParams (-32602).
func (s *Server) CallTool(ctx context.Context, name string, rawArgs json.RawMessage) (any, *RPCError) {
	if s.store == nil && name != "workspace_info" {
		return nil, &RPCError{Code: ErrInternal, Message: "no store configured"}
	}
	switch name {
	case "memory_search":
		return s.handleMemorySearch(ctx, rawArgs)
	case "memory_write":
		return s.handleMemoryWrite(ctx, rawArgs)
	case "memory_reflect":
		return s.handleMemoryReflect(ctx, rawArgs)
	case "episode_search":
		return s.handleEpisodeSearch(ctx, rawArgs)
	case "episode_report":
		return s.handleEpisodeReport(ctx, rawArgs)
	case "workspace_info":
		return s.handleWorkspaceInfo(ctx), nil
	case "file_read":
		return s.handleFileRead(rawArgs)
	case "file_write":
		return s.handleFileWrite(rawArgs)
	default:
		return nil, &RPCError{Code: ErrMethodNotFound, Message: "unknown tool: " + name}
	}
}

func invalidParams(format string, args ...any) *RPCError {
	return &RPCError{Code: ErrInvalidParams, Message: fmt.Sprintf(format, args...)}
}

func decodeArgs(raw json.RawMessage, v any) *RPCError {
	if len(raw) == 0 {
		return nil // all fields optional in this case
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return invalidParams("invalid arguments: %s", err.Error())
	}
	return nil
}

// ---------------------------------------------------------------------------
// memory_search
// ---------------------------------------------------------------------------

type memorySearchArgs struct {
	Query     string   `json:"query"`
	Tags      []string `json:"tags"`
	Level     string   `json:"level"`
	Limit     int      `json:"limit"`
	ProjectID string   `json:"project_id"`
}

func (s *Server) handleMemorySearch(ctx context.Context, raw json.RawMessage) (any, *RPCError) {
	var a memorySearchArgs
	if rpcErr := decodeArgs(raw, &a); rpcErr != nil {
		return nil, rpcErr
	}
	if strings.TrimSpace(a.Query) == "" {
		return nil, invalidParams("missing required field: query")
	}
	projectID := a.ProjectID
	if projectID == "" {
		projectID = s.cfg.ProjectID
	}
	limit := a.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}

	items, err := s.store.SearchMemory(ctx, projectID, a.Query, a.Tags, limit)
	if err != nil {
		return nil, &RPCError{Code: ErrInternal, Message: "search failed: " + err.Error()}
	}
	// Filter before dedupe (issue #135): dedupe keeps the most-specific
	// level per key, so deduping first would drop a key entirely when the
	// winner's level doesn't match the filter but a loser does.
	items = filterByLevel(items, a.Level)
	// Dedup same-key collisions (most-specific level wins, mirroring
	// context.ResolveOverrides) so one key never consumes budget twice.
	items = dedupeByKey(items)

	// Decay clock (plan §1.7, issue #119): serving an item counts as use.
	// Best-effort and never fatal — stores without the method are skipped.
	recordUse(ctx, s.store, items)

	budget := s.tokenBudget()
	contextXML := buildContextXMLBudgeted(s.cfg.ProjectName, s.cfg.Branch, items, budget)
	tokens := estimateTokens(contextXML)
	remaining := budget - len([]rune(contextXML))
	if remaining < 0 {
		remaining = 0
	}

	return map[string]any{
		"context":          contextXML,
		"token_count":      tokens,
		"budget_remaining": remaining,
		"items_included":   countXMLItems(contextXML),
		"reflection_hint":  ReflectionHint,
	}, nil
}

// recordUse bumps use_count/last_used_at on served rows so the confidence
// decay clock (plan §1.7) resets on real retrieval. It asserts the seam
// optionally: narrow stores (fakes, stubs) simply skip it.
func recordUse(ctx context.Context, st Store, items []*MemoryItem) {
	ru, ok := st.(interface {
		RecordMemoryUse(context.Context, string) error
	})
	if !ok {
		return
	}
	for _, it := range items {
		if it == nil || it.ID == "" {
			continue
		}
		_ = ru.RecordMemoryUse(ctx, it.ID)
	}
}

// mcpLevelRank mirrors context.LevelRank for DTOs: session wins over
// personal over project over organization; unknown levels never shadow.
func mcpLevelRank(level string) int {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "session":
		return 3
	case "personal":
		return 2
	case "project":
		return 1
	case "organization", "org":
		return 0
	default:
		return -1
	}
}

// dedupeByKey collapses same-key collisions keeping the most-specific level
// (ties: higher confidence, then first seen). Output order follows first-seen
// winning keys for determinism.
func dedupeByKey(items []*MemoryItem) []*MemoryItem {
	best := make(map[string]*MemoryItem, len(items))
	order := make([]string, 0, len(items))
	for _, it := range items {
		if it == nil {
			continue
		}
		cur, ok := best[it.Key]
		if !ok {
			best[it.Key] = it
			order = append(order, it.Key)
			continue
		}
		rNew, rCur := mcpLevelRank(it.Level), mcpLevelRank(cur.Level)
		if rNew > rCur || (rNew == rCur && it.Confidence > cur.Confidence) {
			best[it.Key] = it
		}
	}
	out := make([]*MemoryItem, 0, len(order))
	for _, k := range order {
		out = append(out, best[k])
	}
	return out
}

func filterByLevel(items []*MemoryItem, level string) []*MemoryItem {
	if strings.TrimSpace(level) == "" {
		return items
	}
	// "org" is an accepted alias for the organization tier (mcpLevelRank
	// already ranks it); match it explicitly instead of dropping everything.
	if strings.EqualFold(strings.TrimSpace(level), "org") {
		level = "organization"
	}
	out := make([]*MemoryItem, 0, len(items))
	for _, it := range items {
		if it != nil && strings.EqualFold(it.Level, level) {
			out = append(out, it)
		}
	}
	return out
}

// estimateTokens approximates token usage at ~4 chars per token.
func estimateTokens(s string) int { return len([]rune(s)) / 4 }

// xmlEscape escapes text for inclusion in the context XML block.
func xmlEscape(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	)
	return r.Replace(s)
}

// buildContextXML assembles memories into the inference-ready XML block from
// implementation-plan.md §1.6. Sections render in builder priority order
// (ephemeral, session, personal, project, organization — identical to
// context.AssembleXML so the swap stays drop-in).
// This is the local fallback for the future internal/context builder: keep
// the output shape identical so the swap is drop-in.
func buildContextXML(project, branch string, items []*MemoryItem) string {
	return buildContextXMLBudgeted(project, branch, items, 0)
}

// buildContextXMLBudgeted renders the same block capped to budget chars
// (issue #41: the effective agent budget). budget <= 0 means unlimited.
// Units that would overflow are dropped lowest-priority-first (organization
// goes before project, project before personal, and so on — issue #135);
// output always stays a well-formed project_memory block.
func buildContextXMLBudgeted(project, branch string, items []*MemoryItem, budget int) string {
	groups := map[string][]*MemoryItem{}
	for _, it := range items {
		lvl := strings.ToLower(it.Level)
		if lvl == "" {
			lvl = "project"
		}
		groups[lvl] = append(groups[lvl], it)
	}
	header := "<project_memory project=\"" + xmlEscape(project) + "\" branch=\"" + xmlEscape(branch) + "\">\n"
	footer := "</project_memory>"
	type section struct {
		open  string
		close string
		units []string
	}
	var sections []section
	for _, lvl := range []string{"ephemeral", "session", "personal", "project", "organization"} {
		mems := groups[lvl]
		if len(mems) == 0 {
			continue
		}
		units := make([]string, 0, len(mems))
		for _, it := range mems {
			scope := it.Scope
			if scope == "" {
				scope = "fact"
			}
			units = append(units, "<item key=\""+xmlEscape(it.Key)+"\" confidence=\""+fmt.Sprintf("%.2f", it.Confidence)+"\" scope=\""+xmlEscape(scope)+"\">\n    "+xmlEscape(it.Content)+"\n  </item>\n")
		}
		sections = append(sections, section{
			open: "<" + lvl + ">\n", close: "</" + lvl + ">\n", units: units,
		})
	}
	fits := func(bodyLen, add int) bool {
		if budget <= 0 {
			return true
		}
		return len(header)+bodyLen+add+len(footer) <= budget
	}
	var body strings.Builder
	for _, s := range sections {
		var sb strings.Builder
		kept := 0
		for _, u := range s.units {
			add := len(u)
			if kept == 0 {
				add += len(s.open) + len(s.close)
			}
			if !fits(body.Len()+sb.Len(), add) {
				continue
			}
			if kept == 0 {
				sb.WriteString(s.open)
			}
			sb.WriteString(u)
			kept++
		}
		if kept > 0 {
			sb.WriteString(s.close)
			body.WriteString(sb.String())
		}
	}
	return header + body.String() + footer
}

// countXMLItems counts rendered <item> units in a context block.
func countXMLItems(block string) int {
	return strings.Count(block, "<item ")
}

// ---------------------------------------------------------------------------
// memory_write
// ---------------------------------------------------------------------------

var validScopes = map[string]bool{
	"fact": true, "preference": true, "decision": true,
	"constraint": true, "pattern": true, "episode_summary": true,
}

var validLevels = map[string]bool{
	"organization": true, "project": true, "personal": true, "session": true,
	"ephemeral": true,
}

type memoryWriteArgs struct {
	Key            string   `json:"key"`
	Content        string   `json:"content"`
	Scope          string   `json:"scope"`
	Level          string   `json:"level"`
	Tags           []string `json:"tags"`
	ContextSnippet string   `json:"context_snippet"`
	ProjectID      string   `json:"project_id"`
	UserID         string   `json:"user_id"`
	SessionID      string   `json:"session_id"`
}

func (s *Server) handleMemoryWrite(ctx context.Context, raw json.RawMessage) (any, *RPCError) {
	var a memoryWriteArgs
	if rpcErr := decodeArgs(raw, &a); rpcErr != nil {
		return nil, rpcErr
	}
	if strings.TrimSpace(a.Key) == "" {
		return nil, invalidParams("missing required field: key")
	}
	n := utf8.RuneCountInString(a.Content)
	if n < 20 || n > 2000 {
		return nil, invalidParams("content must be 20-2000 chars, got %d", n)
	}
	scope := a.Scope
	if scope == "" {
		scope = "fact"
	}
	if !validScopes[strings.ToLower(scope)] {
		return nil, invalidParams("invalid scope %q: want fact|preference|decision|constraint|pattern|episode_summary", a.Scope)
	}
	level := a.Level
	if level == "" {
		level = "project"
	}
	if !validLevels[strings.ToLower(level)] {
		return nil, invalidParams("invalid level %q: want organization|project|personal|session|ephemeral", a.Level)
	}
	projectID := a.ProjectID
	if projectID == "" {
		projectID = s.cfg.ProjectID
	}
	normLevel := strings.ToLower(level)
	if normLevel == "personal" && strings.TrimSpace(a.UserID) == "" {
		return nil, invalidParams("level \"personal\" requires user_id")
	}
	if normLevel == "session" && strings.TrimSpace(a.SessionID) == "" {
		return nil, invalidParams("level \"session\" requires session_id")
	}

	item := &MemoryItem{
		ProjectID:      projectID,
		UserID:         strings.TrimSpace(a.UserID),
		SessionID:      strings.TrimSpace(a.SessionID),
		Key:            strings.TrimSpace(a.Key),
		Content:        a.Content,
		ContextSnippet: a.ContextSnippet,
		Level:          normLevel,
		Scope:          strings.ToLower(scope),
		Tags:           a.Tags,
		Confidence:     1.0,
		Status:         "PROPOSED",
		Source:         "agent:mcp",
		Embedding:      s.embedForWrite(strings.TrimSpace(a.Key), a.Content),
	}
	if err := s.store.CreateMemoryItem(ctx, item); err != nil {
		return nil, &RPCError{Code: ErrInternal, Message: "write failed: " + err.Error()}
	}
	return map[string]any{"id": item.ID, "status": item.Status}, nil
}

// ---------------------------------------------------------------------------
// memory_reflect (voluntary)
// ---------------------------------------------------------------------------

type reflectMemory struct {
	Key     string   `json:"key"`
	Content string   `json:"content"`
	Scope   string   `json:"scope"`
	Level   string   `json:"level"`
	Tags    []string `json:"tags"`
}

type memoryReflectArgs struct {
	Summary  string          `json:"summary"`
	Memories []reflectMemory `json:"memories"`
}

func (s *Server) handleMemoryReflect(ctx context.Context, raw json.RawMessage) (any, *RPCError) {
	var a memoryReflectArgs
	if rpcErr := decodeArgs(raw, &a); rpcErr != nil {
		return nil, rpcErr
	}
	recorded, skipped := 0, 0
	for _, m := range a.Memories {
		n := utf8.RuneCountInString(m.Content)
		if strings.TrimSpace(m.Key) == "" || n < 20 || n > 2000 {
			skipped++ // voluntary tool: skip malformed entries, never fail the call
			continue
		}
		scope := m.Scope
		if scope == "" || !validScopes[strings.ToLower(scope)] {
			scope = "fact"
		}
		level := m.Level
		if level == "" || !validLevels[strings.ToLower(level)] {
			level = "session" // reflections default to session scope (plan §1.4)
		}
		item := &MemoryItem{
			ProjectID:  s.cfg.ProjectID,
			Key:        strings.TrimSpace(m.Key),
			Content:    m.Content,
			Level:      strings.ToLower(level),
			Scope:      strings.ToLower(scope),
			Tags:       m.Tags,
			Confidence: 1.0,
			Status:     "PROPOSED",
			Source:     "agent:reflect",
			Embedding:  s.embedForWrite(strings.TrimSpace(m.Key), m.Content),
		}
		if a.Summary != "" {
			item.ContextSnippet = a.Summary
		}
		if err := s.store.CreateMemoryItem(ctx, item); err != nil {
			skipped++
			continue
		}
		recorded++
	}
	msg := fmt.Sprintf("Recorded %d reflection(s) as PROPOSED.", recorded)
	if recorded == 0 {
		msg = "No takeaways submitted — nothing recorded. Thanks for checking in; the daemon harvests session transcripts automatically."
	}
	return map[string]any{"recorded": recorded, "skipped": skipped, "message": msg}, nil
}

// ---------------------------------------------------------------------------
// episode_search / episode_report
// ---------------------------------------------------------------------------

type episodeSearchArgs struct {
	Query        string `json:"query"`
	ErrorPattern string `json:"error_pattern"`
	File         string `json:"file"`
	Status       string `json:"status"`
	ProjectID    string `json:"project_id"`
	Limit        int    `json:"limit"`
}

func (s *Server) handleEpisodeSearch(ctx context.Context, raw json.RawMessage) (any, *RPCError) {
	var a episodeSearchArgs
	if rpcErr := decodeArgs(raw, &a); rpcErr != nil {
		return nil, rpcErr
	}
	projectID := a.ProjectID
	if projectID == "" {
		projectID = s.cfg.ProjectID
	}
	limit := a.Limit
	if limit <= 0 {
		limit = 5
	}
	if limit > 50 {
		limit = 50
	}
	eps, err := s.store.SearchEpisodes(ctx, projectID, a.ErrorPattern, a.Query, limit)
	if err != nil {
		return nil, &RPCError{Code: ErrInternal, Message: "episode search failed: " + err.Error()}
	}
	out := make([]map[string]any, 0, len(eps))
	for _, ep := range eps {
		if a.File != "" && !fileInvolved(ep, a.File) {
			continue
		}
		if a.Status != "" && !strings.EqualFold(ep.Status, a.Status) {
			continue
		}
		out = append(out, map[string]any{
			"id": ep.ID, "title": ep.Title, "episode_type": ep.EpisodeType,
			"status": ep.Status, "trigger": ep.Trigger, "root_cause": ep.RootCause,
			"resolution": ep.Resolution, "verification": ep.Verification,
			"files_involved": ep.FilesInvolved, "error_patterns": ep.ErrorPatterns,
		})
	}
	return map[string]any{"episodes": out, "count": len(out)}, nil
}

func fileInvolved(ep *Episode, file string) bool {
	for _, f := range ep.FilesInvolved {
		if strings.Contains(strings.ToLower(f), strings.ToLower(file)) {
			return true
		}
	}
	return false
}

var validEpisodeTypes = map[string]bool{
	"bug_fix": true, "feature": true, "refactor": true,
	"incident": true, "investigation": true, "onboarding": true,
}

type episodeReportArgs struct {
	Title       string   `json:"title"`
	EpisodeType string   `json:"episode_type"`
	Trigger     string   `json:"trigger"`
	Tags        []string `json:"tags"`
	ProjectID   string   `json:"project_id"`
}

func (s *Server) handleEpisodeReport(ctx context.Context, raw json.RawMessage) (any, *RPCError) {
	var a episodeReportArgs
	if rpcErr := decodeArgs(raw, &a); rpcErr != nil {
		return nil, rpcErr
	}
	if strings.TrimSpace(a.Title) == "" {
		return nil, invalidParams("missing required field: title")
	}
	if !validEpisodeTypes[strings.ToLower(a.EpisodeType)] {
		return nil, invalidParams("invalid episode_type %q: want bug_fix|feature|refactor|incident|investigation|onboarding", a.EpisodeType)
	}
	projectID := a.ProjectID
	if projectID == "" {
		projectID = s.cfg.ProjectID
	}
	ep := &Episode{
		ProjectID:   projectID,
		Title:       strings.TrimSpace(a.Title),
		EpisodeType: strings.ToLower(a.EpisodeType),
		Trigger:     a.Trigger,
		Tags:        a.Tags,
		Status:      "OPEN",
	}
	if err := s.store.CreateEpisode(ctx, ep); err != nil {
		return nil, &RPCError{Code: ErrInternal, Message: "episode report failed: " + err.Error()}
	}
	return map[string]any{"id": ep.ID, "status": ep.Status}, nil
}

// ---------------------------------------------------------------------------
// workspace_info
// ---------------------------------------------------------------------------

func (s *Server) handleWorkspaceInfo(ctx context.Context) map[string]any {
	project := s.cfg.ProjectName
	branch, commit, dirty, path := s.cfg.Branch, s.cfg.CommitSHA, s.cfg.Dirty, s.cfg.WorkspacePath
	if s.store != nil && s.cfg.ProjectID != "" {
		if ws, err := s.store.GetActiveWorkspace(ctx, s.cfg.ProjectID); err == nil && ws != nil {
			if ws.Branch != "" {
				branch = ws.Branch
			}
			if ws.CommitSHA != "" {
				commit = ws.CommitSHA
			}
			dirty = ws.IsDirty
			if ws.Path != "" {
				path = ws.Path
			}
		}
		if p, err := s.store.GetProject(ctx, s.cfg.ProjectID); err == nil && p != nil {
			if p.DisplayName != "" {
				project = p.DisplayName
			} else if p.FolderName != "" {
				project = p.FolderName
			}
		}
	}
	return map[string]any{
		"project": project, "branch": branch, "commit": commit,
		"is_dirty": dirty, "path": path,
	}
}

// ---------------------------------------------------------------------------
// file_read / file_write (sandboxed; daemon proxy seam)
// ---------------------------------------------------------------------------

type filePathArgs struct {
	Path string `json:"path"`
}

type fileWriteArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func (s *Server) handleFileRead(raw json.RawMessage) (any, *RPCError) {
	var a filePathArgs
	if rpcErr := decodeArgs(raw, &a); rpcErr != nil {
		return nil, rpcErr
	}
	abs, err := secureJoin(s.cfg.WorkspacePath, a.Path)
	if err != nil {
		return nil, invalidParams("%s", err.Error())
	}
	if containsSecret(a.Path, nil) {
		return nil, invalidParams("file %q matches secret pattern", a.Path)
	}
	st, err := os.Stat(abs)
	if err != nil {
		return nil, &RPCError{Code: ErrInternal, Message: "read failed: " + err.Error()}
	}
	if st.Size() > maxFileBytes {
		return nil, invalidParams("file exceeds 1MB limit: %s", a.Path)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, &RPCError{Code: ErrInternal, Message: "read failed: " + err.Error()}
	}
	if containsSecret(a.Path, data) {
		return nil, invalidParams("file %q matches secret pattern", a.Path)
	}
	s.logFileAccess("read", a.Path, len(data))
	return map[string]any{"path": a.Path, "size": len(data), "content": string(data)}, nil
}

func (s *Server) handleFileWrite(raw json.RawMessage) (any, *RPCError) {
	var a fileWriteArgs
	if rpcErr := decodeArgs(raw, &a); rpcErr != nil {
		return nil, rpcErr
	}
	abs, err := secureJoin(s.cfg.WorkspacePath, a.Path)
	if err != nil {
		return nil, invalidParams("%s", err.Error())
	}
	if len(a.Content) > maxFileBytes {
		return nil, invalidParams("content exceeds 1MB limit: %s", a.Path)
	}
	if containsSecret(a.Path, []byte(a.Content)) {
		return nil, invalidParams("file %q matches secret pattern", a.Path)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return nil, &RPCError{Code: ErrInternal, Message: "write failed: " + err.Error()}
	}
	if err := os.WriteFile(abs, []byte(a.Content), 0o644); err != nil {
		return nil, &RPCError{Code: ErrInternal, Message: "write failed: " + err.Error()}
	}
	s.logFileAccess("write", a.Path, len(a.Content))
	return map[string]any{"path": a.Path, "bytes_written": len(a.Content)}, nil
}
