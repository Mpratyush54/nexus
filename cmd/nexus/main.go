// Command nexus — developer CLI client for the central memory control plane.
//
// Separate from the `mem` skeleton in the repo root (./main.go), which stays
// untouched until the Phase 1b migration. See docs/decisions/2026-09-17-cli.md.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"
)

func main() {
	cfg := defaultConfig()
	rest, err := parseGlobalArgs(os.Args[1:], &cfg)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			usage(os.Stdout)
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		usage(os.Stderr)
		os.Exit(2)
	}
	if len(rest) == 0 {
		usage(os.Stderr)
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var runErr error
	switch rest[0] {
	case "status":
		runErr = runStatus(ctx, cfg, rest[1:], os.Stdout)
	case "memory":
		runErr = runMemory(ctx, cfg, rest[1:], os.Stdout)
	case "session":
		runErr = runSession(ctx, cfg, rest[1:], os.Stdout)
	case "branch":
		runErr = runBranch(ctx, cfg, rest[1:], os.Stdout)
	case "episode":
		runErr = runEpisode(ctx, cfg, rest[1:], os.Stdout)
	case "doctor":
		runErr = runDoctor(ctx, cfg, rest[1:], os.Stdout)
	case "migrate":
		runErr = runMigrate(ctx, cfg, rest[1:], os.Stdout)
	case "update":
		runErr = runUpdate(ctx, cfg, rest[1:], os.Stdout)
	case "daemon":
		runErr = runDaemonCmd(cfg, rest[1:], os.Stdout)
	case "help", "-h", "--help":
		usage(os.Stdout)
	case "version", "--version":
		runErr = runVersion(ctx, cfg, rest[1:], os.Stdout)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", rest[0])
		usage(os.Stderr)
		os.Exit(2)
	}
	if runErr != nil {
		var ni *errNotImplemented
		if errors.As(runErr, &ni) {
			fmt.Fprintln(os.Stderr, "not implemented:", ni.Error())
			os.Exit(3)
		}
		fmt.Fprintln(os.Stderr, "error:", runErr)
		os.Exit(1)
	}
}

// parseGlobalArgs parses leading global flags into cfg and returns the
// remaining (subcommand + args). Pure flag parsing: no os.Exit, no I/O.
func parseGlobalArgs(args []string, cfg *Config) ([]string, error) {
	fs := flag.NewFlagSet("nexus", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&cfg.ServerURL, "server", cfg.ServerURL, "central server base URL")
	fs.StringVar(&cfg.Token, "token", cfg.Token, "bearer token for the central server")
	fs.StringVar(&cfg.DaemonURL, "daemon", cfg.DaemonURL, "workspace daemon base URL")
	fs.StringVar(&cfg.ProjectID, "project", cfg.ProjectID, "default project ID")
	fs.StringVar(&cfg.ProjectID, "p", cfg.ProjectID, "default project ID (shorthand)")
	fs.BoolVar(&cfg.JSON, "json", cfg.JSON, "emit JSON instead of tables")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	return fs.Args(), nil
}

type statusOptions struct {
	Project string
	JSON    bool
}

// parseStatusArgs parses `nexus status [--project ID] [--json]`.
func parseStatusArgs(args []string) (statusOptions, error) {
	var o statusOptions
	fs := flag.NewFlagSet("nexus status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.Project, "project", "", "project ID for workspace lookup")
	fs.StringVar(&o.Project, "p", "", "project ID for workspace lookup (shorthand)")
	fs.BoolVar(&o.JSON, "json", false, "emit JSON instead of tables")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if len(fs.Args()) > 0 {
		return o, fmt.Errorf("status takes no positional arguments, got %q", strings.Join(fs.Args(), " "))
	}
	return o, nil
}

// runStatus probes GET /healthz and, when a project is known, reports the
// most-recently-seen online workspace for it.
func runStatus(ctx context.Context, cfg Config, args []string, stdout io.Writer) error {
	o, err := parseStatusArgs(args)
	if err != nil {
		return err
	}
	asJSON := cfg.JSON || o.JSON
	project := firstNonEmpty(o.Project, cfg.ProjectID)

	c := newAPIClient(cfg)
	var health map[string]any
	if err := c.getJSON(ctx, "/healthz", nil, &health); err != nil {
		return fmt.Errorf("server health check failed: %w", err)
	}

	if asJSON {
		out := map[string]any{
			"server":     strings.TrimSuffix(cfg.ServerURL, "/"),
			"healthy":    true,
			"health":     health,
			"project_id": project,
		}
		if project != "" {
			var ws map[string]any
			q := url.Values{}
			if err := c.getJSON(ctx, "/workspaces/"+url.PathEscape(project)+"/active", q, &ws); err != nil {
				if isNotFound(err) {
					out["active_workspace"] = nil
				} else {
					return err
				}
			} else {
				out["active_workspace"] = ws
			}
		}
		return printJSON(stdout, out)
	}

	fmt.Fprintln(stdout, "nexus status — central memory control plane")
	fmt.Fprintln(stdout)
	printTable(stdout,
		[]string{"COMPONENT", "TARGET", "STATE"},
		[][]string{{"server", strings.TrimSuffix(cfg.ServerURL, "/"), "healthy"}})
	if project == "" {
		fmt.Fprintln(stdout, "\nno project selected (use -p/--project or NEXUS_PROJECT for workspace sync status)")
		return nil
	}
	var ws map[string]any
	if err := c.getJSON(ctx, "/workspaces/"+url.PathEscape(project)+"/active", nil, &ws); err != nil {
		if isNotFound(err) {
			fmt.Fprintf(stdout, "\nproject %s: no active workspace (offline >90s or never registered)\n", project)
			return nil
		}
		return err
	}
	fmt.Fprintln(stdout)
	printTable(stdout,
		[]string{"WORKSPACE", "MACHINE", "BRANCH", "DIRTY", "LAST SEEN"},
		[][]string{{
			strField(ws, "id"),
			strField(ws, "machine_id"),
			strField(ws, "branch"),
			strField(ws, "is_dirty"),
			strField(ws, "last_seen"),
		}})
	return nil
}

func usage(w io.Writer) {
	fmt.Fprintln(w, `nexus — central memory developer CLI

Usage:
  nexus [--server URL] [--token TOK] [--daemon URL] [-p PROJECT] [--json] <command> [args]

Commands:
  status                          server health (+ active workspace with -p)
  memory search [-p ID] [--level L] [--limit N] "<query>"
  memory propose -k KEY [-p ID] [--level L] "<fact>"
  memory confirm <id>
  memory reject <id>
  session list|create|join
  branch list|fork|checkout|diff|merge
  episode list [-p ID] [--limit N]
  episode search [-p ID] "<error or query>"
  doctor                          probe server, daemon, and git
  migrate [--vault PATH] [--dry-run]   import legacy vault
  version [--check]               print CLI version (and latest release)
  update [--channel stable] [--yes]    download the latest CLI binary
  daemon install|uninstall|status      install the local workspace daemon as a login service

Global flags (env fallbacks: NEXUS_SERVER / CENTRAL_SERVER_URL, NEXUS_TOKEN, NEXUS_DAEMON, NEXUS_PROJECT):
  --server URL   central server base URL (default: env → ~/.config/central-memory/config.json → compile-time)
  --token TOK    bearer token (NEXUS_TOKEN / CENTRAL_MEMORY_TOKEN)
  --daemon URL   workspace daemon base URL (default http://localhost:7171)
  -p, --project  default project ID
  --json         emit JSON instead of pretty tables`)
}
