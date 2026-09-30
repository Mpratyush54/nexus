// Command nexus-mcp-proxy is the standalone stdio→HTTPS MCP bridge (spec 7.5).
// Prefer `nexus mcp-proxy` when the CLI is on PATH; this binary exists for
// packaging (Velopack ships nexus-mcp-proxy.exe for Antigravity).
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"central-memory/internal/mcpproxy"
)

func main() {
	cfg := mcpproxy.FromEnv("https://api-nexus.pratyushes.dev")
	if cfg.Token == "" {
		fmt.Fprintln(os.Stderr, "nexus-mcp-proxy: NEXUS_TOKEN (or CENTRAL_MEMORY_TOKEN) is required")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := cfg.Serve(ctx, os.Stdin, os.Stdout); err != nil && err != context.Canceled {
		fmt.Fprintln(os.Stderr, "nexus-mcp-proxy:", err)
		os.Exit(1)
	}
}
