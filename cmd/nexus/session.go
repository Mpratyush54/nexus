package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"strings"
)

type sessionOptions struct {
	Sub     string
	ID      string
	Title   string
	Project string
	JSON    bool
}

// parseSessionArgs parses `nexus session <sub> ...`. Pure: no network, no exit.
func parseSessionArgs(args []string) (sessionOptions, error) {
	var o sessionOptions
	if len(args) == 0 {
		return o, errors.New("session requires a subcommand: list|create|join")
	}
	o.Sub = args[0]
	rest := args[1:]

	fs := flag.NewFlagSet("nexus session "+o.Sub, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.Project, "project", "", "project ID")
	fs.StringVar(&o.Project, "p", "", "project ID (shorthand)")
	fs.BoolVar(&o.JSON, "json", false, "emit JSON instead of tables")

	switch o.Sub {
	case "list":
		if err := fs.Parse(rest); err != nil {
			return o, err
		}
		if len(fs.Args()) > 0 {
			return o, fmt.Errorf("session list takes no positional arguments, got %q", strings.Join(fs.Args(), " "))
		}
	case "create":
		if err := fs.Parse(rest); err != nil {
			return o, err
		}
		o.Title = strings.Join(fs.Args(), " ")
		if strings.TrimSpace(o.Title) == "" {
			return o, errors.New(`session create requires a <title>, e.g. nexus session create "Auth refactor"`)
		}
	case "join":
		if err := fs.Parse(rest); err != nil {
			return o, err
		}
		if len(fs.Args()) != 1 {
			return o, errors.New(`session join requires exactly one <id> argument`)
		}
		o.ID = fs.Args()[0]
	default:
		return o, fmt.Errorf("unknown session subcommand %q (want list|create|join)", o.Sub)
	}
	return o, nil
}

// runSession calls the Phase 3 session routes. The Phase 1.8 server does not
// implement them yet, so 404/405 degrades to a tagged not-implemented error
// (exit 3) instead of a raw HTTP dump.
func runSession(ctx context.Context, cfg Config, args []string, stdout io.Writer) error {
	o, err := parseSessionArgs(args)
	if err != nil {
		return err
	}
	asJSON := cfg.JSON || o.JSON
	project := firstNonEmpty(o.Project, cfg.ProjectID)
	c := newAPIClient(cfg)

	switch o.Sub {
	case "list":
		q := url.Values{}
		if project != "" {
			q.Set("project_id", project)
		}
		var resp struct {
			Items []map[string]any `json:"items"`
			Count int              `json:"count"`
		}
		if err := c.getJSON(ctx, "/sessions", q, &resp); err != nil {
			return notImplementedFor("session list", err)
		}
		items := resp.Items
		if items == nil {
			items = []map[string]any{}
		}
		if asJSON {
			return printJSON(stdout, map[string]any{"items": items, "count": len(items)})
		}
		if len(items) == 0 {
			fmt.Fprintln(stdout, "no sessions")
			return nil
		}
		rows := make([][]string, 0, len(items))
		for _, s := range items {
			rows = append(rows, []string{
				strField(s, "id"),
				strField(s, "title"),
				strField(s, "project_id"),
				strField(s, "is_active", "active"),
			})
		}
		printTable(stdout, []string{"ID", "TITLE", "PROJECT", "ACTIVE"}, rows)
		return nil
	case "create":
		if project == "" {
			return errors.New("session create requires a project (-p/--project or NEXUS_PROJECT)")
		}
		var created map[string]any
		err := c.postJSON(ctx, "/sessions",
			map[string]any{"title": o.Title, "project_id": project}, &created)
		if err != nil {
			return notImplementedFor("session create", err)
		}
		if asJSON {
			return printJSON(stdout, created)
		}
		fmt.Fprintf(stdout, "created session %s\n", strField(created, "id"))
		return nil
	case "join":
		var out map[string]any
		err := c.postJSON(ctx, "/sessions/"+url.PathEscape(o.ID)+"/join", map[string]any{}, &out)
		if err != nil {
			return notImplementedFor("session join", err)
		}
		if asJSON {
			return printJSON(stdout, out)
		}
		fmt.Fprintf(stdout, "joined session %s\n", o.ID)
		return nil
	}
	return fmt.Errorf("unknown session subcommand %q", o.Sub)
}
