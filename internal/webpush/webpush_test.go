package webpush

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGenerateAndSend(t *testing.T) {
	keys, err := GenerateVAPIDKeys()
	if err != nil {
		t.Fatal(err)
	}
	if keys.Public == "" || keys.Private == "" {
		t.Fatal("empty keys")
	}
	raw, err := decodeKey(keys.Public)
	if err != nil || len(raw) != 65 || raw[0] != 0x04 {
		t.Fatalf("public key = %d %v", len(raw), err)
	}

	uaPriv, err := ecdh.P256().GenerateKey(bytes.NewReader(bytes.Repeat([]byte{7}, 64)))
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	for i := range auth {
		auth[i] = byte(i + 1)
	}

	var gotAuth, gotEnc, gotTTL string
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotEnc = r.Header.Get("Content-Encoding")
		gotTTL = r.Header.Get("TTL")
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	sub := Subscription{
		Endpoint: srv.URL + "/push/sub",
		P256dh:   b64(uaPriv.PublicKey().Bytes()),
		Auth:     b64(auth),
	}
	msg := []byte(`{"title":"Nexus","body":"hello"}`)
	resp, err := Send(t.Context(), keys, sub, msg, Options{
		Subscriber: "mailto:test@example.com",
		TTL:        60,
		Urgency:    "high",
		HTTPClient: srv.Client(),
		Expires:    time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if gotEnc != "aes128gcm" {
		t.Fatalf("Content-Encoding = %q", gotEnc)
	}
	if gotTTL != "60" {
		t.Fatalf("TTL = %q", gotTTL)
	}
	if !strings.HasPrefix(gotAuth, "vapid t=") || !strings.Contains(gotAuth, ", k=") {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if len(body) < 16+4+1+65+16 {
		t.Fatalf("body too small: %d", len(body))
	}
	if body[16+4] != 65 {
		t.Fatalf("idlen = %d, want 65", body[16+4])
	}
}

func TestVapidJWTAud(t *testing.T) {
	aud, err := audience("https://fcm.googleapis.com/fcm/send/abc")
	if err != nil || aud != "https://fcm.googleapis.com" {
		t.Fatalf("aud = %q err=%v", aud, err)
	}
}

func TestParseVAPIDRoundTrip(t *testing.T) {
	keys, err := GenerateVAPIDKeys()
	if err != nil {
		t.Fatal(err)
	}
	priv, pub, err := parseVAPID(keys)
	if err != nil {
		t.Fatal(err)
	}
	if priv.Curve != elliptic.P256() || pub[0] != 0x04 {
		t.Fatalf("curve/pub mismatch")
	}
	if priv.D.Cmp(big.NewInt(0)) == 0 {
		t.Fatal("zero D")
	}
	_ = ecdsa.PublicKey(priv.PublicKey)
}

func TestEncryptRejectsBadKeys(t *testing.T) {
	_, err := encrypt([]byte("hi"), Subscription{P256dh: "nope", Auth: b64([]byte("x"))}, 0)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestJWTShape(t *testing.T) {
	keys, err := GenerateVAPIDKeys()
	if err != nil {
		t.Fatal(err)
	}
	priv, _, err := parseVAPID(keys)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := vapidJWT(priv, "https://push.example", "mailto:a@b.c", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("parts = %d", len(parts))
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatal(err)
	}
	if claims["aud"] != "https://push.example" {
		t.Fatalf("claims = %+v", claims)
	}
}

func TestSendContextCancel(t *testing.T) {
	keys, err := GenerateVAPIDKeys()
	if err != nil {
		t.Fatal(err)
	}
	ua, err := ecdh.P256().GenerateKey(bytes.NewReader(bytes.Repeat([]byte{3}, 64)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = Send(ctx, keys, Subscription{
		Endpoint: "http://127.0.0.1:1/push",
		P256dh:   b64(ua.PublicKey().Bytes()),
		Auth:     b64(bytes.Repeat([]byte{1}, 16)),
	}, []byte("{}"), Options{})
	if err == nil {
		t.Fatal("expected canceled send to fail")
	}
}
