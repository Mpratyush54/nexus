package server

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"central-memory/internal/store"
)

func diagReport(id string) map[string]any {
	return map[string]any{
		"report_id":   id,
		"kind":        "crash",
		"source":      "web",
		"app_version": "2026.11.25",
		"os":          "Windows 11",
		"message":     "TypeError: boom for alice@example.com",
		"stack":       "TypeError: boom\n    at Ab (https://nexus.example/assets/index-abc.js:1:234)\n    at C:\\Users\\alice\\x.js:1:1",
		"logs":        []string{"GET /v1/x -> 500", "token nxs_secretsecret123"},
		"user_note":   "it broke with Bearer abcdefghijklmnop",
		"timestamp":   time.Now().UTC().Format(time.RFC3339),
		"context":     map[string]string{"route": "/app/chats"},
		"future_key":  "ignored by this server",
	}
}

func postDiag(t *testing.T, s *Server, token, ip string, body any) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/diagnostics", bytes.NewReader(mustJSON(t, body)))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if ip != "" {
		req.RemoteAddr = ip + ":5555"
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	if raw, ok := v.([]byte); ok {
		return raw
	}
	rec := httptest.NewRecorder()
	writeJSON(rec, 200, v)
	return bytes.TrimSpace(rec.Body.Bytes())
}

func TestDiagnosticsPostStoresRedactedReport(t *testing.T) {
	s := newTestServer()
	alice := loginAs(t, s, "alice")

	rec := postDiag(t, s, alice, "10.0.0.1", diagReport("report-0001"))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("post = %d %s", rec.Code, rec.Body.String())
	}
	var resp diagnosticsResponse
	decodeBody(t, rec, &resp)
	if resp.ReportID != "report-0001" || len(resp.Fingerprint) != 16 || resp.Duplicate || resp.RequestID == "" {
		t.Fatalf("response = %+v", resp)
	}
	if rec.Header().Get(RequestIDHeader) != resp.RequestID {
		t.Fatalf("X-Request-ID %q != body %q", rec.Header().Get(RequestIDHeader), resp.RequestID)
	}

	got, err := s.Store.(store.DiagnosticsStore).GetDiagnosticReport(context.Background(), "report-0001")
	if err != nil {
		t.Fatal(err)
	}
	if got.UserID != "alice" {
		t.Fatalf("signed-in report not linked: user_id=%q", got.UserID)
	}
	all := got.Message + got.Stack + strings.Join(got.Logs, "\n") + got.UserNote
	for _, leak := range []string{"alice@example.com", `Users\alice`, "secretsecret123", "abcdefghijklmnop"} {
		if strings.Contains(all, leak) {
			t.Fatalf("stored report leaks %q:\n%s", leak, all)
		}
	}

	// A retry with the same report_id is accepted but not stored twice.
	rec = postDiag(t, s, alice, "10.0.0.1", diagReport("report-0001"))
	if rec.Code != http.StatusOK {
		t.Fatalf("retry = %d %s", rec.Code, rec.Body.String())
	}
	decodeBody(t, rec, &resp)
	if !resp.Duplicate {
		t.Fatalf("retry should be duplicate: %+v", resp)
	}
}

func TestDiagnosticsAnonymousAndExpiredTokenAreAccepted(t *testing.T) {
	s := newTestServer()
	rec := postDiag(t, s, "", "10.0.0.2", diagReport("anon-report-1"))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("anonymous post = %d %s", rec.Code, rec.Body.String())
	}
	rec = postDiag(t, s, "not-a-valid-token", "10.0.0.2", diagReport("anon-report-2"))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("invalid-token post = %d %s", rec.Code, rec.Body.String())
	}
	got, err := s.Store.(store.DiagnosticsStore).GetDiagnosticReport(context.Background(), "anon-report-2")
	if err != nil || got.UserID != "" {
		t.Fatalf("invalid token must post anonymously: %+v %v", got, err)
	}
}

func TestDiagnosticsValidationAndSizeLimit(t *testing.T) {
	s := newTestServer()
	bad := diagReport("bad-kind-01")
	bad["kind"] = "panic"
	rec := postDiag(t, s, "", "10.0.0.3", bad)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "kind must be") {
		t.Fatalf("bad kind = %d %s", rec.Code, rec.Body.String())
	}
	rec = postDiag(t, s, "", "10.0.0.3", []byte("{not json"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad json = %d", rec.Code)
	}
	huge := diagReport("huge-report-1")
	huge["stack"] = strings.Repeat("x", 300<<10)
	rec = postDiag(t, s, "", "10.0.0.4", huge)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("huge = %d %s", rec.Code, rec.Body.String())
	}
	// Error bodies carry the request ID too.
	var env errorEnvelope
	decodeBody(t, rec, &env)
	if env.Error.RequestID == "" || env.Error.RequestID != rec.Header().Get(RequestIDHeader) {
		t.Fatalf("error request_id = %q header %q", env.Error.RequestID, rec.Header().Get(RequestIDHeader))
	}
}

