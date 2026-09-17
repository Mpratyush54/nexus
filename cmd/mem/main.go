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
)

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

	mem := mcp.NewInMemoryMemoryStore()
	srv := mcp.New(
		mcp.Config{ProjectName: name},
		mem, mem,
		mcp.NewInMemoryEpisodeStore(),
		mcp.DaemonWorkspaceProvider{Root: root, Project: name},
		mcp.DaemonFileProxy{Root: root},
	)
	if err := srv.ServeStdio(ctx, os.Stdin, os.Stdout); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("mem mcp: %w", err)
	}
	return nil
}
