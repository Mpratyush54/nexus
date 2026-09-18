package main

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
)

// Audit coverage for cmd/nexus/doctor.go: arg parsing and the three probes
// (server, daemon, git). All HTTP traffic goes to local httptest servers.

func TestAuditDoctorParseEdges(t *testing.T) {
	j, err := parseDoctorArgs(nil)
	if err != nil || j {
		t.Fatalf("bare doctor parse = %v, %v", j, err)
	}
	if _, err := parseDoctorArgs([]string{"extra"}); err == nil {
		t.Error("doctor with positional must fail")
	}
	if _, err := parseDoctorArgs([]string{"--nope"}); err == nil {
		t.Error("doctor unknown flag must fail")
	}
}

func TestAuditProbeServerHealthy(t *testing.T) {
	srv := auditServer(t, 200, `{"ok":true}`, nil)
	defer srv.Close()
	got := probeServer(context.Background(), Config{ServerURL: srv.URL})
	if !got.OK || got.Probe != "server" {
		t.Fatalf("healthy probe wrong: %+v", got)
	}
	if got.Detail != `{"ok":true}` {
		t.Fatalf("healthy detail = %q", got.Detail)
	}
}

func TestAuditProbeServerUnhealthy(t *testing.T) {
	srv := auditServer(t, 500, `boom`, nil)
	defer srv.Close()
	got := probeServer(context.Background(), Config{ServerURL: srv.URL})
	if got.OK {
		t.Fatalf("500 probe must fail: %+v", got)
	}
	if !strings.Contains(got.Detail, "500") {
		t.Fatalf("detail must carry status, got %q", got.Detail)
	}
}

func TestAuditProbeServerUnreachable(t *testing.T) {
	got := probeServer(context.Background(), Config{ServerURL: "http://127.0.0.1:1"})
	if got.OK {
		t.Fatalf("unreachable probe must fail: %+v", got)
	}
	if !strings.Contains(got.Detail, "unreachable") {
		t.Fatalf("detail must say unreachable, got %q", got.Detail)
	}
}

func TestAuditProbeDaemonAuthStatuses(t *testing.T) {
	okSrv := auditServer(t, 200, `{}`, nil)
	defer okSrv.Close()
	got := probeDaemon(context.Background(), Config{DaemonURL: okSrv.URL, DaemonToken: "tok"})
	if !got.OK {
		t.Fatalf("200 daemon probe must pass: %+v", got)
	}

	unauth := auditServer(t, http.StatusUnauthorized, `{}`, nil)
	defer unauth.Close()
	got = probeDaemon(context.Background(), Config{DaemonURL: unauth.URL})
	if got.OK {
		t.Fatalf("401 daemon probe must fail: %+v", got)
	}
	if !strings.Contains(got.Detail, "NEXUS_DAEMON_TOKEN") {
		t.Fatalf("401 detail must hint at token, got %q", got.Detail)
	}
}

func TestAuditResolveDaemonToken(t *testing.T) {
	tok, src := resolveDaemonToken(Config{DaemonToken: "  abc  "})
	if tok != "abc" || src != "NEXUS_DAEMON_TOKEN" {
		t.Fatalf("token = %q source = %q", tok, src)
	}
	tok, src = resolveDaemonToken(Config{})
	if tok != "" || src != "" {
		t.Fatalf("empty config must yield empty token, got %q %q", tok, src)
	}
}

func TestAuditProbeGitShape(t *testing.T) {
	got := probeGit(context.Background())
	if got.Probe != "git" || got.Target == "" || got.Detail == "" {
		t.Fatalf("git probe shape wrong: %+v", got)
	}
}

func TestAuditDoctorAllFailReturnsError(t *testing.T) {
	cfg := Config{ServerURL: "http://127.0.0.1:1", DaemonURL: "http://127.0.0.1:2"}
	var out bytes.Buffer
	err := runDoctor(context.Background(), cfg, nil, &out)
	if err == nil || !strings.Contains(err.Error(), "failing probes") {
		t.Fatalf("expected failing-probes error, got %v", err)
	}
	if !strings.Contains(out.String(), "PROBE") {
		t.Fatalf("doctor must still print a table, got:\n%s", out.String())
	}
}
