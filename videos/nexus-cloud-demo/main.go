// Command nexus-cloud-demo prepares explicitly authorized, synthetic film data.
// It never changes login configuration, grants another person access, or deletes data.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"central-memory/internal/config"
	"central-memory/internal/core"
	"central-memory/internal/store"
)

const demoSlug = "nexus-film-demo-20261024"
const demoFolder = "nexus-film-checkout-demo"

type cloudAPI interface {
	Do(context.Context, string, string, []byte) ([]byte, error)
}
type receipt struct {
	OrganizationID   string   `json:"organization_id"`
	ProjectID        string   `json:"project_id"`
	OrganizationName string   `json:"organization_name"`
	ProjectName      string   `json:"project_name"`
	MemoryIDs        []string `json:"memory_ids"`
	MCPVerified      bool     `json:"mcp_memory_search_verified"`
}

func request(ctx context.Context, c cloudAPI, method, path string, input, output any) error {
	var body []byte
	var err error
	if input != nil {
		body, err = json.Marshal(input)
		if err != nil {
			return err
		}
	}
	raw, err := c.Do(ctx, method, path, body)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	if output != nil {
		return json.Unmarshal(raw, output)
	}
	return nil
}

func prepare(ctx context.Context, c cloudAPI, save func(receipt) error) (receipt, error) {
	return prepareWithOrganization(ctx, c, save, false)
}

func prepareWithOrganization(ctx context.Context, c cloudAPI, save func(receipt) error, existingOrg bool) (receipt, error) {
	var result receipt
	var orgs struct {
		Items []store.Organization `json:"items"`
	}
	if err := request(ctx, c, http.MethodGet, "/orgs", nil, &orgs); err != nil {
		return result, err
	}
	var org store.Organization
	if existingOrg {
		if len(orgs.Items) != 1 || orgs.Items[0].ID == "" {
			return result, errors.New("existing-organization mode requires exactly one organization; no writes attempted")
		}
		org = orgs.Items[0]
	} else {
		for _, item := range orgs.Items {
			if item.Slug == demoSlug {
				org = item
				break
			}
		}
	}
	if org.ID == "" {
		if err := request(ctx, c, http.MethodPost, "/orgs", map[string]string{"name": "Nexus Demo Studio · Film", "slug": demoSlug}, &org); err != nil {
			return result, err
		}
	}
	result.OrganizationID, result.OrganizationName = org.ID, org.Name
	if err := save(result); err != nil {
		return result, err
	}
	var detail struct {
		Projects []store.Project `json:"projects"`
	}
	if err := request(ctx, c, http.MethodGet, "/orgs/"+url.PathEscape(org.ID), nil, &detail); err != nil {
		return result, err
	}
	var project store.Project
	for _, item := range detail.Projects {
		if item.FolderName == demoFolder {
			project = item
			break
		}
	}
	if project.ID == "" {
		if err := request(ctx, c, http.MethodPost, "/orgs/"+url.PathEscape(org.ID)+"/projects", map[string]string{"folder_name": demoFolder, "display_name": "Checkout Recovery · DEMO"}, &project); err != nil {
			return result, err
		}
	}
	result.ProjectID, result.ProjectName = project.ID, project.DisplayName
	if err := save(result); err != nil {
		return result, err
	}
	var existing struct {
		Items []store.MemoryItem `json:"items"`
	}
	search := "/memory/search?project_id=" + url.QueryEscape(project.ID) + "&limit=100"
	if err := request(ctx, c, http.MethodGet, search, nil, &existing); err != nil {
		return result, err
	}
	seeds := []store.MemoryItem{
		{Key: "DEMO_CHECKOUT_20261024_CART", Content: "Synthetic demo decision: preserve the cart until payment is confirmed so a customer can recover from a failed checkout without rebuilding the order.", Scope: "decision", Level: "project", Category: "architecture", Tags: []string{"demo", "checkout", "pinned"}},
		{Key: "DEMO_CHECKOUT_20261024_REVIEW", Content: "Synthetic demo release rule: review the checkout recovery patch with a teammate and verify failure-path coverage before merging.", Scope: "constraint", Level: "project", Category: "conventions", Tags: []string{"demo", "review"}},
		{Key: "DEMO_CHECKOUT_20261024_HANDOFF", Content: "Synthetic demo handoff: include the original decision, the conversation and the recorded code changes when another agent or teammate picks up the task.", Scope: "pattern", Level: "project", Category: "general", Tags: []string{"demo", "handoff"}},
	}
	for _, seed := range seeds {
		seed.ProjectID, seed.Source = project.ID, "nexus-film-demo"
		var found store.MemoryItem
		for _, item := range existing.Items {
			if item.Key == seed.Key {
				found = item
				break
			}
		}
		if found.ID == "" {
			if err := request(ctx, c, http.MethodPost, "/memory", seed, &found); err != nil {
				return result, err
			}
		}
		result.MemoryIDs = append(result.MemoryIDs, found.ID)
		if err := save(result); err != nil {
			return result, err
		}
		if found.Status != "CONFIRMED" {
			if err := request(ctx, c, http.MethodPost, "/memory/"+url.PathEscape(found.ID)+"/confirm", map[string]any{}, nil); err != nil {
				return result, err
			}
		}
	}
	var rpc struct {
		Error  json.RawMessage `json:"error"`
		Result json.RawMessage `json:"result"`
	}
	call := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "memory_search", "arguments": map[string]any{"project_id": project.ID, "query": "DEMO_CHECKOUT_20261024_CART", "limit": 3}}}
	if err := request(ctx, c, http.MethodPost, "/v1/agent/mcp", call, &rpc); err != nil {
		return result, err
	}
	if len(rpc.Error) > 0 && string(rpc.Error) != "null" {
		return result, errors.New("MCP returned an error; no success claim recorded")
	}
	result.MCPVerified = strings.Contains(string(rpc.Result), "preserve the cart")
	if !result.MCPVerified {
		return result, errors.New("MCP did not return the synthetic cart decision")
	}
	return result, save(result)
}

