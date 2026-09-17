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

type branchOptions struct {
	Sub     string
	Name    string // fork|checkout target, merge source, diff target
	Extra   string // merge target (optional second positional)
	From    string // fork source branch (default main)
	Project string
	JSON    bool
}

// parseBranchArgs parses `nexus branch <sub> ...`. Pure: no network, no exit.
func parseBranchArgs(args []string) (branchOptions, error) {
	var o branchOptions
	if len(args) == 0 {
		return o, errors.New("branch requires a subcommand: list|fork|checkout|diff|merge")
	}
	o.Sub = args[0]
	rest := args[1:]

	fs := flag.NewFlagSet("nexus branch "+o.Sub, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.Project, "project", "", "project ID")
	fs.StringVar(&o.Project, "p", "", "project ID (shorthand)")
	fs.BoolVar(&o.JSON, "json", false, "emit JSON instead of tables")
	if o.Sub == "fork" {
		fs.StringVar(&o.From, "from", "main", "source branch to fork from")
	}

	switch o.Sub {
	case "list":
		if err := fs.Parse(rest); err != nil {
			return o, err
		}
		if len(fs.Args()) > 0 {
			return o, fmt.Errorf("branch list takes no positional arguments, got %q", strings.Join(fs.Args(), " "))
		}
	case "fork", "checkout":
		if err := fs.Parse(rest); err != nil {
			return o, err
		}
		if len(fs.Args()) != 1 {
			return o, fmt.Errorf("branch %s requires exactly one <name> argument", o.Sub)
		}
		o.Name = fs.Args()[0]
	case "diff":
		if err := fs.Parse(rest); err != nil {
			return o, err
		}
		if len(fs.Args()) > 1 {
			return o, errors.New("branch diff takes at most one [target] argument")
		}
		if len(fs.Args()) == 1 {
			o.Name = fs.Args()[0]
		}
	case "merge":
		if err := fs.Parse(rest); err != nil {
			return o, err
		}
		if len(fs.Args()) < 1 || len(fs.Args()) > 2 {
			return o, errors.New(`branch merge requires <source> [target], e.g. nexus branch merge bob-exp main`)
		}
		o.Name = fs.Args()[0]
		if len(fs.Args()) == 2 {
			o.Extra = fs.Args()[1]
		}
	default:
		return o, fmt.Errorf("unknown branch subcommand %q (want list|fork|checkout|diff|merge)", o.Sub)
	}
	return o, nil
}

// runBranch calls the Phase 5 copy-on-write branch routes. None exist on the
// Phase 1.8 server, so every call degrades to a tagged not-implemented error
// (exit 3) until migrations/005_branches lands.
func runBranch(ctx context.Context, cfg Config, args []string, stdout io.Writer) error {
	o, err := parseBranchArgs(args)
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
		if err := c.getJSON(ctx, "/branches", q, &resp); err != nil {
			return notImplementedFor("branch list", err)
		}
		items := resp.Items
		if items == nil {
			items = []map[string]any{}
		}
		if asJSON {
			return printJSON(stdout, map[string]any{"items": items, "count": len(items)})
		}
		if len(items) == 0 {
			fmt.Fprintln(stdout, "no branches")
			return nil
		}
		rows := make([][]string, 0, len(items))
		for _, b := range items {
			rows = append(rows, []string{
				strField(b, "name"),
				strField(b, "visibility"),
				strField(b, "owner_id", "owner"),
				strField(b, "created_at"),
			})
		}
		printTable(stdout, []string{"NAME", "VISIBILITY", "OWNER", "CREATED"}, rows)
		return nil
	case "fork":
		if project == "" {
			return errors.New("branch fork requires a project (-p/--project or NEXUS_PROJECT)")
		}
		var created map[string]any
		err := c.postJSON(ctx, "/branches",
			map[string]any{"name": o.Name, "project_id": project, "from": o.From}, &created)
		if err != nil {
			return notImplementedFor("branch fork", err)
		}
		if asJSON {
			return printJSON(stdout, created)
		}
		fmt.Fprintf(stdout, "forked branch %s from %s\n", o.Name, o.From)
		return nil
	case "checkout":
		var out map[string]any
		err := c.postJSON(ctx, "/branches/"+url.PathEscape(o.Name)+"/checkout", map[string]any{}, &out)
		if err != nil {
			return notImplementedFor("branch checkout", err)
		}
		if asJSON {
			return printJSON(stdout, out)
		}
		fmt.Fprintf(stdout, "checked out branch %s\n", o.Name)
		return nil
	case "diff":
		q := url.Values{}
		if project != "" {
			q.Set("project_id", project)
		}
		if o.Name != "" {
			q.Set("target", o.Name)
		}
		var out map[string]any
		if err := c.getJSON(ctx, "/branches/diff", q, &out); err != nil {
			return notImplementedFor("branch diff", err)
		}
		if asJSON {
			return printJSON(stdout, out)
		}
		if d := strField(out, "diff", "text"); d != "" {
			fmt.Fprintln(stdout, d)
			return nil
		}
		return printJSON(stdout, out)
	case "merge":
		target := firstNonEmpty(o.Extra, "main")
		var out map[string]any
		err := c.postJSON(ctx, "/branches/merge",
			map[string]any{"source": o.Name, "target": target, "project_id": project}, &out)
		if err != nil {
			return notImplementedFor("branch merge", err)
		}
		if asJSON {
			return printJSON(stdout, out)
		}
		if conflicts := strField(out, "conflicts"); conflicts != "" && conflicts != "0" {
			fmt.Fprintf(stdout, "merged %s into %s with conflicts: %s\n", o.Name, target, conflicts)
			return nil
		}
		fmt.Fprintf(stdout, "merged %s into %s\n", o.Name, target)
		return nil
	}
	return fmt.Errorf("unknown branch subcommand %q", o.Sub)
}
