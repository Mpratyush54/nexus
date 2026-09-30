// Package secrets seals secret-file bytes so they are never stored as
// plaintext (D8, D18). The blob kind is secret_envelope.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
)

// BlobKind is the blobs.kind value for an encrypted secret file.
const BlobKind = "secret_envelope"

// ErrKeySize is returned when the AES-256 key is not 32 bytes.
var ErrKeySize = errors.New("secrets: key must be 32 bytes")

// ErrEnvelope is returned when an envelope cannot be opened.
var ErrEnvelope = errors.New("secrets: envelope decrypt failed")

// Encrypt seals plaintext with AES-GCM under key. key must be 32 bytes.
// The result is nonce concatenated with ciphertext (including the GCM tag).
// Plaintext is not logged.
func Encrypt(key, plaintext []byte) ([]byte, error) {
	gcm, err := gcmFrom(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// Open decrypts an envelope produced by Encrypt. A different key, a
// truncated blob, or a tampered tag returns ErrEnvelope.
func Open(key, envelope []byte) ([]byte, error) {
	gcm, err := gcmFrom(key)
	if err != nil {
		return nil, err
	}
	ns := gcm.NonceSize()
	if len(envelope) < ns {
		return nil, ErrEnvelope
	}
	nonce, ct := envelope[:ns], envelope[ns:]
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, ErrEnvelope
	}
	return plain, nil
}

func gcmFrom(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, ErrKeySize
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
