package daemon

// Regression test for issue #155: daemon→server calls carry the
// configured server JWT as Authorization: Bearer.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPostJSONSendsServerToken(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	d, err := NewDaemon(t.TempDir(), "local-token")
	if err != nil {
		t.Fatal(err)
	}
	// No token configured: no Authorization header.
	if err := d.postJSON(context.Background(), srv.URL, "/x", map[string]any{}, nil); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "" {
		t.Fatalf("Authorization = %q without ServerToken, want empty", gotAuth)
	}
	// Token configured: Bearer attached.
	d.ServerToken = "server-jwt-abc"
	if err := d.postJSON(context.Background(), srv.URL, "/x", map[string]any{}, nil); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer server-jwt-abc" {
		t.Fatalf("Authorization = %q, want Bearer server-jwt-abc", gotAuth)
	}
}
