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

// episodeItemJSON mirrors the wire shape of store.Episode without importing
// server internals.
type episodeItemJSON struct {
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	EpisodeType  string   `json:"episode_type"`
	Status       string   `json:"status"`
	Trigger      string   `json:"trigger"`
	RootCause    string   `json:"root_cause"`
	Resolution   string   `json:"resolution"`
	ErrorPattern []string `json:"-"`
}

type episodeSearchResponse struct {
	Items []*episodeItemJSON `json:"items"`
	Count int                `json:"count"`
}

type episodeOptions struct {
	Sub          string
	Query        string
	Project      string
	ErrorPattern string
	Limit        int
	JSON         bool
}

// parseEpisodeArgs parses `nexus episode <sub> ...`. Pure: no network, no exit.
func parseEpisodeArgs(args []string) (episodeOptions, error) {
	var o episodeOptions
	if len(args) == 0 {
		return o, errors.New("episode requires a subcommand: list|search")
	}
	o.Sub = args[0]
	rest := args[1:]

	fs := flag.NewFlagSet("nexus episode "+o.Sub, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.Project, "project", "", "project ID")
	fs.StringVar(&o.Project, "p", "", "project ID (shorthand)")
	fs.IntVar(&o.Limit, "limit", 20, "max results")
	fs.IntVar(&o.Limit, "n", 20, "max results (shorthand)")
	fs.BoolVar(&o.JSON, "json", false, "emit JSON instead of tables")
	if o.Sub == "search" {
		fs.StringVar(&o.ErrorPattern, "error-pattern", "", "exact error pattern match (defaults to the query)")
	}

	switch o.Sub {
	case "list":
		if err := fs.Parse(rest); err != nil {
			return o, err
		}
		if len(fs.Args()) > 0 {
			return o, fmt.Errorf("episode list takes no positional arguments, got %q", strings.Join(fs.Args(), " "))
		}
		if o.Limit <= 0 {
			return o, errors.New("episode list: --limit must be positive")
		}
	case "search":
		if err := fs.Parse(rest); err != nil {
			return o, err
		}
		o.Query = strings.Join(fs.Args(), " ")
		if strings.TrimSpace(o.Query) == "" {
			return o, errors.New(`episode search requires a query, e.g. nexus episode search -p <id> "ConnectionTimeout"`)
		}
		if o.Limit <= 0 {
			return o, errors.New("episode search: --limit must be positive")
		}
		if o.ErrorPattern == "" {
			o.ErrorPattern = o.Query
		}
	default:
		return o, fmt.Errorf("unknown episode subcommand %q (want list|search)", o.Sub)
	}
	return o, nil
}

func runEpisode(ctx context.Context, cfg Config, args []string, stdout io.Writer) error {
	o, err := parseEpisodeArgs(args)
	if err != nil {
		return err
	}
	asJSON := cfg.JSON || o.JSON
	project := firstNonEmpty(o.Project, cfg.ProjectID)
	if project == "" {
		return errors.New("episode requires a project (-p/--project or NEXUS_PROJECT)")
	}
	c := newAPIClient(cfg)

	q := url.Values{}
	q.Set("project_id", project)
	q.Set("limit", fmt.Sprint(o.Limit))
	if o.Sub == "search" {
		// The server matches error_pattern against known patterns OR q
		// against the episode narrative; sending the query on both covers
		// the "how did we fix this before?" lookup in one call.
		q.Set("q", o.Query)
		q.Set("error_pattern", o.ErrorPattern)
	}
	var resp episodeSearchResponse
	if err := c.getJSON(ctx, "/episodes/search", q, &resp); err != nil {
		return err
	}
	items := resp.Items
	if items == nil {
		items = []*episodeItemJSON{}
	}
	if asJSON {
		return printJSON(stdout, map[string]any{"items": items, "count": len(items)})
	}
	if len(items) == 0 {
		fmt.Fprintln(stdout, "no episodes found")
		return nil
	}
	rows := make([][]string, 0, len(items))
	for _, ep := range items {
		rows = append(rows, []string{
			ep.ID, ep.EpisodeType, ep.Status,
			truncate(ep.Title, 60),
			truncate(firstNonEmpty(ep.RootCause, ep.Trigger), 60),
		})
	}
	printTable(stdout, []string{"ID", "TYPE", "STATUS", "TITLE", "CAUSE/TRIGGER"}, rows)
	return nil
}
