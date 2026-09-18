package server

import (
	"net/http"
	"testing"

	"central-memory/internal/store"
)

func TestPlatformAdminRequired(t *testing.T) {
	s := newTestServer()
	alice := loginAs(t, s, "alice")
	rec := doJSON(t, s, http.MethodGet, "/admin/overview", alice, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin overview = %d, want 403", rec.Code)
	}
}

func TestPlatformAdminEnvBootstrapAndRelease(t *testing.T) {
	t.Setenv("PLATFORM_ADMIN_USERNAMES", "alice")
	t.Setenv("AWS_REGION", "ap-south-1")
	s := newTestServer()
	alice := loginAs(t, s, "alice")

	rec := doJSON(t, s, http.MethodGet, "/admin/overview", alice, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("overview = %d %s", rec.Code, rec.Body.String())
	}
	var overview map[string]any
	decodeBody(t, rec, &overview)
	if overview["role"] != "SUPER_ADMIN" {
		t.Fatalf("role = %+v", overview["role"])
	}

	bob := loginAs(t, s, "bob")
	rec = doJSON(t, s, http.MethodPut, "/admin/users/bob", alice, map[string]any{
		"is_platform_admin": true,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("grant bob = %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodGet, "/admin/overview", bob, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("bob overview after grant = %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodPost, "/admin/releases", alice, map[string]any{
		"app":     "cli",
		"version": "0.2.0",
		"channel": "stable",
		"notes":   "first desktop CLI channel",
		"artifacts": []map[string]any{
			{"os": "windows", "arch": "amd64", "url": "https://example.test/nexus-windows-amd64.exe", "filename": "nexus-windows-amd64.exe"},
			{"os": "linux", "arch": "amd64", "url": "https://example.test/nexus-linux-amd64", "filename": "nexus-linux-amd64"},
		},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("publish = %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodGet, "/platform/releases/latest?app=cli", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("latest = %d %s", rec.Code, rec.Body.String())
	}
	var latest store.AppRelease
	decodeBody(t, rec, &latest)
	if latest.Version != "0.2.0" || latest.App != "cli" || len(latest.Artifacts) != 2 {
		t.Fatalf("latest = %+v", latest)
	}

	rec = doJSON(t, s, http.MethodGet, "/version", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("version = %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodPost, "/admin/releases/cli/0.2.0/yank", alice, map[string]any{})
	if rec.Code != http.StatusOK {
		t.Fatalf("yank = %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodGet, "/platform/releases/latest?app=cli", "", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("yanked latest = %d, want 404", rec.Code)
	}

	rec = doJSON(t, s, http.MethodPut, "/admin/users/alice", bob, map[string]any{
		"is_platform_admin": false,
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("revoke env admin = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
}

func TestHealthzIncludesVersion(t *testing.T) {
	s := newTestServer()
	rec := doJSON(t, s, http.MethodGet, "/healthz", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz = %d", rec.Code)
	}
	var body map[string]any
	decodeBody(t, rec, &body)
	if body["ok"] != true {
		t.Fatalf("ok = %+v", body["ok"])
	}
	if _, ok := body["version"]; !ok {
		t.Fatalf("missing version: %+v", body)
	}
}
