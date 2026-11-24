package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"central-memory/internal/core"
)

func TestProductTourUsesRealMemoryMCPAndOrganizationHandlers(t *testing.T) {
	workspace, err := filepath.Abs("workspace")
	if err != nil {
		t.Fatal(err)
	}
	srv, st, err := seed(workspace, "")
	if err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(srv.Handler())
	defer api.Close()
	cloud := &core.HTTPCloud{BaseURL: api.URL, Token: st.SenderToken}
	if proof, err := memoryProof(t.Context(), cloud, st.ProjectID); err != nil || len(proof) != 2 {
		t.Fatalf("MCP proof: %v", err)
	}
	for _, tool := range []string{"memory_search", "project_knowledge"} {
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": map[string]any{"project_id": st.ProjectID, "query": "cart", "limit": 10}}})
		raw, err := cloud.Do(t.Context(), http.MethodPost, "/v1/agent/mcp", body)
		if err != nil {
			t.Fatalf("real %s: %v", tool, err)
		}
		if !strings.Contains(string(raw), "Preserve the cart") {
			t.Fatalf("%s did not retrieve the seeded decision: %s", tool, raw)
		}
		if tool == "project_knowledge" && strings.Contains(string(raw), "personal/review-style") {
			t.Fatal("project knowledge included personal data")
		}
	}
	raw, err := cloud.Do(t.Context(), http.MethodGet, "/orgs/"+st.OrganizationID, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{sender, recipient, "OWNER", "MEMBER", "Checkout"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("real organization response missing %q", want)
		}
	}
}

func TestDemoBrowserBootstrapIsOptionalAndRejectsForeignHosts(t *testing.T) {
	srv, st, err := seed(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		root, host string
		want       int
	}{
		{"", "127.0.0.1", http.StatusNotFound},
		{t.TempDir(), "127.0.0.1:47831", http.StatusOK},
		{t.TempDir(), "untrusted.example", http.StatusForbidden},
	} {
		req := httptest.NewRequest(http.MethodGet, "/demo/start", nil)
		req.Host = test.host
		rec := httptest.NewRecorder()
		demoWebHandler(srv.Handler(), st, test.root).ServeHTTP(rec, req)
		if rec.Code != test.want {
			t.Fatalf("host %s: got %d want %d", test.host, rec.Code, test.want)
		}
		if rec.Code == http.StatusOK && rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("bootstrap may be cached")
		}
		if rec.Code != http.StatusOK && strings.Contains(rec.Body.String(), st.SenderToken) {
			t.Fatal("rejected bootstrap leaked demo token")
		}
	}
}
