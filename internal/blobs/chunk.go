// Package blobs splits large files into content-addressed chunks (D19).
//
// Files over ChunkThreshold (64 MiB) are split into DefaultChunkSize
// (16 MiB) pieces. Files over MaxFileBytes (10 GiB) are refused. A caller
// can pass a smaller ChunkSize so tests can prove the split without
// allocating 64 MiB.
package blobs

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
)

const (
	// DefaultChunkSize is the piece size for files over ChunkThreshold.
	DefaultChunkSize = 16 << 20
	// ChunkThreshold is the size above which a file is split. A file of
	// this size or smaller stays one chunk when the default piece size is used.
	ChunkThreshold = 64 << 20
	// MaxFileBytes is the hard per-file limit. Larger files are not uploaded.
	MaxFileBytes = 10 << 30
)

// ErrTooLarge is returned when a file is over MaxFileBytes.
var ErrTooLarge = errors.New("blobs: file exceeds 10 GiB")

// ErrCorrupt is returned when assembled chunks have a bad hash, a gap,
// a duplicate index, or a total that does not match the set.
var ErrCorrupt = errors.New("blobs: corrupt chunk assembly")

// Chunk is one content-addressed piece of a file. SHA256 is the hex
// sha256 of Body. Index is zero-based and Total is the piece count.
type Chunk struct {
	Index  int
	Total  int
	SHA256 string
	Body   []byte
}

// CheckSize refuses files over MaxFileBytes. Pass a size so tests can
// cover the 10 GiB limit without allocating the file.
func CheckSize(size int64) error {
	if size < 0 {
		return errors.New("blobs: negative size")
	}
	if size > MaxFileBytes {
		return ErrTooLarge
	}
	return nil
}

// ChunkCount reports how many pieces a file of size bytes becomes.
// chunkSize <= 0 selects DefaultChunkSize. With that default, a file at
// or under ChunkThreshold is one piece. Any other chunkSize is used as
// given, including below the threshold.
func ChunkCount(size int64, chunkSize int) (int, error) {
	if err := CheckSize(size); err != nil {
		return 0, err
	}
	if size == 0 {
		return 1, nil
	}
	cs := pieceSize(size, chunkSize)
	n := (size + cs - 1) / cs
	if n > int64(^uint(0)>>1) {
		return 0, ErrTooLarge
	}
	return int(n), nil
}

// Split divides data into content-addressed chunks.
// chunkSize <= 0 selects DefaultChunkSize. See ChunkCount for when the
// 64 MiB threshold applies.
func Split(data []byte, chunkSize int) ([]Chunk, error) {
	n, err := ChunkCount(int64(len(data)), chunkSize)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return []Chunk{newChunk(0, 1, nil)}, nil
	}
	cs := pieceSize(int64(len(data)), chunkSize)
	out := make([]Chunk, 0, n)
	for i := 0; i < n; i++ {
		start := int64(i) * cs
		end := start + cs
		if end > int64(len(data)) {
			end = int64(len(data))
		}
		out = append(out, newChunk(i, n, data[start:end]))
	}
	return out, nil
}

// Assemble concatenates chunks in index order. It checks that every
// sha256 matches its body and that the indexes are exactly 0..total-1
// with one shared Total. A mismatch or a gap returns ErrCorrupt.
func Assemble(chunks []Chunk) ([]byte, error) {
	if len(chunks) == 0 {
		return nil, ErrCorrupt
	}
	total := chunks[0].Total
	if total <= 0 || total != len(chunks) {
		return nil, ErrCorrupt
	}
	ordered := make([][]byte, total)
	seen := make([]bool, total)
	for _, c := range chunks {
		if c.Total != total || c.Index < 0 || c.Index >= total || seen[c.Index] {
			return nil, ErrCorrupt
		}
		sum := sha256.Sum256(c.Body)
		got := hex.EncodeToString(sum[:])
		want := strings.ToLower(strings.TrimSpace(c.SHA256))
		if got != want {
			return nil, ErrCorrupt
		}
		seen[c.Index] = true
		ordered[c.Index] = c.Body
	}
	for _, ok := range seen {
		if !ok {
			return nil, ErrCorrupt
		}
	}
	var out []byte
	for _, part := range ordered {
		out = append(out, part...)
	}
	return out, nil
}

// pieceSize is the byte length of each piece. The default size keeps a
// file at or under ChunkThreshold whole. An injected size always splits.
func pieceSize(size int64, chunkSize int) int64 {
	if chunkSize <= 0 {
		chunkSize = DefaultChunkSize
	}
	if chunkSize == DefaultChunkSize && size <= ChunkThreshold {
		if size == 0 {
			return 1
		}
		return size
	}
	return int64(chunkSize)
}

func newChunk(index, total int, body []byte) Chunk {
	cloned := append([]byte(nil), body...)
	sum := sha256.Sum256(cloned)
	return Chunk{
		Index:  index,
		Total:  total,
		SHA256: hex.EncodeToString(sum[:]),
		Body:   cloned,
	}
}
