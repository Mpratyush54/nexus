// Package mcp exposes the Central Memory control plane to pull-model AI
// coding agents (Claude Code, OpenCode, Antigravity) via the Model Context
// Protocol (MCP).
//
// Transport: stdlib-only JSON-RPC 2.0 over stdio (newline-delimited JSON).
// No SSE, no HTTP server, no third-party MCP SDK — the daemon spawns this
// server as a child process and speaks to it on stdin/stdout.
//
// Deliberately NOT implemented: MCP sampling (roots/sampling/*). Per
// implementation-plan.md Phase 1.4 / 2.2, all memory extraction happens
// silently via daemon-side transcript harvesting; sampling would require
// user approval, burn context tokens, and disrupt the user's workflow.
// The only extraction affordances here are a zero-cost piggyback
// reflection_hint on every memory_search response and the voluntary
// memory_reflect tool, which agents may call but are never forced to.
//
// See docs/decisions/2026-09-17-mcp-server.md for the full rationale.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const (
	// JSONRPCVersion is the only JSON-RPC version this server speaks.
	JSONRPCVersion = "2.0"

	// ProtocolVersion advertises MCP protocol compatibility to hosts.
	ProtocolVersion = "2024-11-05"

	// ServerName and ServerVersion identify this server in initialize.
	ServerName    = "central-memory"
	ServerVersion = "0.1.0"

	// DefaultTokenBudget caps memory_search context output (chars, ~4 chars/token).
	DefaultTokenBudget = 4000

	// maxScanBytes bounds a single stdio frame (10 MiB).
	maxScanBytes = 10 * 1024 * 1024
)

// JSON-RPC 2.0 error codes.
const (
	ErrParse          = -32700
	ErrInvalidRequest = -32600
	ErrMethodNotFound = -32601
	ErrInvalidParams  = -32602
	ErrInternal       = -32603
)

// Request is a JSON-RPC 2.0 request. ID is a pointer so notifications
// (requests without an id) are distinguishable: ID == nil means "do not reply".
type Request struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method"`
	Params  json.RawMessage  `json:"params,omitempty"`
}

// RPCError is a JSON-RPC 2.0 error object.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("json-rpc %d: %s", e.Code, e.Message) }

// Response is a JSON-RPC 2.0 response. Exactly one of Result / Error is set.
type Response struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Result  any              `json:"result,omitempty"`
	Error   *RPCError        `json:"error,omitempty"`
}

// toolsCallParams are the params for a tools/call request.
type toolsCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// Config carries the single-user skeleton's ambient context. In Phase 1
// there is one project / one workspace; multi-session scoping arrives later.
type Config struct {
	ProjectID     string
	ProjectName   string
	Branch        string
	CommitSHA     string
	WorkspacePath string
	Dirty         bool
	// TokenBudget caps memory_search context output in chars. <=0 means default.
	TokenBudget int
}

// Server is a JSON-RPC 2.0 MCP server. It is safe for concurrent use;
// all mutable state lives in the injected Store.
type Server struct {
	store Store
	cfg   Config
}

// NewServer wires a Server to a Store. The store is held behind the narrow
// Store interface declared in tools.go, so test fakes and the future
// Postgres-backed adapter are interchangeable without touching this package.
func NewServer(st Store, cfg Config) *Server {
	if cfg.TokenBudget <= 0 {
		cfg.TokenBudget = DefaultTokenBudget
	}
	return &Server{store: st, cfg: cfg}
}

// tokenBudget reports the effective context budget in chars.
func (s *Server) tokenBudget() int {
	if s.cfg.TokenBudget <= 0 {
		return DefaultTokenBudget
	}
	return s.cfg.TokenBudget
}

func okResult(id *json.RawMessage, result any) *Response {
	return &Response{JSONRPC: JSONRPCVersion, ID: id, Result: result}
}

func errResult(id *json.RawMessage, code int, msg string) *Response {
	return &Response{JSONRPC: JSONRPCVersion, ID: id, Error: &RPCError{Code: code, Message: msg}}
}

// Handle processes one raw JSON-RPC frame and returns the response.
// It returns nil for notifications (no id) — the caller must not reply.
func (s *Server) Handle(ctx context.Context, frame json.RawMessage) *Response {
	var req Request
	dec := json.NewDecoder(strings.NewReader(string(frame)))
	dec.UseNumber()
	if err := dec.Decode(&req); err != nil {
		return errResult(nil, ErrParse, "parse error: "+err.Error())
	}
	if req.JSONRPC != JSONRPCVersion || req.Method == "" {
		return errResult(req.ID, ErrInvalidRequest, "invalid request: jsonrpc must be \"2.0\" with a method")
	}
	// Notification: acknowledge by silence.
	if req.ID == nil {
		s.dispatch(ctx, &req) // fire-and-forget side effects only
		return nil
	}

	return s.dispatch(ctx, &req)
}

func (s *Server) dispatch(ctx context.Context, req *Request) *Response {
	switch req.Method {
	case "initialize":
		return okResult(req.ID, map[string]any{
			"protocolVersion": ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": ServerName, "version": ServerVersion},
		})
	case "notifications/initialized":
		return okResult(req.ID, map[string]any{})
	case "tools/list":
		return okResult(req.ID, map[string]any{"tools": ListTools()})
	case "tools/call":
		var p toolsCallParams
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &p); err != nil {
				return errResult(req.ID, ErrInvalidParams, "invalid tools/call params: "+err.Error())
			}
		}
		if p.Name == "" {
			return errResult(req.ID, ErrInvalidParams, "missing required field: name")
		}
		result, rpcErr := s.CallTool(ctx, p.Name, p.Arguments)
		if rpcErr != nil {
			return &Response{JSONRPC: JSONRPCVersion, ID: req.ID, Error: rpcErr}
		}
		return okResult(req.ID, result)
	default:
		return errResult(req.ID, ErrMethodNotFound, "method not found: "+req.Method)
	}
}

// Serve runs the stdio loop: one JSON-RPC frame per line on r, responses on
// w. It returns when r hits EOF or ctx is cancelled. Malformed frames yield
// a parse-error response without killing the loop.
func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), maxScanBytes)
	out := bufio.NewWriter(w)
	defer out.Flush()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if !sc.Scan() {
			if err := sc.Err(); err != nil {
				return fmt.Errorf("mcp stdio read: %w", err)
			}
			return nil // EOF: host closed stdin
		}
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		resp := s.Handle(ctx, json.RawMessage(line))
		if resp == nil {
			continue // notification: no reply
		}
		raw, err := json.Marshal(resp)
		if err != nil {
			raw = []byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32603,"message":"internal error"}}`)
		}
		if _, err := out.Write(append(raw, '\n')); err != nil {
			return fmt.Errorf("mcp stdio write: %w", err)
		}
		out.Flush()
	}
}
