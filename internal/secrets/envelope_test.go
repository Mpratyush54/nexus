package secrets

import (
	"bytes"
	"crypto/rand"
	"errors"
	"testing"
)

func TestEnvelopeRoundTripAndKey(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	plain := []byte("super-secret-plaintext-value")
	ct, err := Encrypt(key, plain)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(ct, plain) || bytes.Contains(ct, plain) {
		t.Fatal("envelope contains plaintext")
	}
	if BlobKind != "secret_envelope" {
		t.Fatalf("kind %s", BlobKind)
	}
	got, err := Open(key, ct)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("opened %q", got)
	}
	ct2, err := Encrypt(key, plain)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(ct, ct2) {
		t.Fatal("nonce was reused")
	}
	other := append([]byte(nil), key...)
	other[0] ^= 0xff
	if _, err := Open(other, ct); !errors.Is(err, ErrEnvelope) {
		t.Fatalf("wrong key: %v", err)
	}
	if _, err := Encrypt(key[:31], plain); !errors.Is(err, ErrKeySize) {
		t.Fatalf("short key: %v", err)
	}
	if _, err := Open(key, ct[:4]); !errors.Is(err, ErrEnvelope) {
		t.Fatalf("truncated: %v", err)
	}
	tampered := append([]byte(nil), ct...)
	tampered[len(tampered)-1] ^= 0xff
	if _, err := Open(key, tampered); !errors.Is(err, ErrEnvelope) {
		t.Fatalf("tamper: %v", err)
	}
}
