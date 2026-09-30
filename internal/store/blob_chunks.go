package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"sync"
)

// ErrChunkSHA means the stored chunk bytes do not match chunk_sha256.
var ErrChunkSHA = errors.New("store: chunk_sha256 does not match body")

// ErrChunkRange means index is outside 0..total-1.
var ErrChunkRange = errors.New("store: chunk index out of range")

// ErrChunkTotal means a later chunk disagrees with the total already stored
// for the same project and assembly id.
var ErrChunkTotal = errors.New("store: chunk total does not match earlier chunks")

// BlobChunk is one uploaded piece. ParentSHA256 is the assembly id: for a
// single-chunk object it is sha256 of the body, and for a multi-chunk object
// it is sha256 of the concatenated plaintext. The map key is project,
// parent sha256, and index.
type BlobChunk struct {
	ProjectID    string
	ParentSHA256 string
	Index        int
	Total        int
	ChunkSHA256  string
	Body         []byte
}

type blobChunkKey struct {
	Project string
	Parent  string
	Index   int
}

type secretKey struct {
	Project string
	SHA256  string
}

type secretRecord struct {
	Kind       string
	Ciphertext []byte
}

type blobChunkMem struct {
	mu      sync.Mutex
	chunks  map[blobChunkKey]BlobChunk
	secrets map[secretKey]secretRecord
}

var blobChunks = &blobChunkMem{
	chunks:  map[blobChunkKey]BlobChunk{},
	secrets: map[secretKey]secretRecord{},
}

// PutBlobChunk stores one chunk. Body is copied. sha256(body) must equal
// ChunkSHA256. Callers pass ciphertext for secret pieces; this map does not
// interpret the bytes.
func PutBlobChunk(c BlobChunk) error {
	if c.Total < 1 || c.Index < 0 || c.Index >= c.Total {
		return ErrChunkRange
	}
	sum := sha256.Sum256(c.Body)
	got := hex.EncodeToString(sum[:])
	want := strings.ToLower(strings.TrimSpace(c.ChunkSHA256))
	if got != want {
		return ErrChunkSHA
	}
	c.ChunkSHA256 = want
	c.Body = append([]byte(nil), c.Body...)

	blobChunks.mu.Lock()
	defer blobChunks.mu.Unlock()
	for k, existing := range blobChunks.chunks {
		if k.Project == c.ProjectID && k.Parent == c.ParentSHA256 && existing.Total != c.Total {
			return ErrChunkTotal
		}
	}
	blobChunks.chunks[blobChunkKey{Project: c.ProjectID, Parent: c.ParentSHA256, Index: c.Index}] = c
	return nil
}

// LoadBlobChunks returns copies of every chunk for project and assembly id,
// ordered by index.
func LoadBlobChunks(projectID, parentSHA256 string) []BlobChunk {
	blobChunks.mu.Lock()
	defer blobChunks.mu.Unlock()
	var out []BlobChunk
	for k, c := range blobChunks.chunks {
		if k.Project != projectID || k.Parent != parentSHA256 {
			continue
		}
		cp := c
		cp.Body = append([]byte(nil), c.Body...)
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Index < out[j].Index })
	return out
}

// PutSecretEnvelope stores ciphertext only. kind is secret_envelope.
// Plaintext must not be passed here.
func PutSecretEnvelope(projectID, sha256sum, kind string, ciphertext []byte) {
	blobChunks.mu.Lock()
	defer blobChunks.mu.Unlock()
	blobChunks.secrets[secretKey{Project: projectID, SHA256: sha256sum}] = secretRecord{
		Kind:       kind,
		Ciphertext: append([]byte(nil), ciphertext...),
	}
}

// LoadSecretEnvelope returns the stored ciphertext and kind.
func LoadSecretEnvelope(projectID, sha256sum string) (ciphertext []byte, kind string, ok bool) {
	blobChunks.mu.Lock()
	defer blobChunks.mu.Unlock()
	rec, ok := blobChunks.secrets[secretKey{Project: projectID, SHA256: sha256sum}]
	if !ok {
		return nil, "", false
	}
	return append([]byte(nil), rec.Ciphertext...), rec.Kind, true
}

// ResetBlobChunks clears the in-memory chunk and secret maps. Tests use it
// so cases do not share uploads.
func ResetBlobChunks() {
	blobChunks.mu.Lock()
	defer blobChunks.mu.Unlock()
	blobChunks.chunks = map[blobChunkKey]BlobChunk{}
	blobChunks.secrets = map[secretKey]secretRecord{}
}
