// Package webpush implements RFC 8291 (aes128gcm) + RFC 8292 (VAPID) delivery
// using only the Go standard library (crypto/ecdh, crypto/hkdf, crypto/ecdsa).
package webpush

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Subscription is a Web Push subscription (Push API JSON).
type Subscription struct {
	Endpoint string
	P256dh   string
	Auth     string
}

// Options control one notification send.
type Options struct {
	Subscriber string        // JWT sub; mailto: or https:
	TTL        int           // seconds; default 86400
	Urgency    string        // very-low | low | normal | high
	RecordSize int           // aes128gcm rs; 0 → fit in one record
	HTTPClient *http.Client  // nil → http.DefaultClient
	Expires    time.Duration // VAPID JWT lifetime; default 12h
}

// Keys is a VAPID P-256 key pair (URL-safe base64, no padding).
type Keys struct {
	Public  string // uncompressed 65-byte point
	Private string // 32-byte scalar
}

// ErrNotConfigured is returned when VAPID keys are missing or invalid.
var ErrNotConfigured = errors.New("webpush: VAPID keys not configured")

// KeysFromEnv loads VAPID_PUBLIC_KEY, VAPID_PRIVATE_KEY, and optional
// VAPID_SUBJECT. ok is false when either key is missing.
func KeysFromEnv() (keys Keys, subject string, ok bool) {
	pub := strings.TrimSpace(os.Getenv("VAPID_PUBLIC_KEY"))
	priv := strings.TrimSpace(os.Getenv("VAPID_PRIVATE_KEY"))
	subject = strings.TrimSpace(os.Getenv("VAPID_SUBJECT"))
	if subject == "" {
		subject = "mailto:nexus@localhost"
	}
	if pub == "" || priv == "" {
		return Keys{}, subject, false
	}
	return Keys{Public: pub, Private: priv}, subject, true
}

// GenerateVAPIDKeys mints a new P-256 pair in the URL-safe unpadded form
// expected by the Push API applicationServerKey field.
func GenerateVAPIDKeys() (Keys, error) {
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return Keys{}, err
	}
	return Keys{
		Public:  b64(priv.PublicKey().Bytes()),
		Private: b64(priv.Bytes()),
	}, nil
}

// Send encrypts message and POSTs it to sub.Endpoint with a VAPID JWT.
func Send(ctx context.Context, keys Keys, sub Subscription, message []byte, opts Options) (*http.Response, error) {
	if strings.TrimSpace(sub.Endpoint) == "" {
		return nil, errors.New("webpush: subscription endpoint is required")
	}
	priv, pub, err := parseVAPID(keys)
	if err != nil {
		return nil, err
	}
	body, err := encrypt(message, sub, opts.RecordSize)
	if err != nil {
		return nil, err
	}
	if opts.Expires <= 0 {
		opts.Expires = 12 * time.Hour
	}
	if opts.Subscriber == "" {
		opts.Subscriber = "mailto:nexus@localhost"
	}
	aud, err := audience(sub.Endpoint)
	if err != nil {
		return nil, err
	}
	jwt, err := vapidJWT(priv, aud, opts.Subscriber, time.Now().Add(opts.Expires))
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
	req.ContentLength = int64(len(body))
	ttl := opts.TTL
	if ttl <= 0 {
		ttl = 86400
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("TTL", fmt.Sprintf("%d", ttl))
	if u := strings.TrimSpace(opts.Urgency); u != "" {
		req.Header.Set("Urgency", u)
	}
	req.Header.Set("Authorization", "vapid t="+jwt+", k="+b64(pub))

	client := opts.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	return client.Do(req)
}

func parseVAPID(keys Keys) (*ecdsa.PrivateKey, []byte, error) {
	d, err := decodeKey(keys.Private)
	if err != nil {
		return nil, nil, fmt.Errorf("webpush: private key: %w", err)
	}
	if len(d) > 32 {
		d = d[len(d)-32:]
	}
	if len(d) < 32 {
		padded := make([]byte, 32)
		copy(padded[32-len(d):], d)
		d = padded
	}
	ecdhPriv, err := ecdh.P256().NewPrivateKey(d)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrNotConfigured, err)
	}
	pub := ecdhPriv.PublicKey().Bytes()
	if want, err := decodeKey(keys.Public); err == nil && len(want) == 65 && string(want) != string(pub) {
		// Public key mismatch is not fatal — derived public is authoritative.
		_ = want
	}
	x, y := elliptic.Unmarshal(elliptic.P256(), pub)
	if x == nil {
		return nil, nil, fmt.Errorf("%w: could not unmarshal public key", ErrNotConfigured)
	}
	return &ecdsa.PrivateKey{
		PublicKey: ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y},
		D:         new(big.Int).SetBytes(d),
	}, pub, nil
}