func TestDiagnosticsRateLimits(t *testing.T) {
	s := newTestServer()
	for i := 0; i < diagAnonPerIPPerHour; i++ {
		rec := postDiag(t, s, "", "10.1.0.1", diagReport("ip-limit-"+string(rune('a'+i))+"-xxxx"))
		if rec.Code != http.StatusAccepted {
			t.Fatalf("report %d = %d %s", i, rec.Code, rec.Body.String())
		}
	}
	rec := postDiag(t, s, "", "10.1.0.1", diagReport("ip-limit-over-1"))
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("over anon limit = %d retry-after=%q", rec.Code, rec.Header().Get("Retry-After"))
	}
	// Another IP is unaffected; a signed-in user from the limited IP is too.
	if rec := postDiag(t, s, "", "10.1.0.2", diagReport("other-ip-0001")); rec.Code != http.StatusAccepted {
		t.Fatalf("other ip = %d", rec.Code)
	}
	bob := loginAs(t, s, "bob")
	if rec := postDiag(t, s, bob, "10.1.0.1", diagReport("signed-in-0001")); rec.Code != http.StatusAccepted {
		t.Fatalf("signed-in from limited ip = %d", rec.Code)
	}

	// The window resets.
	gates := s.diagGates()
	gates.anonIP.now = func() time.Time { return time.Now().Add(diagLimiterWindow + time.Minute) }
	if rec := postDiag(t, s, "", "10.1.0.1", diagReport("after-window-1")); rec.Code != http.StatusAccepted {
		t.Fatalf("after window = %d %s", rec.Code, rec.Body.String())
	}
}

func TestDiagnosticsGlobalAnonymousCap(t *testing.T) {
	s := newTestServer()
	gates := s.diagGates()
	gates.anonGlobal = newWindowLimiter(3, time.Hour)
	for i := 0; i < 3; i++ {
		ip := "10.2.0." + string(rune('1'+i))
		if rec := postDiag(t, s, "", ip, diagReport("global-cap-"+string(rune('a'+i))+"-x")); rec.Code != http.StatusAccepted {
			t.Fatalf("report %d = %d", i, rec.Code)
		}
	}
	if rec := postDiag(t, s, "", "10.2.0.9", diagReport("global-cap-over")); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("over global cap from fresh ip = %d", rec.Code)
	}
}

func TestWindowLimiterBoundsKeys(t *testing.T) {
	l := newWindowLimiter(1, time.Hour)
	l.maxKeys = 4
	for i := 0; i < 20; i++ {
		l.allow(string(rune('a' + i)))
	}
	if len(l.counts) > 4 {
		t.Fatalf("limiter kept %d keys, max 4", len(l.counts))
	}
}

