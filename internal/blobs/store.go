package blobs

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
)

// ErrNotFound is returned when a blob hash is missing from the store.
var ErrNotFound = errors.New("blobs: not found")

// BlobStore is the content-addressed blob backend used by capture and restore.
// Production will use S3; ByteaStore stands in for Postgres BYTEA locally.
type BlobStore interface {
	// Put stores body under its sha256. uploaded is false when the hash
	// was already present (dedup). The caller must pass the hex digest of body.
	Put(sha256hex string, body []byte) (uploaded bool, err error)
	// Get returns a copy of the bytes for sha256hex.
	Get(sha256hex string) ([]byte, error)
	// Has reports whether sha256hex is already stored.
	Has(sha256hex string) bool
	// Len returns how many distinct blobs are stored.
	Len() int
}

// ByteaStore is an in-memory content-addressed blob store.
//
// Label: BYTEA as local S3 stand-in — the same sha256 keys that production
// keeps in Postgres BYTEA today (and will move to S3 per F7/D8) are stored
// here for the local capture → complete → restore verifier.
type ByteaStore struct {
	mu   sync.Mutex
	blob map[string][]byte
}

// NewByteaStore returns an empty BYTEA stand-in blob store.
func NewByteaStore() *ByteaStore {
	return &ByteaStore{blob: make(map[string][]byte)}
}

// Put implements BlobStore.
func (s *ByteaStore) Put(sha256hex string, body []byte) (bool, error) {
	sum := NormalizeSHA256(sha256hex)
	if sum == "" {
		return false, errors.New("blobs: empty sha256")
	}
	got := sha256.Sum256(body)
	if hex.EncodeToString(got[:]) != sum {
		return false, errors.New("blobs: body does not match sha256")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.blob[sum]; ok {
		return false, nil
	}
	s.blob[sum] = append([]byte(nil), body...)
	return true, nil
}

// Get implements BlobStore.
func (s *ByteaStore) Get(sha256hex string) ([]byte, error) {
	sum := NormalizeSHA256(sha256hex)
	s.mu.Lock()
	defer s.mu.Unlock()
	body, ok := s.blob[sum]
	if !ok {
		return nil, ErrNotFound
	}
	return append([]byte(nil), body...), nil
}

// Has implements BlobStore.
func (s *ByteaStore) Has(sha256hex string) bool {
	sum := NormalizeSHA256(sha256hex)
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.blob[sum]
	return ok
}

// Len implements BlobStore.
func (s *ByteaStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.blob)
}

// NormalizeSHA256 lowercases and strips an optional "sha256:" prefix.
func NormalizeSHA256(h string) string {
	h = strings.TrimSpace(strings.ToLower(h))
	return strings.TrimPrefix(h, "sha256:")
}

// HashBytes returns the hex sha256 of body.
func HashBytes(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
