// Package server implements the Central Server REST API (Phase 1.8).
//
// Replaceable JWT note: authentication here is a stdlib-only HMAC-SHA256
// stub that mints JWT-shaped tokens (header.payload.signature). It is
// intentionally interface-compatible with a future golang-jwt/jwt/v5
// migration: swap Authenticator.Generate/Validate internals for jwt.NewWithClaims
// + jwt.ParseWithClaims without changing routes.go call sites. See
// docs/decisions/2026-09-17-central-server.md and the TODO below.
package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// TODO(jwt-v5): replace this stub with github.com/golang-jwt/jwt/v5.
// The Generate/Validate signatures are deliberately jwt-like (subject in,
// subject out) so the swap is mechanical. Do NOT add the dependency until
// Phase 1.9 (see decision doc) — go.mod must stay dependency-free for now.

// ErrInvalidToken is returned when a token is malformed or has a bad signature.
var ErrInvalidToken = errors.New("invalid token")

// ErrExpiredToken is returned when a token's exp claim is in the past.
var ErrExpiredToken = errors.New("token expired")

// DefaultTokenTTL is the lifetime of tokens issued by /auth/login.
const DefaultTokenTTL = 24 * time.Hour

// Authenticator mints and validates HS256 JWT-shaped tokens using only stdlib.
type Authenticator struct {
	key []byte
}

// NewAuthenticator builds an Authenticator with the given HMAC key.
// The key must be non-empty; callers should load it from a secret store.
func NewAuthenticator(key []byte) *Authenticator {
	return &Authenticator{key: key}
}

// NewAuthenticatorFromEnv builds an Authenticator from CENTRAL_MEMORY_JWT_KEY,
// falling back to an insecure dev key. The fallback is deliberate for local
// development and unit tests; production must set the env var (backed by
// AWS Secrets Manager when RDS Aurora is wired in).
func NewAuthenticatorFromEnv() *Authenticator {
	key := os.Getenv("CENTRAL_MEMORY_JWT_KEY")
	if key == "" {
		key = "dev-only-insecure-key-replace-via-env"
	}
	return NewAuthenticator([]byte(key))
}

var b64 = base64.RawURLEncoding

type jwtHeader struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
}

type jwtPayload struct {
	Sub string `json:"sub"`
	Exp int64  `json:"exp"`
	Iat int64  `json:"iat"`
}

// Generate mints a token for subject with the given TTL.
func (a *Authenticator) Generate(subject string, ttl time.Duration) (string, error) {
	if subject == "" {
		return "", errors.New("subject must not be empty")
	}
	if len(a.key) == 0 {
		return "", errors.New("authenticator has no key configured")
	}
	now := time.Now().UTC()
	headerJSON, _ := json.Marshal(jwtHeader{Alg: "HS256", Typ: "JWT"})
	payloadJSON, _ := json.Marshal(jwtPayload{
		Sub: subject,
		Exp: now.Add(ttl).Unix(),
		Iat: now.Unix(),
	})
	encHeader := b64.EncodeToString(headerJSON)
	encPayload := b64.EncodeToString(payloadJSON)
	signingInput := encHeader + "." + encPayload
	mac := hmac.New(sha256.New, a.key)
	mac.Write([]byte(signingInput))
	sig := b64.EncodeToString(mac.Sum(nil))
	return signingInput + "." + sig, nil
}

// Validate checks the signature and expiry of token and returns the subject.
func (a *Authenticator) Validate(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", ErrInvalidToken
	}
	signingInput := parts[0] + "." + parts[1]
	wantMAC := hmac.New(sha256.New, a.key)
	wantMAC.Write([]byte(signingInput))
	wantSig := wantMAC.Sum(nil)
	gotSig, err := b64.DecodeString(parts[2])
	if err != nil {
		return "", ErrInvalidToken
	}
	if !hmac.Equal(gotSig, wantSig) {
		return "", ErrInvalidToken
	}
	payloadJSON, err := b64.DecodeString(parts[1])
	if err != nil {
		return "", ErrInvalidToken
	}
	var payload jwtPayload
	if err := json.Unmarshal(payloadJSON, &payload); err != nil {
		return "", ErrInvalidToken
	}
	if payload.Sub == "" {
		return "", ErrInvalidToken
	}
	if time.Now().UTC().Unix() > payload.Exp {
		return "", ErrExpiredToken
	}
	return payload.Sub, nil
}

// bearerSubject extracts and validates a "Bearer <token>" header value.
func (a *Authenticator) bearerSubject(header string) (string, error) {
	if header == "" {
		return "", ErrInvalidToken
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return "", ErrInvalidToken
	}
	token := strings.TrimSpace(strings.TrimPrefix(header, prefix))
	if token == "" {
		return "", ErrInvalidToken
	}
	sub, err := a.Validate(token)
	if err != nil {
		return "", err
	}
	return sub, nil
}

// keyID is a non-sensitive hint for debugging only (never the key itself).
func (a *Authenticator) keyID() string {
	sum := sha256.Sum256(a.key)
	return fmt.Sprintf("hmac-sha256:%x", sum[:4])
}