func vapidJWT(priv *ecdsa.PrivateKey, aud, sub string, exp time.Time) (string, error) {
	header := b64([]byte(`{"typ":"JWT","alg":"ES256"}`))
	claims, err := json.Marshal(map[string]any{
		"aud": aud,
		"exp": exp.Unix(),
		"sub": sub,
	})
	if err != nil {
		return "", err
	}
	payload := header + "." + b64(claims)
	sum := sha256.Sum256([]byte(payload))
	r, s, err := ecdsa.Sign(rand.Reader, priv, sum[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return payload + "." + b64(sig), nil
}

func audience(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("webpush: invalid endpoint: %w", err)
	}
	return u.Scheme + "://" + u.Host, nil
}

func encrypt(plaintext []byte, sub Subscription, recordSize int) ([]byte, error) {
	uaPubRaw, err := decodeKey(sub.P256dh)
	if err != nil || len(uaPubRaw) != 65 {
		return nil, errors.New("webpush: invalid p256dh key")
	}
	authSecret, err := decodeKey(sub.Auth)
	if err != nil || len(authSecret) == 0 {
		return nil, errors.New("webpush: invalid auth secret")
	}
	asPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	uaPub, err := ecdh.P256().NewPublicKey(uaPubRaw)
	if err != nil {
		return nil, fmt.Errorf("webpush: ua public key: %w", err)
	}
	shared, err := asPriv.ECDH(uaPub)
	if err != nil {
		return nil, err
	}
	asPub := asPriv.PublicKey().Bytes()

	authInfo := make([]byte, 0, 14+1+65+65)
	authInfo = append(authInfo, []byte("WebPush: info")...)
	authInfo = append(authInfo, 0x00)
	authInfo = append(authInfo, uaPubRaw...)
	authInfo = append(authInfo, asPub...)
	ikm, err := hkdf.Key(sha256.New, shared, authSecret, string(authInfo), 32)
	if err != nil {
		return nil, err
	}

	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}

	// RFC 8188: encrypt plaintext || 0x02 (last-record delimiter).
	pad := append(append([]byte{}, plaintext...), 0x02)
	cek, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	ciphertext := gcm.Seal(nil, nonce, pad, nil)

	rs := recordSize
	if rs <= 0 {
		rs = len(ciphertext)
	}
	if rs < len(ciphertext) {
		return nil, errors.New("webpush: record size too small for payload")
	}

	out := make([]byte, 0, 16+4+1+65+len(ciphertext))
	out = append(out, salt...)
	var rsBuf [4]byte
	binary.BigEndian.PutUint32(rsBuf[:], uint32(rs))
	out = append(out, rsBuf[:]...)
	out = append(out, byte(len(asPub)))
	out = append(out, asPub...)
	out = append(out, ciphertext...)
	return out, nil
}

func decodeKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, errors.New("empty key")
	}
	encodings := []*base64.Encoding{
		base64.RawURLEncoding,
		base64.URLEncoding,
		base64.RawStdEncoding,
		base64.StdEncoding,
	}
	var last error
	for _, enc := range encodings {
		b, err := enc.DecodeString(s)
		if err == nil {
			return b, nil
		}
		last = err
	}
	return nil, last
}

func b64(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}
