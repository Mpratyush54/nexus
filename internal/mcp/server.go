// Package mcp exposes central-memory to pull-model agents (Claude Code,
// OpenCode) over MCP-style JSON-RPC on stdio (issue #7, plan §1.4).
//
// Tools (plan §1.4 table):
//
//	memory_search   {query, tags?, level?, limit?} → Context Builder XML +
//	                piggyback reflection_hint + token_count/budget_remaining
//	memory_write    {key, content, scope?, level?, tags?, context_snippet?} →
//	                PROPOSED item id (content validated 20–2000 chars)
//	memory_reflect  {summary?, items?} → voluntary, never forced; writes items
//	                as PROPOSED, acks empty calls
//	episode_search  {query?, error_pattern?, file?, status?} → matching episodes
//	episode_report  {title, episode_type, trigger?, tags?} → opens an episode
//	workspace_info  {} → {project, branch, commit, is_dirty, path}
//	file_read       {path} → proxied through the daemon sandbox
//	file_write      {path, content} → proxied through the daemon sandbox
//
// DECOUPLING: this package never edits internal/context, internal/store or
// internal/daemon. It reuses their types through narrow interfaces:
//
//   - store.SearchQuery / store.RankedMemory / store.MemoryItem via the
//     MemoryBackend interface (production wiring adapts any store.Querier —
//     e.g. *store.DB — through QuerierBackend, which calls store.Search).
//   - builder.ContextInput / builder.BuildXML for memory_search rendering;
//     store.MemoryItem is mapped to builder.Item at this boundary.
//   - daemon.ReadFileSandboxed / daemon.WriteFileSandboxed / daemon.Git* via
//     the FileBackend / WorkspaceProvider interfaces (DaemonFileProxy,
//     DaemonWorkspaceProvider in backend.go), so sandbox enforcement is
//     reused, never reimplemented.
//
// Transport (protocol.go): newline-delimited JSON-RPC 2.0 on stdio, stdlib
// encoding/json only — no new dependencies (see ADR-007).
package mcp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	builder "central-memory/internal/context"
	"central-memory/internal/daemon"
	"central-memory/internal/store"
)

// JSON-RPC 2.0 error codes used by Server.Call.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternal       = -32603
)

// CallError is a JSON-RPC error returned by Server.Call.
type CallError struct {
	Code    int
	Message string
}

func (e *CallError) Error() string { return fmt.Sprintf("mcp: code %d: %s", e.Code, e.Message) }

func invalidParams(format string, args ...any) *CallError {
	return &CallError{Code: CodeInvalidParams, Message: fmt.Sprintf(format, args...)}
}

func internalErr(err error) *CallError {
	return &CallError{Code: CodeInternal, Message: err.Error()}
}

// ReflectionHint piggybacks on every memory_search response (plan §1.4,
// Layer 4): zero-cost and zero-disruption — the agent is already reading the
// response. The agent may act on it or ignore it; the transcript harvester
// (Layer 2) is the safety net that catches everything regardless.
const ReflectionHint = "If you've made decisions or learned facts in this session not shown above, call memory_write to record them."

// Tool names (plan §1.4).
const (
	ToolMemorySearch  = "memory_search"
	ToolMemoryWrite   = "memory_write"
	ToolMemoryReflect = "memory_reflect"
	ToolEpisodeSearch = "episode_search"
	ToolEpisodeReport = "episode_report"
	ToolWorkspaceInfo = "workspace_info"
	ToolFileRead      = "file_read"
	ToolFileWrite     = "file_write"
)

// Content guards from the memory_items CHECK constraint (plan §1.1):
// content must be 20–2000 chars. Counted in runes, not bytes.
const (
	MinContentChars = 20
	MaxContentChars = 2000
)

// ---------------------------------------------------------------------------
// Narrow interfaces (reuse without ownership)
// ---------------------------------------------------------------------------

// MemoryBackend runs a semantic memory search over store types. Production
// wiring uses QuerierBackend around a store.Querier; tests use fakes or
// InMemoryMemoryStore.
type MemoryBackend interface {
	SearchMemories(ctx context.Context, q store.SearchQuery) ([]store.RankedMemory, error)
}

