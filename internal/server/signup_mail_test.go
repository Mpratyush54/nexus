package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"central-memory/internal/store"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// signupDB fakes the few queries the sign-up code endpoints make: no user
// has the email or username yet, a pending sign-up exists once stored.
type signupDB struct {
	writes atomic.Int32
}

type signupRow struct{ sql string }

func (d *signupDB) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	return signupRow{sql: sql}
}
func (d *signupDB) Query(context.Context, string, ...any) (store.Rows, error) {
	return nil, errors.New("unexpected query")
}
func (d *signupDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	d.writes.Add(1)
	return pgconn.CommandTag{}, nil
}

func (r signupRow) Scan(dest ...any) error {
	if !strings.Contains(r.sql, "FROM signup_otps") {
		return pgx.ErrNoRows
	}
	*dest[0].(*string) = "maya@example.com"
	*dest[1].(*string) = "maya"
	*dest[2].(*string) = "hash"
	*dest[3].(*string) = "codehash"
	*dest[4].(*int) = 0
	*dest[5].(*time.Time) = time.Now().Add(10 * time.Minute)
	*dest[6].(*time.Time) = time.Now()
	return nil
}

// scriptedMail blocks each Send until released (when gate is set) and fails
// the first `fail` sends.
type scriptedMail struct {
	gate  chan struct{}
	fail  int32
	calls atomic.Int32
	mu    sync.Mutex
	to    []string
}

func (m *scriptedMail) Configured() bool { return true }
func (m *scriptedMail) Send(to, _, _ string) error {
	n := m.calls.Add(1)
	if m.gate != nil {
		<-m.gate
	}
	if n <= atomic.LoadInt32(&m.fail) {
		return errors.New("relay said no")
	}
	m.mu.Lock()
	m.to = append(m.to, to)
	m.mu.Unlock()
	return nil
}

func newSignupServer(t *testing.T, m *scriptedMail) (*Server, *signupDB) {
	t.Helper()
	t.Setenv("CENTRAL_MEMORY_LOCAL_DEV", "")
	s := newTestServer()
	db := &signupDB{}
	s.Accounts = store.NewUserStore(db)
	if m != nil {
		s.Mail = m
	}
	s.outbox().backoff = []time.Duration{time.Millisecond, time.Millisecond}
	return s, db
}

var signupBody = map[string]any{"email": "maya@example.com", "username": "maya", "password": "correct horse battery"}

