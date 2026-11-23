package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"central-memory/internal/core"
)

func TestDemoCredentialUsesRealMintBindingAndMCP(t *testing.T) {
	srv, st, err := seed(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(srv.Handler())
	defer api.Close()
	owner := &core.HTTPCloud{BaseURL: api.URL, Token: st.SenderToken}
	const create = `{"name":"Cursor demo MCP","agent_id":"cursor"}`
	raw, err := owner.Do(t.Context(), http.MethodPost, "/auth/tokens", []byte(create))
	if err != nil {
		t.Fatal("token creation failed", err)
	}
	var minted struct {
		Token string `json:"token"`
		ID    string `json:"id"`
	}
	if err = json.Unmarshal(raw, &minted); err != nil || minted.Token == "" {
		t.Fatal("token response missing credential")
	}
	request := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"memory_search","arguments":{"project_id":"` + st.ProjectID + `","query":"cart"}}}`
	req, _ := http.NewRequest(http.MethodPost, api.URL+"/v1/agent/mcp", strings.NewReader(request))
	req.Header.Set("Authorization", "Bearer "+minted.Token)
	req.Header.Set("X-Nexus-Agent", "cursor")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	json.NewDecoder(res.Body).Decode(&body)
	res.Body.Close()
	b, _ := json.Marshal(body)
	if res.StatusCode != 200 || !strings.Contains(string(b), "Preserve the cart") {
		t.Fatal("minted demo credential did not retrieve memory")
	}
	req, _ = http.NewRequest(http.MethodPost, api.URL+"/v1/agent/mcp", strings.NewReader(request))
	req.Header.Set("Authorization", "Bearer "+minted.Token)
	req.Header.Set("X-Nexus-Agent", "opencode")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatal("agent binding was not enforced")
	}
	if _, err = owner.Do(t.Context(), http.MethodDelete, "/auth/tokens/"+minted.ID, nil); err != nil {
		t.Fatal(err)
	}
	client := &core.HTTPCloud{BaseURL: api.URL, Token: minted.Token}
	if _, err = client.Do(t.Context(), http.MethodGet, "/projects", nil); err == nil {
		t.Fatal("revoked demo token remained valid")
	}
}
