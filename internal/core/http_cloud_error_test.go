package core

import (
	"errors"
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
