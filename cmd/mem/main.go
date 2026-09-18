// Command mem — local CLI client entrypoint (issue #39, plan target tree).
//
// Thin dispatch only: status and projects reuse the same underlying
// internal/project funcs as the root mem CLI (Leaves/Fingerprint for
// projects, CachedLeaves for status); mcp serves the MCP tool surface over
// stdio via (mcp.Server).ServeStdio with local wiring (in-memory stores,
// DaemonWorkspaceProvider/DaemonFileProxy rooted at the current directory,
// default HashEmbed via CENTRAL_EMBEDDING_* / Config.Embedder; openai and
// ollama providers fall back to HashEmbed on failure).
//
// Root main.go is deliberately NOT edited — package-main funcs there are
// unexportable, so this entrypoint calls the same internal funcs rather than
// importing them. The full #21 CLI stays with its owner.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	memctx "central-memory/internal/context"
	"central-memory/internal/mcp"
	"central-memory/internal/project"
	"central-memory/internal/store"
)

type mcpStore struct {
	mem *store.MemStore
}

var _ mcp.Store = mcpStore{}

// memoryDTO maps a store row to the MCP view. Field fidelity (Issue #116,
// #135): every field mcp.MemoryItem carries (ID, ProjectID, UserID,
// SessionID, Key, Content, ContextSnippet, Level, Scope, Tags, Confidence,
// Status, Source, Embedding) is copied — the DTO grew UserID/SessionID/
// Embedding after #116, so dropping them here would silently unsearchable
// writes and ownerless reads. Truly store-only fields (OrgID,
// SourceEventID, ProposedBy, ConfirmedBy, SupersededBy, UseCount,
// LastUsedAt, CreatedAt, UpdatedAt) remain on the store row where decay /
// relevance (plan §1.7) is served.
func memoryDTO(item *store.MemoryItem) *mcp.MemoryItem {
	return &mcp.MemoryItem{
		ID: item.ID, ProjectID: item.ProjectID, UserID: item.UserID, SessionID: item.SessionID,
		Key: item.Key, Content: item.Content,
		ContextSnippet: item.ContextSnippet, Level: item.Level, Scope: item.Scope,
		Tags: item.Tags, Confidence: item.Confidence, Status: item.Status, Source: item.Source,
		Embedding: item.Embedding,
	}
}

func (s mcpStore) SearchMemory(ctx context.Context, projectID, query string, tags []string, limit int) ([]*mcp.MemoryItem, error) {
	items, err := s.mem.SearchMemory(ctx, projectID, query, tags, limit)
	if err != nil {
		return nil, err
	}
	out := make([]*mcp.MemoryItem, 0, len(items))
	for _, item := range items {
		out = append(out, memoryDTO(item))
	}
	return out, nil
}

// SearchMemoryVector enables MCP memory_search vector ranking (issue #165).
func (s mcpStore) SearchMemoryVector(ctx context.Context, projectID string, queryVec []float32, limit int) ([]*mcp.MemoryItem, error) {
	items, err := s.mem.SearchMemoryVector(ctx, projectID, queryVec, limit)
	if err != nil {
		return nil, err
	}
	out := make([]*mcp.MemoryItem, 0, len(items))
	for _, item := range items {
		out = append(out, memoryDTO(item))
	}
	return out, nil
}

func (s mcpStore) CreateMemoryItem(ctx context.Context, item *mcp.MemoryItem) error {
	row := &store.MemoryItem{
		ID: item.ID, ProjectID: item.ProjectID, UserID: item.UserID, SessionID: item.SessionID,
		Key: item.Key, Content: item.Content,
		ContextSnippet: item.ContextSnippet, Level: item.Level, Scope: item.Scope,
		Tags: item.Tags, Confidence: item.Confidence, Status: item.Status, Source: item.Source,
		Embedding: item.Embedding,
	}
	if err := s.mem.CreateMemoryItem(ctx, row); err != nil {
		return err
	}
	// Write back only store-minted fields (Issue #116 struct-clobber fix):
	// ID plus server defaults. Caller-supplied Key/Content/Tags/etc are
	// preserved verbatim — never overwritten from the row.
	copyMintedMemoryFields(item, row)
	return nil
}

// copyMintedMemoryFields copies only fields the store may mint or default
// (ID, Confidence/Status/Level defaults) back onto the caller's item.
func copyMintedMemoryFields(item *mcp.MemoryItem, row *store.MemoryItem) {
	if row.ID != "" {
		item.ID = row.ID
	}
	if row.Status != "" {
		item.Status = row.Status
	}
	if item.Confidence == 0 && row.Confidence != 0 {
		item.Confidence = row.Confidence
	}
	if item.Level == "" && row.Level != "" {
		item.Level = row.Level
	}
}

