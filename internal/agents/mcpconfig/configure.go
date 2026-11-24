// Package mcpconfig rewrites agent MCP configs to remote HTTPS or the
// stdio mcp-proxy (spec 7.5). Cursor uses the remote URL; Antigravity uses
// the proxy until its header bug is fixed.
package mcpconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"central-memory/internal/mcpproxy"
)

const (
	defaultServer = "https://api-nexus.pratyushes.dev"
	serverName    = "nexus"
)

// Options control agents.configure_mcp.
type Options struct {
	// Agent is "cursor", "antigravity", or empty (defaults to cursor).
	Agent string
	// Path is the mcp.json (or mcp_config.json) to write. Empty uses
	// ~/.cursor/mcp.json under Home (or $HOME).
	Path string
	// Home overrides the user home for default path resolution.
	Home string
	// ServerURL is the cloud API base. Empty uses the default host.
	ServerURL string
	// ProjectID is written into headers / proxy env as X-Nexus-Project.
	ProjectID string
	// Transport forces "remote" or "proxy". Empty picks by agent.
	Transport string
	// ProxyCommand is the executable for proxy mode. Empty resolves it with
	// mcpproxy.Locate: the app's bundled nexus-mcp-proxy first, then a CLI
	// on PATH. An explicit command defaults to ProxyArgs ["mcp-proxy"].
	ProxyCommand string
	// ProxyArgs are args after the command.
	ProxyArgs []string
	// AppDir holds the bundled proxy; empty uses the running executable's
	// folder (Nexus.exe inside the desktop app).
	AppDir string
	// LookPath is the PATH fallback; nil uses exec.LookPath.
	LookPath func(string) (string, error)
}

// Result is what agents.configure_mcp returns.
type Result struct {
	Agent     string `json:"agent"`
	Path      string `json:"path"`
	Transport string `json:"transport"`
	MCPURL    string `json:"mcp_url,omitempty"`
	Command   string `json:"command,omitempty"`
}

// Configure rewrites the agent MCP config file for remote Nexus MCP.
func Configure(opts Options) (Result, error) {
	agent := strings.ToLower(strings.TrimSpace(opts.Agent))
	if agent == "" {
		agent = "cursor"
	}
	transport := strings.ToLower(strings.TrimSpace(opts.Transport))
	if transport == "" {
		if agent == "antigravity" {
			transport = "proxy"
		} else {
			transport = "remote"
		}
	}
	if transport != "remote" && transport != "proxy" {
		return Result{}, fmt.Errorf("mcpconfig: transport must be remote or proxy")
	}

	path := strings.TrimSpace(opts.Path)
	if path == "" {
		home := strings.TrimSpace(opts.Home)
		if home == "" {
			var err error
			home, err = os.UserHomeDir()
			if err != nil {
				return Result{}, fmt.Errorf("mcpconfig: home: %w", err)
			}
		}
		path = filepath.Join(home, ".cursor", "mcp.json")
	}

	base := strings.TrimSuffix(strings.TrimSpace(opts.ServerURL), "/")
	if base == "" {
		base = defaultServer
	}
	mcpURL := base + "/v1/agent/mcp"

	entry := map[string]any{}
	res := Result{Agent: agent, Path: path, Transport: transport, MCPURL: mcpURL}
	switch transport {
	case "remote":
		headers := map[string]string{
			"Authorization": "Bearer ${NEXUS_TOKEN}",
		}
		if pid := strings.TrimSpace(opts.ProjectID); pid != "" {
			headers["X-Nexus-Project"] = pid
		}
		entry["url"] = mcpURL
		entry["headers"] = headers
	case "proxy":
		cmd := strings.TrimSpace(opts.ProxyCommand)
		args := opts.ProxyArgs
		if cmd == "" {
			appDir := strings.TrimSpace(opts.AppDir)
			if appDir == "" {
				appDir = mcpproxy.DefaultAppDir()
			}
			argv, err := mcpproxy.Locate(appDir, opts.LookPath)
			if err != nil {
				return Result{}, fmt.Errorf("mcpconfig: %w", err)
			}
			cmd = argv[0]
			if len(args) == 0 {
				args = argv[1:]
			}
		} else if len(args) == 0 {
			args = []string{"mcp-proxy"}
		}
		if args == nil {
			args = []string{}
		}
		env := map[string]string{}
		if pid := strings.TrimSpace(opts.ProjectID); pid != "" {
			env["NEXUS_PROJECT"] = pid
		}
		if base != defaultServer {
			env["NEXUS_SERVER"] = base
		}
		entry["command"] = cmd
		entry["args"] = args
		if len(env) > 0 {
			entry["env"] = env
		}
		res.Command = cmd
		res.MCPURL = ""
	}

	doc, err := loadOrEmpty(path)
	if err != nil {
		return Result{}, err
	}
	servers, _ := doc["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}
	servers[serverName] = entry
	doc["mcpServers"] = servers

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return Result{}, err
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return Result{}, err
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return Result{}, err
	}
	return res, nil
}

func loadOrEmpty(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	if len(bytesTrim(raw)) == 0 {
		return map[string]any{}, nil
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("mcpconfig: parse %s: %w", path, err)
	}
	if doc == nil {
		doc = map[string]any{}
	}
	return doc, nil
}

func bytesTrim(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}
