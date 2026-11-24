//go:build nexuscorelib

package main

// The desktop Agents browser: everything each coding agent is set up with,
// the read-only content of one item, and removing one MCP server entry.
//
//	agents.setup.browse {workspace?} -> Inventory (all harnesses, nexus_mcp,
//	    header_keys, shared_with; no secret values)
//	agents.setup.read {agent, kind, name, path, workspace?} -> ItemContent
//	    (only items browse returned; MCP entries masked, never the whole config)
//	agents.setup.mcp_remove {agent, name, path, workspace?, dry_run}
//	    -> RemoveResult (backs the config up first)

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"central-memory/internal/agentsetup"
)

func agentsBrowseCall(name string, args json.RawMessage) (any, error) {
	var req struct {
		Agent     string `json:"agent"`
		Kind      string `json:"kind"`
		Name      string `json:"name"`
		Path      string `json:"path"`
		Workspace string `json:"workspace"`
		DryRun    bool   `json:"dry_run"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, fmt.Errorf("invalid request: %w", err)
		}
	}
	opts := agentsetup.BrowseOptions{Workspace: strings.TrimSpace(req.Workspace)}
	switch name {
	case "agents.setup.browse":
		return agentsetup.Browse(opts), nil
	case "agents.setup.read":
		if req.Agent == "" || req.Kind == "" || req.Path == "" {
			return nil, errors.New("choose an item to show")
		}
		return agentsetup.ReadItem(opts, req.Agent, req.Kind, req.Name, req.Path)
	case "agents.setup.mcp_remove":
		if req.Agent == "" || req.Name == "" || req.Path == "" {
			return nil, errors.New("choose an MCP server to remove")
		}
		return agentsetup.RemoveMCP(opts, req.Agent, req.Name, req.Path, req.DryRun)
	}
	return nil, fmt.Errorf("unknown method %s", name)
}
