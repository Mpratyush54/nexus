package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"central-memory/internal/config"
	"central-memory/internal/mcpproxy"
)

const mcpProxyDefaultServer = "https://api-nexus.pratyushes.dev"

// runMCPProxy is `nexus mcp-proxy`: stdio JSON-RPC → POST /v1/agent/mcp.
// No local DB and no TCP listen (spec 7.5).
func runMCPProxy(cfg Config, args []string, stdout io.Writer, stdin io.Reader) error {
	if len(args) > 0 {
		return fmt.Errorf("mcp-proxy takes no arguments, got %q", args[0])
	}
	base := firstNonEmpty(cfg.ServerURL, mcpProxyDefaultServer)
	pcfg := mcpproxy.FromEnv(base)
	if cfg.Token != "" {
		pcfg.Token = cfg.Token
	}
	if cfg.ProjectID != "" {
		pcfg.ProjectID = cfg.ProjectID
	}
	if pcfg.Token == "" {
		file, _ := config.LoadFile()
		pcfg.Token = firstNonEmpty(file.Token)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return pcfg.Serve(ctx, stdin, stdout)
}
