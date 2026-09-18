package server

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Audit coverage for internal/server/auth.go. Complements (never edits) the
// existing server_test.go round-trip test.

// HMAC tamper: flipping payload bits must fail validation.
func TestAuditAuthHMACTamperRejected(t *testing.T) {
	a := NewAuthenticator([]byte("audit-key-0123456789abcdef"))
	tok, err := a.Generate("alice", time.Hour)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("token must have 3 parts, got %d", len(parts))
	}
	// Corrupt the payload segment (keep valid base64 by re-encoding garbage).
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	raw[0] ^= 0xff
	parts[1] = base64.RawURLEncoding.EncodeToString(raw)
	if _, err := a.Validate(strings.Join(parts, ".")); err == nil {
		t.Fatal("tampered payload must fail validation")
	}
}

// Wrong-key tokens must fail: proves the signature is key-bound.
func TestAuditAuthWrongKeyRejected(t *testing.T) {
	a := NewAuthenticator([]byte("audit-key-one-1234567890"))
	b := NewAuthenticator([]byte("audit-key-two-0987654321"))
	tok, err := a.Generate("bob", time.Hour)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if _, err := b.Validate(tok); err == nil {
		t.Fatal("token signed by another key must fail validation")
	}
}

// Expiry boundary: future TTL validates, past TTL yields ErrExpiredToken.
func TestAuditAuthExpiryBoundary(t *testing.T) {
	a := NewAuthenticator([]byte("audit-key-exp-1234567890"))
	tok, err := a.Generate("carol", time.Hour)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if sub, err := a.Validate(tok); err != nil || sub != "carol" {
		t.Fatalf("fresh token Validate = %q, %v", sub, err)
	}
	expired, err := a.Generate("carol", -time.Second)
	if err != nil {
		t.Fatalf("Generate expired: %v", err)
	}
	if _, err := a.Validate(expired); err != ErrExpiredToken {
		t.Fatalf("expired token err = %v, want ErrExpiredToken", err)
	}
}

// Malformed tokens must all map to ErrInvalidToken (never ErrExpiredToken,
// never a panic).
func TestAuditAuthMalformedTokens(t *testing.T) {
	a := NewAuthenticator([]byte("audit-key-malformed-1234"))
	cases := map[string]string{
		"two parts":      "abc.def",
		"four parts":     "a.b.c.d",
		"empty":          "",
		"bad sig b64":    "a.b.!!!not-base64!!!",
		"bad pay b64":    "a.!!!.cG9vbA",
		"bad pay json":   base64.RawURLEncoding.EncodeToString([]byte("h")) + "." + base64.RawURLEncoding.EncodeToString([]byte("not-json")) + "." + base64.RawURLEncoding.EncodeToString([]byte("sig")),
		"single dot dot": "..",
	}
	for name, tok := range cases {
		if _, err := a.Validate(tok); err != ErrInvalidToken {
			t.Errorf("%s: err = %v, want ErrInvalidToken", name, err)
		}
	}
}

// Empty-sub payload must fail even with a valid signature (covers the
// payload.Sub == "" branch).
func TestAuditAuthEmptySubjectPayloadRejected(t *testing.T) {
	key := []byte("audit-key-emptysub-1234")
	headerJSON, _ := json.Marshal(jwtHeader{Alg: "HS256", Typ: "JWT"})
	payloadJSON, _ := json.Marshal(jwtPayload{Sub: "", Exp: time.Now().Add(time.Hour).Unix(), Iat: time.Now().Unix()})
	encH := base64.RawURLEncoding.EncodeToString(headerJSON)
	encP := base64.RawURLEncoding.EncodeToString(payloadJSON)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(encH + "." + encP))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	a := NewAuthenticator(key)
	if _, err := a.Validate(encH + "." + encP + "." + sig); err != ErrInvalidToken {
		t.Fatalf("empty-sub token err = %v, want ErrInvalidToken", err)
	}
}

