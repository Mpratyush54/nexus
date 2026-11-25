package core

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHTTPCloudAddsBearerAndPreservesMethodBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/test" || r.Header.Get("Authorization") != "Bearer token" {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"ok":true}` {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	defer srv.Close()

	raw, err := (&HTTPCloud{BaseURL: srv.URL, Token: "token"}).Do(context.Background(), http.MethodPost, "/v1/test", []byte(`{"ok":true}`))
	if err != nil || string(raw) != `{"items":[]}` {
		t.Fatalf("raw=%s err=%v", raw, err)
	}
}

func TestHTTPCloudTimeoutPerRequest(t *testing.T) {
	// The route that projects.brief_refresh maps to must get the long limit.
	method, path, _, err := cloudRequest("projects.brief_refresh", json.RawMessage(`{"project_id":"p 1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := cloudTimeout(method, path); got != 120*time.Second {
		t.Fatalf("brief refresh timeout = %v", got)
	}
	cases := []struct {
		method, path string
		want         time.Duration
	}{
		{http.MethodGet, "/v1/projects/p1/brief", 30 * time.Second},
		{http.MethodGet, "/v1/projects/p1/brief/refresh", 30 * time.Second},
		{http.MethodPost, "/v1/projects/p1/memories", 30 * time.Second},
		{http.MethodPost, "/v1/test", 30 * time.Second},
		{http.MethodGet, "/v1/blobs/abc", 2 * time.Minute},
	}
	for _, c := range cases {
		if got := cloudTimeout(c.method, c.path); got != c.want {
			t.Fatalf("%s %s timeout = %v, want %v", c.method, c.path, got, c.want)
		}
	}
}

func TestHTTPCloudRejectsMissingCredentials(t *testing.T) {
	if _, err := (&HTTPCloud{}).Do(context.Background(), http.MethodGet, "/v1/test", nil); err != ErrOffline {
		t.Fatalf("err=%v", err)
	}
}