func signupStatus(t *testing.T, s *Server) map[string]any {
	t.Helper()
	rec := doJSON(t, s, http.MethodPost, "/auth/signup/status", "", map[string]any{"email": "maya@example.com"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSignupSendCodeAnswersWhileMailRelayHangs(t *testing.T) {
	m := &scriptedMail{gate: make(chan struct{})}
	s, _ := newSignupServer(t, m)

	start := time.Now()
	rec := doJSON(t, s, http.MethodPost, "/auth/signup/send-code", "", signupBody)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("send-code: %d %s", rec.Code, rec.Body.String())
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("send-code waited for the mail relay (%v)", took)
	}
	if !strings.Contains(rec.Body.String(), `"delivery":"sending"`) || strings.Contains(rec.Body.String(), "dev_code") {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if got := signupStatus(t, s)["delivery"]; got != mailSending {
		t.Fatalf("delivery while relay hangs = %v", got)
	}
	close(m.gate)
	s.waitMail()
	if got := signupStatus(t, s)["delivery"]; got != mailSent {
		t.Fatalf("delivery after relay answers = %v", got)
	}
}

func TestSignupCodeMailRetriesThenSucceeds(t *testing.T) {
	m := &scriptedMail{fail: 2}
	s, _ := newSignupServer(t, m)
	if rec := doJSON(t, s, http.MethodPost, "/auth/signup/send-code", "", signupBody); rec.Code != http.StatusAccepted {
		t.Fatalf("send-code: %d %s", rec.Code, rec.Body.String())
	}
	s.waitMail()
	if m.calls.Load() != 3 || len(m.to) != 1 {
		t.Fatalf("calls=%d delivered=%v", m.calls.Load(), m.to)
	}
	if got := signupStatus(t, s)["delivery"]; got != mailSent {
		t.Fatalf("delivery = %v", got)
	}
}

func TestSignupCodeMailFailureIsReported(t *testing.T) {
	m := &scriptedMail{fail: 99}
	s, _ := newSignupServer(t, m)
	if rec := doJSON(t, s, http.MethodPost, "/auth/signup/send-code", "", signupBody); rec.Code != http.StatusAccepted {
		t.Fatalf("send-code: %d %s", rec.Code, rec.Body.String())
	}
	s.waitMail()
	if m.calls.Load() != 3 {
		t.Fatalf("attempts = %d, want 3", m.calls.Load())
	}
	if got := signupStatus(t, s)["delivery"]; got != mailFailed {
		t.Fatalf("delivery = %v", got)
	}
}

func TestSignupCodeRefusesWithoutMailerBeforeStoringAnything(t *testing.T) {
	s, db := newSignupServer(t, nil)
	s.Mail = &failingConfiguredFalse{}
	for _, env := range []struct{ dev, app string }{{"", ""}, {"1", "https://nexus.pratyushes.dev"}} {
		t.Setenv("CENTRAL_MEMORY_LOCAL_DEV", env.dev)
		t.Setenv("PUBLIC_APP_URL", env.app)
		for _, path := range []string{"/auth/signup/send-code", "/auth/signup/resend", "/auth/password/forgot"} {
			s.login = newRateGate(100, 100)
			body := map[string]any{"email": "maya@example.com"}
			if path == "/auth/signup/send-code" {
				body = signupBody
			}
			rec := doJSON(t, s, http.MethodPost, path, "", body)
			if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "email delivery is not configured") {
				t.Fatalf("%s (dev=%q app=%q): %d %s", path, env.dev, env.app, rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), `"request_id"`) {
				t.Fatalf("%s: no request id: %s", path, rec.Body.String())
			}
		}
	}
	if n := db.writes.Load(); n != 0 {
		t.Fatalf("stored %d rows although no code could be sent", n)
	}
}

func TestSignupDevCodeOnlyForLocalBoot(t *testing.T) {
	s, _ := newSignupServer(t, nil)
	s.Mail = &failingConfiguredFalse{}
	t.Setenv("CENTRAL_MEMORY_LOCAL_DEV", "1")
	t.Setenv("PUBLIC_APP_URL", "http://127.0.0.1:5173")
	rec := doJSON(t, s, http.MethodPost, "/auth/signup/send-code", "", signupBody)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"dev_code"`) {
		t.Fatalf("local dev: %d %s", rec.Code, rec.Body.String())
	}
}

func TestSignupCodeEmailsAreSpacedOut(t *testing.T) {
	m := &scriptedMail{}
	s, _ := newSignupServer(t, m)
	if rec := doJSON(t, s, http.MethodPost, "/auth/signup/send-code", "", signupBody); rec.Code != http.StatusAccepted {
		t.Fatalf("first: %d %s", rec.Code, rec.Body.String())
	}
	rec := doJSON(t, s, http.MethodPost, "/auth/signup/resend", "", map[string]any{"email": "maya@example.com"})
	if rec.Code != http.StatusTooManyRequests || !strings.Contains(rec.Body.String(), "too many codes") {
		t.Fatalf("immediate resend: %d %s", rec.Code, rec.Body.String())
	}
	s.waitMail()
}

func TestDevMailCodes(t *testing.T) {
	cases := []struct {
		dev, app string
		want     bool
	}{
		{"", "", false},
		{"1", "", true},
		{"1", "http://localhost:5173", true},
		{"1", "http://127.0.0.1:47910", true},
		{"1", "https://nexus.pratyushes.dev", false},
	}
	for _, c := range cases {
		t.Setenv("CENTRAL_MEMORY_LOCAL_DEV", c.dev)
		t.Setenv("PUBLIC_APP_URL", c.app)
		if got := devMailCodes(); got != c.want {
			t.Errorf("dev=%q app=%q: got %v", c.dev, c.app, got)
		}
	}
}

type failingConfiguredFalse struct{}

func (failingConfiguredFalse) Configured() bool { return false }
func (failingConfiguredFalse) Send(string, string, string) error {
	return errors.New("must not be called")
}