// QuerierBackend adapts any store.Querier (e.g. *store.DB, which already
// asserts Querier) to MemoryBackend by calling store.Search. No driver
// import here — the pool owner keeps that.
type QuerierBackend struct {
	Q store.Querier
}

// SearchMemories implements MemoryBackend.
func (b QuerierBackend) SearchMemories(ctx context.Context, q store.SearchQuery) ([]store.RankedMemory, error) {
	if b.Q == nil {
		return nil, errors.New("mcp: no store querier configured")
	}
	return store.Search(ctx, b.Q, q)
}

// MemoryWriteInput is one memory fact proposed by an agent.
type MemoryWriteInput struct {
	Key            string
	Content        string
	Scope          string // default "fact"
	Level          string // default "project"
	Tags           []string
	ContextSnippet string
}

// WrittenMemory is the stored receipt for a memory_write.
type WrittenMemory struct {
	ID     string
	Status string // always store.StatusProposed on this path
}

// MemoryWriter persists agent-proposed memories. Production wiring will
// INSERT with status PROPOSED (issue #8 server); tests and local mode use
// InMemoryMemoryStore.
type MemoryWriter interface {
	WriteMemory(ctx context.Context, in MemoryWriteInput) (WrittenMemory, error)
}

// Episode mirrors the episodes row the Context Builder and agents need
// (plan §1.1). The full episodes engine (embeddings, event linking) is
// issue #11's; this struct carries the searchable/renderable subset so the
// MCP layer compiles and serves against it today.
type Episode struct {
	ID            string
	ProjectID     string
	Title         string
	Type          string // bug_fix|feature|refactor|incident|investigation|onboarding
	Trigger       string
	Investigation string
	RootCause     string
	Resolution    string
	Verification  string
	Status        string // OPEN|INVESTIGATING|RESOLVED|WONT_FIX
	Tags          []string
	FilesInvolved []string
	ErrorPatterns []string
}

// EpisodeFilter scopes an episode_search (plan §2.4: by error pattern,
// file involvement, semantic query, status).
type EpisodeFilter struct {
	Query        string
	ErrorPattern string
	File         string
	Status       string
}

// EpisodeReportInput opens an episode for an active bug/investigation.
type EpisodeReportInput struct {
	Title   string
	Type    string
	Trigger string
	Tags    []string
}

// EpisodeBackend searches and opens episodes. Production wiring will hit
// Postgres (issue #11); tests use InMemoryEpisodeStore.
type EpisodeBackend interface {
	SearchEpisodes(ctx context.Context, f EpisodeFilter) ([]Episode, error)
	ReportEpisode(ctx context.Context, in EpisodeReportInput) (Episode, error)
}

// WorkspaceInfo is the workspace_info result (plan §1.4 exit criterion 1).
type WorkspaceInfo struct {
	Project string
	Branch  string
	Commit  string
	IsDirty bool
	Path    string
}

// WorkspaceProvider reports live workspace state. Production wiring uses
// DaemonWorkspaceProvider (daemon.Git* helpers); tests use
// StaticWorkspaceProvider.
type WorkspaceProvider interface {
	WorkspaceInfo(ctx context.Context) (WorkspaceInfo, error)
}

// FileBackend proxies file_read/file_write through the daemon sandbox
// (plan §1.4 exit criterion 2: reads go through the daemon, not direct FS
// from the agent's perspective, and are silently logged as events by the
// interceptor once the daemon core wires it).
type FileBackend interface {
	ReadFile(ctx context.Context, path string) (string, error)
	WriteFile(ctx context.Context, path, content string) error
}

// ---------------------------------------------------------------------------
// Budget resolution (issue #41, plan §§1.6/4.1)
//
// Per-agent context budgets live in the agent registry (store/agents.go:
// seed budgets, per-project config overrides). The MCP layer consumes them
// through the narrow BudgetResolver seam below — production wiring passes a
// *store.AgentStore (which already implements BudgetForProject), tests pass
// a fake or nothing. No new import was needed: this package already imports
// internal/store and store imports nothing from mcp (no cycle).
// ---------------------------------------------------------------------------

// BudgetResolver resolves the Context Builder char budget for a named agent
// inside a project (project config override wins, else the agent default,
// else the builder default). *store.AgentStore implements it.
type BudgetResolver interface {
	BudgetForProject(ctx context.Context, projectID, agentName string) (int, error)
}

