package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The server records use on GET /memory/search only when asked (browsing
// the Memory page must not count), so agent retrieval over REST opts in.
func TestHTTPStoreSearchOptsIntoUseTracking(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query().Get("record_use")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	defer srv.Close()
	if _, err := NewHTTPStore(srv.URL, "tok").SearchMemory(context.Background(), "p1", "deploy", nil, 5); err != nil {
		t.Fatal(err)
	}
	if got != "1" {
		t.Fatalf("record_use = %q, want 1", got)
	}
}
