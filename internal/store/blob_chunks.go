package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

// ErrChunkSHA means the stored chunk bytes do not match chunk_sha256.
var ErrChunkSHA = errors.New("store: chunk_sha256 does not match body")

// ErrChunkRange means index is outside 0..total-1.
var ErrChunkRange = errors.New("store: chunk index out of range")

// ErrChunkTotal means a later chunk disagrees with the total already stored
// for the same project and assembly id.
var ErrChunkTotal = errors.New("store: chunk total does not match earlier chunks")

// ErrChunkSetTooLarge means one upload's pieces exceed the per-user limit
// on its own (MaxChunkBytesPerUser): it can never be stored.
var ErrChunkSetTooLarge = errors.New("store: upload is larger than the chunk memory limit")

// ErrChunkQuota means the pieces held for in-progress uploads, for this
// user or for the whole server, are at their limit; retry later.
var ErrChunkQuota = errors.New("store: too many in-progress uploads")

// Uploaded pieces live in server memory until their upload is assembled.
// They are freed after a successful assembly, expire when an upload is
// abandoned (no piece for ChunkSetTTL), and are bounded per user and in
// all. Variables so tests can use small values.
var (
	ChunkSetTTL                = time.Hour
	MaxChunkBytesPerUser int64 = 512 << 20
	MaxChunkBytesTotal   int64 = 2 << 30
	chunkNow                   = time.Now
)

// BlobChunk is one uploaded piece. ParentSHA256 is the assembly id: for a
// single-chunk object it is sha256 of the body, and for a multi-chunk object
// it is sha256 of the concatenated plaintext. Pieces are grouped by
// project and parent sha256. Owner is the uploading user (memory limits).
type BlobChunk struct {
	ProjectID    string
	ParentSHA256 string
	Owner        string
	Index        int
	Total        int
	ChunkSHA256  string
	Body         []byte
}

type chunkSetKey struct {
	Project string
	Parent  string
}

// chunkSet is one in-progress upload.
type chunkSet struct {
	owner   string
	total   int
	bytes   int64
	touched time.Time
	pieces  map[int]BlobChunk
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
	sets    map[chunkSetKey]*chunkSet
	byOwner map[string]int64
	total   int64
	secrets map[secretKey]secretRecord
}

var blobChunks = &blobChunkMem{
	sets:    map[chunkSetKey]*chunkSet{},
	byOwner: map[string]int64{},
	secrets: map[secretKey]secretRecord{},
}

// dropSetLocked frees an upload's pieces.
func (m *blobChunkMem) dropSetLocked(k chunkSetKey) {
	set := m.sets[k]
	if set == nil {
		return
	}
	m.total -= set.bytes
	if m.byOwner[set.owner] -= set.bytes; m.byOwner[set.owner] <= 0 {
		delete(m.byOwner, set.owner)
	}
	delete(m.sets, k)
}

// expireLocked frees uploads that received no piece for ChunkSetTTL.
func (m *blobChunkMem) expireLocked(now time.Time) {
	for k, set := range m.sets {
		if now.Sub(set.touched) > ChunkSetTTL {
			m.dropSetLocked(k)
		}
	}
}

// PutBlobChunk stores one chunk. Body is copied. sha256(body) must equal
// ChunkSHA256. Callers pass ciphertext for secret pieces; this map does not
// interpret the bytes. Sending a piece again replaces it.
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

	blobChunks.mu.Lock()
	defer blobChunks.mu.Unlock()
	now := chunkNow()
	blobChunks.expireLocked(now)
	key := chunkSetKey{Project: c.ProjectID, Parent: c.ParentSHA256}
	set := blobChunks.sets[key]
	if set != nil && set.total != c.Total {
		return ErrChunkTotal
	}
	owner := c.Owner
	if set != nil {
		owner = set.owner
	}
	var replaced int64
	if set != nil {
		replaced = int64(len(set.pieces[c.Index].Body))
	}
	grow := int64(len(c.Body)) - replaced
	setBytes := grow
	if set != nil {
		setBytes += set.bytes
	}
	if setBytes > MaxChunkBytesPerUser {
		return ErrChunkSetTooLarge
	}
	if blobChunks.byOwner[owner]+grow > MaxChunkBytesPerUser || blobChunks.total+grow > MaxChunkBytesTotal {
		return ErrChunkQuota
	}
	if set == nil {
		set = &chunkSet{owner: owner, total: c.Total, pieces: map[int]BlobChunk{}}
		blobChunks.sets[key] = set
	}
	c.Owner = owner
	c.Body = append([]byte(nil), c.Body...)
	set.pieces[c.Index] = c
	set.bytes += grow
	set.touched = now
	blobChunks.byOwner[owner] += grow
	blobChunks.total += grow
	return nil
}

