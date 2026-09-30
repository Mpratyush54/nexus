package secrets

import (
	"crypto/rand"
	"encoding/base64"
	"os"
	"strings"
)

// Box wraps data keys with a process master key (dev stand-in for KMS, D18).
// Callers persist only the wrapped bytes. Plaintext keys are returned to
// the owner over the request and are not stored.
type Box struct {
	master []byte
}

// NewBox creates a box. When NEXUS_DEV_KMS_KEY is set to a base64-encoded
// 32-byte key, that master is used so wraps survive process restarts.
// Otherwise a fresh random 32-byte master is generated.
func NewBox() (*Box, error) {
	if raw := strings.TrimSpace(os.Getenv("NEXUS_DEV_KMS_KEY")); raw != "" {
		key, err := base64.StdEncoding.DecodeString(raw)
		if err == nil && len(key) == 32 {
			return &Box{master: append([]byte(nil), key...)}, nil
		}
	}
	master := make([]byte, 32)
	if _, err := rand.Read(master); err != nil {
		return nil, err
	}
	return &Box{master: master}, nil
}

// Generate returns a fresh 32-byte data key and its wrapped form.
func (b *Box) Generate() (plain, wrapped []byte, err error) {
	if b == nil {
		return nil, nil, ErrKeySize
	}
	plain = make([]byte, 32)
	if _, err = rand.Read(plain); err != nil {
		return nil, nil, err
	}
	wrapped, err = Encrypt(b.master, plain)
	if err != nil {
		return nil, nil, err
	}
	return plain, wrapped, nil
}

// Unwrap opens a wrapped data key.
func (b *Box) Unwrap(wrapped []byte) ([]byte, error) {
	if b == nil {
		return nil, ErrKeySize
	}
	return Open(b.master, wrapped)
}