// compile-time proof the production registry satisfies the narrow seam.
var _ BudgetResolver = (*store.AgentStore)(nil)

// ---------------------------------------------------------------------------
// Validation (pure, unit-tested)
// ---------------------------------------------------------------------------

var validLevels = map[string]bool{
	store.LevelOrganization: true,
	store.LevelProject:      true,
	store.LevelPersonal:     true,
	store.LevelSession:      true,
}

var validScopes = map[string]bool{
	"fact": true, "preference": true, "decision": true,
	"constraint": true, "pattern": true, "episode_summary": true,
}

var validEpisodeTypes = map[string]bool{
	"bug_fix": true, "feature": true, "refactor": true,
	"incident": true, "investigation": true, "onboarding": true,
}

var validEpisodeStatus = map[string]bool{
	"OPEN": true, "INVESTIGATING": true, "RESOLVED": true, "WONT_FIX": true,
}

// ValidateMemoryWrite enforces the plan §1.1 content CHECK (20–2000 chars)
// plus level/scope allowlists. Levels/scopes must already be normalized
// (lowercase, defaults applied).
func ValidateMemoryWrite(in MemoryWriteInput) error {
	if strings.TrimSpace(in.Key) == "" {
		return errors.New("key is required")
	}
	if n := len([]rune(in.Content)); n < MinContentChars || n > MaxContentChars {
		return fmt.Errorf("content must be %d-%d chars, got %d", MinContentChars, MaxContentChars, n)
	}
	if !validLevels[in.Level] {
		return fmt.Errorf("unknown level %q (want organization|project|personal|session)", in.Level)
	}
	if !validScopes[in.Scope] {
		return fmt.Errorf("unknown scope %q (want fact|preference|decision|constraint|pattern|episode_summary)", in.Scope)
	}
	return nil
}