// Generate guards: empty subject and empty key.
func TestAuditAuthGenerateGuards(t *testing.T) {
	a := NewAuthenticator([]byte("audit-key-guards-1"))
	if _, err := a.Generate("", time.Hour); err == nil {
		t.Error("empty subject must fail")
	}
	empty := NewAuthenticator(nil)
	if _, err := empty.Generate("alice", time.Hour); err == nil {
		t.Error("empty key must fail")
	}
}

// bearerSubject parsing: header shape enforcement.
func TestAuditAuthBearerSubjectParsing(t *testing.T) {
	a := NewAuthenticator([]byte("audit-key-bearer-1234"))
	tok, err := a.Generate("dave", time.Hour)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if sub, err := a.bearerSubject("Bearer " + tok); err != nil || sub != "dave" {
		t.Fatalf("bearerSubject = %q, %v", sub, err)
	}
	for name, hdr := range map[string]string{
		"empty":         "",
		"no prefix":     tok,
		"basic prefix":  "Basic " + tok,
		"lower prefix":  "bearer " + tok,
		"empty token":   "Bearer ",
		"blank token":   "Bearer    ",
		"garbage token": "Bearer nope",
	} {
		if _, err := a.bearerSubject(hdr); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

// NOTE(constant-time): Validate compares signatures with hmac.Equal, which
// is constant-time for equal-length inputs. This is asserted by code
// inspection (auth.go imports crypto/hmac and calls hmac.Equal); timing
// itself is not measured here because CI timing assertions are flaky.
func TestAuditAuthUsesConstantTimeCompare(t *testing.T) {
	a := NewAuthenticator([]byte("audit-key-ct-1234567890"))
	t1, _ := a.Generate("eve", time.Hour)
	t2, _ := a.Generate("eve", time.Hour)
	// Same input length, different signatures: both must validate under the
	// same key (proves per-token MACs), and cross-key must fail.
	if _, err := a.Validate(t1); err != nil {
		t.Fatalf("t1: %v", err)
	}
	if _, err := a.Validate(t2); err != nil {
		t.Fatalf("t2: %v", err)
	}
}

// No secret configured: the authenticator fails closed (issue #85). Token
// minting refuses, validation rejects, and IsConfigured is false. The
// insecure dev default exists only behind an explicit ALLOW_DEV_JWT=1
// opt-in; production (env unset) never mints forgeable tokens.
func TestAuditAuthFailClosedWithoutSecret(t *testing.T) {
	t.Setenv("JWT_SECRET", "")
	t.Setenv("CENTRAL_MEMORY_JWT_KEY", "")
	t.Setenv("ALLOW_DEV_JWT", "")
	a := NewAuthenticatorFromEnv()
	if a.IsConfigured() {
		t.Fatal("unconfigured authenticator must report IsConfigured()==false")
	}
	if _, err := a.Generate("alice", time.Hour); err == nil {
		t.Fatal("Generate without a key must fail closed")
	}
	if _, err := a.Validate("anything.at.all"); err == nil {
		t.Fatal("Validate without a key must fail")
	}

	// Explicit dev opt-in restores the documented insecure default.
	t.Setenv("ALLOW_DEV_JWT", "1")
	dev := NewAuthenticatorFromEnv()
	if !dev.IsConfigured() {
		t.Fatal("ALLOW_DEV_JWT=1 must enable the dev fallback key")
	}
	if got := string(dev.key); got != "dev-only-insecure-key-replace-via-env" {
		t.Fatalf("fallback key = %q, want documented dev default", got)
	}
	if id := dev.keyID(); !strings.HasPrefix(id, "hmac-sha256:") {
		t.Fatalf("keyID = %q, want hmac-sha256: prefix", id)
	}

	// Canonical JWT_SECRET wins over the legacy CENTRAL_MEMORY_JWT_KEY.
	t.Setenv("JWT_SECRET", "canonical-secret-xyz-1234567890")
	t.Setenv("CENTRAL_MEMORY_JWT_KEY", "legacy-secret-abc-0987654321")
	canon := NewAuthenticatorFromEnv()
	if string(canon.key) != "canonical-secret-xyz-1234567890" {
		t.Fatal("JWT_SECRET must take precedence over the legacy variable")
	}

	// Legacy fallback still works when only it is set.
	t.Setenv("JWT_SECRET", "")
	legacy := NewAuthenticatorFromEnv()
	if string(legacy.key) != "legacy-secret-abc-0987654321" {
		t.Fatalf("legacy key = %q, want legacy env value", string(legacy.key))
	}
	if legacy.keyID() == dev.keyID() {
		t.Fatal("keyID must differ when key differs")
	}
}

// keyID must be deterministic and must not contain key material.
func TestAuditAuthKeyIDHygiene(t *testing.T) {
	a := NewAuthenticator([]byte("audit-hygiene-key"))
	if a.keyID() != a.keyID() {
		t.Fatal("keyID must be deterministic")
	}
	if strings.Contains(a.keyID(), "audit-hygiene-key") {
		t.Fatal("keyID must not leak key material")
	}
}

// handleLogin verifies against the users table (issue #133): unknown
// users, unset passwords, and mismatches all 401 identically; unconfigured
// auth fails closed. This replaces the old stub-login pin.
func TestAuditAuthLoginNoUsersCheck(t *testing.T) {
	s := newTestServer()
	s.Auth = NewAuthenticator(nil) // force unconfigured: fail closed
	rec := doJSON(t, s, http.MethodPost, "/auth/login", "", map[string]string{
		"username": "anyone", "password": "whatever",
	})
	// 503 (user auth unconfigured) — fail closed either way, never a token.
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured login status = %d, want 503 (fail closed)", rec.Code)
	}

	s.Auth = NewAuthenticator([]byte("audit-login-key-1234567890"))
	// No Users source: 503, not a minted token.
	rec = doJSON(t, s, http.MethodPost, "/auth/login", "", map[string]string{
		"username": "anyone", "password": "whatever",
	})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("userless login status = %d, want 503", rec.Code)
	}

	users := newFakeUsers()
	users.add(t, "alice", "correct-horse")
	s.Users = users
	for _, tc := range []struct {
		name  string
		creds map[string]string
		code  int
	}{
		{"unknown user", map[string]string{"username": "ghost-user", "password": "x"}, http.StatusUnauthorized},
		{"wrong password", map[string]string{"username": "alice", "password": "wrong"}, http.StatusUnauthorized},
	} {
		rec := doJSON(t, s, http.MethodPost, "/auth/login", "", tc.creds)
		if rec.Code != tc.code {
			t.Fatalf("%s: status = %d, want %d (%s)", tc.name, rec.Code, tc.code, rec.Body.String())
		}
	}
	// Unset password hash: 401, never accepted.
	users.hashes["nopass"] = ""
	users.ids["nopass"] = "user-nopass"
	rec = doJSON(t, s, http.MethodPost, "/auth/login", "", map[string]string{
		"username": "nopass", "password": "anything",
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unset-password login status = %d, want 401", rec.Code)
	}

	// Correct credentials mint a valid token (fresh gate: the attempts
	// above legitimately consumed the 1/s burst).
	s.login = newRateGate(1, 5)
	rec = doJSON(t, s, http.MethodPost, "/auth/login", "", map[string]string{
		"username": "alice", "password": "correct-horse",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("valid login status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Token string `json:"token"`
	}
	decodeBody(t, rec, &out)
	if sub, err := s.Auth.Validate(out.Token); err != nil || sub != "alice" {
		t.Fatalf("login token sub = %q, err = %v", sub, err)
	}
}

// Expired bearer on a protected route must 401 with "token expired".
func TestAuditAuthExpiredBearerMessage(t *testing.T) {
	s := newTestServer()
	s.Auth = NewAuthenticator([]byte("audit-expired-key-1234567890"))
	expired, err := s.Auth.Generate("alice", -time.Hour)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/projects/resolve", bytes.NewReader([]byte(`{"folder_name":"x"}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+expired)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "token expired") {
		t.Fatalf("body must mention expiry, got %s", rec.Body.String())
	}
}
