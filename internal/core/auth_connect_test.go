package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"central-memory/internal/config"
)

func authConnectServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"version":"9.9.9"}`))
	})
	mux.HandleFunc("GET /projects", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good-token" {
			http.Error(w, "nope", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"items":[]}`))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func isolateAuthEnv(t *testing.T) {
	t.Helper()
	t.Setenv("CENTRAL_MEMORY_CONFIG_DIR", t.TempDir())
	for _, name := range []string{"NEXUS_TOKEN", "CENTRAL_MEMORY_TOKEN", "CENTRAL_SERVER_TOKEN", "NEXUS_SERVER", "CENTRAL_SERVER_URL"} {
		t.Setenv(name, "")
	}
}

func callAuthConnect(t *testing.T, args map[string]any) map[string]any {
	t.Helper()
	raw, _ := json.Marshal(args)
	out, err := authConnect(context.Background(), raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	return out.(map[string]any)
}

func TestAuthConnectTestOnlyDoesNotSave(t *testing.T) {
	isolateAuthEnv(t)
	server := authConnectServer(t)
	out := callAuthConnect(t, map[string]any{"server_url": server.URL + "/", "token": "good-token"})
	if out["reachable"] != true || out["token_valid"] != true || out["saved"] != false || out["server_version"] != "9.9.9" {
		t.Fatalf("out = %+v", out)
	}
	cfg, err := config.LoadFile()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Token != "" {
		t.Fatalf("test-only connect saved a token: %+v", cfg)
	}
}

func TestAuthConnectSavesValidToken(t *testing.T) {
	isolateAuthEnv(t)
	server := authConnectServer(t)
	if err := config.SaveFile(config.File{Token: "old", UserID: "u-old", Username: "someone-else"}); err != nil {
		t.Fatal(err)
	}
	out := callAuthConnect(t, map[string]any{"server_url": server.URL, "token": " good-token ", "save": true})
	if out["saved"] != true {
		t.Fatalf("out = %+v", out)
	}
	cfg, err := config.LoadFile()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Token != "good-token" || cfg.ServerURL != server.URL || cfg.Username != "" || cfg.UserID != "" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestAuthConnectRejectedTokenIsNotSaved(t *testing.T) {
	isolateAuthEnv(t)
	server := authConnectServer(t)
	out := callAuthConnect(t, map[string]any{"server_url": server.URL, "token": "bad", "save": true})
	if out["reachable"] != true || out["token_valid"] != false || out["saved"] != false {
		t.Fatalf("out = %+v", out)
	}
	cfg, _ := config.LoadFile()
	if cfg.Token != "" {
		t.Fatalf("rejected token saved: %+v", cfg)
	}
}

func TestAuthConnectUnreachableAndInvalidURL(t *testing.T) {
	isolateAuthEnv(t)
	server := authConnectServer(t)
	url := server.URL
	server.Close()
	out := callAuthConnect(t, map[string]any{"server_url": url})
	if out["reachable"] != false || out["message"] == "" {
		t.Fatalf("out = %+v", out)
	}
	if _, err := authConnect(context.Background(), json.RawMessage(`{"server_url":"ftp://x"}`), nil); err == nil {
		t.Fatal("expected an error for a non-http URL")
	}
}
