// JSON-RPC line transport for the MCP server (issue #7).
//
// Wire format: one JSON-RPC 2.0 message per line on stdin, one response line
// per request on stdout, stdlib encoding/json only. This is a deliberate
// simplification of MCP streamable-HTTP/SSE: the tool semantics (names,
// inputs, outputs) match plan §1.4 exactly, but session management,
// progress notifications and content-block negotiation are out of scope
// (see ADR-007).
//
// Supported methods:
//
//	initialize                → {protocolVersion, capabilities, serverInfo}
//	ping                      → {}
//	tools/list                → {tools: [...8 plan tools...]}
//	tools/call {name, arguments} → handler result object, or JSON-RPC error
//	<tool-name> <args-object> → direct dispatch (convenience, same as call)
//
// Unknown methods → -32601; bad arguments → -32602 (from Server.Call);
// unparsable lines → -32700 with a null id. Notifications (no id) get no
// response — including notifications/initialized.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
)

// ProtocolVersion is the MCP protocol version claimed in initialize.
const ProtocolVersion = "2024-11-05"

// ServerName/ServerVersion identify this server in initialize.
const (
	ServerName    = "central-memory-mcp"
	ServerVersion = "0.1.0"
)

// Request is one inbound JSON-RPC 2.0 message. ID is a pointer-to-raw so
// notifications (absent id) are distinguishable from explicit nulls.
type Request struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method"`
	Params  json.RawMessage  `json:"params,omitempty"`
}

// RPCError is the JSON-RPC error object.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Response is one outbound JSON-RPC 2.0 message.
type Response struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id"`
	Result  any              `json:"result,omitempty"`
	Error   *RPCError        `json:"error,omitempty"`
}

func okResponse(id *json.RawMessage, result any) *Response {
	return &Response{JSONRPC: "2.0", ID: id, Result: result}
}

func errResponse(id *json.RawMessage, code int, msg string) *Response {
	return &Response{JSONRPC: "2.0", ID: id, Error: &RPCError{Code: code, Message: msg}}
}

func callErrResponse(id *json.RawMessage, cerr *CallError) *Response {
	return errResponse(id, cerr.Code, cerr.Message)
}

// toolsCallParams is the tools/call envelope.
type toolsCallParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// ServeStdio runs the line protocol until EOF or ctx cancellation: each
// non-blank line of r is one Request, each request (non-notification) gets
// one response line on w.
func (s *Server) ServeStdio(ctx context.Context, r io.Reader, w io.Writer) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 8<<20) // file_write payloads
	bw := bufio.NewWriter(w)
	defer func() { _ = bw.Flush() }()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if !sc.Scan() {
			return sc.Err()
		}
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		resp, ok := s.handleLine(ctx, line)
		if !ok {
			continue // notification: no response
		}
		out, err := json.Marshal(resp)
		if err != nil {
			return err
		}
		out = append(out, '\n')
		if _, err := bw.Write(out); err != nil {
			return err
		}
		if err := bw.Flush(); err != nil {
			return err
		}
	}
}

// handleLine processes one raw line; ok=false means "notification, send
// nothing".
func (s *Server) handleLine(ctx context.Context, line []byte) (resp *Response, ok bool) {
	var req Request
	dec := json.NewDecoder(bytes.NewReader(line))
	if err := dec.Decode(&req); err != nil {
		return errResponse(nil, CodeParseError, "parse error: "+firstLine(err.Error())), true
	}
	if req.Method == "" {
		return errResponse(req.ID, CodeInvalidRequest, "missing method"), true
	}
	if req.ID == nil {
		// Notification: acknowledged by silence (covers
		// notifications/initialized and any other fire-and-forget).
		return nil, false
	}
	switch req.Method {
	case "initialize":
		return okResponse(req.ID, map[string]any{
			"protocolVersion": ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": ServerName, "version": ServerVersion},
		}), true
	case "ping":
		return okResponse(req.ID, map[string]any{}), true
	case "tools/list":
		return okResponse(req.ID, map[string]any{"tools": s.Tools()}), true
	case "tools/call":
		var p toolsCallParams
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &p); err != nil {
				return errResponse(req.ID, CodeInvalidParams, "invalid tools/call params: "+firstLine(err.Error())), true
			}
		}
		if p.Name == "" {
			return errResponse(req.ID, CodeInvalidParams, "tools/call: name is required"), true
		}
		if p.Arguments == nil {
			p.Arguments = map[string]any{}
		}
		result, cerr := s.Call(ctx, p.Name, p.Arguments)
		if cerr != nil {
			return callErrResponse(req.ID, cerr), true
		}
		return okResponse(req.ID, result), true
	default:
		// Direct dispatch: method names the tool, params are its arguments.
		args := map[string]any{}
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &args); err != nil {
				return errResponse(req.ID, CodeInvalidParams, "invalid params: "+firstLine(err.Error())), true
			}
		}
		result, cerr := s.Call(ctx, req.Method, args)
		if cerr != nil {
			return callErrResponse(req.ID, cerr), true
		}
		return okResponse(req.ID, result), true
	}
}

func firstLine(s string) string {
	if i := bytes.IndexByte([]byte(s), '\n'); i >= 0 {
		return s[:i]
	}
	if len(s) > 200 {
		return s[:200]
	}
	return s
}