// ValidateEpisodeReport requires a title and a known episode_type
// (plan §1.1 CHECK). Trigger may be empty for a just-opened investigation.
func ValidateEpisodeReport(in EpisodeReportInput) error {
	if strings.TrimSpace(in.Title) == "" {
		return errors.New("title is required")
	}
	if !validEpisodeTypes[in.Type] {
		return fmt.Errorf("unknown episode_type %q (want bug_fix|feature|refactor|incident|investigation|onboarding)", in.Type)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Server
// ---------------------------------------------------------------------------

// Config anchors the server to one project/workspace.
type Config struct {
	ProjectID   string
	ProjectName string
	Branch      string
	// AgentName selects the per-agent context budget (issue #41, plan
	// §4.1): when set, budget resolution consults Budgets (project-scoped)
	// then the store seed default for the named agent, instead of the
	// static BudgetChars below. Empty means "no agent" — BudgetChars (or
	// the builder default) applies, preserving pre-#41 behaviour.
	AgentName string
	// Budgets resolves the project-scoped budget for AgentName
	// (store.BudgetForProject via *store.AgentStore in production). Nil
	// skips project overrides and falls through to the seed default. A
	// resolver error also falls through — a budget must never fail a
	// search.
	Budgets BudgetResolver
	// BudgetChars is the static memory_search XML cap, honoured only when
	// no seed budget applies (AgentName empty or unknown); <=0 selects
	// builder.DefaultBudgetChars (4000, plan §1.6).
	BudgetChars int
	// Embed maps a search query to a vector; nil selects HashEmbed
	// (deterministic stdlib fallback — real LLM embeddings are injected
	// by the server/processor wiring, issues #8/#10).
	Embed func(query string) []float32
}

// Server dispatches MCP tool calls to narrow backends.
type Server struct {
	cfg       Config
	Memories  MemoryBackend
	Writer    MemoryWriter
	Episodes  EpisodeBackend
	Workspace WorkspaceProvider
	Files     FileBackend
}

// New wires a Server. Backends may be nil only for tools the caller never
// invokes — Call fails cleanly (CodeInternal) instead of panicking.
func New(cfg Config, mem MemoryBackend, w MemoryWriter, ep EpisodeBackend, ws WorkspaceProvider, f FileBackend) *Server {
	return &Server{cfg: cfg, Memories: mem, Writer: w, Episodes: ep, Workspace: ws, Files: f}
}

func (s *Server) budget(ctx context.Context) int {
	if s != nil {
		if agent := strings.TrimSpace(s.cfg.AgentName); agent != "" {
			// Project-scoped override first (per-project config wins).
			if s.cfg.Budgets != nil && strings.TrimSpace(s.cfg.ProjectID) != "" {
				if b, err := s.cfg.Budgets.BudgetForProject(ctx, s.cfg.ProjectID, agent); err == nil && b > 0 {
					return b
				}
			}
			// Seed default for known agents (claude/opencode 10k, copilot
			// 8k, cursor/windsurf 6k). Unknown names skip this so an
			// explicit BudgetChars is honoured instead of being shadowed
			// by the seed fallback (which equals the builder default).
			if isSeedAgent(agent) {
				return store.BudgetFor(agent)
			}
		}
		if s.cfg.BudgetChars > 0 {
			return s.cfg.BudgetChars
		}
	}
	return builder.DefaultBudgetChars
}

// isSeedAgent reports whether name matches a registry seed row
// (case/whitespace-insensitive). Local scan over the exported seed table —
// no store API change needed (store/agents.go untouched per issue #41).
func isSeedAgent(name string) bool {
	norm := store.NormalizeAgentName(name)
	for _, a := range store.SeedAgents {
		if store.NormalizeAgentName(a.Name) == norm {
			return true
		}
	}
	return false
}

func (s *Server) embed() func(string) []float32 {
	if s != nil && s.cfg.Embed != nil {
		return s.cfg.Embed
	}
	return HashEmbed
}

// Call dispatches one tool by name. Unknown names yield CodeMethodNotFound.
func (s *Server) Call(ctx context.Context, name string, args map[string]any) (any, *CallError) {
	if args == nil {
		args = map[string]any{}
	}
	switch name {
	case ToolMemorySearch:
		return s.handleMemorySearch(ctx, args)
	case ToolMemoryWrite:
		return s.handleMemoryWrite(ctx, args)
	case ToolMemoryReflect:
		return s.handleMemoryReflect(ctx, args)
	case ToolEpisodeSearch:
		return s.handleEpisodeSearch(ctx, args)
	case ToolEpisodeReport:
		return s.handleEpisodeReport(ctx, args)
	case ToolWorkspaceInfo:
		return s.handleWorkspaceInfo(ctx, args)
	case ToolFileRead:
		return s.handleFileRead(ctx, args)
	case ToolFileWrite:
		return s.handleFileWrite(ctx, args)
	default:
		return nil, &CallError{Code: CodeMethodNotFound, Message: fmt.Sprintf("unknown tool %q", name)}
	}
}

// ---------------------------------------------------------------------------
// Tool handlers
// ---------------------------------------------------------------------------

func (s *Server) handleMemorySearch(ctx context.Context, args map[string]any) (any, *CallError) {
	if s.Memories == nil {
		return nil, internalErr(errors.New("memory_search: no memory backend configured"))
	}
	query := strings.TrimSpace(strArg(args, "query"))
	if query == "" {
		return nil, invalidParams("memory_search: query is required")
	}
	tags := strSliceArg(args, "tags")
	level := strings.ToLower(strings.TrimSpace(strArg(args, "level")))
	if level != "" && !validLevels[level] {
		return nil, invalidParams("memory_search: unknown level %q (want organization|project|personal|session)", level)
	}
	sq := store.SearchQuery{
		ProjectID:      s.cfg.ProjectID,
		QueryEmbedding: s.embed()(query),
		Tags:           tags,
		Level:          level,
		Limit:          intArg(args, "limit"),
	}
	ranked, err := s.Memories.SearchMemories(ctx, sq)
	if err != nil {
		return nil, internalErr(fmt.Errorf("memory_search: %w", err))
	}
	in := builder.ContextInput{ProjectName: s.cfg.ProjectName, Branch: s.cfg.Branch}
	for _, r := range ranked {
		it := builder.Item{
			Key:        r.Item.Key,
			Content:    r.Item.Content,
			Level:      r.Item.Level,
			Scope:      r.Item.Scope,
			Confidence: r.Item.Confidence,
			DecidedBy:  r.Item.Source,
			LastUsedAt: r.Item.LastUsedAt,
		}
		switch r.Item.Level {
		case builder.LevelSession:
			in.Session = append(in.Session, it)
		case builder.LevelPersonal:
			in.Personal = append(in.Personal, it)
		case builder.LevelOrganization:
			in.Organization = append(in.Organization, it)
		default:
			in.Project = append(in.Project, it)
		}
	}
	xmlOut, stats := builder.BuildXML(in, s.budget(ctx))
	return map[string]any{
		"context":          xmlOut,
		"token_count":      stats.CharsUsed,
		"budget_remaining": stats.BudgetRemaining,
		"items_included":   stats.ItemsIncluded,
		"reflection_hint":  ReflectionHint,
	}, nil
}

func (s *Server) handleMemoryWrite(ctx context.Context, args map[string]any) (any, *CallError) {
	if s.Writer == nil {
		return nil, internalErr(errors.New("memory_write: no memory writer configured"))
	}
	in, cerr := parseMemoryWriteInput(args)
	if cerr != nil {
		return nil, cerr
	}
	w, err := s.Writer.WriteMemory(ctx, in)
	if err != nil {
		return nil, internalErr(fmt.Errorf("memory_write: %w", err))
	}
	return map[string]any{"id": w.ID, "status": w.Status, "key": in.Key}, nil
}

func (s *Server) handleMemoryReflect(ctx context.Context, args map[string]any) (any, *CallError) {
	if s.Writer == nil {
		return nil, internalErr(errors.New("memory_reflect: no memory writer configured"))
	}
	rawItems, _ := args["items"].([]any)
	ids := []string{}
	for i, raw := range rawItems {
		m, ok := raw.(map[string]any)
		if !ok {
			return nil, invalidParams("memory_reflect: items[%d] must be an object", i)
		}
		in, cerr := parseMemoryWriteInput(m)
		if cerr != nil {
			return nil, invalidParams("memory_reflect: items[%d]: %s", i, strings.TrimPrefix(cerr.Message, "memory_write: "))
		}
		w, err := s.Writer.WriteMemory(ctx, in)
		if err != nil {
			return nil, internalErr(fmt.Errorf("memory_reflect: %w", err))
		}
		ids = append(ids, w.ID)
	}
	msg := "Thanks — nothing to record. This session is still captured passively via transcript harvesting."
	if len(ids) > 0 {
		msg = fmt.Sprintf("Recorded %d memories as PROPOSED for review.", len(ids))
	}
	return map[string]any{
		"accepted": len(ids),
		"ids":      ids,
		"message":  msg,
		"summary":  strArg(args, "summary"),
	}, nil
}

func (s *Server) handleEpisodeSearch(ctx context.Context, args map[string]any) (any, *CallError) {
	if s.Episodes == nil {
		return nil, internalErr(errors.New("episode_search: no episode backend configured"))
	}
	f := EpisodeFilter{
		Query:        strArg(args, "query"),
		ErrorPattern: strArg(args, "error_pattern"),
		File:         strArg(args, "file"),
		Status:       strings.ToUpper(strings.TrimSpace(strArg(args, "status"))),
	}
	if f.Status != "" && !validEpisodeStatus[f.Status] {
		return nil, invalidParams("episode_search: unknown status %q (want OPEN|INVESTIGATING|RESOLVED|WONT_FIX)", f.Status)
	}
	eps, err := s.Episodes.SearchEpisodes(ctx, f)
	if err != nil {
		return nil, internalErr(fmt.Errorf("episode_search: %w", err))
	}
	out := make([]any, 0, len(eps))
	for _, e := range eps {
		out = append(out, episodeToMap(e))
	}
	return map[string]any{"episodes": out, "count": len(eps)}, nil
}

func (s *Server) handleEpisodeReport(ctx context.Context, args map[string]any) (any, *CallError) {
	if s.Episodes == nil {
		return nil, internalErr(errors.New("episode_report: no episode backend configured"))
	}
	in := EpisodeReportInput{
		Title:   strings.TrimSpace(strArg(args, "title")),
		Type:    strings.ToLower(strings.TrimSpace(strArg(args, "episode_type"))),
		Trigger: strArg(args, "trigger"),
		Tags:    strSliceArg(args, "tags"),
	}
	if err := ValidateEpisodeReport(in); err != nil {
		return nil, invalidParams("episode_report: %s", err.Error())
	}
	ep, err := s.Episodes.ReportEpisode(ctx, in)
	if err != nil {
		return nil, internalErr(fmt.Errorf("episode_report: %w", err))
	}
	return episodeToMap(ep), nil
}

func (s *Server) handleWorkspaceInfo(ctx context.Context, _ map[string]any) (any, *CallError) {
	if s.Workspace == nil {
		return nil, internalErr(errors.New("workspace_info: no workspace provider configured"))
	}
	w, err := s.Workspace.WorkspaceInfo(ctx)
	if err != nil {
		return nil, internalErr(fmt.Errorf("workspace_info: %w", err))
	}
	return map[string]any{
		"project":  w.Project,
		"branch":   w.Branch,
		"commit":   w.Commit,
		"is_dirty": w.IsDirty,
		"path":     w.Path,
	}, nil
}

func (s *Server) handleFileRead(ctx context.Context, args map[string]any) (any, *CallError) {
	if s.Files == nil {
		return nil, internalErr(errors.New("file_read: no file backend configured"))
	}
	path := strArg(args, "path")
	if strings.TrimSpace(path) == "" {
		return nil, invalidParams("file_read: path is required")
	}
	content, err := s.Files.ReadFile(ctx, path)
	if err != nil {
		return nil, fileErr("file_read", err)
	}
	return map[string]any{"path": path, "content": content, "size": len(content)}, nil
}

func (s *Server) handleFileWrite(ctx context.Context, args map[string]any) (any, *CallError) {
	if s.Files == nil {
		return nil, internalErr(errors.New("file_write: no file backend configured"))
	}
	path := strArg(args, "path")
	if strings.TrimSpace(path) == "" {
		return nil, invalidParams("file_write: path is required")
	}
	content := strArg(args, "content")
	if err := s.Files.WriteFile(ctx, path, content); err != nil {
		return nil, fileErr("file_write", err)
	}
	return map[string]any{"status": "ok", "path": path}, nil
}

// fileErr maps sandbox/filesystem failures to JSON-RPC errors: sandbox
// violations and missing files are client-correctable (InvalidParams),
// anything else is internal.
func fileErr(tool string, err error) *CallError {
	switch {
	case errors.Is(err, daemon.ErrTraversal):
		return invalidParams("%s: path escapes workspace", tool)
	case errors.Is(err, daemon.ErrSecretHit):
		return invalidParams("%s: refused: secret pattern match", tool)
	case os.IsNotExist(err):
		return invalidParams("%s: file not found", tool)
	default:
		return internalErr(fmt.Errorf("%s: %w", tool, err))
	}
}

func parseMemoryWriteInput(args map[string]any) (MemoryWriteInput, *CallError) {
	in := MemoryWriteInput{
		Key:            strings.TrimSpace(strArg(args, "key")),
		Content:        strArg(args, "content"),
		Level:          strings.ToLower(strings.TrimSpace(strArg(args, "level"))),
		Scope:          strings.ToLower(strings.TrimSpace(strArg(args, "scope"))),
		Tags:           strSliceArg(args, "tags"),
		ContextSnippet: strArg(args, "context_snippet"),
	}
	if in.Level == "" {
		in.Level = store.LevelProject
	}
	if in.Scope == "" {
		in.Scope = "fact"
	}
	if err := ValidateMemoryWrite(in); err != nil {
		return MemoryWriteInput{}, invalidParams("memory_write: %s", err.Error())
	}
	return in, nil
}

func episodeToMap(e Episode) map[string]any {
	return map[string]any{
		"id":             e.ID,
		"title":          e.Title,
		"episode_type":   e.Type,
		"trigger":        e.Trigger,
		"investigation":  e.Investigation,
		"root_cause":     e.RootCause,
		"resolution":     e.Resolution,
		"verification":   e.Verification,
		"status":         e.Status,
		"tags":           orEmpty(e.Tags),
		"files_involved": orEmpty(e.FilesInvolved),
		"error_patterns": orEmpty(e.ErrorPatterns),
	}
}

func orEmpty(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

// ---------------------------------------------------------------------------
// JSON arg helpers (encoding/json decodes objects to map[string]any,
// numbers to float64, arrays to []any)
// ---------------------------------------------------------------------------

func strArg(args map[string]any, name string) string {
	v, _ := args[name].(string)
	return v
}

func strSliceArg(args map[string]any, name string) []string {
	raw, ok := args[name].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, e := range raw {
		if str, ok := e.(string); ok {
			out = append(out, str)
		}
	}
	return out
}

func intArg(args map[string]any, name string) int {
	switch v := args[name].(type) {
	case float64:
		return int(v)
	case float32:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	}
	return 0
}

// ---------------------------------------------------------------------------
// Tool catalog (tools/list)
// ---------------------------------------------------------------------------

// Tool describes one MCP tool for tools/list.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func strProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

// Tools returns the 8 plan §1.4 tools in stable order.
func (s *Server) Tools() []Tool {
	return []Tool{
		{
			Name:        ToolMemorySearch,
			Description: "Search project memory. Returns an inference-ready XML context block plus a reflection hint.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": strProp("Natural-language search query (required)."),
					"tags":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"level": strProp("Filter: organization|project|personal|session."),
					"limit": map[string]any{"type": "integer", "description": "Max items (default 20, cap 100)."},
				},
				"required": []string{"query"},
			},
		},
		{
			Name:        ToolMemoryWrite,
			Description: "Record one memory fact (20-2000 chars). Stored as PROPOSED for review.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"key":             strProp("Machine key, e.g. testing/framework (required)."),
					"content":         strProp("Natural-language fact, 20-2000 chars (required)."),
					"scope":           strProp("fact|preference|decision|constraint|pattern|episode_summary (default fact)."),
					"level":           strProp("organization|project|personal|session (default project)."),
					"tags":            map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"context_snippet": strProp("1-2 line provenance, e.g. decided by Alice during auth refactor."),
				},
				"required": []string{"key", "content"},
			},
		},
		{
			Name:        ToolMemoryReflect,
			Description: "Summarize decisions, facts, and preferences from this session. Call when finishing a task or before ending a session.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"summary": strProp("Free-text session summary (optional)."),
					"items": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"key":     strProp("Machine key (required)."),
								"content": strProp("Fact, 20-2000 chars (required)."),
								"scope":   strProp("fact|preference|decision|constraint|pattern|episode_summary."),
								"level":   strProp("organization|project|personal|session."),
								"tags":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
							},
							"required": []string{"key", "content"},
						},
					},
				},
			},
		},
		{
			Name:        ToolEpisodeSearch,
			Description: "Find past bug/incident/feature episodes by query text, error pattern, file, or status.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query":         strProp("Semantic/substring query over the episode narrative."),
					"error_pattern": strProp("Error string, e.g. ConnectionTimeout."),
					"file":          strProp("File path, e.g. internal/server/ws.go."),
					"status":        strProp("OPEN|INVESTIGATING|RESOLVED|WONT_FIX."),
				},
			},
		},
		{
			Name:        ToolEpisodeReport,
			Description: "Open a new episode for an active bug or investigation.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"title":        strProp("Episode title (required)."),
					"episode_type": strProp("bug_fix|feature|refactor|incident|investigation|onboarding (required)."),
					"trigger":      strProp("What started it, e.g. ConnectionTimeout in ws.go:142."),
					"tags":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				},
				"required": []string{"title", "episode_type"},
			},
		},
		{
			Name:        ToolWorkspaceInfo,
			Description: "Show the current workspace: project, branch, commit, dirty state, path.",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			Name:        ToolFileRead,
			Description: "Read a file through the daemon sandbox (1MB cap, secret/traversal guarded).",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": strProp("Workspace-relative path (required)."),
				},
				"required": []string{"path"},
			},
		},
		{
			Name:        ToolFileWrite,
			Description: "Write a file through the daemon sandbox (secret/traversal guarded).",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":    strProp("Workspace-relative path (required)."),
					"content": strProp("File content (required)."),
				},
				"required": []string{"path", "content"},
			},
		},
	}
}
