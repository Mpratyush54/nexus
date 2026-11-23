package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

type proofAPI interface {
	Do(context.Context, string, string, []byte) ([]byte, error)
}

// Freezes actual JSON-RPC responses, not simulated client output. Credentials
// never enter the proof. This fixture contains only explicitly synthetic data.
func memoryProof(ctx context.Context, api proofAPI, project string) (map[string]json.RawMessage, error) {
	proof := make(map[string]json.RawMessage)
	for _, tool := range []string{"memory_search", "project_knowledge"} {
		body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": map[string]any{"project_id": project, "query": "cart", "limit": 10}}})
		if err != nil {
			return nil, err
		}
		raw, err := api.Do(ctx, http.MethodPost, "/v1/agent/mcp", body)
		if err != nil {
			return nil, err
		}
		var rpc struct {
			Error  json.RawMessage `json:"error"`
			Result json.RawMessage `json:"result"`
		}
		if err = json.Unmarshal(raw, &rpc); err != nil {
			return nil, err
		}
		if (len(rpc.Error) > 0 && string(rpc.Error) != "null") || !strings.Contains(string(rpc.Result), "Preserve the cart") {
			return nil, errors.New("MCP did not retrieve the real synthetic cart decision")
		}
		if tool == "project_knowledge" && strings.Contains(string(rpc.Result), "personal/review-style") {
			return nil, errors.New("project knowledge unexpectedly included personal memory")
		}
		proof[tool] = append(json.RawMessage(nil), raw...)
	}
	return proof, nil
}
