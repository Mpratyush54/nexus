package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"central-memory/internal/server"
)

// Audit coverage for cmd/server/main.go: fail-closed stubStore, the 503
// login shim, healthz pass-through, and getenv fallback.

func TestAuditServerStubStoreFailClosed(t *testing.T) {
	ctx := t.Context()
	st := stubStore{}
	cases := map[string]func() error{
		"ResolveProject": func() error { _, err := st.ResolveProject(ctx, "u", "c", "f"); return err },
		"GetProject":     func() error { _, err := st.GetProject(ctx, "id"); return err },
		"RegisterWorkspace": func() error {
			return st.RegisterWorkspace(ctx, nil)
		},
		"Heartbeat": func() error { return st.Heartbeat(ctx, "w", "b", "c", false) },
		"GetActiveWorkspace": func() error {
			_, err := st.GetActiveWorkspace(ctx, "p")
			return err
		},
		"CreateMemoryItem": func() error { return st.CreateMemoryItem(ctx, nil) },
		"GetMemoryItem": func() error {
			_, err := st.GetMemoryItem(ctx, "id")
			return err
		},
		"SearchMemory": func() error {
			_, err := st.SearchMemory(ctx, "p", "q", nil, 1)
			return err
		},
		"ConfirmMemory": func() error { return st.ConfirmMemory(ctx, "id", "by") },
		"CreateEpisode": func() error { return st.CreateEpisode(ctx, nil) },
		"GetEpisode": func() error {
			_, err := st.GetEpisode(ctx, "id")
			return err
		},
		"SearchEpisodes": func() error {
			_, err := st.SearchEpisodes(ctx, "p", "e", "q", 1)
			return err
		},
		"ResolveEpisode": func() error { return st.ResolveEpisode(ctx, "id", "r", "v", "by") },
		"AppendEvent":    func() error { return st.AppendEvent(ctx, nil) },
		"ListEvents": func() error {
			_, err := st.ListEvents(ctx, "p", 0, 1)
			return err
		},
		"Subscribe": func() error {
			_, _, err := st.Subscribe(ctx, "p")
			return err
		},
	}
	for name, fn := range cases {
		if err := fn(); !errors.Is(err, errStorePending) {
			t.Errorf("%s: err = %v, want errStorePending", name, err)
		}
	}
	if errStorePending.Error() == "" {
		t.Error("errStorePending must carry a message")
	}
}

func TestAuditServerLogin503(t *testing.T) {
	h := newHandler("audit-secret-1234567890", testConfig(t))
	req := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(`{"username":"a","password":"b"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("login status = %d, want 503", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "user store adapter pending") {
		t.Fatalf("login body must mention pending adapter, got %s", body)
	}
	if !strings.Contains(body, `"code":503`) {
		t.Fatalf("login body must carry code 503, got %s", body)
	}
}

func TestAuditServerHealthzLive(t *testing.T) {
	h := newHandler("audit-secret-1234567890", testConfig(t))
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz status = %d, want 200", rec.Code)
	}
	if strings.TrimSpace(rec.Body.String()) != `{"ok":true}`+"\n" && !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("healthz body = %s, want {\"ok\":true}", rec.Body.String())
	}
}

func TestAuditServerDataRouteFailsClosed(t *testing.T) {
	secret := "audit-secret-1234567890"
	h := newHandler(secret, testConfig(t))
	tok, err := server.NewAuthenticator([]byte(secret)).Generate("alice", time.Hour)
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/projects/resolve", strings.NewReader(`{"folder_name":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	// The stub fails closed: the handler surfaces a 500, never fake data.
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("data route status = %d, want 500 fail-closed", rec.Code)
	}
}

func TestAuditServerGetenv(t *testing.T) {
	t.Setenv("AUDIT_SERVER_UNSET_XYZ", "")
	if got := getenv("AUDIT_SERVER_UNSET_XYZ", "fb"); got != "fb" {
		t.Fatalf("unset getenv = %q, want fb", got)
	}
	t.Setenv("AUDIT_SERVER_SET_XYZ", "  v  ")
	if got := getenv("AUDIT_SERVER_SET_XYZ", "fb"); got != "v" {
		t.Fatalf("set getenv = %q, want trimmed v", got)
	}
}
