// Command mem — local CLI client entrypoint (issue #39, plan target tree).
//
// Thin dispatch only: status and projects reuse the same underlying
// internal/project funcs as the root mem CLI (Leaves/Fingerprint for
// projects, CachedLeaves for status); mcp serves the MCP tool surface over
// stdio via (mcp.Server).ServeStdio with local wiring (in-memory stores,
// DaemonWorkspaceProvider/DaemonFileProxy rooted at the current directory,
// default HashEmbed until LLM embeddings land via Config.Embed).
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

	"central-memory/internal/mcp"
	"central-memory/internal/project"
	"central-memory/internal/store"
)

type mcpStore struct {
	mem *store.MemStore
}

var _ mcp.Store = mcpStore{}

func memoryDTO(item *store.MemoryItem) *mcp.MemoryItem {
	return &mcp.MemoryItem{
		ID: item.ID, ProjectID: item.ProjectID, Key: item.Key, Content: item.Content,
		ContextSnippet: item.ContextSnippet, Level: item.Level, Scope: item.Scope,
		Tags: item.Tags, Confidence: item.Confidence, Status: item.Status, Source: item.Source,
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

func (s mcpStore) CreateMemoryItem(ctx context.Context, item *mcp.MemoryItem) error {
	row := &store.MemoryItem{
		ID: item.ID, ProjectID: item.ProjectID, Key: item.Key, Content: item.Content,
		ContextSnippet: item.ContextSnippet, Level: item.Level, Scope: item.Scope,
		Tags: item.Tags, Confidence: item.Confidence, Status: item.Status, Source: item.Source,
	}
	if err := s.mem.CreateMemoryItem(ctx, row); err != nil {
		return err
	}
	*item = *memoryDTO(row)
	return nil
}

func episodeDTO(ep *store.Episode) *mcp.Episode {
	return &mcp.Episode{
		ID: ep.ID, ProjectID: ep.ProjectID, Title: ep.Title, EpisodeType: ep.EpisodeType,
		Trigger: ep.Trigger, RootCause: ep.RootCause, Resolution: ep.Resolution,
		Verification: ep.Verification, Tags: ep.Tags, FilesInvolved: ep.FilesInvolved,
		ErrorPatterns: ep.ErrorPatterns, Status: ep.Status,
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
		ID: ep.ID, ProjectID: ep.ProjectID, Title: ep.Title, EpisodeType: ep.EpisodeType,
		Trigger: ep.Trigger, RootCause: ep.RootCause, Resolution: ep.Resolution,
		Verification: ep.Verification, Tags: ep.Tags, FilesInvolved: ep.FilesInvolved,
		ErrorPatterns: ep.ErrorPatterns, Status: ep.Status,
	}
	if err := s.mem.CreateEpisode(ctx, row); err != nil {
		return err
	}
	*ep = *episodeDTO(row)
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

  mem status                       show system status
  mem projects                     list detected projects
  mem mcp                          serve MCP tools over stdio (for agents)`)
}

// cmdProjects mirrors the root CLI: project.Leaves + project.Fingerprint.
func cmdProjects() error {
	for _, leaf := range project.Leaves() {
		origin, root := project.Fingerprint(leaf)
		id := leaf
		if origin != "" {
			id += "  [" + origin + "]"
		} else if root != "" && len(root) >= 12 {
			id += "  [root " + root[:12] + "]"
		}
		fmt.Println(id)
	}
	return nil
}

// cmdStatus mirrors the root CLI: project.CachedLeaves health summary.
func cmdStatus() error {
	fmt.Println("mem status — multiplayer central memory")
	fmt.Println()
	leaves := project.CachedLeaves()
	fmt.Printf("projects detected: %d\n", len(leaves))
	fmt.Println("daemon: not yet implemented")
	fmt.Println("server: not yet implemented")
	return nil
}

// cmdMCP serves the 8 plan §1.4 tools on stdin/stdout until EOF or SIGINT/
// SIGTERM. Local wiring only: in-memory memory/episode stores (writes land
// as PROPOSED, nothing persists), live git state + sandboxed files via the
// daemon helpers for the current directory.
func cmdMCP() error {
	root, err := filepath.Abs(".")
	if err != nil {
		return fmt.Errorf("mem mcp: resolve workspace root: %w", err)
	}
	name := filepath.Base(root)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	mem := store.NewMemStore()
	p, err := mem.ResolveProject(ctx, "", "", name)
	if err != nil {
		return fmt.Errorf("mem mcp: resolve project: %w", err)
	}
	srv := mcp.NewServer(mcpStore{mem}, mcp.Config{
		ProjectID: p.ID, ProjectName: name, WorkspacePath: root,
	})
	if err := srv.Serve(ctx, os.Stdin, os.Stdout); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("mem mcp: %w", err)
	}
	return nil
}
