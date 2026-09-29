package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"central-memory/internal/config"
)

func TestWorkspaceSwitchAndRecent(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("CENTRAL_MEMORY_CONFIG_DIR", cfgDir)
	ws := t.TempDir()

	d := testDaemon(t)
	p := NewCORSProxy(d)

	w := doProxy(p, http.MethodPost, "/local/workspace/switch", `{}`, "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing path: %d %s", w.Code, w.Body.String())
	}

	w = doProxy(p, http.MethodPost, "/local/workspace/switch", `{"path":"C:\\no\\such\\dir-xyz"}`, "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid path: %d %s", w.Code, w.Body.String())
	}

	body := `{"path":` + mustJSON(ws) + `}`
	w = doProxy(p, http.MethodPost, "/local/workspace/switch", body, "")
	if w.Code != http.StatusOK {
		t.Fatalf("valid switch: %d %s", w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["ok"] != true {
		t.Fatalf("body=%s", w.Body.String())
	}

	w = doProxy(p, http.MethodGet, "/local/workspace/recent", "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("recent: %d", w.Code)
	}
	var list []string
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0] != config.NormalizeWorkspacePath(ws) {
		t.Fatalf("recent=%#v", list)
	}
}

func TestDiagnosticsHealthzLatency(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		time.Sleep(5 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`ok`))
	}))
	defer srv.Close()

	d := &Daemon{ServerURL: srv.URL, ServerToken: "tok", UserID: "u1", Root: t.TempDir(), MachineID: "m1"}
	res := RunDiagnostics(d)
	if !res.ServerReachable {
		t.Fatalf("expected reachable: %#v", res)
	}
	if res.ServerLatencyMs < 0 {
		t.Fatalf("latency=%d", res.ServerLatencyMs)
	}
	if !res.TokenValid {
		t.Fatal("token should be valid")
	}

	nilRes := RunDiagnostics(nil)
	if len(nilRes.Errors) == 0 {
		t.Fatal("nil daemon should report errors")
	}
	empty := RunDiagnostics(&Daemon{ServerURL: srv.URL})
	if empty.WorkspaceLinked || strings.TrimSpace(empty.WorkspaceID) != "" {
		t.Fatalf("empty root should not be linked: %#v", empty)
	}
}

func TestStatusPageMarkers(t *testing.T) {
	d := testDaemon(t)
	p := NewCORSProxy(d)
	w := doProxy(p, http.MethodGet, "/", "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/html") {
		t.Fatalf("Content-Type=%q", ct)
	}
	body := w.Body.String()
	for _, marker := range []string{`id="harness-grid"`, `id="activity-feed"`, `id="diagPanel"`} {
		if !strings.Contains(body, marker) {
			t.Fatalf("missing marker %s", marker)
		}
	}
}

func mustJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
