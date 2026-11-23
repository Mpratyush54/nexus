package continuex

import (
	"context"
	"encoding/json"
	"time"

	"central-memory/internal/usage"
)

// LiveSession is one open chat with a locally installed agent.
type LiveSession interface {
	Send(ctx context.Context, text string) error               // Queue a user message, including before init.
	Approve(requestID string, allow bool, reason string) error // Answer a pending permission request.
	Interrupt() error                                          // Stop the current turn, keeping the session open.
	Events() <-chan LiveEvent                                  // Closes after process exit and queued events drain.
	NativeID() string                                          // Empty until the agent reports its native session ID.
	Close() error                                              // Terminate and wait for exit, with a bounded grace period.
}

type LiveEvent struct {
	Kind       string // status, text_delta, text, tool_start, tool_end, approval, turn_done, error, exited
	Text       string
	ToolID     string
	ToolName   string
	ToolInput  json.RawMessage
	ToolOutput string // Redacted tool result, capped at 64 KiB.
	IsError    bool
	RequestID  string
	NativeID   string
	CostUSD    float64
	// Usage is per model: the turn so far on "usage" events, the whole
	// turn on turn_done. Empty when the agent reports none.
	Usage []usage.Entry
	At    time.Time
	// ParentToolID links work done inside a sub-agent (Claude's Task tool,
	// Codex collab agents) to the tool call that started it.
	ParentToolID string
}

// MCPServer is an extra MCP server handed to the agent for this session,
// e.g. Nexus memory (remote, HTTP). Header values are secrets: drivers pass
// them to the agent but never log or emit them.
type MCPServer struct {
	Name    string
	URL     string
	Headers map[string]string
	// AutoAllow lists tool names (without prefix) the agent may call without
	// asking, where the agent supports that (read-only memory lookups).
	AutoAllow []string
}

type LiveOptions struct {
	Workspace      string      // Required existing directory; becomes cmd.Dir.
	ResumeID       string      // Optional native session ID.
	MCPServers     []MCPServer // Optional extra MCP servers (Nexus memory).
	Model          string      // Optional; validated using WithModel's rules.
	PermissionMode string      // Empty, acceptEdits, or plan; never bypassPermissions.
	SystemContext  string      // Optional context passed via --append-system-prompt.
}
