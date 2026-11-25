package core

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCloudErrorIsReadable(t *testing.T) {
	err := error(newCloudError(402, "402 Payment Required",
		[]byte(`{"error":{"code":402,"message":"plan limit reached: orgs (1/1) on Free \u2014 upgrade to continue"}}`)))
	if got := err.Error(); got != "Plan limit reached: orgs (1/1) on Free \u2014 upgrade to continue" {
		t.Fatalf("message = %q", got)
	}
	var ce *CloudError
	if !errors.As(err, &ce) || ce.Status != 402 {
		t.Fatalf("status lost: %#v", err)
	}
	if got := newCloudError(409, "409 Conflict", []byte(`{"error":"an organization needs at least one owner"}`)).Error(); got != "An organization needs at least one owner" {
		t.Fatalf("string error = %q", got)
	}
	if got := newCloudError(502, "502 Bad Gateway", []byte("<html>oops</html>")).Error(); got != "502 Bad Gateway" {
		t.Fatalf("html body leaked: %q", got)
	}
	if got := newCloudError(400, "400 Bad Request", []byte(`{"error":"équipe introuvable"}`)).Error(); got != "Équipe introuvable" {
		t.Fatalf("multi-byte first rune = %q", got)
	}
	if got := newCloudError(0, "", nil).Error(); got != "" {
		t.Fatalf("empty message = %q", got)
	}
}

func TestCloudErrorCarriesRequestID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", "7K3M9QW2HX4D")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"code":500,"message":"could not list members","request_id":"7K3M9QW2HX4D"}}`))
	}))
	defer srv.Close()
	c := &HTTPCloud{BaseURL: srv.URL, Token: "nxs_secret_token"}
	_, err := c.Do(context.Background(), http.MethodGet, "/v1/orgs?q=private", nil)
	var ce *CloudError
	if !errors.As(err, &ce) || ce.RequestID != "7K3M9QW2HX4D" {
		t.Fatalf("request id lost: %#v", err)
	}
	if got := err.Error(); got != "Could not list members (ref 7K3M9QW2HX4D)" {
		t.Fatalf("message = %q", got)
	}
}
