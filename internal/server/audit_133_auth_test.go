package server

// Regression tests for issue #133: empty-key forgery, alg confusion,
// TTL caps, PBKDF2 password verification.

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestEmptyKeyValidateRejectsForgery(t *testing.T) {
	// Attacker self-signs with the empty key; an unconfigured server must
	// still reject (previously hmac(nil) verified hmac(nil)).
	forger := NewAuthenticator(nil)
	selfSigned, err := forger.Generate("admin", time.Hour)
	if err == nil {
		t.Fatalf("unconfigured Generate should fail, minted %q", selfSigned)
	}
	if _, err := NewAuthenticator(nil).Validate("x.y.z"); err == nil {
		t.Fatal("unconfigured Validate should reject everything")
	}
}

func TestAlgConfusionRejected(t *testing.T) {
	a := NewAuthenticator([]byte("alg-test-key-0123456789abcdef"))
	payloadJSON, _ := json.Marshal(jwtPayload{Sub: "alice", Exp: 9999999999, Iat: 1})
	headerJSON, _ := json.Marshal(jwtHeader{Alg: "none", Typ: "JWT"})
	forged := base64.RawURLEncoding.EncodeToString(headerJSON) + "." +
		base64.RawURLEncoding.EncodeToString(payloadJSON) + "."
	if _, err := a.Validate(forged); err == nil {
		t.Fatal("alg:none token should be rejected")
	}
	// Wrong alg entirely.
	headerJSON, _ = json.Marshal(jwtHeader{Alg: "RS256", Typ: "JWT"})
	forged = base64.RawURLEncoding.EncodeToString(headerJSON) + "." +
		base64.RawURLEncoding.EncodeToString(payloadJSON) + "."
	if _, err := a.Validate(forged); err == nil {
		t.Fatal("RS256 token should be rejected by an HS256 verifier")
	}
}

func TestLoginRateLimited(t *testing.T) {
	// Fast hashing for determinism (issue #157): real PBKDF2 latency
	// (~1s/verify on slow CI) refills the 1/s bucket between attempts,
	// so throttling never trips. The production iteration count is
	// covered by TestPasswordHashRoundTrip below.
	old := pbkdf2Iterations
	pbkdf2Iterations = 1000
	defer func() { pbkdf2Iterations = old }()
	s := newTestServer()
	s.Auth = NewAuthenticator([]byte("ratelimit-key-0123456789abcdef"))
	users := newFakeUsers()
	users.add(t, "alice", "secret")
	s.Users = users
	body := map[string]string{"username": "alice", "password": "wrong"}
	var limited bool
	for i := 0; i < 12; i++ {
		rec := doJSON(t, s, http.MethodPost, "/auth/login", "", body)
		if rec.Code == 429 {
			limited = true
			break
		}
		if rec.Code != 401 {
			t.Fatalf("attempt %d: status = %d, want 401 or 429", i, rec.Code)
		}
	}
	if !limited {
		t.Fatal("login attempts were never rate-limited")
	}
}

func TestPasswordHashRoundTrip(t *testing.T) {
	h, err := HashPassword("correct-horse-battery")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !strings.HasPrefix(h, "pbkdf2-sha256$") {
		t.Fatalf("hash format = %q", h)
	}
	if !VerifyPassword("correct-horse-battery", h) {
		t.Fatal("correct password should verify")
	}
	if VerifyPassword("wrong", h) {
		t.Fatal("wrong password must not verify")
	}
	// Salts differ per hash.
	h2, _ := HashPassword("correct-horse-battery")
	if h == h2 {
		t.Fatal("salts must differ between hashes")
	}
	// Malformed/foreign formats fail closed.
	for _, bad := range []string{"", "plaintext", "bcrypt$2a$10$xyz", "pbkdf2-sha256$abc$salt$hash", "pbkdf2-sha256$210000$$"} {
		if VerifyPassword("x", bad) {
			t.Fatalf("malformed hash %q should fail closed", bad)
		}
	}
	if _, err := HashPassword(""); err == nil {
		t.Fatal("empty password should not hash")
	}
}
