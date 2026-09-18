package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Contract tests for the deploy env decisions (issues #74/#112/#126/#127):
// DATABASE_URL primary with DB_* fallback assembly, JWT_SECRET canonical
// with CENTRAL_MEMORY_JWT_KEY legacy fallback, and the /readyz gate.
// Pure resolution helpers are tested directly; /readyz is tested over HTTP
// with injected configs (no live DB, no /migrations dir needed).

func testConfig(t *testing.T) serverConfig {
	t.Helper()
	return serverConfig{
		port:           "8080",
		databaseURL:    "postgres://central:pw@localhost:5432/central_memory?sslmode=disable",
		databaseSource: "DATABASE_URL",
		sslMode:        "disable",
		jwtSecret:      "audit-secret-1234567890",
		jwtSource:      "JWT_SECRET",
		migrationsDir:  t.TempDir(),
		localDev:       true,
	}
}

func envGetter(pairs map[string]string) func(string) string {
	return func(k string) string { return pairs[k] }
}

func TestContractDatabaseURLPrimary(t *testing.T) {
	get := envGetter(map[string]string{
		"DATABASE_URL": "postgres://u:p@h:5432/db?sslmode=require",
		"DB_HOST":      "other",
	})
	dsn, src := resolveDatabaseURL(get)
	if dsn != "postgres://u:p@h:5432/db?sslmode=require" || src != "DATABASE_URL" {
		t.Fatalf("got %q from %q, want verbatim DATABASE_URL", dsn, src)
	}
}

func TestContractDatabaseURLAssembledFromDBParts(t *testing.T) {
	get := envGetter(map[string]string{
		"DB_HOST": "aurora.example", "DB_PORT": "5432",
		"DB_NAME": "central_memory", "DB_USER": "central_app",
		"DB_PASSWORD": "pw", "DB_SSLMODE": "require",
	})
	dsn, src := resolveDatabaseURL(get)
	if src != "DB_* (assembled)" {
		t.Fatalf("source = %q, want DB_* (assembled)", src)
	}
	if !strings.Contains(dsn, "central_app") || !strings.Contains(dsn, "sslmode=require") {
		t.Fatalf("assembled dsn missing user/sslmode: %q", dsn)
	}
	if got := sslModeOf(dsn); got != "require" {
		t.Fatalf("sslModeOf = %q, want require", got)
	}
}

func TestContractDatabaseURLDefaults(t *testing.T) {
	// Port and sslmode default when unset (5432 / require = prod posture).
	dsn := buildDatabaseURLFromParts("h", "", "db", "u", "p", "")
	if !strings.Contains(dsn, ":5432/") || !strings.Contains(dsn, "sslmode=require") {
		t.Fatalf("defaults not applied: %q", dsn)
	}
	if dsn, src := resolveDatabaseURL(envGetter(nil)); dsn != "" || src != "" {
		// resolveDatabaseURL returns ("","") when nothing is set.
		t.Fatalf("empty env must yield empty dsn, got %q from %q", dsn, src)
	}
	if dsn := buildDatabaseURLFromParts("", "", "", "", "", ""); dsn != "" {
		t.Fatalf("missing host/name/user must yield empty dsn, got %q", dsn)
	}
}

func TestContractJWTSecretPrecedence(t *testing.T) {
	secret, src := resolveJWTSecret(envGetter(map[string]string{
		"JWT_SECRET": "new", "CENTRAL_MEMORY_JWT_KEY": "legacy",
	}))
	if secret != "new" || src != "JWT_SECRET" {
		t.Fatalf("got %q/%q, want canonical JWT_SECRET to win", secret, src)
	}
	secret, src = resolveJWTSecret(envGetter(map[string]string{
		"CENTRAL_MEMORY_JWT_KEY": "legacy",
	}))
	if secret != "legacy" || src != "CENTRAL_MEMORY_JWT_KEY" {
		t.Fatalf("got %q/%q, want legacy fallback", secret, src)
	}
	secret, src = resolveJWTSecret(envGetter(nil))
	if src != "dev-default" || secret == "" {
		t.Fatalf("got %q/%q, want insecure dev default", secret, src)
	}
}

func TestContractReadyzGate(t *testing.T) {
	ok, _ := readyStatus(serverConfig{}, true)
	if ok {
		t.Fatal("no DSN must be not-ready")
	}
	ok, reason := readyStatus(serverConfig{
		databaseURL: "postgres://u:p@h/db?sslmode=disable", sslMode: "disable",
	}, true)
	if ok || !strings.Contains(reason, "sslmode") {
		t.Fatalf("non-require sslmode without local-dev must be not-ready, got ok=%v reason=%q", ok, reason)
	}
	ok, _ = readyStatus(serverConfig{
		databaseURL: "postgres://u:p@h/db?sslmode=require", sslMode: "require",
	}, true)
	if !ok {
		t.Fatal("sslmode=require must be ready")
	}
	ok, _ = readyStatus(serverConfig{
		databaseURL: "postgres://u:p@h/db?sslmode=disable", sslMode: "disable", localDev: true,
	}, true)
	if !ok {
		t.Fatal("local-dev bypass must be ready")
	}
	ok, _ = readyStatus(serverConfig{
		databaseURL: "postgres://u:p@h/db?sslmode=require", sslMode: "require",
	}, false)
	if ok {
		t.Fatal("missing migrations dir must be not-ready")
	}
}

func TestContractReadyzEndpoint(t *testing.T) {
	cfg := testConfig(t)
	h := newHandler(cfg.jwtSecret, cfg)
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("readyz status = %d, want 200 (local-dev bypass)", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("readyz not JSON: %v", err)
	}
	if body["ok"] != true {
		t.Fatalf("readyz ok = %v, want true", body["ok"])
	}

	// Prod posture without require must 503.
	cfg2 := testConfig(t)
	cfg2.localDev = false
	h2 := newHandler(cfg2.jwtSecret, cfg2)
	rec2 := httptest.NewRecorder()
	h2.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec2.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz status = %d, want 503 without require/local-dev", rec2.Code)
	}
}