// episodeDTO maps a store episode to the MCP view. Field fidelity (Issue
// #116, #136): Investigation, SessionID, and Embedding ride along —
// dropping the narrative core, session link, and vector made mem-written
// episodes unsearchable and context-poor. Store-only auditing fields
// (OpenedAt, ResolvedAt, CreatedBy, ResolvedBy) stay on the store row.
func episodeDTO(ep *store.Episode) *mcp.Episode {
	return &mcp.Episode{
		ID: ep.ID, ProjectID: ep.ProjectID, SessionID: ep.SessionID, Title: ep.Title, EpisodeType: ep.EpisodeType,
		Trigger: ep.Trigger, Investigation: ep.Investigation, RootCause: ep.RootCause, Resolution: ep.Resolution,
		Verification: ep.Verification, Tags: ep.Tags, FilesInvolved: ep.FilesInvolved,
		ErrorPatterns: ep.ErrorPatterns, Embedding: ep.Embedding, Status: ep.Status,
	}
}

func (s mcpStore) SearchEpisodes(ctx context.Context, projectID, errorPattern, query string, limit int) ([]*mcp.Episode, error) {
	episodes, err := s.mem.SearchEpisodes(ctx, projectID, errorPattern, query, limit)
	if err != nil {
		return nil, err
	}
	out := make([]*mcp.Episode, 0, len(episodes))
	for _, ep := range episodes {
		out = append(out, episodeDTO(ep))
	}
	return out, nil
}

func (s mcpStore) CreateEpisode(ctx context.Context, ep *mcp.Episode) error {
	row := &store.Episode{
		ID: ep.ID, ProjectID: ep.ProjectID, SessionID: ep.SessionID, Title: ep.Title, EpisodeType: ep.EpisodeType,
		Trigger: ep.Trigger, Investigation: ep.Investigation, RootCause: ep.RootCause, Resolution: ep.Resolution,
		Verification: ep.Verification, Tags: ep.Tags, FilesInvolved: ep.FilesInvolved,
		ErrorPatterns: ep.ErrorPatterns, Embedding: ep.Embedding, Status: ep.Status,
	}
	if err := s.mem.CreateEpisode(ctx, row); err != nil {
		return err
	}
	// Write back only store-minted fields (Issue #116 struct-clobber fix).
	if row.ID != "" {
		ep.ID = row.ID
	}
	if row.Status != "" {
		ep.Status = row.Status
	}
	return nil
}

func (s mcpStore) GetActiveWorkspace(ctx context.Context, projectID string) (*mcp.Workspace, error) {
	ws, err := s.mem.GetActiveWorkspace(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return &mcp.Workspace{Branch: ws.Branch, CommitSHA: ws.CommitSHA, IsDirty: ws.IsDirty, Path: ws.Path}, nil
}

func (s mcpStore) GetProject(ctx context.Context, id string) (*mcp.Project, error) {
	p, err := s.mem.GetProject(ctx, id)
	if err != nil {
		return nil, err
	}
	return &mcp.Project{DisplayName: p.DisplayName, FolderName: p.FolderName}, nil
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "projects":
		err = cmdProjects()
	case "status":
		err = cmdStatus()
	case "mcp":
		err = cmdMCP()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Println(`mem — central memory CLI client (cmd entrypoint, issue #39)

  mem status                       show system status (shared with root mem; server health: nexus status)
  mem projects                     list detected projects (shared with root mem; see also: nexus memory search)
  mem mcp                          serve MCP tools over stdio (for agents)

See also: root mem (mem daemon) and nexus (nexus status|memory|migrate) — projects/status share internal/project helpers.`)
}

// cmdProjects unifies with the root CLI via internal/project helpers (Issue #84).
func cmdProjects() error {
	return project.PrintProjects(os.Stdout)
}

// cmdStatus unifies with the root CLI via internal/project helpers (Issue #84).
func cmdStatus() error {
	return project.PrintStatus(os.Stdout)
}

// cmdMCP serves the 8 plan §1.4 tools on stdin/stdout until EOF or SIGINT/
// SIGTERM. Local wiring only: in-memory memory/episode stores (writes land
// as PROPOSED, nothing persists), live git state + sandboxed files via the
// daemon helpers for the current directory. Project resolution uses the full
// identity triple (Issue #116): Fingerprint(origin, root) of the workspace
// root plus the folder name fallback — never folder-name only.
func cmdMCP() error {
	root, err := filepath.Abs(".")
	if err != nil {
		return fmt.Errorf("mem mcp: resolve workspace root: %w", err)
	}
	name := filepath.Base(root)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	origin, rootCommit := project.Fingerprint(root)
	mem := store.NewMemStore()
	p, err := mem.ResolveProject(ctx, origin, rootCommit, name)
	if err != nil {
		return fmt.Errorf("mem mcp: resolve project: %w", err)
	}
	srv := mcp.NewServer(mcpStore{mem}, mcp.Config{
		ProjectID: p.ID, ProjectName: name, WorkspacePath: root,
		Embedder: memctx.EmbedderFromEnv(),
	})
	if err := srv.Serve(ctx, os.Stdin, os.Stdout); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("mem mcp: %w", err)
	}
	return nil
}
