package authbrowser

import (
	"testing"
	"time"
)

func TestRedirectAndDeviceCode(t *testing.T) {
	if RedirectURI(true, "http://127.0.0.1:9/callback") != AppCallback {
		t.Fatal("app callback")
	}
	if RedirectURI(false, "http://127.0.0.1:9/callback") != "http://127.0.0.1:9/callback" {
		t.Fatal("loopback fallback")
	}
	now := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	dc, err := NewDeviceCode(now)
	if err != nil || len(dc.UserCode) != 8 || len(dc.DeviceCode) < 16 {
		t.Fatalf("device: %+v %v", dc, err)
	}
	if !dc.ExpiresAt.Equal(now.Add(15 * time.Minute)) {
		t.Fatal("expiry")
	}
}
