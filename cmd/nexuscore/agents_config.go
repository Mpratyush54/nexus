//go:build nexuscorelib

package main

// Editing agent setup from the desktop Agents page: instruction (rules)
// files and MCP servers. Every write is backed up and atomic.
//
//	agents.config.rules_files {workspace} -> []RulesFile
//	agents.config.rules_read {path, workspace} -> RulesDoc
//	agents.config.rules_template {path, workspace} -> {content}
//	agents.config.rules_preview {path, workspace, content, etag} -> RulesPreview
//	agents.config.rules_save {path, workspace, content, etag, allow_managed, state_dir} -> RulesSaved
//	agents.config.mcp_templates {} -> []MCPTemplate
//	agents.config.mcp_from_template {template, values} -> ServerSpec
//	agents.config.mcp_parse {text} -> {servers: []ServerSpec}
//	agents.config.mcp_get {agent, scope, path, name, workspace, state_dir} -> ServerEdit
//	agents.config.mcp_save {agent, scope, path, original_name, spec, dry_run, workspace, state_dir} -> SaveMCPResult
//	agents.config.mcp_set_enabled {agent, scope, path, name, enabled, workspace, state_dir} -> ToggleResult
//	agents.config.mcp_forget {agent, scope, path, name, workspace, state_dir} -> {}
//	agents.config.mcp_test {agent, scope, path, name, original_name, spec?, timeout_ms, workspace, state_dir} -> MCPTestResult

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"central-memory/internal/agentsetup"
)

func agentsConfigCall(ctx context.Context, name string, args json.RawMessage) (any, error) {
	var req struct {
		Workspace    string                 `json:"workspace"`
		StateDir     string                 `json:"state_dir"`
		Path         string                 `json:"path"`
		Content      string                 `json:"content"`
		ETag         string                 `json:"etag"`
		AllowManaged bool                   `json:"allow_managed"`
		Agent        string                 `json:"agent"`
		Scope        string                 `json:"scope"`
		Name         string                 `json:"name"`
		OriginalName string                 `json:"original_name"`
		Spec         *agentsetup.ServerSpec `json:"spec"`
		DryRun       bool                   `json:"dry_run"`
		Enabled      bool                   `json:"enabled"`
		Text         string                 `json:"text"`
		Template     string                 `json:"template"`
		Values       map[string]string      `json:"values"`
		TimeoutMS    int                    `json:"timeout_ms"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, fmt.Errorf("invalid request: %w", err)
		}
	}
	o := agentsetup.EditOptions{Workspace: strings.TrimSpace(req.Workspace), StateDir: strings.TrimSpace(req.StateDir)}
	switch name {
	case "agents.config.rules_files":
		return agentsetup.RulesFiles(o), nil
	case "agents.config.rules_read":
		return agentsetup.ReadRules(o, req.Path)
	case "agents.config.rules_template":
		text, err := agentsetup.RulesTemplate(o, req.Path)
		return map[string]string{"content": text}, err
	case "agents.config.rules_preview":
		return agentsetup.PreviewRules(o, req.Path, req.Content, req.ETag)
	case "agents.config.rules_save":
		return agentsetup.SaveRules(o, req.Path, req.Content, req.ETag, req.AllowManaged)
	case "agents.config.mcp_templates":
		return agentsetup.MCPTemplates(), nil
	case "agents.config.mcp_from_template":
		return agentsetup.FromTemplate(req.Template, req.Values)
	case "agents.config.mcp_parse":
		servers, err := agentsetup.ParseMCP(req.Text)
		return map[string]any{"servers": servers}, err
	case "agents.config.mcp_get":
		return agentsetup.GetMCP(o, req.Agent, req.Scope, req.Path, req.Name)
	case "agents.config.mcp_save":
		if req.Spec == nil {
			return nil, fmt.Errorf("choose a server to save")
		}
		return agentsetup.SaveMCP(o, agentsetup.SaveMCPRequest{Agent: req.Agent, Scope: req.Scope, Path: req.Path,
			OriginalName: req.OriginalName, Spec: *req.Spec, DryRun: req.DryRun})
	case "agents.config.mcp_set_enabled":
		return agentsetup.SetMCPEnabled(o, req.Agent, req.Scope, req.Path, req.Name, req.Enabled)
	case "agents.config.mcp_forget":
		return map[string]any{}, agentsetup.ForgetParked(o, req.Agent, req.Scope, req.Path, req.Name)
	case "agents.config.mcp_test":
		return agentsetup.TestMCP(ctx, o, agentsetup.MCPTestRequest{Agent: req.Agent, Scope: req.Scope, Path: req.Path, Name: req.Name,
			OriginalName: req.OriginalName, Spec: req.Spec, TimeoutMS: req.TimeoutMS}), nil
	}
	return nil, fmt.Errorf("unknown method %s", name)
}
