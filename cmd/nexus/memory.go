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

// memoryItemJSON mirrors the wire shape of store.MemoryItem without importing
// server internals, so the CLI stays resilient to store changes.
type memoryItemJSON struct {
	ID         string   `json:"id"`
	Key        string   `json:"key"`
	Content    string   `json:"content"`
	Level      string   `json:"level"`
	Scope      string   `json:"scope"`
	Status     string   `json:"status"`
	Confidence float32  `json:"confidence"`
	Tags       []string `json:"tags"`
}

type memorySearchResponse struct {
	Items []*memoryItemJSON `json:"items"`
	Count int               `json:"count"`
}

type memoryOptions struct {
	Sub     string
	Query   string
	Key     string
	Content string
	ID      string
	Project string
	Level   string
	Tags    string
	Limit   int
	JSON    bool
}

// parseMemoryArgs parses `nexus memory <sub> ...`. Pure: no network, no exit.
func parseMemoryArgs(args []string) (memoryOptions, error) {
	var o memoryOptions
	if len(args) == 0 {
		return o, errors.New("memory requires a subcommand: search|propose|confirm|reject")
	}
	o.Sub = args[0]
	rest := args[1:]

	fs := flag.NewFlagSet("nexus memory "+o.Sub, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.Project, "project", "", "project ID")
	fs.StringVar(&o.Project, "p", "", "project ID (shorthand)")
	fs.BoolVar(&o.JSON, "json", false, "emit JSON instead of tables")

	switch o.Sub {
	case "search":
		fs.StringVar(&o.Level, "level", "", "filter by level (organization|project|personal|session)")
		fs.StringVar(&o.Tags, "tags", "", "comma-separated tag filter")
		fs.IntVar(&o.Limit, "limit", 20, "max results")
		fs.IntVar(&o.Limit, "n", 20, "max results (shorthand)")
		if err := fs.Parse(rest); err != nil {
			return o, err
		}
		o.Query = strings.Join(fs.Args(), " ")
		if strings.TrimSpace(o.Query) == "" {
			return o, errors.New(`memory search requires a query, e.g. nexus memory search -p <id> "redis caching"`)
		}
		if o.Limit <= 0 {
			return o, errors.New("memory search: --limit must be positive")
		}
	case "propose":
		fs.StringVar(&o.Key, "key", "", "machine key, e.g. testing/framework")
		fs.StringVar(&o.Key, "k", "", "machine key (shorthand)")
		fs.StringVar(&o.Level, "level", "project", "memory level")
		if err := fs.Parse(rest); err != nil {
			return o, err
		}
		o.Content = strings.Join(fs.Args(), " ")
		if strings.TrimSpace(o.Key) == "" {
			return o, errors.New(`memory propose requires -k KEY, e.g. nexus memory propose -k testing/framework "..."`)
		}
		if n := len(strings.TrimSpace(o.Content)); n < 20 || n > 2000 {
			return o, fmt.Errorf("memory propose: content must be 20-2000 characters (got %d)", n)
		}
	case "confirm", "reject":
		if err := fs.Parse(rest); err != nil {
			return o, err
		}
		if len(fs.Args()) != 1 {
			return o, fmt.Errorf("memory %s requires exactly one <id> argument", o.Sub)
		}
		o.ID = fs.Args()[0]
	default:
		return o, fmt.Errorf("unknown memory subcommand %q (want search|propose|confirm|reject)", o.Sub)
	}
	return o, nil
}

func runMemory(ctx context.Context, cfg Config, args []string, stdout io.Writer) error {
	o, err := parseMemoryArgs(args)
	if err != nil {
		return err
	}
	asJSON := cfg.JSON || o.JSON
	project := firstNonEmpty(o.Project, cfg.ProjectID)
	c := newAPIClient(cfg)

	switch o.Sub {
	case "search":
		if project == "" {
			return errors.New("memory search requires a project (-p/--project or NEXUS_PROJECT)")
		}
		q := url.Values{}
		q.Set("project_id", project)
		q.Set("q", o.Query)
		if o.Tags != "" {
			q.Set("tags", o.Tags)
		}
		if o.Limit > 0 {
			q.Set("limit", fmt.Sprint(o.Limit))
		}
		// Level is forwarded for future server support and filtered
		// client-side today (Phase 1.8 search has no level parameter).
		if o.Level != "" {
			q.Set("level", o.Level)
		}
		var resp memorySearchResponse
		if err := c.getJSON(ctx, "/memory/search", q, &resp); err != nil {
			return err
		}
		items := resp.Items
		if o.Level != "" {
			items = filterByLevel(items, o.Level)
		}
		if asJSON {
			return printJSON(stdout, map[string]any{"items": itemsOrEmpty(items), "count": len(items)})
		}
		rows := make([][]string, 0, len(items))
		for _, it := range items {
			rows = append(rows, []string{
				it.ID, it.Key, it.Level, it.Status,
				fmt.Sprintf("%.2f", it.Confidence),
				truncate(it.Content, 80),
			})
		}
		if len(rows) == 0 {
			fmt.Fprintln(stdout, "no memories found")
			return nil
		}
		printTable(stdout, []string{"ID", "KEY", "LEVEL", "STATUS", "CONF", "CONTENT"}, rows)
		return nil
	case "propose":
		if project == "" {
			return errors.New("memory propose requires a project (-p/--project or NEXUS_PROJECT)")
		}
		body := map[string]any{
			"key":        o.Key,
			"content":    strings.TrimSpace(o.Content),
			"project_id": project,
			"level":      o.Level,
			"source":     "cli:nexus",
		}
		var created memoryItemJSON
		if err := c.postJSON(ctx, "/memory", body, &created); err != nil {
			return err
		}
		if asJSON {
			return printJSON(stdout, &created)
		}
		fmt.Fprintf(stdout, "proposed %s\n", created.ID)
		printTable(stdout,
			[]string{"ID", "KEY", "LEVEL", "STATUS", "CONTENT"},
			[][]string{{created.ID, created.Key, created.Level, created.Status, truncate(created.Content, 80)}})
		return nil
	case "confirm":
		var out map[string]any
		err := c.postJSON(ctx, "/memory/"+url.PathEscape(o.ID)+"/confirm", map[string]any{}, &out)
		if err != nil {
			return notImplementedFor("memory confirm", err)
		}
		if asJSON {
			return printJSON(stdout, out)
		}
		fmt.Fprintf(stdout, "confirmed %s\n", o.ID)
		return nil
	case "reject":
		var out map[string]any
		err := c.postJSON(ctx, "/memory/"+url.PathEscape(o.ID)+"/reject", map[string]any{}, &out)
		if err != nil {
			return notImplementedFor("memory reject", err)
		}
		if asJSON {
			return printJSON(stdout, out)
		}
		fmt.Fprintf(stdout, "rejected %s\n", o.ID)
		return nil
	}
	return fmt.Errorf("unknown memory subcommand %q", o.Sub)
}

func filterByLevel(items []*memoryItemJSON, level string) []*memoryItemJSON {
	kept := make([]*memoryItemJSON, 0, len(items))
	for _, it := range items {
		if strings.EqualFold(it.Level, level) {
			kept = append(kept, it)
		}
	}
	return kept
}

func itemsOrEmpty(items []*memoryItemJSON) []*memoryItemJSON {
	if items == nil {
		return []*memoryItemJSON{}
	}
	return items
}
