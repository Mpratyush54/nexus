package core

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
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

func TestHTTPCloudRejectsMissingCredentials(t *testing.T) {
	if _, err := (&HTTPCloud{}).Do(context.Background(), http.MethodGet, "/v1/test", nil); err != ErrOffline {
		t.Fatalf("err=%v", err)
	}
}
