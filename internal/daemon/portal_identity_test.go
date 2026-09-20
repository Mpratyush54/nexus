package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPortalIdentityGateBlocksMismatch(t *testing.T) {
	d := &Daemon{UserID: "user-desktop", Root: t.TempDir(), MachineID: "m1"}
	proxy := NewCORSProxy(d)
	h := proxy.Handler()

	req := httptest.NewRequest(http.MethodGet, "/local/harvest", nil)
	req.Header.Set("Origin", "https://nexus.pratyushes.dev")
	req.Header.Set(HeaderPortalUserID, "user-portal")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	var body map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if !strings.Contains(body["error"], "mismatch") {
		t.Fatalf("error = %q, want mismatch", body["error"])
	}
}

func TestPortalIdentityGateAllowsMatch(t *testing.T) {
	d := &Daemon{UserID: "user-same", Root: t.TempDir(), MachineID: "m1"}
	proxy := NewCORSProxy(d)
	h := proxy.Handler()

	req := httptest.NewRequest(http.MethodGet, "/local/harvest", nil)
	req.Header.Set("Origin", "https://nexus.pratyushes.dev")
	req.Header.Set(HeaderPortalUserID, "user-same")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestPortalIdentityGateAllowsLocalStatusPage(t *testing.T) {
	d := &Daemon{UserID: "user-desktop", Root: t.TempDir(), MachineID: "m1"}
	proxy := NewCORSProxy(d)
	h := proxy.Handler()

	// No Origin → local status UI / curl
	req := httptest.NewRequest(http.MethodGet, "/local/harvest", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("local call status = %d, want 200", rec.Code)
	}
}

func TestStatusRemainsOpenWithoutPortalHeader(t *testing.T) {
	d := &Daemon{UserID: "user-desktop", Root: t.TempDir(), MachineID: "m1"}
	proxy := NewCORSProxy(d)
	h := proxy.Handler()

	req := httptest.NewRequest(http.MethodGet, "/local/status", nil)
	req.Header.Set("Origin", "https://nexus.pratyushes.dev")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status probe = %d, want 200 so portal can detect mismatch", rec.Code)
	}
}