// LoadBlobChunks returns copies of every chunk for project and assembly id,
// ordered by index.
func LoadBlobChunks(projectID, parentSHA256 string) []BlobChunk {
	blobChunks.mu.Lock()
	defer blobChunks.mu.Unlock()
	blobChunks.expireLocked(chunkNow())
	set := blobChunks.sets[chunkSetKey{Project: projectID, Parent: parentSHA256}]
	if set == nil {
		return nil
	}
	out := make([]BlobChunk, 0, len(set.pieces))
	for _, c := range set.pieces {
		cp := c
		cp.Body = append([]byte(nil), c.Body...)
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Index < out[j].Index })
	return out
}

// DeleteBlobChunks frees an upload's pieces (after it was assembled).
func DeleteBlobChunks(projectID, parentSHA256 string) {
	blobChunks.mu.Lock()
	defer blobChunks.mu.Unlock()
	blobChunks.dropSetLocked(chunkSetKey{Project: projectID, Parent: parentSHA256})
}

// BlobChunkBytes reports the piece bytes held for in-progress uploads, in
// all and for owner.
func BlobChunkBytes(owner string) (total, forOwner int64) {
	blobChunks.mu.Lock()
	defer blobChunks.mu.Unlock()
	return blobChunks.total, blobChunks.byOwner[owner]
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
	blobChunks.sets = map[chunkSetKey]*chunkSet{}
	blobChunks.byOwner = map[string]int64{}
	blobChunks.total = 0
	blobChunks.secrets = map[secretKey]secretRecord{}
}

// ErrAssembleTooLarge means an upload's pieces add up to more than the
// assembled-size limit.
var ErrAssembleTooLarge = errors.New("store: assembled file is larger than the limit")

// ErrAssembleMissing means a piece of the upload is missing or inconsistent.
var ErrAssembleMissing = errors.New("store: missing chunk")

// ErrAssembleSHA means the pieces do not add up to the declared sha256.
var ErrAssembleSHA = errors.New("store: assembled bytes do not match sha256")

// AssembleBlobChunks joins an upload's pieces in index order, checking the
// assembled sha256 against parentSHA256 as it goes. The size is checked
// before anything is copied (ErrAssembleTooLarge over maxBytes), and the
// result is built once at its exact size: no per-piece copies and no
// growing buffer (this used to take several times the file in memory).
func AssembleBlobChunks(projectID, parentSHA256 string, maxBytes int64) (data []byte, pieces int, err error) {
	blobChunks.mu.Lock()
	blobChunks.expireLocked(chunkNow())
	set := blobChunks.sets[chunkSetKey{Project: projectID, Parent: parentSHA256}]
	if set == nil || set.total < 1 || len(set.pieces) != set.total {
		blobChunks.mu.Unlock()
		return nil, 0, ErrAssembleMissing
	}
	// Piece bodies are never modified once stored (a re-sent piece replaces
	// the map entry), so they can be read after the lock is released.
	bodies := make([][]byte, set.total)
	var size int64
	for i := 0; i < set.total; i++ {
		p, ok := set.pieces[i]
		if !ok {
			blobChunks.mu.Unlock()
			return nil, 0, ErrAssembleMissing
		}
		bodies[i] = p.Body
		size += int64(len(p.Body))
	}
	blobChunks.mu.Unlock()
	if maxBytes > 0 && size > maxBytes {
		return nil, 0, ErrAssembleTooLarge
	}
	h := sha256.New()
	out := make([]byte, 0, size)
	for _, b := range bodies {
		h.Write(b)
		out = append(out, b...)
	}
	if hex.EncodeToString(h.Sum(nil)) != strings.ToLower(strings.TrimSpace(parentSHA256)) {
		return nil, 0, ErrAssembleSHA
	}
	return out, len(bodies), nil
}

// BlobChunkSetBytesExcept is the size of an upload's stored pieces other
// than piece index (a re-sent piece replaces it, so it is not counted).
func BlobChunkSetBytesExcept(projectID, parentSHA256 string, index int) int64 {
	blobChunks.mu.Lock()
	defer blobChunks.mu.Unlock()
	set := blobChunks.sets[chunkSetKey{Project: projectID, Parent: parentSHA256}]
	if set == nil {
		return 0
	}
	return set.bytes - int64(len(set.pieces[index].Body))
}