func main() {
	apply := flag.Bool("apply", false, "create/reuse only the explicitly named synthetic film organization/project")
	existingOrg := flag.Bool("existing-org", false, "explicitly use the sole existing organization without modifying it or its members")
	out := flag.String("receipt", "", "required local receipt path; contains IDs, never credentials")
	flag.Parse()
	if !*apply || *out == "" {
		fmt.Fprintln(os.Stderr, "--apply and --receipt are required")
		os.Exit(2)
	}
	cfg, err := config.LoadFile()
	if err != nil || cfg.Token == "" {
		fmt.Fprintln(os.Stderr, "Saved Nexus sign-in is unavailable; credentials were not changed.")
		os.Exit(1)
	}
	u, err := url.Parse(strings.TrimRight(cfg.ServerURL, "/"))
	if err != nil || u.Scheme != "https" || u.Host != "api-nexus.pratyushes.dev" || u.User != nil || u.RawQuery != "" || u.Path != "" {
		fmt.Fprintln(os.Stderr, "Refusing an unexpected saved API origin; no writes attempted.")
		os.Exit(1)
	}
	c := &core.HTTPCloud{BaseURL: u.String(), Token: cfg.Token, Client: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	var me struct {
		ID string `json:"id"`
	}
	if err = request(ctx, c, http.MethodGet, "/users/me", nil, &me); err != nil || me.ID == "" || (cfg.UserID != "" && cfg.UserID != me.ID) {
		fmt.Fprintln(os.Stderr, "Saved sign-in could not be verified; no writes attempted.")
		os.Exit(1)
	}
	save := func(r receipt) error {
		if err := os.MkdirAll(filepath.Dir(*out), 0700); err != nil {
			return err
		}
		b, err := json.MarshalIndent(r, "", "  ")
		if err != nil {
			return err
		}
		return os.WriteFile(*out, b, 0600)
	}
	r, err := prepareWithOrganization(ctx, c, save, *existingOrg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Demo preparation stopped:", err)
		os.Exit(1)
	}
	encoded, _ := json.MarshalIndent(r, "", "  ")
	fmt.Println(string(encoded))
}
