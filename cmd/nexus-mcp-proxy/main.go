// Command nexus-mcp-proxy is the standalone stdio→HTTPS MCP bridge (spec 7.5).
// The desktop app ships it next to Nexus.exe so agents that drop remote MCP
// headers (Antigravity) work without the separate nexus CLI. It is a thin
// wrapper over internal/mcpproxy, the same code as `nexus mcp-proxy`: no
// database, no cache, no listener.
//
// Configuration comes from NEXUS_SERVER, NEXUS_TOKEN and NEXUS_PROJECT, or
// from the flags --server, --token-env (name of the env var holding the
// token) and --project. A leading "mcp-proxy" argument is accepted so the
// binary is a drop-in for `nexus mcp-proxy` configs.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"central-memory/internal/mcpproxy"
)

const defaultServer = "https://api-nexus.pratyushes.dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "mcp-proxy" {
		args = args[1:]
	}
	fs := flag.NewFlagSet("nexus-mcp-proxy", flag.ContinueOnError)
	fs.SetOutput(stderr)
	server := fs.String("server", "", "Nexus API base URL (default $NEXUS_SERVER or "+defaultServer+")")
	tokenEnv := fs.String("token-env", "", "name of the environment variable holding the token (default NEXUS_TOKEN)")
	project := fs.String("project", "", "project id sent as X-Nexus-Project (default $NEXUS_PROJECT)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "nexus-mcp-proxy: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	cfg := mcpproxy.FromEnv(defaultServer)
	if s := strings.TrimSpace(*server); s != "" {
		cfg.BaseURL = s
	}
	if name := strings.TrimSpace(*tokenEnv); name != "" {
		cfg.Token = strings.TrimSpace(os.Getenv(name))
	}
	if p := strings.TrimSpace(*project); p != "" {
		cfg.ProjectID = p
	}
	if cfg.Token == "" {
		fmt.Fprintln(stderr, "nexus-mcp-proxy: NEXUS_TOKEN (or the variable named by --token-env) is required")
		return 2
	}
	if err := cfg.Serve(ctx, stdin, stdout); err != nil && err != context.Canceled {
		fmt.Fprintln(stderr, "nexus-mcp-proxy:", err)
		return 1
	}
	return 0
}
