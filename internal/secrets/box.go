package secrets

import "crypto/rand"

// Box wraps data keys with a process master key (dev stand-in for KMS, D18).
// Callers persist only the wrapped bytes. Plaintext keys are returned to
// the owner over the request and are not stored.
type Box struct {
	master []byte
}

// NewBox creates a box with a random 32-byte master.
func NewBox() (*Box, error) {
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