func TestAdminDiagnosticsRequiresPlatformAdmin(t *testing.T) {
	t.Setenv("PLATFORM_ADMIN_USER_IDS", "root-admin")
	s := newTestServer()
	for _, id := range []string{"group-a-0001", "group-a-0002"} {
		if rec := postDiag(t, s, "", "10.3.0.1", diagReport(id)); rec.Code != http.StatusAccepted {
			t.Fatalf("seed = %d", rec.Code)
		}
	}
	other := diagReport("group-b-0001")
	other["source"] = "desktop"
	other["app_version"] = "0.9.0"
	if rec := postDiag(t, s, loginAs(t, s, "carol"), "10.3.0.1", other); rec.Code != http.StatusAccepted {
		t.Fatalf("seed b = %d", rec.Code)
	}

	for _, path := range []string{"/admin/diagnostics", "/admin/diagnostics/abc", "/admin/diagnostics/reports/group-a-0001"} {
		if rec := doJSON(t, s, http.MethodGet, path, "", nil); rec.Code != http.StatusUnauthorized {
			t.Fatalf("anonymous %s = %d", path, rec.Code)
		}
		if rec := doJSON(t, s, http.MethodGet, path, loginAs(t, s, "carol"), nil); rec.Code != http.StatusForbidden {
			t.Fatalf("non-admin %s = %d", path, rec.Code)
		}
	}

	admin := loginAs(t, s, "root-admin")
	rec := doJSON(t, s, http.MethodGet, "/admin/diagnostics", admin, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin list = %d %s", rec.Code, rec.Body.String())
	}
	var list struct {
		Items []store.DiagnosticGroup `json:"items"`
		Count int                     `json:"count"`
	}
	decodeBody(t, rec, &list)
	if list.Count != 2 {
		t.Fatalf("groups = %d, want 2: %s", list.Count, rec.Body.String())
	}
	var web store.DiagnosticGroup
	for _, g := range list.Items {
		if g.Source == "web" {
			web = g
		}
	}
	if web.Count != 2 || len(web.Versions) != 1 || web.Versions[0] != "2026.11.25" || web.FirstSeen.IsZero() || web.LastSeen.IsZero() {
		t.Fatalf("web group = %+v", web)
	}

	rec = doJSON(t, s, http.MethodGet, "/admin/diagnostics?source=desktop", admin, nil)
	decodeBody(t, rec, &list)
	if list.Count != 1 || list.Items[0].Users != 1 {
		t.Fatalf("filtered = %+v", list)
	}
	if rec := doJSON(t, s, http.MethodGet, "/admin/diagnostics?source=phone", admin, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad filter = %d", rec.Code)
	}

	rec = doJSON(t, s, http.MethodGet, "/admin/diagnostics/"+web.Fingerprint, admin, nil)
	var detail struct {
		Items []store.DiagnosticReport `json:"items"`
	}
	decodeBody(t, rec, &detail)
	if rec.Code != http.StatusOK || len(detail.Items) != 2 || detail.Items[0].Stack == "" {
		t.Fatalf("group detail = %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodGet, "/admin/diagnostics/reports/group-a-0002", admin, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"report_id":"group-a-0002"`) {
		t.Fatalf("report detail = %d %s", rec.Code, rec.Body.String())
	}
	if rec := doJSON(t, s, http.MethodGet, "/admin/diagnostics/reports/missing-0001", admin, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("missing report = %d", rec.Code)
	}
}

func TestDiagnosticsRetentionPrunesOldReports(t *testing.T) {
	ms := store.NewMemStore()
	ctx := context.Background()
	now := time.Now().UTC()
	for id, age := range map[string]time.Duration{"old-report-01": 31 * 24 * time.Hour, "new-report-01": 29 * 24 * time.Hour} {
		if _, err := ms.InsertDiagnosticReport(ctx, &store.DiagnosticReport{
			ReportID: id, Fingerprint: "fp", Kind: "crash", Source: "web", Message: "x", ReceivedAt: now.Add(-age),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if n := pruneDiagnostics(ctx, ms, now); n != 1 {
		t.Fatalf("pruned %d, want 1", n)
	}
	if _, err := ms.GetDiagnosticReport(ctx, "old-report-01"); err == nil {
		t.Fatal("31-day-old report survived retention")
	}
	if _, err := ms.GetDiagnosticReport(ctx, "new-report-01"); err != nil {
		t.Fatalf("29-day-old report was deleted: %v", err)
	}
	stop := StartDiagnosticsRetention(ctx, ms, time.Hour)
	stop()
	stop() // idempotent
}

func TestDiagnosticsUnavailableWithoutStoreSupport(t *testing.T) {
	s := newTestServer()
	s.Store = nil
	rec := postDiag(t, s, "", "10.4.0.1", diagReport("no-store-0001"))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no store = %d", rec.Code)
	}
}

func TestRequestIDOnEveryResponse(t *testing.T) {
	s := newTestServer()
	var logs bytes.Buffer
	s.Log.SetOutput(&logs)

	rec := doJSON(t, s, http.MethodGet, "/projects", "", nil) // 401
	id := rec.Header().Get(RequestIDHeader)
	if len(id) != 12 {
		t.Fatalf("X-Request-ID = %q", id)
	}
	var env errorEnvelope
	decodeBody(t, rec, &env)
	if env.Error.RequestID != id {
		t.Fatalf("body request_id %q != header %q", env.Error.RequestID, id)
	}
	if !strings.Contains(logs.String(), "rid="+id) || !strings.Contains(logs.String(), `err="missing or invalid bearer token"`) {
		t.Fatalf("log line lacks rid/err: %s", logs.String())
	}
	if !strings.Contains(rec.Header().Get("Access-Control-Expose-Headers"), RequestIDHeader) {
		t.Fatal("browsers cannot read X-Request-ID without Access-Control-Expose-Headers")
	}

	ok := doJSON(t, s, http.MethodGet, "/healthz", "", nil)
	if ok.Header().Get(RequestIDHeader) == "" || ok.Header().Get(RequestIDHeader) == id {
		t.Fatalf("healthz request id = %q", ok.Header().Get(RequestIDHeader))
	}

	// A well-formed inbound ID (from a proxy) is kept; a malformed one is replaced.
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set(RequestIDHeader, "edge-abc-12345")
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Header().Get(RequestIDHeader) != "edge-abc-12345" {
		t.Fatalf("inbound id not kept: %q", rec.Header().Get(RequestIDHeader))
	}
	req = httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set(RequestIDHeader, "bad id\nINJECTED")
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if got := rec.Header().Get(RequestIDHeader); strings.Contains(got, "INJECTED") || len(got) != 12 {
		t.Fatalf("malformed inbound id kept: %q", got)
	}
}

func TestPanicRecoveredWithRequestID(t *testing.T) {
	s := newTestServer()
	var logs bytes.Buffer
	s.Log.SetOutput(&logs)
	s.Mux.HandleFunc("GET /test-panic", func(http.ResponseWriter, *http.Request) { panic("kaboom") })
	rec := doJSON(t, s, http.MethodGet, "/test-panic", "", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("panic = %d", rec.Code)
	}
	id := rec.Header().Get(RequestIDHeader)
	var env errorEnvelope
	decodeBody(t, rec, &env)
	if env.Error.RequestID != id || !strings.Contains(logs.String(), "panic rid="+id) || !strings.Contains(logs.String(), "kaboom") {
		t.Fatalf("panic not logged with rid: body=%s logs=%s", rec.Body.String(), logs.String())
	}
}
